// 本命令使用阿里云 OpenAPI 发布第二代沙箱模板，或从受保护的 AIO 实例导出模型工具定义；不会自动创建收费镜像仓库。

package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/OmniNetOpensource/aether-personal/backend/internal/agent/tools"
	"github.com/OmniNetOpensource/aether-personal/backend/internal/sandbox"
	openapi "github.com/alibabacloud-go/darabonba-openapi/v2/utils"
	cloud "github.com/alibabacloud-go/sandbox-20260820/client"
	"github.com/alibabacloud-go/tea/dara"
)

// main 是维护者命令的错误出口，凭证只从环境或权限受限文件读取。
func main() {
	if err := run(); err != nil {
		slog.Error("sandbox template command failed", "error", err)
		os.Exit(1)
	}
}

// run 将发布和工具导出分开；服务进程只需要 FC Key，不接收这里的模板管理 AK。
func run() error {
	action := flag.String("action", "status", "spec、build、status 或 export-tools")
	region := flag.String("region", "ap-southeast-1", "FC 地域")
	team := flag.String("team-id", "", "FC Team UUID")
	name := flag.String("name", "aether-desktop", "新模板名称")
	image := flag.String("image", "", "官方镜像或预先发布的自定义镜像地址")
	setupScript := flag.String("setup-script", "", "可选：在快照构建期间安装依赖的 build.sh")
	runtimeScript := flag.String("runtime-script", "", "与 setup-script 一起提供的用户桌面 start.sh")
	id := flag.String("template-id", "", "status 查询的模板 ID")
	endpoint := flag.String("endpoint", "", "export-tools 使用的 AIO HTTPS MCP 地址")
	tokenFile := flag.String("token-file", "", "AIO bearer token 文件，不通过命令行明文传入")
	trafficFile := flag.String("traffic-token-file", "", "FC 数据面 token 文件")
	output := flag.String("output", "", "导出的 sandbox_tools.json 文件路径")
	flag.Parse()
	if *action == "export-tools" {
		return exportTools(*endpoint, *tokenFile, *trafficFile, *output)
	}
	if *action != "spec" && *action != "build" && *action != "status" {
		return fmt.Errorf("unknown action %q", *action)
	}
	if !regexp.MustCompile(`^[a-z]+-[a-z]+-[0-9]+$`).MatchString(*region) {
		return errors.New("region must be an Alibaba Cloud region ID")
	}
	if *team == "" || (*action != "status" && *image == "") || (*action == "status" && *id == "") {
		return errors.New("team-id and image (build) or template-id (status) are required")
	}
	// 构建快照可直接使用官方镜像，不要求购买私有仓库；初始化脚本不含任何用户凭证。
	input := &cloud.CreateTemplateInput{Name: name, TeamID: team,
		RuntimeConfig: &cloud.CreateTemplateRuntimeConfig{Cpu: dara.Float32(4), MemorySize: dara.Int32(8192), DiskSize: dara.Int32(15360), InternetAccess: dara.Bool(true),
			SandboxConfig: &cloud.CreateTemplateSandboxConfig{Image: image, Generation: dara.Int32(2)}},
		BuildConfig: &cloud.CreateTemplateBuildConfig{InstallStorageClients: dara.Bool(true)}}
	if (*setupScript == "") != (*runtimeScript == "") {
		return errors.New("setup-script and runtime-script must be provided together")
	}
	if *setupScript != "" {
		// 脚本压缩后可通过控制台或 OpenAPI 传入，不需要额外的文件托管服务。
		encodedScripts := make([]string, 2)
		for index, path := range []string{*setupScript, *runtimeScript} {
			content, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			var compressed bytes.Buffer
			writer := gzip.NewWriter(&compressed)
			if _, err := writer.Write(content); err != nil {
				return err
			}
			if err := writer.Close(); err != nil {
				return err
			}
			encodedScripts[index] = base64.StdEncoding.EncodeToString(compressed.Bytes())
		}
		command := "set -euo pipefail; mkdir -p /opt/aether; printf %s " + encodedScripts[0] + " | base64 -d | gzip -d > /opt/aether/build.sh; printf %s " + encodedScripts[1] + " | base64 -d | gzip -d > /opt/aether/start.sh; bash /opt/aether/build.sh; touch /opt/aether/template-ready"
		// 依赖安装属于 RUN 构建步骤，不能占用只有五分钟的快照就绪检查窗口。
		input.RuntimeConfig.SandboxConfig.Steps = []*cloud.CreateTemplateStep{{Type: dara.String("RUN"), Args: []*string{dara.String("bash -c '" + command + "'"), dara.String("root")}}}
		input.RuntimeConfig.SandboxConfig.StartCommand = dara.String("sleep infinity")
		input.RuntimeConfig.SandboxConfig.ReadyCommand = dara.String("test -f /opt/aether/template-ready && test -x /opt/aether/start.sh && test -x /usr/local/bin/aiod")
	}
	if *action == "spec" {
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(input)
	}
	ak, secret := os.Getenv("ALIBABA_CLOUD_ACCESS_KEY_ID"), os.Getenv("ALIBABA_CLOUD_ACCESS_KEY_SECRET")
	if ak == "" || secret == "" {
		return errors.New("operator ALIBABA_CLOUD_ACCESS_KEY_ID and ALIBABA_CLOUD_ACCESS_KEY_SECRET are required")
	}
	client, err := cloud.NewClient(&openapi.Config{AccessKeyId: dara.String(ak), AccessKeySecret: dara.String(secret), SecurityToken: dara.String(os.Getenv("ALIBABA_CLOUD_SECURITY_TOKEN")),
		RegionId: region, Endpoint: dara.String("sandbox." + *region + ".aliyuncs.com"), ReadTimeout: dara.Int(60000), ConnectTimeout: dara.Int(15000)})
	if err != nil {
		return err
	}
	// 模板只烘焙依赖，不启动有用户状态的进程；运行阶段由 /opt/aether/start.sh 启动。
	if *action == "build" {
		response, err := client.CreateTemplateWithOptions(&cloud.CreateTemplateRequest{Body: input}, nil, &dara.RuntimeOptions{Autoretry: dara.Bool(false)})
		if err != nil {
			return err
		}
		fmt.Println("Template build submitted:", dara.StringValue(response.Body.TemplateID))
		fmt.Println("Run -action=status with this template ID; configure SANDBOX_TEMPLATE only after state is ready.")
		return nil
	}
	response, err := client.GetTemplate(id, &cloud.GetTemplateRequest{TeamID: team})
	if err != nil {
		return err
	}
	encoded, err := json.MarshalIndent(response.Body.Status, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(encoded))
	return nil
}

