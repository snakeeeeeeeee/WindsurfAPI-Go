package handler

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCORSMiddlewareHandlesPreflight(t *testing.T) {
	called := false
	mw := CORSMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusTeapot)
	}))
	req := httptest.NewRequest(http.MethodOptions, "/v1/chat/completions", nil)
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, req)
	if called {
		t.Fatal("preflight should not call next handler")
	}
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status=%d", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Fatalf("allow origin=%q", got)
	}
	if got := rec.Header().Get("Access-Control-Allow-Headers"); !strings.Contains(got, "Anthropic-Version") || !strings.Contains(got, "X-Dashboard-Password") {
		t.Fatalf("allow headers=%q", got)
	}
}

func TestCORSMiddlewarePassesThroughWithHeaders(t *testing.T) {
	mw := CORSMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	}))
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status=%d", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Expose-Headers"); !strings.Contains(got, "X-Windsurf-Account-ID") {
		t.Fatalf("expose headers=%q", got)
	}
}

func TestMaxBodyMiddlewareLimitsWriteMethods(t *testing.T) {
	mw := MaxBodyMiddleware(4)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusRequestEntityTooLarge)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader("12345"))
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestMaxBodyMiddlewareSkipsReadMethods(t *testing.T) {
	mw := MaxBodyMiddleware(4)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		if string(raw) != "12345" {
			t.Fatalf("body=%q", string(raw))
		}
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodGet, "/healthz", strings.NewReader("12345"))
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestMaxBodyMiddlewareFuncReadsDynamicLimit(t *testing.T) {
	limit := int64(4)
	mw := MaxBodyMiddlewareFunc(func() int64 { return limit })(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusRequestEntityTooLarge)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader("12345"))
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("first status=%d", rec.Code)
	}

	limit = 8
	req = httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader("12345"))
	rec = httptest.NewRecorder()
	mw.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("second status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestAuthMiddlewareFuncReadsDynamicKeys(t *testing.T) {
	keys := []string{"sk-old"}
	mw := AuthMiddlewareFunc(func() []string { return append([]string(nil), keys...) })(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer sk-old")
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("old key before patch status=%d", rec.Code)
	}

	keys = []string{"sk-new"}
	req = httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer sk-old")
	rec = httptest.NewRecorder()
	mw.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("old key after patch status=%d", rec.Code)
	}
	req = httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("X-API-Key", "sk-new")
	rec = httptest.NewRecorder()
	mw.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("new key after patch status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestExtractAPIKeyBearerCaseInsensitiveAndStrict(t *testing.T) {
	for _, auth := range []string{"bearer  abc123  ", "BEARER abc123", "Bearer abc123"} {
		req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
		req.Header.Set("Authorization", auth)
		if got := extractAPIKey(req); got != "abc123" {
			t.Fatalf("extractAPIKey(%q)=%q", auth, got)
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "raw-secret")
	req.Header.Set("X-API-Key", "fallback")
	if got := extractAPIKey(req); got != "" {
		t.Fatalf("malformed authorization should not fall back, got=%q", got)
	}

	req = httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer one, Bearer two")
	req.Header.Set("X-API-Key", "fallback")
	if got := extractAPIKey(req); got != "" {
		t.Fatalf("duplicate authorization should not fall back, got=%q", got)
	}

	req = httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("X-API-Key", "fallback")
	if got := extractAPIKey(req); got != "fallback" {
		t.Fatalf("x-api-key fallback=%q", got)
	}
}

func TestDashboardAuthMiddlewareFuncReadsDynamicPassword(t *testing.T) {
	dashboardLockout.resetForTests()
	pass := "dash-old"
	keys := []string{"sk-old"}
	mw := DashboardAuthMiddlewareFunc(func() []string { return append([]string(nil), keys...) }, func() string { return pass })(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	req := httptest.NewRequest(http.MethodGet, "/dashboard/api/overview", nil)
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("X-Dashboard-Password", "dash-old")
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("old password before patch status=%d", rec.Code)
	}

	pass = "dash-new"
	req = httptest.NewRequest(http.MethodGet, "/dashboard/api/overview", nil)
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("X-Dashboard-Password", "dash-old")
	rec = httptest.NewRecorder()
	mw.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("old password after patch status=%d", rec.Code)
	}
	req = httptest.NewRequest(http.MethodGet, "/dashboard/api/overview", nil)
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("X-Dashboard-Password", "dash-new")
	rec = httptest.NewRecorder()
	mw.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("new password after patch status=%d body=%s", rec.Code, rec.Body.String())
	}
}
