// 本文件用同一词表过滤所有模型请求中的消息文本，并保留工具调用和渠道协议字段。

package agent

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"
)

// modelFilterWords 是编译进后端的词表，每行一个字面匹配词。
//
//go:embed model_filter_words.txt
var modelFilterWords string

// modelTextReplacer 在进程内共用，按词表顺序解决同一位置的匹配。
var modelTextReplacer = newModelTextReplacer()

// newModelTextReplacer 从词表生成替换规则，空行不参与匹配。
func newModelTextReplacer() *strings.Replacer {
	var replacements []string
	for _, line := range strings.Split(modelFilterWords, "\n") {
		if word := strings.TrimSpace(line); word != "" {
			replacements = append(replacements, word, "[已过滤]")
		}
	}
	return strings.NewReplacer(replacements...)
}

// filterModelMessages 只改请求副本。Gemini 和 Claude 可能直接回放原生正文，因此两种正文都要处理。
func filterModelMessages(messages []json.RawMessage) ([]json.RawMessage, error) {
	textPaths := [][]string{{"content"}, {"claude_content", "text"}, {"gemini_content", "parts", "text"}}
	filtered := make([]json.RawMessage, len(messages))
	for index, raw := range messages {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil {
			return nil, fmt.Errorf("decode model message %d: %w", index, err)
		}
		filtered[index] = raw
		changed := false
		for _, path := range textPaths {
			value := fields[path[0]]
			replacement, err := filterModelText(value, path[1:]...)
			if err != nil {
				return nil, fmt.Errorf("filter model message %d: %w", index, err)
			}
			if !bytes.Equal(value, replacement) {
				fields[path[0]] = replacement
				changed = true
			}
		}
		if changed {
			encoded, err := json.Marshal(fields)
			if err != nil {
				return nil, err
			}
			filtered[index] = encoded
		}
	}
	return filtered, nil
}

// filterModelText 按字段路径过滤文本，数组把同一路径应用到每个元素。
// 通用 content 可直接是字符串，也可由带 text 字段的内容块组成。
func filterModelText(raw json.RawMessage, path ...string) (json.RawMessage, error) {
	if len(raw) == 0 {
		return raw, nil
	}
	switch raw[0] {
	case '"':
		if len(path) > 0 {
			return raw, nil
		}
		var body string
		if err := json.Unmarshal(raw, &body); err != nil {
			return nil, err
		}
		filtered := modelTextReplacer.Replace(body)
		if filtered == body {
			return raw, nil
		}
		return json.Marshal(filtered)
	case '[':
		var blocks []json.RawMessage
		if err := json.Unmarshal(raw, &blocks); err != nil {
			return nil, err
		}
		if len(path) == 0 {
			path = []string{"text"}
		}
		changed := false
		for index, block := range blocks {
			filtered, err := filterModelText(block, path...)
			if err != nil {
				return nil, err
			}
			changed = changed || !bytes.Equal(block, filtered)
			blocks[index] = filtered
		}
		if changed {
			return json.Marshal(blocks)
		}
	case '{':
		if len(path) == 0 {
			return raw, nil
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil {
			return nil, err
		}
		// Gemini 的推理块与签名一起回传；只过滤面向用户的正文。
		if path[0] == "text" && string(fields["thought"]) == "true" {
			return raw, nil
		}
		value := fields[path[0]]
		filtered, err := filterModelText(value, path[1:]...)
		if err != nil {
			return nil, err
		}
		if !bytes.Equal(value, filtered) {
			fields[path[0]] = filtered
			return json.Marshal(fields)
		}
	}
	return raw, nil
}
