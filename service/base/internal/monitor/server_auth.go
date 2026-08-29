package monitor

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	mathrand "math/rand"
	"net/http"
	"strings"
	"time"

	"easy_proxies/internal/profile"
	"easy_proxies/internal/store"
)

func writeJSON(w http.ResponseWriter, payload any) {
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(payload)
}

// withAuth 认证中间件，如果配置了密码则需要验证
func (s *Server) withAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		credentials := s.credentialSnapshot()
		password := credentials.Password
		// 如果没有配置密码，直接放行
		if password == "" {
			next(w, r)
			return
		}

		// 检查 Cookie 中的 session token
		cookie, err := r.Cookie("session_token")
		if err == nil && s.validateSession(cookie.Value) {
			next(w, r)
			return
		}

		// 检查 Authorization header:
		// 1. Bearer session token (WebUI login flow)
		// 2. Raw management password / Bearer management password (service-to-service flow)
		authHeader := strings.TrimSpace(r.Header.Get("Authorization"))
		if authHeader != "" {
			if token, ok := bearerTokenFromHeader(authHeader); ok && s.validateSession(token) {
				next(w, r)
				return
			}
			if credentials.Username != "" {
				if username, suppliedPassword, ok := r.BasicAuth(); ok && secureComparePair(
					username,
					suppliedPassword,
					credentials.Username,
					credentials.Password,
				) {
					next(w, r)
					return
				}
			}
			if s.validateManagementPassword(authHeader) {
				next(w, r)
				return
			}
		}

		// 未授权
		w.WriteHeader(http.StatusUnauthorized)
		writeJSON(w, map[string]any{"error": "未授权，请先登录"})
	}
}

func bearerTokenFromHeader(authHeader string) (string, bool) {
	const prefix = "Bearer "
	if len(authHeader) < len(prefix) || !strings.EqualFold(authHeader[:len(prefix)], prefix) {
		return "", false
	}
	return strings.TrimSpace(authHeader[len(prefix):]), true
}

func (s *Server) validateManagementPassword(authHeader string) bool {
	password := s.credentialSnapshot().Password
	if password == "" {
		return false
	}

	if secureCompareStrings(authHeader, password) {
		return true
	}

	token, ok := bearerTokenFromHeader(authHeader)
	if !ok {
		return false
	}

	return secureCompareStrings(token, password)
}

func (s *Server) credentialSnapshot() profile.CredentialSnapshot {
	if manager := s.profileManagerSnapshot(); manager != nil && manager.LocalServerEnabled() {
		credentials := manager.Credentials()
		if credentials.Generation == 0 {
			credentials.Generation = 1
		}
		return credentials
	}
	return profile.CredentialSnapshot{Password: s.managementPassword(), Generation: 1}
}

func (s *Server) managementPassword() string {
	if s == nil {
		return ""
	}
	s.cfgMu.RLock()
	defer s.cfgMu.RUnlock()
	return s.cfg.Password
}

func (s *Server) runtimeConfig() Config {
	if s == nil {
		return Config{}
	}
	s.cfgMu.RLock()
	defer s.cfgMu.RUnlock()
	cfg := s.cfg
	cfg.ProbeTargets = append([]string(nil), s.cfg.ProbeTargets...)
	return cfg
}

// handleAuth 处理登录认证
func (s *Server) handleAuth(w http.ResponseWriter, r *http.Request) {
	credentials := s.credentialSnapshot()
	password := credentials.Password
	// 如果没有配置密码，直接返回成功（不需要token）
	if password == "" {
		writeJSON(w, map[string]any{"message": "无需密码", "no_password": true})
		return
	}

	// GET 请求用于检查是否需要密码（供前端初始化时使用）
	if r.Method == http.MethodGet {
		if credentials.Username != "" {
			writeJSON(w, map[string]any{
				"message":           "需要用户名和密码",
				"auth_mode":         "canonical_pair",
				"username_required": true,
				"no_password":       false,
			})
			return
		}
		writeJSON(w, map[string]any{"message": "需要密码", "auth_mode": "password", "username_required": false, "no_password": false})
		return
	}

	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		writeJSON(w, map[string]any{"error": "请求格式错误"})
		return
	}

	// 使用 constant-time 比较防止时序攻击
	valid := secureCompareStrings(req.Password, password)
	if credentials.Username != "" {
		valid = secureComparePair(req.Username, req.Password, credentials.Username, credentials.Password)
	}
	if !valid {
		// 添加随机延迟防止暴力破解
		time.Sleep(time.Duration(100+mathrand.Intn(200)) * time.Millisecond)
		w.WriteHeader(http.StatusUnauthorized)
		writeJSON(w, map[string]any{"error": "密码错误"})
		return
	}

	// 创建新会话
	session, err := s.createSession()
	if err != nil {
		s.logger.Printf("Failed to create session: %v", err)
		w.WriteHeader(http.StatusInternalServerError)
		writeJSON(w, map[string]any{"error": "服务器错误"})
		return
	}

	// 设置 HttpOnly Cookie
	isSecure := r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
	http.SetCookie(w, &http.Cookie{
		Name:     "session_token",
		Value:    session.Token,
		Path:     "/",
		HttpOnly: true,
		Secure:   isSecure,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   int(s.sessionTTL.Seconds()),
	})

	writeJSON(w, map[string]any{
		"message": "登录成功",
		"token":   session.Token,
	})
}

