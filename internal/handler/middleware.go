package handler

import (
	"crypto/rand"
	"encoding/hex"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	dashboardAuthMaxFailures = 5
	dashboardAuthWindow      = 10 * time.Minute
	dashboardAuthLockout     = 30 * time.Minute

	defaultMaxRequestBodyBytes int64 = 25 * 1024 * 1024
)

type lockoutEntry struct {
	count        int
	firstFailed  time.Time
	blockedUntil time.Time
}

type lockoutState struct {
	mu      sync.Mutex
	entries map[string]lockoutEntry
	now     func() time.Time
}

type LockoutStatus struct {
	Blocked      bool          `json:"blocked"`
	RetryAfter   time.Duration `json:"-"`
	RetryAfterMS int64         `json:"retry_after_ms"`
	Count        int           `json:"count"`
}

var dashboardLockout = &lockoutState{entries: map[string]lockoutEntry{}, now: time.Now}

func CORSMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		setCORSHeaders(w)
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func MaxBodyMiddleware(maxBytes int64) func(http.Handler) http.Handler {
	if maxBytes <= 0 {
		maxBytes = defaultMaxRequestBodyBytes
	}
	return MaxBodyMiddlewareFunc(func() int64 { return maxBytes })
}

func MaxBodyMiddlewareFunc(maxBytes func() int64) func(http.Handler) http.Handler {
	if maxBytes == nil {
		maxBytes = func() int64 { return defaultMaxRequestBodyBytes }
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if shouldLimitRequestBody(r) {
				limit := maxBytes()
				if limit <= 0 {
					limit = defaultMaxRequestBodyBytes
				}
				r.Body = http.MaxBytesReader(w, r.Body, limit)
			}
			next.ServeHTTP(w, r)
		})
	}
}

func shouldLimitRequestBody(r *http.Request) bool {
	switch r.Method {
	case http.MethodPost, http.MethodPut, http.MethodPatch:
		return r.Body != nil
	default:
		return false
	}
}

func setCORSHeaders(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", strings.Join([]string{
		"Content-Type",
		"Authorization",
		"X-API-Key",
		"X-Caller-Key",
		"X-Request-ID",
		"X-Dashboard-Password",
		"X-Windsurf-Strict-Reuse",
		"X-Windsurf-Test-Account-IDs",
		"Anthropic-Version",
		"Anthropic-Beta",
	}, ", "))
	w.Header().Set("Access-Control-Expose-Headers", strings.Join([]string{
		"X-Request-ID",
		"Request-Id",
		"OpenAI-Model",
		"OpenAI-Processing-Ms",
		"Anthropic-Model",
		"X-Windsurf-Account-ID",
		"X-Windsurf-Attempt",
	}, ", "))
}

