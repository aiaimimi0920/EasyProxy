package monitor

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"

	"easy_proxies/internal/config"
)

// ConnectorManager exposes local connector CRUD and preferred-IP generation.
type ConnectorManager interface {
	ListConfigConnectors(ctx context.Context) ([]config.ConnectorSourceConfig, error)
	CreateConnector(ctx context.Context, connector config.ConnectorSourceConfig) (config.ConnectorSourceConfig, error)
	UpdateConnector(ctx context.Context, name string, connector config.ConnectorSourceConfig) (config.ConnectorSourceConfig, error)
	DeleteConnector(ctx context.Context, name string) error
	SetConnectorEnabled(ctx context.Context, name string, enabled bool) error
	RefreshRuntimeSources(ctx context.Context) error
	RefreshPreferredEntryIPs(ctx context.Context, name string, options PreferredIPRefreshOptions) (*PreferredIPRefreshResult, error)
}

var (
	ErrConnectorNotFound = errors.New("连接器不存在")
	ErrConnectorConflict = errors.New("连接器名称已存在")
	ErrInvalidConnector  = errors.New("无效的连接器配置")
)

type PreferredIPRefreshOptions struct {
	TopCount       int     `json:"top_count"`
	LatencyThreads int     `json:"latency_threads"`
	LatencySamples int     `json:"latency_samples"`
	MaxLossRate    float64 `json:"max_loss_rate"`
	AllIP          bool    `json:"all_ip"`
}

type PreferredIPSelection struct {
	IP               string  `json:"ip"`
	AverageLatencyMs float64 `json:"average_latency_ms"`
	LossRate         float64 `json:"loss_rate"`
	SpeedMBS         float64 `json:"speed_mb_s"`
	Colo             string  `json:"colo"`
}

type PreferredIPRefreshResult struct {
	TemplateName        string                         `json:"template_name"`
	ArtifactDir         string                         `json:"artifact_dir"`
	ResultCSV           string                         `json:"result_csv"`
	SelectedIPs         []PreferredIPSelection         `json:"selected_ips"`
	GeneratedConnectors []config.ConnectorSourceConfig `json:"generated_connectors"`
	RuntimeRefreshed    bool                           `json:"runtime_refreshed"`
}

type connectorPayload struct {
	Name            string         `json:"name"`
	Input           string         `json:"input"`
	Enabled         *bool          `json:"enabled"`
	TemplateOnly    *bool          `json:"template_only"`
	Group           string         `json:"group"`
	Notes           string         `json:"notes"`
	ConnectorType   string         `json:"connector_type"`
	ConnectorConfig map[string]any `json:"connector_config"`
}

func (p connectorPayload) toConfig(existing *config.ConnectorSourceConfig) config.ConnectorSourceConfig {
	connector := config.ConnectorSourceConfig{
		Name:            p.Name,
		Input:           p.Input,
		Group:           p.Group,
		Notes:           p.Notes,
		ConnectorType:   p.ConnectorType,
		ConnectorConfig: p.ConnectorConfig,
	}
	if existing != nil {
		connector.Enabled = existing.Enabled
		connector.TemplateOnly = existing.TemplateOnly
	}
	if p.Enabled != nil {
		connector.Enabled = *p.Enabled
	} else if existing == nil {
		connector.Enabled = true
	}
	if p.TemplateOnly != nil {
		connector.TemplateOnly = *p.TemplateOnly
	}
	return connector
}

func (s *Server) SetConnectorManager(cm ConnectorManager) {
	if s != nil {
		s.connectorMgr = cm
	}
}

func (s *Server) ensureConnectorManager(w http.ResponseWriter) bool {
	if s.connectorMgr == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		writeJSON(w, map[string]any{"error": "连接器管理未启用"})
		return false
	}
	return true
}

func (s *Server) respondConnectorError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	switch {
	case errors.Is(err, ErrConnectorNotFound):
		status = http.StatusNotFound
	case errors.Is(err, ErrConnectorConflict), errors.Is(err, ErrInvalidConnector):
		status = http.StatusBadRequest
	}
	w.WriteHeader(status)
	writeJSON(w, map[string]any{"error": err.Error()})
}

