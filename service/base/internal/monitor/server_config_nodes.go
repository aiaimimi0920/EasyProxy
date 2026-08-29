package monitor

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"easy_proxies/internal/config"
)

type nodePayload struct {
	Name     string `json:"name"`
	URI      string `json:"uri"`
	Port     uint16 `json:"port"`
	Username string `json:"username"`
	Password string `json:"password"`
}

func (p nodePayload) toConfig() config.NodeConfig {
	return config.NodeConfig{
		Name:     p.Name,
		URI:      p.URI,
		Port:     p.Port,
		Username: p.Username,
		Password: p.Password,
	}
}

// handleConfigNodes handles GET (list) and POST (create) for config nodes.
func (s *Server) handleConfigNodes(w http.ResponseWriter, r *http.Request) {
	nodeManager, ok := s.requireNodeManager(w)
	if !ok {
		return
	}

	switch r.Method {
	case http.MethodGet:
		nodes, err := nodeManager.ListConfigNodes(r.Context())
		if err != nil {
			s.respondNodeError(w, err)
			return
		}
		writeJSON(w, map[string]any{"nodes": nodes})
	case http.MethodPost:
		var payload nodePayload
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			writeJSON(w, map[string]any{"error": "请求格式错误"})
			return
		}
		node, err := nodeManager.CreateNode(r.Context(), payload.toConfig())
		if err != nil {
			s.respondNodeError(w, err)
			return
		}
		writeJSON(w, map[string]any{"node": node, "message": "节点已添加，请点击重载使配置生效"})
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// handleConfigNodeItem handles PUT (update) and DELETE for a specific config node.
func (s *Server) handleConfigNodeItem(w http.ResponseWriter, r *http.Request) {
	nodeManager, ok := s.requireNodeManager(w)
	if !ok {
		return
	}

	namePart := strings.TrimPrefix(r.URL.Path, "/api/nodes/config/")
	nodeName, err := url.PathUnescape(namePart)
	if err != nil || nodeName == "" {
		w.WriteHeader(http.StatusBadRequest)
		writeJSON(w, map[string]any{"error": "节点名称无效"})
		return
	}

	switch r.Method {
	case http.MethodPut:
		var payload nodePayload
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			writeJSON(w, map[string]any{"error": "请求格式错误"})
			return
		}
		node, err := nodeManager.UpdateNode(r.Context(), nodeName, payload.toConfig())
		if err != nil {
			s.respondNodeError(w, err)
			return
		}
		writeJSON(w, map[string]any{"node": node, "message": "节点已更新，请点击重载使配置生效"})
	case http.MethodPatch:
		var body struct {
			Enabled *bool `json:"enabled"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			writeJSON(w, map[string]any{"error": "请求格式错误"})
			return
		}
		if body.Enabled == nil {
			w.WriteHeader(http.StatusBadRequest)
			writeJSON(w, map[string]any{"error": "缺少 enabled 字段"})
			return
		}
		if err := nodeManager.SetNodeEnabled(r.Context(), nodeName, *body.Enabled); err != nil {
			s.respondNodeError(w, err)
			return
		}
		action := "已启用"
		if !*body.Enabled {
			action = "已禁用"
		}
		// Auto-reload after toggle
		reloadMsg := ""
		if err := nodeManager.TriggerReload(r.Context()); err != nil {
			s.logger.Printf("auto-reload after toggle failed: %v", err)
			reloadMsg = "（自动重载失败，请手动重载）"
		} else {
			reloadMsg = "（已自动重载）"
		}
		writeJSON(w, map[string]any{"message": fmt.Sprintf("节点 %s %s%s", nodeName, action, reloadMsg)})
	case http.MethodDelete:
		if err := nodeManager.DeleteNode(r.Context(), nodeName); err != nil {
			s.respondNodeError(w, err)
			return
		}
		writeJSON(w, map[string]any{"message": "节点已删除，请点击重载使配置生效"})
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// handleConfigNodesBatchToggle handles batch enable/disable for multiple nodes.
func (s *Server) handleConfigNodesBatchToggle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	nodeManager, ok := s.requireNodeManager(w)
	if !ok {
		return
	}

	var body struct {
		Names   []string `json:"names"`
		Enabled bool     `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		writeJSON(w, map[string]any{"error": "请求格式错误"})
		return
	}
	if len(body.Names) == 0 {
		w.WriteHeader(http.StatusBadRequest)
		writeJSON(w, map[string]any{"error": "节点列表为空"})
		return
	}

	var errs []string
	successCount := 0
	for _, name := range body.Names {
		if err := nodeManager.SetNodeEnabled(r.Context(), name, body.Enabled); err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", name, err))
		} else {
			successCount++
		}
	}

	action := "启用"
	if !body.Enabled {
		action = "禁用"
	}

	// Auto-reload after batch toggle
	reloadMsg := ""
	if successCount > 0 {
		if err := nodeManager.TriggerReload(r.Context()); err != nil {
			s.logger.Printf("auto-reload after batch toggle failed: %v", err)
			reloadMsg = "（自动重载失败，请手动重载）"
		} else {
			reloadMsg = "（已自动重载）"
		}
	}

	result := map[string]any{
		"message": fmt.Sprintf("成功%s %d 个节点%s", action, successCount, reloadMsg),
		"success": successCount,
		"total":   len(body.Names),
	}
	if len(errs) > 0 {
		result["errors"] = errs
	}
	writeJSON(w, result)
}