// AuthMiddleware validates the Bearer token against a list of allowed API keys.
// Returns a function that wraps an http.Handler with auth checking.
func AuthMiddleware(apiKeys []string) func(http.Handler) http.Handler {
	keySet := make(map[string]struct{}, len(apiKeys))
	for _, k := range apiKeys {
		keySet[k] = struct{}{}
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			setNoStore(w)
			reqID := requestID(r)
			w.Header().Set("X-Request-ID", reqID)
			w.Header().Set("Request-Id", reqID)
			token := extractAPIKey(r)
			if token == "" {
				w.Header().Set("Content-Type", "application/json")
				http.Error(w, `{"error":"missing authorization"}`, http.StatusUnauthorized)
				return
			}

			if _, ok := keySet[token]; !ok {
				w.Header().Set("Content-Type", "application/json")
				http.Error(w, `{"error":"invalid authorization"}`, http.StatusUnauthorized)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

func AuthMiddlewareFunc(apiKeys func() []string) func(http.Handler) http.Handler {
	if apiKeys == nil {
		apiKeys = func() []string { return nil }
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			setNoStore(w)
			reqID := requestID(r)
			w.Header().Set("X-Request-ID", reqID)
			w.Header().Set("Request-Id", reqID)
			token := extractAPIKey(r)
			if token == "" {
				w.Header().Set("Content-Type", "application/json")
				http.Error(w, `{"error":"missing authorization"}`, http.StatusUnauthorized)
				return
			}
			if !containsSecret(apiKeys(), token) {
				w.Header().Set("Content-Type", "application/json")
				http.Error(w, `{"error":"invalid authorization"}`, http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func DashboardAuthMiddleware(apiKeys []string, dashboardPassword string) func(http.Handler) http.Handler {
	apiKeySet := make(map[string]struct{}, len(apiKeys))
	for _, k := range apiKeys {
		if k = strings.TrimSpace(k); k != "" {
			apiKeySet[k] = struct{}{}
		}
	}
	dashboardPassword = strings.TrimSpace(dashboardPassword)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			setNoStore(w)
			reqID := requestID(r)
			w.Header().Set("X-Request-ID", reqID)
			w.Header().Set("Request-Id", reqID)
			if !isLocalRequest(r) && insecureDashboardPassword(dashboardPassword) {
				w.Header().Set("Content-Type", "application/json")
				http.Error(w, `{"error":"dashboard password must be configured for non-local access"}`, http.StatusForbidden)
				return
			}
			clientIP := clientIP(r)
			if status := dashboardLockout.check(clientIP); status.Blocked {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Retry-After", retryAfterSeconds(status.RetryAfter))
				http.Error(w, `{"error":"dashboard authorization locked","retry_after_ms":`+formatInt(status.RetryAfterMS)+`}`, http.StatusTooManyRequests)
				return
			}
			token := extractAPIKey(r)
			local := isLocalRequest(r)
			if token != "" && local {
				if _, ok := apiKeySet[token]; ok {
					dashboardLockout.success(clientIP)
					next.ServeHTTP(w, r)
					return
				}
				if dashboardPassword != "" && token == dashboardPassword {
					dashboardLockout.success(clientIP)
					next.ServeHTTP(w, r)
					return
				}
			}
			if pass := dashboardPasswordCandidate(r); pass != "" && dashboardPassword != "" && pass == dashboardPassword {
				dashboardLockout.success(clientIP)
				next.ServeHTTP(w, r)
				return
			}
			status := dashboardLockout.fail(clientIP)
			if status.Blocked {
				w.Header().Set("Retry-After", retryAfterSeconds(status.RetryAfter))
			}
			w.Header().Set("Content-Type", "application/json")
			http.Error(w, `{"error":"invalid dashboard authorization"}`, http.StatusUnauthorized)
		})
	}
}

func DashboardAuthMiddlewareFunc(apiKeys func() []string, dashboardPassword func() string) func(http.Handler) http.Handler {
	if apiKeys == nil {
		apiKeys = func() []string { return nil }
	}
	if dashboardPassword == nil {
		dashboardPassword = func() string { return "" }
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			setNoStore(w)
			reqID := requestID(r)
			w.Header().Set("X-Request-ID", reqID)
			w.Header().Set("Request-Id", reqID)
			pass := strings.TrimSpace(dashboardPassword())
			if !isLocalRequest(r) && insecureDashboardPassword(pass) {
				w.Header().Set("Content-Type", "application/json")
				http.Error(w, `{"error":"dashboard password must be configured for non-local access"}`, http.StatusForbidden)
				return
			}
			clientIP := clientIP(r)
			if status := dashboardLockout.check(clientIP); status.Blocked {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Retry-After", retryAfterSeconds(status.RetryAfter))
				http.Error(w, `{"error":"dashboard authorization locked","retry_after_ms":`+formatInt(status.RetryAfterMS)+`}`, http.StatusTooManyRequests)
				return
			}
			token := extractAPIKey(r)
			local := isLocalRequest(r)
			if token != "" && local {
				if containsSecret(apiKeys(), token) {
					dashboardLockout.success(clientIP)
					next.ServeHTTP(w, r)
					return
				}
				if pass != "" && token == pass {
					dashboardLockout.success(clientIP)
					next.ServeHTTP(w, r)
					return
				}
			}
			if candidate := dashboardPasswordCandidate(r); candidate != "" && pass != "" && candidate == pass {
				dashboardLockout.success(clientIP)
				next.ServeHTTP(w, r)
				return
			}
			status := dashboardLockout.fail(clientIP)
			if status.Blocked {
				w.Header().Set("Retry-After", retryAfterSeconds(status.RetryAfter))
			}
			w.Header().Set("Content-Type", "application/json")
			http.Error(w, `{"error":"invalid dashboard authorization"}`, http.StatusUnauthorized)
		})
	}
}

func containsSecret(values []string, token string) bool {
	token = strings.TrimSpace(token)
	if token == "" {
		return false
	}
	for _, value := range values {
		if strings.TrimSpace(value) == token {
			return true
		}
	}
	return false
}

func dashboardPasswordCandidate(r *http.Request) string {
	if pass := strings.TrimSpace(r.Header.Get("X-Dashboard-Password")); pass != "" {
		return pass
	}
	if pass := strings.TrimSpace(r.URL.Query().Get("dashboard_password")); pass != "" {
		return pass
	}
	return ""
}

func (s *lockoutState) check(ip string) LockoutStatus {
	if ip == "" {
		return LockoutStatus{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	ent, ok := s.entries[ip]
	if !ok {
		return LockoutStatus{}
	}
	if ent.blockedUntil.After(now) {
		return lockoutStatus(ent, now)
	}
	if !ent.blockedUntil.IsZero() || now.Sub(ent.firstFailed) > dashboardAuthWindow {
		delete(s.entries, ip)
		return LockoutStatus{}
	}
	return lockoutStatus(ent, now)
}

func (s *lockoutState) fail(ip string) LockoutStatus {
	if ip == "" {
		return LockoutStatus{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	ent := s.entries[ip]
	if ent.firstFailed.IsZero() || now.Sub(ent.firstFailed) > dashboardAuthWindow {
		ent = lockoutEntry{firstFailed: now}
	}
	ent.count++
	if ent.count >= dashboardAuthMaxFailures {
		ent.blockedUntil = now.Add(dashboardAuthLockout)
	}
	s.entries[ip] = ent
	return lockoutStatus(ent, now)
}

func (s *lockoutState) success(ip string) {
	if ip == "" {
		return
	}
	s.mu.Lock()
	delete(s.entries, ip)
	s.mu.Unlock()
}

func (s *lockoutState) resetForTests() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries = map[string]lockoutEntry{}
	s.now = time.Now
}

func lockoutStatus(ent lockoutEntry, now time.Time) LockoutStatus {
	retry := time.Duration(0)
	blocked := ent.blockedUntil.After(now)
	if blocked {
		retry = ent.blockedUntil.Sub(now)
	}
	return LockoutStatus{Blocked: blocked, RetryAfter: retry, RetryAfterMS: retry.Milliseconds(), Count: ent.count}
}

func extractAPIKey(r *http.Request) string {
	auth := strings.TrimSpace(r.Header.Get("Authorization"))
	if auth != "" {
		parts := strings.Split(auth, ",")
		if len(parts) == 1 {
			fields := strings.Fields(strings.TrimSpace(parts[0]))
			if len(fields) == 2 && strings.EqualFold(fields[0], "Bearer") {
				return strings.TrimSpace(fields[1])
			}
		}
		return ""
	}
	return strings.TrimSpace(r.Header.Get("X-API-Key"))
}

func requestID(r *http.Request) string {
	if v := strings.TrimSpace(r.Header.Get("X-Request-ID")); v != "" {
		return v
	}
	var b [12]byte
	if _, err := rand.Read(b[:]); err == nil {
		return "req_" + hex.EncodeToString(b[:])
	}
	return "req"
}

func isLocalRequest(r *http.Request) bool {
	ip := net.ParseIP(clientIP(r))
	return ip != nil && ip.IsLoopback()
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	return strings.TrimSpace(host)
}

func insecureDashboardPassword(password string) bool {
	password = strings.TrimSpace(password)
	return password == "" || password == "admin"
}

func retryAfterSeconds(d time.Duration) string {
	if d <= 0 {
		return "0"
	}
	secs := int64(d.Seconds())
	if d%time.Second != 0 {
		secs++
	}
	return formatInt(secs)
}

func formatInt(v int64) string {
	if v == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	neg := v < 0
	if neg {
		v = -v
	}
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