// handleExport 导出所有当前有效可用节点的原始代理 URI（如 trojan://、vless:// 等），每行一个。
// “有效可用”包括探测可用节点，以及最近被真实流量证明可用的节点。

func (s *Server) generateSessionToken() (string, error) {
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		return "", fmt.Errorf("failed to generate session token: %w", err)
	}
	return hex.EncodeToString(tokenBytes), nil
}

// createSession creates a new session with expiration.
func (s *Server) createSession() (*Session, error) {
	token, err := s.generateSessionToken()
	if err != nil {
		return nil, err
	}

	now := time.Now()
	generation := s.credentialSnapshot().Generation
	if generation == 0 {
		generation = 1
	}
	session := &Session{
		Token:                token,
		CreatedAt:            now,
		ExpiresAt:            now.Add(s.sessionTTL),
		CredentialGeneration: generation,
	}

	// Persist to Store if available
	storeRef := s.storeSnapshot()
	if storeRef != nil {
		storeSession := &store.Session{
			Token:                session.Token,
			CreatedAt:            session.CreatedAt,
			ExpiresAt:            session.ExpiresAt,
			CredentialGeneration: session.CredentialGeneration,
		}
		if err := storeRef.CreateSession(context.Background(), storeSession); err != nil {
			s.logger.Printf("Failed to persist session to store: %v", err)
		}
	}

	// Also keep in memory for fast lookups
	s.sessionMu.Lock()
	s.sessions[token] = session
	s.sessionMu.Unlock()

	return session, nil
}

// validateSession checks if a session token is valid and not expired.
func (s *Server) validateSession(token string) bool {
	storeRef := s.storeSnapshot()
	currentGeneration := s.credentialSnapshot().Generation
	if currentGeneration == 0 {
		currentGeneration = 1
	}
	// Check in-memory cache first
	s.sessionMu.RLock()
	session, exists := s.sessions[token]
	s.sessionMu.RUnlock()

	if exists {
		sessionGeneration := session.CredentialGeneration
		if sessionGeneration == 0 {
			sessionGeneration = 1
		}
		if time.Now().After(session.ExpiresAt) || sessionGeneration != currentGeneration {
			s.sessionMu.Lock()
			delete(s.sessions, token)
			s.sessionMu.Unlock()
			// Also delete from store
			if storeRef != nil {
				_ = storeRef.DeleteSession(context.Background(), token)
			}
			return false
		}
		return true
	}

	// Fallback: check Store (e.g., after restart)
	if storeRef != nil {
		storeSess, err := storeRef.GetSession(context.Background(), token)
		if err != nil || storeSess == nil {
			return false
		}
		storeGeneration := storeSess.CredentialGeneration
		if storeGeneration == 0 {
			storeGeneration = 1
		}
		if time.Now().After(storeSess.ExpiresAt) || storeGeneration != currentGeneration {
			_ = storeRef.DeleteSession(context.Background(), token)
			return false
		}
		// Restore to in-memory cache
		s.sessionMu.Lock()
		s.sessions[token] = &Session{
			Token:                storeSess.Token,
			CreatedAt:            storeSess.CreatedAt,
			ExpiresAt:            storeSess.ExpiresAt,
			CredentialGeneration: storeGeneration,
		}
		s.sessionMu.Unlock()
		return true
	}

	return false
}

// cleanupExpiredSessions periodically removes expired sessions.
func (s *Server) cleanupExpiredSessions() {
	ticker := time.NewTicker(1 * time.Hour)
	defer ticker.Stop()

	for {
		select {
		case <-s.done:
			return
		case <-ticker.C:
			now := time.Now()
			s.sessionMu.Lock()
			for token, session := range s.sessions {
				if now.After(session.ExpiresAt) {
					delete(s.sessions, token)
				}
			}
			s.sessionMu.Unlock()

			// Also cleanup in Store
			if storeRef := s.storeSnapshot(); storeRef != nil {
				_ = storeRef.CleanupExpiredSessions(context.Background())
			}
		}
	}
}

// secureCompareStrings performs constant-time string comparison to prevent timing attacks.
func secureCompareStrings(a, b string) bool {
	aHash := sha256.Sum256([]byte(a))
	bHash := sha256.Sum256([]byte(b))
	return subtle.ConstantTimeCompare(aHash[:], bHash[:]) == 1
}

func secureComparePair(username, password, expectedUsername, expectedPassword string) bool {
	usernameOK := secureCompareStrings(username, expectedUsername)
	passwordOK := secureCompareStrings(password, expectedPassword)
	return usernameOK && passwordOK
}
