// 本文件实现单一 memory 工具：在 PostgreSQL 的用户私有 /memories 命名空间中查看和编辑文本文件，保留 memory.md 根入口。

package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/OmniNetOpensource/aether-personal/backend/internal/database"
	"github.com/google/uuid"
)

// 根目录与根文件是固定逻辑名称，路径只映射数据库记录，不访问宿主机文件系统。
const (
	memoryDirectory = "/memories"
	// memoryRootPath 是文件工具维护的永久根文档，正文只在调用工具时读取。
	memoryRootPath       = "/memories/memory.md"
	memoryViewCharacters = 16000
)

// memoryTool 使用 Anthropic 风格的六个文件命令，兼容现有 OpenRouter 与 Gemini 普通函数工具协议。
var memoryTool = Tool{
	Name:       "memory",
	Definition: json.RawMessage(`{"type":"function","function":{"name":"memory","description":"Maintain persistent text files in this user's private /memories namespace. Commands: view lists a directory up to two levels deep or reads a file with 1-based line numbers and optional view_range [start,end] (-1 means to the end); create writes a new file and refuses an existing path; str_replace replaces exactly one occurrence of old_str (omit new_str to remove it); insert inserts insert_text after insert_line (0 means before the first line); delete removes a file or directory recursively; rename moves a file or directory without overwriting. Directories are implicit. /memories/memory.md is the editable permanent root containing concise core agreements and topic links; keep details in files such as /memories/topics/development.md. No root or topic content is loaded automatically. When stored preferences, prior agreements or project context would help, view /memories/memory.md first, then read only the relevant linked topics. Skip unnecessary memory reads. View returns the current file. Read before editing and maintain root links yourself when creating, renaming or deleting topics. The root directory and root file cannot be deleted or renamed. All paths must stay within /memories. Files are text, backed by PostgreSQL, not the host filesystem. No versions, source IDs or coupled root-content parameters are needed.","parameters":{"type":"object","additionalProperties":false,"properties":{"command":{"type":"string","enum":["view","create","str_replace","insert","delete","rename"]},"path":{"type":"string"},"view_range":{"type":"array","items":{"type":"integer"},"minItems":2,"maxItems":2},"file_text":{"type":"string"},"old_str":{"type":"string"},"new_str":{"type":"string"},"insert_line":{"type":"integer","minimum":0},"insert_text":{"type":"string"},"old_path":{"type":"string"},"new_path":{"type":"string"}},"required":["command"]}}}`),
	Execute:    executeMemory,
}

// subagentMemoryTool 向 Subagent 仅暴露只读 view 命令，避免多个并行 Subagent 同时改写同一记忆文件造成覆盖冲突。
var subagentMemoryTool = Tool{
	Name:               "memory",
	Definition:         json.RawMessage(`{"type":"function","function":{"name":"memory","description":"Read persistent text files in this user's private /memories namespace (read-only view access for subagents). /memories/memory.md is the root containing core agreements and topic links; view it first only when user preferences or prior context would help your assigned task, then view only relevant linked topics.","parameters":{"type":"object","additionalProperties":false,"properties":{"command":{"type":"string","enum":["view"]},"path":{"type":"string"},"view_range":{"type":"array","items":{"type":"integer"},"minItems":2,"maxItems":2}},"required":["command","path"]}}}`),
	OutsideTransaction: true,
	Execute: func(ctx context.Context, deps Dependencies, _ *database.Queries, userID uuid.UUID, arguments string) (string, error) {
		var check struct {
			Command string `json:"command"`
		}
		if err := json.Unmarshal([]byte(arguments), &check); err != nil {
			return "Error: memory arguments must be valid JSON", nil
		}
		if check.Command != "view" {
			return "Error: subagents only have read-only view access to memory", nil
		}
		return executeMemory(ctx, deps, database.New(deps.DatabasePool), userID, arguments)
	},
}

// memoryInput 只表达文件操作；指针区分必填字符串缺失与允许的空文件或空插入。
type memoryInput struct {
	Command    string  `json:"command"`
	Path       string  `json:"path"`
	ViewRange  []int   `json:"view_range"`
	FileText   *string `json:"file_text"`
	OldStr     *string `json:"old_str"`
	NewStr     string  `json:"new_str"`
	InsertLine *int    `json:"insert_line"`
	InsertText *string `json:"insert_text"`
	OldPath    string  `json:"old_path"`
	NewPath    string  `json:"new_path"`
}