// exportTools 导出真实 MCP schema，并补齐固定版本遗漏的数组元素类型；新工具必须先完成应用侧登记。
func exportTools(endpoint, tokenPath, trafficPath, output string) (err error) {
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("endpoint must be an HTTPS MCP URL without credentials")
	}
	if output == "" || tokenPath == "" || trafficPath == "" {
		return errors.New("output, token-file and traffic-token-file are required")
	}
	token, err := os.ReadFile(tokenPath)
	if err != nil {
		return err
	}
	traffic, err := os.ReadFile(trafficPath)
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(token)) == "" || strings.TrimSpace(string(traffic)) == "" {
		return errors.New("token files must not be empty")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	session, err := sandbox.ConnectMCP(ctx, endpoint, strings.TrimSpace(string(token)), strings.TrimSpace(string(traffic)))
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, session.Close()) }()
	catalog := make([]map[string]any, 0)
	for tool, err := range session.Tools(ctx, nil) {
		if err != nil {
			return err
		}
		registered, known := tools.ByName(tool.Name)
		if !known || !registered.Ordered {
			return fmt.Errorf("unregistered sandbox tool %q; review before exporting", tool.Name)
		}
		encoded, err := json.Marshal(tool.InputSchema)
		if err != nil {
			return err
		}
		var schema map[string]any
		if err := json.Unmarshal(encoded, &schema); err != nil {
			return err
		}
		properties, _ := schema["properties"].(map[string]any)
		for name, value := range properties {
			property, ok := value.(map[string]any)
			if !ok || property["type"] != "array" || property["items"] != nil {
				continue
			}
			element := "string"
			if name == "view_range" {
				element = "integer"
			}
			property["items"] = map[string]string{"type": element}
		}
		catalog = append(catalog, map[string]any{"name": tool.Name, "description": tool.Description, "inputSchema": schema})
	}
	encoded, err := json.MarshalIndent(catalog, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(output, append(encoded, '\n'), 0644); err != nil {
		return err
	}
	fmt.Println("Exported sandbox tool definitions:", len(catalog))
	return nil
}