// handleConfigNodesBatchDelete handles batch deletion for multiple nodes.
func (s *Server) handleConfigNodesBatchDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	nodeManager, ok := s.requireNodeManager(w)
	if !ok {
		return
	}

	var body struct {
		Names []string `json:"names"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		writeJSON(w, map[string]any{"error": "请求格式错误"})
		return
	}
	if len(body.Names) == 0 {
		w.WriteHeader(http.StatusBadRequest)
		writeJSON(w, map[string]any{"error": "节点列表为空"})
		return
	}

	var errs []string
	successCount := 0
	for _, name := range body.Names {
		if err := nodeManager.DeleteNode(r.Context(), name); err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", name, err))
		} else {
			successCount++
		}
	}

	// Auto-reload after batch delete
	reloadMsg := ""
	if successCount > 0 {
		if err := nodeManager.TriggerReload(r.Context()); err != nil {
			s.logger.Printf("auto-reload after batch delete failed: %v", err)
			reloadMsg = "（自动重载失败，请手动重载）"
		} else {
			reloadMsg = "（已自动重载）"
		}
	}

	result := map[string]any{
		"message": fmt.Sprintf("成功删除 %d 个节点%s", successCount, reloadMsg),
		"success": successCount,
		"total":   len(body.Names),
	}
	if len(errs) > 0 {
		result["errors"] = errs
	}
	writeJSON(w, result)
}

// handleReload triggers a configuration reload.
func (s *Server) handleReload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	nodeManager, ok := s.requireNodeManager(w)
	if !ok {
		return
	}

	if err := nodeManager.TriggerReload(r.Context()); err != nil {
		s.respondNodeError(w, err)
		return
	}
	writeJSON(w, map[string]any{
		"message": "重载成功，现有连接已被中断",
	})
}

func (s *Server) requireNodeManager(w http.ResponseWriter) (NodeManager, bool) {
	nodeManager := s.nodeManagerSnapshot()
	if nodeManager == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		writeJSON(w, map[string]any{"error": "节点管理未启用"})
		return nil, false
	}
	return nodeManager, true
}

func (s *Server) respondNodeError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	switch {
	case errors.Is(err, ErrNodeNotFound):
		status = http.StatusNotFound
	case errors.Is(err, ErrNodeConflict), errors.Is(err, ErrInvalidNode):
		status = http.StatusBadRequest
	}
	w.WriteHeader(status)
	writeJSON(w, map[string]any{"error": err.Error()})
}

// Session management functions

// generateSessionToken creates a cryptographically secure random token.