// executeMemory 校验工具边界后执行一个命令；数据库变更与回执沿用 Runner 的同一事务和用户写入锁。
func executeMemory(ctx context.Context, _ Dependencies, queries *database.Queries, userID uuid.UUID, arguments string) (string, error) {
	var input memoryInput
	if err := json.Unmarshal([]byte(arguments), &input); err != nil {
		return "Error: memory arguments must be valid JSON", nil
	}
	// 路径先验证，再初始化根，避免无效命令或越界路径产生任何文件操作。
	var err error
	switch input.Command {
	case "view", "create", "str_replace", "insert", "delete":
		input.Path, err = normalizeMemoryPath(input.Path)
	case "rename":
		input.OldPath, err = normalizeMemoryPath(input.OldPath)
		if err == nil {
			input.NewPath, err = normalizeMemoryPath(input.NewPath)
		}
	default:
		return "Error: unknown memory command", nil
	}
	if err != nil {
		return "Error: " + err.Error(), nil
	}
	if err := queries.EnsureMemoryRoot(ctx, userID); err != nil {
		return "", err
	}
	files, err := queries.ListMemoryFiles(ctx, userID)
	if err != nil {
		return "", err
	}
	isFile, isDirectory := memoryPathKind(files, input.Path)
	switch input.Command {
	case "view":
		if isFile {
			content, err := queries.GetMemoryFile(ctx, database.GetMemoryFileParams{UserID: userID, Path: input.Path})
			if err != nil {
				return "", err
			}
			return viewMemoryFile(input.Path, content, input.ViewRange), nil
		}
		if isDirectory {
			return viewMemoryDirectory(input.Path, files), nil
		}
		return "Error: memory path does not exist", nil
	case "create":
		if input.FileText == nil {
			return "Error: file_text is required", nil
		}
		if isFile || isDirectory {
			return "Error: memory path already exists", nil
		}
		if memoryHasFileParent(files, input.Path) {
			return "Error: a parent path is a file", nil
		}
		err := queries.CreateMemoryFile(ctx, database.CreateMemoryFileParams{UserID: userID, Path: input.Path, Content: *input.FileText})
		return "File created successfully at: " + input.Path, err
	case "str_replace", "insert":
		if !isFile {
			return "Error: memory file does not exist", nil
		}
		content, err := queries.GetMemoryFile(ctx, database.GetMemoryFileParams{UserID: userID, Path: input.Path})
		if err != nil {
			return "", err
		}
		// 精确替换拒绝未匹配和重复匹配，插入按刚读到的行号进行；不裁剪 Markdown 正文。
		if input.Command == "str_replace" {
			if input.OldStr == nil || *input.OldStr == "" {
				return "Error: a nonempty old_str is required", nil
			}
			if count := strings.Count(content, *input.OldStr); count != 1 {
				return fmt.Sprintf("Error: old_str must match exactly once; found %d occurrences. View the file and choose unique text.", count), nil
			}
			content = strings.Replace(content, *input.OldStr, input.NewStr, 1)
		} else {
			lines := memoryLines(content)
			if input.InsertLine == nil || *input.InsertLine < 0 || *input.InsertLine > len(lines) || input.InsertText == nil {
				return fmt.Sprintf("Error: insert_text and insert_line between 0 and %d are required", len(lines)), nil
			}
			if *input.InsertText != "" {
				offset := len(strings.Join(lines[:*input.InsertLine], ""))
				before, after, inserted := content[:offset], content[offset:], *input.InsertText
				if before != "" && !strings.HasSuffix(before, "\n") {
					before += "\n"
				}
				if after != "" && !strings.HasSuffix(inserted, "\n") {
					inserted += "\n"
				}
				content = before + inserted + after
			}
		}
		err = queries.UpdateMemoryFile(ctx, database.UpdateMemoryFileParams{UserID: userID, Path: input.Path, Content: content})
		return "The memory file has been edited: " + input.Path, err
	case "delete":
		if input.Path == memoryDirectory || input.Path == memoryRootPath {
			return "Error: edit the permanent memory.md root instead of deleting it", nil
		}
		if !isFile && !isDirectory {
			return "Error: memory path does not exist", nil
		}
		err := queries.DeleteMemoryPath(ctx, database.DeleteMemoryPathParams{UserID: userID, Path: input.Path})
		return "Successfully deleted " + input.Path, err
	case "rename":
		if input.OldPath == memoryDirectory || input.OldPath == memoryRootPath {
			return "Error: the memory root cannot be renamed", nil
		}
		oldFile, oldDirectory := memoryPathKind(files, input.OldPath)
		if !oldFile && !oldDirectory {
			return "Error: memory source path does not exist", nil
		}
		newFile, newDirectory := memoryPathKind(files, input.NewPath)
		if newFile || newDirectory {
			return "Error: memory destination already exists", nil
		}
		if strings.HasPrefix(input.NewPath, input.OldPath+"/") || memoryHasFileParent(files, input.NewPath) {
			return "Error: destination is inside the source or has a file parent", nil
		}
		err := queries.RenameMemoryPath(ctx, database.RenameMemoryPathParams{UserID: userID, OldPath: input.OldPath, NewPath: input.NewPath})
		return fmt.Sprintf("Successfully renamed %s to %s", input.OldPath, input.NewPath), err
	}
	return "", nil
}

