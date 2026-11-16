// 本文件提供 App 的只读记忆查看接口：校验聊天会话并按当前用户列出记忆文件元信息及读取正文。

package httpapi

import (
	"errors"
	"net/http"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/OmniNetOpensource/aether-personal/backend/internal/database"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// userMemoryFileSummary 描述单个记忆文件的路径、字节数与更新时间，用于前端生成资源管理器目录树。
type userMemoryFileSummary struct {
	Path      string    `json:"path"`
	Bytes     int64     `json:"bytes"`
	UpdatedAt time.Time `json:"updated_at"`
}

// userMemoryFileDetail 返回单个记忆文件的完整正文和元信息，供前端渲染预览与查看源码。
type userMemoryFileDetail struct {
	Path      string    `json:"path"`
	Content   string    `json:"content"`
	Bytes     int64     `json:"bytes"`
	UpdatedAt time.Time `json:"updated_at"`
}

// listMemoryFiles 按路径顺序返回当前用户已保存的记忆文件列表；无记录时返回空数组而不自动创建文件。
func (s *server) listMemoryFiles(w http.ResponseWriter, r *http.Request, userID uuid.UUID) {
	rows, err := s.queries.ListUserMemoryFiles(r.Context(), userID)
	if err != nil {
		internalError(w, "list user memory files", err)
		return
	}
	files := make([]userMemoryFileSummary, 0, len(rows))
	for _, row := range rows {
		files = append(files, userMemoryFileSummary{
			Path:      row.Path,
			Bytes:     row.Bytes,
			UpdatedAt: row.UpdatedAt,
		})
	}
	writeAdminJSON(w, struct {
		Files []userMemoryFileSummary `json:"files"`
	}{
		Files: files,
	})
}

// getMemoryFileContent 校验请求的逻辑路径后返回单个记忆文件的只读正文与更新时间。
func (s *server) getMemoryFileContent(w http.ResponseWriter, r *http.Request, userID uuid.UUID) {
	filePath, ok := normalizeMemoryFilePath(r.URL.Query().Get("path"))
	if !ok {
		http.Error(w, "记忆文件路径无效", http.StatusBadRequest)
		return
	}
	row, err := s.queries.GetUserMemoryFile(r.Context(), database.GetUserMemoryFileParams{
		UserID: userID,
		Path:   filePath,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		http.Error(w, "找不到该记忆文件", http.StatusNotFound)
		return
	}
	if err != nil {
		internalError(w, "get user memory file", err)
		return
	}
	writeAdminJSON(w, userMemoryFileDetail{
		Path:      row.Path,
		Content:   row.Content,
		Bytes:     row.Bytes,
		UpdatedAt: row.UpdatedAt,
	})
}

// normalizeMemoryFilePath 约束查询路径必须位于 /memories/ 之下，防止反斜杠或父目录跳转写法。
func normalizeMemoryFilePath(raw string) (string, bool) {
	if raw == "" || strings.ContainsAny(raw, "\\\x00") {
		return "", false
	}
	if slices.Contains(strings.Split(raw, "/"), "..") {
		return "", false
	}
	cleaned := path.Clean(raw)
	if !strings.HasPrefix(cleaned, "/memories/") || cleaned == "/memories/" {
		return "", false
	}
	return cleaned, true
}