func (s *Server) handleConfigConnectors(w http.ResponseWriter, r *http.Request) {
	if !s.ensureConnectorManager(w) {
		return
	}

	switch r.Method {
	case http.MethodGet:
		connectors, err := s.connectorMgr.ListConfigConnectors(r.Context())
		if err != nil {
			s.respondConnectorError(w, err)
			return
		}
		writeJSON(w, map[string]any{"connectors": connectors})
	case http.MethodPost:
		var payload connectorPayload
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			writeJSON(w, map[string]any{"error": "请求格式错误"})
			return
		}
		connector, err := s.connectorMgr.CreateConnector(r.Context(), payload.toConfig(nil))
		if err != nil {
			s.respondConnectorError(w, err)
			return
		}
		message := "连接器已添加"
		if err := s.connectorMgr.RefreshRuntimeSources(r.Context()); err != nil {
			message += "，但自动刷新失败，请手动刷新"
		} else {
			message += "，并已自动刷新"
		}
		writeJSON(w, map[string]any{"connector": connector, "message": message})
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleConfigConnectorItem(w http.ResponseWriter, r *http.Request) {
	if !s.ensureConnectorManager(w) {
		return
	}

	namePart := strings.TrimPrefix(r.URL.Path, "/api/connectors/config/")
	if idx := strings.Index(namePart, "/"); idx >= 0 {
		namePart = namePart[:idx]
	}
	connectorName, err := url.PathUnescape(namePart)
	if err != nil || connectorName == "" {
		w.WriteHeader(http.StatusBadRequest)
		writeJSON(w, map[string]any{"error": "连接器名称无效"})
		return
	}

	switch r.Method {
	case http.MethodPut:
		var payload connectorPayload
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			writeJSON(w, map[string]any{"error": "请求格式错误"})
			return
		}
		currentConnectors, err := s.connectorMgr.ListConfigConnectors(r.Context())
		if err != nil {
			s.respondConnectorError(w, err)
			return
		}
		var existing *config.ConnectorSourceConfig
		for idx := range currentConnectors {
			if currentConnectors[idx].Name == connectorName {
				copied := currentConnectors[idx]
				existing = &copied
				break
			}
		}
		connector, err := s.connectorMgr.UpdateConnector(r.Context(), connectorName, payload.toConfig(existing))
		if err != nil {
			s.respondConnectorError(w, err)
			return
		}
		message := "连接器已更新"
		if err := s.connectorMgr.RefreshRuntimeSources(r.Context()); err != nil {
			message += "，但自动刷新失败，请手动刷新"
		} else {
			message += "，并已自动刷新"
		}
		writeJSON(w, map[string]any{"connector": connector, "message": message})
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
		if err := s.connectorMgr.SetConnectorEnabled(r.Context(), connectorName, *body.Enabled); err != nil {
			s.respondConnectorError(w, err)
			return
		}
		action := "已启用"
		if !*body.Enabled {
			action = "已禁用"
		}
		if err := s.connectorMgr.RefreshRuntimeSources(r.Context()); err != nil {
			action += "，但自动刷新失败，请手动刷新"
		} else {
			action += "，并已自动刷新"
		}
		writeJSON(w, map[string]any{"message": action})
	case http.MethodDelete:
		if err := s.connectorMgr.DeleteConnector(r.Context(), connectorName); err != nil {
			s.respondConnectorError(w, err)
			return
		}
		message := "连接器已删除"
		if err := s.connectorMgr.RefreshRuntimeSources(r.Context()); err != nil {
			message += "，但自动刷新失败，请手动刷新"
		} else {
			message += "，并已自动刷新"
		}
		writeJSON(w, map[string]any{"message": message})
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleConnectorPreferredIPRefresh(w http.ResponseWriter, r *http.Request) {
	if !s.ensureConnectorManager(w) {
		return
	}
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	namePart := strings.TrimPrefix(r.URL.Path, "/api/connectors/config/")
	namePart = strings.TrimSuffix(namePart, "/preferred-ips/refresh")
	connectorName, err := url.PathUnescape(namePart)
	if err != nil || connectorName == "" {
		w.WriteHeader(http.StatusBadRequest)
		writeJSON(w, map[string]any{"error": "连接器名称无效"})
		return
	}

	var options PreferredIPRefreshOptions
	if r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&options); err != nil && !errors.Is(err, io.EOF) {
			w.WriteHeader(http.StatusBadRequest)
			writeJSON(w, map[string]any{"error": "请求格式错误"})
			return
		}
	}

	result, err := s.connectorMgr.RefreshPreferredEntryIPs(r.Context(), connectorName, options)
	if err != nil {
		s.respondConnectorError(w, err)
		return
	}
	writeJSON(w, map[string]any{
		"message": "优选入口 IP 已刷新",
		"result":  result,
	})
}
