// 本文件定义沙箱基础设施配置与用户工作区入口；配置为空时仍允许看板启动，实际调用明确报告未配置。

package sandbox

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Config 只包含部署基础设施，不包含管理员保存在数据库里的 FC API Key。
type Config struct {
	Region          string
	Template        string
	VPCID           string
	SecurityGroupID string
	VSwitchIDs      []string
	Timeout         time.Duration
}

// Validate 在调用云端之前检查完整配置；不启动没有长期存储的临时电脑。
func (c Config) Validate() error {
	if !regexp.MustCompile(`^[a-z]+-[a-z]+-[0-9]+$`).MatchString(c.Region) || c.Template == "" || c.VPCID == "" || c.SecurityGroupID == "" || len(c.VSwitchIDs) == 0 || c.Timeout < time.Minute || c.Timeout > time.Hour {
		return fmt.Errorf("configure SANDBOX_REGION, SANDBOX_TEMPLATE, SANDBOX_VPC_ID, SANDBOX_SECURITY_GROUP_ID, SANDBOX_VSWITCH_IDS and SANDBOX_TIMEOUT (1m–1h)")
	}
	return nil
}

// ValidateBinding 仅接受当前地域的 AgenticFS Access Point 和独立 RAM 角色，不接受普通 NAS 目录。
func ValidateBinding(region, serverAddr, roleARN string) error {
	address := `^ap-[a-z0-9]+\.[a-z0-9]+-[a-z0-9]+\.` + regexp.QuoteMeta(region) + `\.nas\.aliyuncs\.com:/$`
	if region == "" || !regexp.MustCompile(address).MatchString(serverAddr) {
		return fmt.Errorf("server-addr must be an AgenticFS access point in %s ending in :/", region)
	}
	if !regexp.MustCompile(`^acs:ram::[0-9]+:role/[a-zA-Z0-9._/-]+$`).MatchString(roleARN) || strings.HasSuffix(roleARN, "/") {
		return fmt.Errorf("role-arn must identify this user's scoped RAM role")
	}
	return nil
}
