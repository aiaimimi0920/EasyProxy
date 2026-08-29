package monitor

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"easy_proxies/internal/config"
)

func (s *Server) handleExport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	// 只导出当前有效可用的节点
	snapshots := s.mgr.SnapshotFiltered(true)
	var lines []string

	for _, snap := range snapshots {
		// 导出节点的原始 URI
		if snap.URI == "" {
			continue
		}
		lines = append(lines, snap.URI)
	}

	// 返回纯文本，每行一个 URI
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename=nodes_export.txt")
	_, _ = w.Write([]byte(strings.Join(lines, "\n")))
}

// handleImport 导入节点 URI 列表（每行一个），支持与导出格式互通
func (s *Server) handleImport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	nodeManager, ok := s.requireNodeManager(w)
	if !ok {
		return
	}

	var req struct {
		Content string `json:"content"` // 节点 URI 文本，每行一个
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 10<<20)).Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		writeJSON(w, map[string]any{"error": "请求格式错误"})
		return
	}

	content := strings.TrimSpace(req.Content)
	if content == "" {
		w.WriteHeader(http.StatusBadRequest)
		writeJSON(w, map[string]any{"error": "导入内容为空"})
		return
	}

	// 解析每行 URI
	lines := strings.Split(content, "\n")
	var imported int
	var errs []string

	for _, line := range lines {
		line = strings.TrimSpace(line)
		// 跳过空行和注释
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		// 验证是否为合法代理 URI
		if !config.IsProxyURI(line) {
			errs = append(errs, fmt.Sprintf("无效的代理 URI: %s", truncateStr(line, 60)))
			continue
		}

		// 从 URI 中提取名称
		name := ""
		if parsed, err := url.Parse(line); err == nil && parsed.Fragment != "" {
			if decoded, decErr := url.QueryUnescape(parsed.Fragment); decErr == nil {
				name = decoded
			} else {
				name = parsed.Fragment
			}
		}
		if name == "" {
			name = fmt.Sprintf("imported-%d", imported+1)
		}

		node := config.NodeConfig{
			Name: name,
			URI:  line,
		}

		if _, err := nodeManager.CreateNode(r.Context(), node); err != nil {
			errs = append(errs, fmt.Sprintf("添加节点 %q 失败: %v", name, err))
			continue
		}
		imported++
	}

	result := map[string]any{
		"message":  fmt.Sprintf("成功导入 %d 个节点", imported),
		"imported": imported,
	}
	if len(errs) > 0 {
		result["errors"] = errs
	}
	writeJSON(w, result)
}

// truncateStr truncates a string to maxLen and appends "..." if truncated.
func truncateStr(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// handleSettings handles GET/PUT for all system settings.
func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		if s.localServerSettingsMode() {
			writeJSON(w, s.getLocalServerSettings())
			return
		}
		resp := s.getAllSettings()
		writeJSON(w, resp)
	case http.MethodPut:
		body, err := io.ReadAll(io.LimitReader(r.Body, 10<<20))
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			writeJSON(w, map[string]any{"error": "请求格式错误"})
			return
		}
		var req allSettingsRequest
		if err := json.Unmarshal(body, &req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			writeJSON(w, map[string]any{"error": "请求格式错误"})
			return
		}
		if s.localServerSettingsMode() {
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(body, &fields); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				writeJSON(w, map[string]any{"error": "请求格式错误"})
				return
			}
			if nonEmptyJSONField(fields, "listener_password") ||
				nonEmptyJSONField(fields, "management_password") ||
				nonEmptyJSONField(fields, "multi_port_password") {
				w.WriteHeader(http.StatusConflict)
				writeJSON(w, map[string]any{"error": "credential_source_conflict"})
				return
			}
		}

		needReload, err := s.updateAllSettingsWithReload(req)
		if err != nil {
			if errors.Is(err, errReloadInProgress) {
				w.WriteHeader(http.StatusConflict)
				writeJSON(w, map[string]any{"error": "配置正在重载，请稍后重试", "need_reload": true})
				return
			}
			w.WriteHeader(http.StatusInternalServerError)
			writeJSON(w, map[string]any{"error": err.Error()})
			return
		}

		writeJSON(w, map[string]any{
			"message":     "设置已保存",
			"need_reload": needReload,
		})
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func nonEmptyJSONField(fields map[string]json.RawMessage, key string) bool {
	raw, ok := fields[key]
	if !ok {
		return false
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return true
	}
	return strings.TrimSpace(value) != ""
}