// normalizeMemoryPath 归一用户私有逻辑路径，拒绝父目录跳转、反斜杠及编码后的跳转写法。
func normalizeMemoryPath(value string) (string, error) {
	decoded, err := url.PathUnescape(value)
	if err != nil || strings.ContainsAny(decoded, "\\\x00") {
		return "", errors.New("invalid memory path")
	}
	if slices.Contains(strings.Split(decoded, "/"), "..") {
		return "", errors.New("memory paths cannot traverse parent directories")
	}
	cleaned := path.Clean(value)
	if cleaned != memoryDirectory && !strings.HasPrefix(cleaned, memoryDirectory+"/") {
		return "", errors.New("memory paths must stay under /memories")
	}
	return cleaned, nil
}

// memoryPathKind 从文件清单识别文件与隐式目录，不单独持久化目录记录。
func memoryPathKind(files []database.ListMemoryFilesRow, name string) (bool, bool) {
	for _, file := range files {
		if file.Path == name {
			return true, false
		}
		if strings.HasPrefix(file.Path, name+"/") {
			return false, true
		}
	}
	return false, false
}

// memoryHasFileParent 阻止在文本文件下面创建或移动另一个文件，保持文件与目录语义一致。
func memoryHasFileParent(files []database.ListMemoryFilesRow, name string) bool {
	for _, file := range files {
		if strings.HasPrefix(name, file.Path+"/") {
			return true
		}
	}
	return false
}

// memoryLines 保留每行原始换行符，供查看与插入共用；末尾换行不额外算一行。
func memoryLines(content string) []string {
	lines := strings.SplitAfter(content, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// viewMemoryFile 按 1 起算的行号读取，过长结果截断并提示使用 view_range，不修改原正文。
func viewMemoryFile(name, content string, requested []int) string {
	lines := memoryLines(content)
	start, end := 1, len(lines)
	if requested != nil {
		if len(requested) != 2 {
			return "Error: view_range must contain [start, end]"
		}
		start, end = requested[0], requested[1]
		if end == -1 {
			end = len(lines)
		}
		if start < 1 || end < start || end > len(lines) {
			return "Error: view_range is outside the file's line range"
		}
	}
	var result strings.Builder
	fmt.Fprintf(&result, "Here's the content of %s with line numbers:\n", name)
	characters := utf8.RuneCountInString(result.String())
	for number := start; number <= end; number++ {
		line := fmt.Sprintf("%6d\t%s", number, lines[number-1])
		if !strings.HasSuffix(line, "\n") {
			line += "\n"
		}
		result.WriteString(line)
		characters += utf8.RuneCountInString(line)
		if requested == nil && characters > memoryViewCharacters {
			return string([]rune(result.String())[:memoryViewCharacters]) + "\n[Truncated; use view_range to read the remaining lines.]"
		}
	}
	return result.String()
}

// viewMemoryDirectory 以最多两层展示文件与隐式目录及其总字节数，不生成或重写根正文。
func viewMemoryDirectory(name string, files []database.ListMemoryFilesRow) string {
	entries := map[string]int64{}
	for _, file := range files {
		relative, found := strings.CutPrefix(file.Path, name+"/")
		if !found {
			continue
		}
		parts := strings.Split(relative, "/")
		if len(parts) <= 2 {
			entries[file.Path] = file.Bytes
		}
		for depth := 1; depth <= min(2, len(parts)-1); depth++ {
			entries[name+"/"+strings.Join(parts[:depth], "/")+"/"] += file.Bytes
		}
	}
	names := make([]string, 0, len(entries))
	for entry := range entries {
		names = append(names, entry)
	}
	slices.Sort(names)
	var result strings.Builder
	fmt.Fprintf(&result, "Files and directories up to 2 levels deep in %s:\n", name)
	for _, entry := range names {
		fmt.Fprintf(&result, "%d B\t%s\n", entries[entry], entry)
	}
	return result.String()
}
