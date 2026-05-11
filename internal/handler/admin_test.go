package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zhangyu/windsurfapi-go/internal/account"
	"github.com/zhangyu/windsurfapi-go/internal/config"
	"github.com/zhangyu/windsurfapi-go/internal/modelaccess"
	"github.com/zhangyu/windsurfapi-go/internal/models"
	proxypool "github.com/zhangyu/windsurfapi-go/internal/proxy"
	reusepool "github.com/zhangyu/windsurfapi-go/internal/reuse"
	runtimeconfig "github.com/zhangyu/windsurfapi-go/internal/runtimeconfig"
	"github.com/zhangyu/windsurfapi-go/internal/store"
	"github.com/zhangyu/windsurfapi-go/internal/windsurf"
	"github.com/zhangyu/windsurfapi-go/internal/windsurf/direct"
)

type fakeDashboardDirectClient struct {
	probeErr error
	probes   []direct.ChatRequest
}

func (f *fakeDashboardDirectClient) GetUserStatusWithProxy(context.Context, string, string) (*direct.UserStatus, error) {
	return &direct.UserStatus{PlanName: "Individual", DailyPercent: ptrFloat(96), WeeklyPercent: ptrFloat(98)}, nil
}

func (f *fakeDashboardDirectClient) CheckMessageRateLimitWithProxy(context.Context, string, string) (*direct.RateLimit, error) {
	return &direct.RateLimit{HasCapacity: true}, nil
}

func (f *fakeDashboardDirectClient) GetCascadeModelConfigsWithProxy(context.Context, string, string) (*direct.ModelConfigs, error) {
	return &direct.ModelConfigs{Configs: []map[string]any{{"model": "claude-sonnet-4.6"}}}, nil
}

func (f *fakeDashboardDirectClient) ProbeChat(_ context.Context, apiKey, proxyURL string, model *models.Model, prompt string) (*windsurf.ChatResult, error) {
	f.probes = append(f.probes, direct.ChatRequest{
		APIKey: apiKey, ProxyURL: proxyURL, Model: model,
		Messages: []windsurf.ChatMessage{{Role: "user", Content: prompt}},
	})
	if f.probeErr != nil {
		return nil, f.probeErr
	}
	return &windsurf.ChatResult{Text: "OK", FinishReason: "stop"}, nil
}

func (f *fakeDashboardDirectClient) Snapshot() direct.Stats {
	return direct.Stats{Protocol: "grpc", Hosts: []string{"server.codeium.com"}}
}

func ptrFloat(v float64) *float64 { return &v }

func TestAuthMiddlewareAcceptsXAPIKey(t *testing.T) {
	mw := AuthMiddleware([]string{"sk-test"})
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	req := httptest.NewRequest(http.MethodGet, "/debug/accounts", nil)
	req.Header.Set("X-API-Key", "sk-test")
	rec := httptest.NewRecorder()
	mw(next).ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("X-Request-ID") == "" {
		t.Fatal("X-Request-ID missing")
	}
}

func TestDashboardAuthMiddlewareAcceptsDashboardPassword(t *testing.T) {
	dashboardLockout.resetForTests()
	mw := DashboardAuthMiddleware([]string{"sk-test"}, "dash-secret")
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	req := httptest.NewRequest(http.MethodGet, "/dashboard/api/overview", nil)
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("X-Dashboard-Password", "dash-secret")
	rec := httptest.NewRecorder()
	mw(next).ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestDashboardAuthMiddlewareAcceptsDashboardPasswordQueryForStreams(t *testing.T) {
	dashboardLockout.resetForTests()
	mw := DashboardAuthMiddleware(nil, "dash-secret")
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	req := httptest.NewRequest(http.MethodGet, "/dashboard/api/logs/stream?dashboard_password=dash-secret", nil)
	req.RemoteAddr = "127.0.0.1:12345"
	rec := httptest.NewRecorder()
	mw(next).ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestDashboardAuthMiddlewareRemoteRejectsAPIKeyFallback(t *testing.T) {
	dashboardLockout.resetForTests()
	mw := DashboardAuthMiddleware([]string{"sk-test"}, "dash-secret")
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	req := httptest.NewRequest(http.MethodGet, "/dashboard/api/overview", nil)
	req.RemoteAddr = "203.0.113.9:12345"
	req.Header.Set("Authorization", "Bearer sk-test")
	rec := httptest.NewRecorder()
	mw(next).ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestDashboardAuthMiddlewareFailClosedForRemoteDefaultPassword(t *testing.T) {
	dashboardLockout.resetForTests()
	mw := DashboardAuthMiddleware([]string{"sk-test"}, "admin")
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	req := httptest.NewRequest(http.MethodGet, "/dashboard/api/overview", nil)
	req.RemoteAddr = "203.0.113.9:12345"
	req.Header.Set("Authorization", "Bearer sk-test")
	rec := httptest.NewRecorder()
	mw(next).ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestDashboardAuthMiddlewareLocksOutRepeatedFailures(t *testing.T) {
	dashboardLockout.resetForTests()
	defer dashboardLockout.resetForTests()
	mw := DashboardAuthMiddleware(nil, "dash-secret")
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	for i := 0; i < 5; i++ {
		req := httptest.NewRequest(http.MethodGet, "/dashboard/api/overview", nil)
		req.RemoteAddr = "203.0.113.9:12345"
		req.Header.Set("X-Dashboard-Password", "wrong")
		rec := httptest.NewRecorder()
		mw(next).ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d status=%d body=%s", i+1, rec.Code, rec.Body.String())
		}
	}
	req := httptest.NewRequest(http.MethodGet, "/dashboard/api/overview", nil)
	req.RemoteAddr = "203.0.113.9:12345"
	req.Header.Set("X-Dashboard-Password", "dash-secret")
	rec := httptest.NewRecorder()
	mw(next).ServeHTTP(rec, req)
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("lockout status=%d retry-after=%q body=%s", rec.Code, rec.Header().Get("Retry-After"), rec.Body.String())
	}
}

func TestDashboardAuthMiddlewareSuccessClearsFailureCounter(t *testing.T) {
	dashboardLockout.resetForTests()
	defer dashboardLockout.resetForTests()
	mw := DashboardAuthMiddleware(nil, "dash-secret")
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	for i := 0; i < 4; i++ {
		req := httptest.NewRequest(http.MethodGet, "/dashboard/api/overview", nil)
		req.RemoteAddr = "203.0.113.10:12345"
		req.Header.Set("X-Dashboard-Password", "wrong")
		rec := httptest.NewRecorder()
		mw(next).ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d status=%d body=%s", i+1, rec.Code, rec.Body.String())
		}
	}
	okReq := httptest.NewRequest(http.MethodGet, "/dashboard/api/overview", nil)
	okReq.RemoteAddr = "203.0.113.10:12345"
	okReq.Header.Set("X-Dashboard-Password", "dash-secret")
	okRec := httptest.NewRecorder()
	mw(next).ServeHTTP(okRec, okReq)
	if okRec.Code != http.StatusNoContent {
		t.Fatalf("success status=%d body=%s", okRec.Code, okRec.Body.String())
	}
	failReq := httptest.NewRequest(http.MethodGet, "/dashboard/api/overview", nil)
	failReq.RemoteAddr = "203.0.113.10:12345"
	failReq.Header.Set("X-Dashboard-Password", "wrong")
	failRec := httptest.NewRecorder()
	mw(next).ServeHTTP(failRec, failReq)
	if failRec.Code != http.StatusUnauthorized {
		t.Fatalf("post-success status=%d body=%s", failRec.Code, failRec.Body.String())
	}
}

func TestAuthLoginAndAccountsHandlers(t *testing.T) {
	mgr := testHandlerAccountManager(t)
	body := `{"email":"a@example.com","token":"devin-session-token$abc","tier":"pro","notes":"primary"}`
	req := httptest.NewRequest(http.MethodPost, "/auth/login", strings.NewReader(body))
	rec := httptest.NewRecorder()
	AuthLoginHandler(mgr).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("login status=%d body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "devin-session-token") {
		t.Fatalf("token leaked in response: %s", rec.Body.String())
	}

	listReq := httptest.NewRequest(http.MethodGet, "/auth/accounts", nil)
	listRec := httptest.NewRecorder()
	AuthAccountsHandler(mgr).ServeHTTP(listRec, listReq)
	if listRec.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", listRec.Code, listRec.Body.String())
	}
	var list map[string]any
	if err := json.Unmarshal(listRec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	accounts := list["accounts"].([]any)
	if len(accounts) != 1 {
		t.Fatalf("accounts=%v", accounts)
	}
	acc := accounts[0].(map[string]any)
	if acc["email"] != "a@example.com" || acc["tier"] != "pro" || acc["token_set"] != true {
		t.Fatalf("account=%v", acc)
	}
}

func TestDashboardImportAccountsCompatibilityAPI(t *testing.T) {
	mgr := testHandlerAccountManager(t)
	handler := DashboardAPIHandler(mgr, nil, nil, nil, reusepool.NewPool(), nil)
	body := `{
		"accounts": [
			{"label":"first","api_key":"devin-session-token$from-api-key","proxy_url":"http://user:secret@proxy.local:8080"}
		],
		"text": "text@example.com----password----devin-session-token$text-token----auth1_should_not_be_persisted\nbad line"
	}`
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/dashboard/api/import-accounts", strings.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("import status=%d body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "$from-api-key") || strings.Contains(rec.Body.String(), "$text-token") || strings.Contains(rec.Body.String(), "auth1_should") || strings.Contains(rec.Body.String(), "secret") {
		t.Fatalf("secret leaked in import response: %s", rec.Body.String())
	}
	var got struct {
		Success  bool             `json:"success"`
		Imported int              `json:"imported"`
		Failed   int              `json:"failed"`
		Warnings []string         `json:"warnings"`
		Results  []map[string]any `json:"results"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !got.Success || got.Imported != 2 || got.Failed != 0 || len(got.Warnings) != 1 || len(got.Results) != 2 {
		t.Fatalf("body=%+v", got)
	}

	accounts, err := mgr.GetAllAccounts()
	if err != nil {
		t.Fatal(err)
	}
	if len(accounts) != 2 {
		t.Fatalf("accounts=%+v", accounts)
	}
	var parsed account.Account
	for _, acc := range accounts {
		if acc.Email == "text@example.com" {
			parsed = acc
		}
	}
	if parsed.FirebaseToken != "devin-session-token$text-token" {
		t.Fatalf("parsed token=%q", parsed.FirebaseToken)
	}
}

func TestSafeAccountMasksProxyCredentials(t *testing.T) {
	mgr := testHandlerAccountManager(t)
	id, err := mgr.AddAccount("proxy-safe@example.com", "devin-session-token$abc", "u", "http://user:secret@proxy.local:8080", "")
	if err != nil {
		t.Fatal(err)
	}
	acc, err := mgr.GetAccount(int(id))
	if err != nil {
		t.Fatal(err)
	}
	safe := safeAccount(acc)
	if safe["proxy_url"] != "http://user:%2A%2A%2A@proxy.local:8080" {
		t.Fatalf("proxy not masked: %+v", safe)
	}
	if _, ok := safe["firebase_token"]; ok || strings.Contains(fmt.Sprint(safe), "secret@") {
		t.Fatalf("secret leaked: %+v", safe)
	}
}

func TestAuthAccountPatchAndDelete(t *testing.T) {
	mgr := testHandlerAccountManager(t)
	id, _ := mgr.AddAccount("patch@example.com", "devin-session-token$abc", "u", "", "")
	patch := []byte(`{"enabled":false,"banned":true,"notes":"disabled","proxy_url":"http://proxy.local:8080"}`)
	req := httptest.NewRequest(http.MethodPatch, "/auth/accounts/1", bytes.NewReader(patch))
	rec := httptest.NewRecorder()
	AuthAccountByIDHandler(mgr).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("patch status=%d body=%s", rec.Code, rec.Body.String())
	}
	acc, err := mgr.GetAccount(int(id))
	if err != nil {
		t.Fatal(err)
	}
	if acc.Enabled || !acc.Banned || acc.Notes != "disabled" || acc.ProxyURL != "http://proxy.local:8080" {
		t.Fatalf("patched account=%+v", acc)
	}

	clearReq := httptest.NewRequest(http.MethodPatch, "/auth/accounts/1", bytes.NewReader([]byte(`{"notes":"","proxy_url":""}`)))
	clearRec := httptest.NewRecorder()
	AuthAccountByIDHandler(mgr).ServeHTTP(clearRec, clearReq)
	if clearRec.Code != http.StatusOK {
		t.Fatalf("clear status=%d body=%s", clearRec.Code, clearRec.Body.String())
	}
	cleared, err := mgr.GetAccount(int(id))
	if err != nil {
		t.Fatal(err)
	}
	if cleared.Notes != "" || cleared.ProxyURL != "" {
		t.Fatalf("clear did not persist: %+v", cleared)
	}

	delReq := httptest.NewRequest(http.MethodDelete, "/auth/accounts/1", nil)
	delRec := httptest.NewRecorder()
	AuthAccountByIDHandler(mgr).ServeHTTP(delRec, delReq)
	if delRec.Code != http.StatusOK {
		t.Fatalf("delete status=%d body=%s", delRec.Code, delRec.Body.String())
	}
	deleted, err := mgr.GetAccount(int(id))
	if err != nil {
		t.Fatal(err)
	}
	if deleted != nil {
		t.Fatalf("account still exists: %+v", deleted)
	}
}

func TestAuthAccountModelsHandler(t *testing.T) {
	mgr := testHandlerAccountManager(t)
	id, _ := mgr.AddAccount("models@example.com", "devin-session-token$abc", "u", "", "")
	body := []byte(`{"blocked_models":["claude-sonnet-4.6","claude-opus-4.6","claude-sonnet-4.6"]}`)
	req := httptest.NewRequest(http.MethodPut, "/auth/accounts/1/models", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	AuthAccountModelsHandler(mgr).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("put status=%d body=%s", rec.Code, rec.Body.String())
	}

	listReq := httptest.NewRequest(http.MethodGet, "/auth/accounts/1/models", nil)
	listRec := httptest.NewRecorder()
	AuthAccountModelsHandler(mgr).ServeHTTP(listRec, listReq)
	if listRec.Code != http.StatusOK {
		t.Fatalf("get status=%d body=%s", listRec.Code, listRec.Body.String())
	}
	var got struct {
		AccountID     int      `json:"account_id"`
		BlockedModels []string `json:"blocked_models"`
	}
	if err := json.Unmarshal(listRec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.AccountID != int(id) {
		t.Fatalf("account_id=%d want %d", got.AccountID, id)
	}
	if strings.Join(got.BlockedModels, ",") != "claude-opus-4.6,claude-sonnet-4.6" {
		t.Fatalf("blocked_models=%v", got.BlockedModels)
	}

	snap := mgr.Snapshot()
	if len(snap.Accounts) != 1 || strings.Join(snap.Accounts[0].BlockedModels, ",") != "claude-opus-4.6,claude-sonnet-4.6" {
		t.Fatalf("snapshot blocked models=%+v", snap.Accounts)
	}
}

func TestDashboardConfigAPI(t *testing.T) {
	cfg := &config.Config{
		Server:    config.ServerConfig{Port: 3456, APIKeys: []string{"sk-test"}, MaxRequestBodyBytes: 26214400},
		Direct:    config.DirectConfig{Hosts: []string{"old"}, TimeoutSeconds: 30},
		Health:    config.HealthConfig{Enabled: true, IntervalSeconds: 300, TimeoutSeconds: 20, Model: "claude-sonnet-4.6"},
		Scheduler: config.SchedulerConfig{MaxInflightPerAccount: 4, ReservationTTLSeconds: 180},
	}
	rc := runtimeconfig.NewManager(cfg)
	rec := httptest.NewRecorder()
	dashboardConfigAPI(rec, httptest.NewRequest(http.MethodGet, "/dashboard/api/config", nil), rc)
	if rec.Code != http.StatusOK {
		t.Fatalf("get status=%d body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "sk-test") {
		t.Fatalf("api key leaked: %s", rec.Body.String())
	}

	patch := `{"server":{"max_request_body_bytes":123456},"direct":{"hosts":["server.one"],"timeout_seconds":45},"scheduler":{"redis_enabled":true,"max_inflight_per_account":8,"reservation_ttl_seconds":90}}`
	patchRec := httptest.NewRecorder()
	dashboardConfigAPI(patchRec, httptest.NewRequest(http.MethodPatch, "/dashboard/api/config", strings.NewReader(patch)), rc)
	if patchRec.Code != http.StatusOK {
		t.Fatalf("patch status=%d body=%s", patchRec.Code, patchRec.Body.String())
	}
	if cfg.Server.MaxRequestBodyBytes != 123456 || cfg.Direct.TimeoutSeconds != 45 || cfg.Direct.Hosts[0] != "server.one" || !cfg.Scheduler.RedisEnabled {
		t.Fatalf("cfg=%+v", cfg)
	}
}

func TestDashboardCompatibilityAPIs(t *testing.T) {
	oldStats := globalRequestStats
	globalRequestStats = &requestStatsStore{}
	t.Cleanup(func() { globalRequestStats = oldStats })
	cfg := &config.Config{
		Server:    config.ServerConfig{Port: 3456, APIKeys: []string{"sk-old"}, MaxRequestBodyBytes: 26214400},
		Dashboard: config.DashboardConfig{Password: "dash-old"},
		Direct:    config.DirectConfig{Hosts: []string{"old"}, TimeoutSeconds: 30},
		Health:    config.HealthConfig{Enabled: true, IntervalSeconds: 300, TimeoutSeconds: 20, Model: "claude-sonnet-4.6"},
		Scheduler: config.SchedulerConfig{MaxInflightPerAccount: 4, ReservationTTLSeconds: 180},
	}
	rc := runtimeconfig.NewManager(cfg)
	am := testHandlerAccountManager(t)
	rp := reusepool.NewPool()
	handler := DashboardAPIHandler(am, nil, rc, nil, rp, nil)

	authRec := httptest.NewRecorder()
	handler.ServeHTTP(authRec, httptest.NewRequest(http.MethodGet, "/dashboard/api/auth", nil))
	if authRec.Code != http.StatusOK || !strings.Contains(authRec.Body.String(), `"valid":true`) {
		t.Fatalf("auth status=%d body=%s", authRec.Code, authRec.Body.String())
	}

	credsGet := httptest.NewRecorder()
	handler.ServeHTTP(credsGet, httptest.NewRequest(http.MethodGet, "/dashboard/api/settings/credentials", nil))
	if credsGet.Code != http.StatusOK || strings.Contains(credsGet.Body.String(), "sk-old") || !strings.Contains(credsGet.Body.String(), `"apiKey_masked"`) {
		t.Fatalf("creds get status=%d body=%s", credsGet.Code, credsGet.Body.String())
	}

	credsPut := httptest.NewRecorder()
	handler.ServeHTTP(credsPut, httptest.NewRequest(http.MethodPut, "/dashboard/api/settings/credentials", strings.NewReader(`{"apiKey":"sk-new-secret","dashboardPassword":"dash-new-secret"}`)))
	if credsPut.Code != http.StatusOK || strings.Contains(credsPut.Body.String(), "sk-new-secret") || rc.DashboardPassword() != "dash-new-secret" {
		t.Fatalf("creds put status=%d body=%s pass=%q", credsPut.Code, credsPut.Body.String(), rc.DashboardPassword())
	}
	if got := rc.APIKeys(); len(got) != 1 || got[0] != "sk-new-secret" {
		t.Fatalf("api keys=%v", got)
	}

	envGet := httptest.NewRecorder()
	handler.ServeHTTP(envGet, httptest.NewRequest(http.MethodGet, "/dashboard/api/settings/env", nil))
	if envGet.Code != http.StatusOK || strings.Contains(envGet.Body.String(), "sk-new-secret") || !strings.Contains(envGet.Body.String(), "max_request_body_bytes") {
		t.Fatalf("env get status=%d body=%s", envGet.Code, envGet.Body.String())
	}

	envPut := httptest.NewRecorder()
	handler.ServeHTTP(envPut, httptest.NewRequest(http.MethodPut, "/dashboard/api/settings/env", strings.NewReader(`{"direct":{"hosts":["server.one"],"timeout_seconds":45}}`)))
	if envPut.Code != http.StatusOK || cfg.Direct.TimeoutSeconds != 45 || cfg.Direct.Hosts[0] != "server.one" {
		t.Fatalf("env put status=%d body=%s cfg=%+v", envPut.Code, envPut.Body.String(), cfg.Direct)
	}

	recordRequestEvent(RequestEvent{RequestID: "req_reset", Route: "chat", Status: "ok", HTTPStatus: 200})
	statsDel := httptest.NewRecorder()
	handler.ServeHTTP(statsDel, httptest.NewRequest(http.MethodDelete, "/dashboard/api/stats", nil))
	if statsDel.Code != http.StatusOK || requestStatsSnapshot()["total"] != 0 {
		t.Fatalf("stats delete status=%d body=%s stats=%+v", statsDel.Code, statsDel.Body.String(), requestStatsSnapshot())
	}

	rp.Checkin("fp", &reusepool.Entry{AccountID: 1, ModelID: "m", CallerKey: "caller"}, time.Minute)
	poolDel := httptest.NewRecorder()
	handler.ServeHTTP(poolDel, httptest.NewRequest(http.MethodDelete, "/dashboard/api/experimental/conversation-pool", nil))
	if poolDel.Code != http.StatusOK || len(rp.Snapshot()) != 0 {
		t.Fatalf("pool delete status=%d body=%s entries=%+v", poolDel.Code, poolDel.Body.String(), rp.Snapshot())
	}
}

func TestDashboardExperimentalAvailabilityAndLegacyCompatibilityAPIs(t *testing.T) {
	cfg := &config.Config{
		Server:    config.ServerConfig{Port: 3456, APIKeys: []string{"sk-old"}, MaxRequestBodyBytes: 26214400},
		Dashboard: config.DashboardConfig{Password: "dash-old"},
		Direct:    config.DirectConfig{Hosts: []string{"old"}, TimeoutSeconds: 30},
		Health:    config.HealthConfig{Enabled: true, IntervalSeconds: 300, TimeoutSeconds: 20, MarkInvalidBanned: true, CheckModelConfigs: true, Model: "claude-sonnet-4.6"},
		Scheduler: config.SchedulerConfig{MaxInflightPerAccount: 4, ReservationTTLSeconds: 180},
	}
	rc := runtimeconfig.NewManager(cfg)
	rp := reusepool.NewPool()
	handler := DashboardAPIHandler(testHandlerAccountManager(t), nil, rc, nil, rp, nil)

	experimentalGet := httptest.NewRecorder()
	handler.ServeHTTP(experimentalGet, httptest.NewRequest(http.MethodGet, "/dashboard/api/experimental", nil))
	if experimentalGet.Code != http.StatusOK || !strings.Contains(experimentalGet.Body.String(), `"directOnly":true`) {
		t.Fatalf("experimental get status=%d body=%s", experimentalGet.Code, experimentalGet.Body.String())
	}

	experimentalPut := httptest.NewRecorder()
	handler.ServeHTTP(experimentalPut, httptest.NewRequest(http.MethodPut, "/dashboard/api/experimental", strings.NewReader(`{"directNativeChatPrompts":true,"cascadeConversationReuse":false}`)))
	if experimentalPut.Code != http.StatusOK || !rc.Snapshot().Direct.NativeChatPrompts {
		t.Fatalf("experimental put status=%d body=%s direct=%+v", experimentalPut.Code, experimentalPut.Body.String(), rc.Snapshot().Direct)
	}

	availabilityGet := httptest.NewRecorder()
	handler.ServeHTTP(availabilityGet, httptest.NewRequest(http.MethodGet, "/dashboard/api/availability/config", nil))
	if availabilityGet.Code != http.StatusOK || !strings.Contains(availabilityGet.Body.String(), `"model":"claude-sonnet-4.6"`) {
		t.Fatalf("availability get status=%d body=%s", availabilityGet.Code, availabilityGet.Body.String())
	}

	availabilityPut := httptest.NewRecorder()
	handler.ServeHTTP(availabilityPut, httptest.NewRequest(http.MethodPut, "/dashboard/api/availability/config", strings.NewReader(`{"enabled":false,"interval_seconds":600,"timeout_seconds":30,"model":"claude-sonnet-4.6"}`)))
	if availabilityPut.Code != http.StatusOK || rc.Snapshot().Health.Enabled || rc.Snapshot().Health.IntervalSeconds != 600 {
		t.Fatalf("availability put status=%d body=%s health=%+v", availabilityPut.Code, availabilityPut.Body.String(), rc.Snapshot().Health)
	}

	localAvailability := httptest.NewRecorder()
	handler.ServeHTTP(localAvailability, httptest.NewRequest(http.MethodGet, "/dashboard/api/accounts/import-local-availability", nil))
	if localAvailability.Code != http.StatusOK || !strings.Contains(localAvailability.Body.String(), "direct_only_go_backend") {
		t.Fatalf("local availability status=%d body=%s", localAvailability.Code, localAvailability.Body.String())
	}

	selfUpdate := httptest.NewRecorder()
	handler.ServeHTTP(selfUpdate, httptest.NewRequest(http.MethodGet, "/dashboard/api/self-update/check", nil))
	if selfUpdate.Code != http.StatusOK || !strings.Contains(selfUpdate.Body.String(), "self_update_not_supported") {
		t.Fatalf("self-update status=%d body=%s", selfUpdate.Code, selfUpdate.Body.String())
	}

	lsUpdate := httptest.NewRecorder()
	handler.ServeHTTP(lsUpdate, httptest.NewRequest(http.MethodPost, "/dashboard/api/langserver/update", nil))
	if lsUpdate.Code != http.StatusOK || !strings.Contains(lsUpdate.Body.String(), "ERR_LEGACY_LS_NOT_ON_MAIN_PATH") {
		t.Fatalf("ls update status=%d body=%s", lsUpdate.Code, lsUpdate.Body.String())
	}

	login := httptest.NewRecorder()
	handler.ServeHTTP(login, httptest.NewRequest(http.MethodPost, "/dashboard/api/windsurf-login", strings.NewReader(`{}`)))
	if login.Code != http.StatusNotImplemented || !strings.Contains(login.Body.String(), "ERR_WINDSURF_LOGIN_NOT_IMPLEMENTED") {
		t.Fatalf("login status=%d body=%s", login.Code, login.Body.String())
	}

	reveal := httptest.NewRecorder()
	handler.ServeHTTP(reveal, httptest.NewRequest(http.MethodPost, "/dashboard/api/account/1/reveal-key", nil))
	if reveal.Code != http.StatusForbidden || strings.Contains(reveal.Body.String(), "devin-session-token") || !strings.Contains(reveal.Body.String(), "ERR_REVEAL_KEY_DISABLED") {
		t.Fatalf("reveal status=%d body=%s", reveal.Code, reveal.Body.String())
	}

	serviceRestart := httptest.NewRecorder()
	handler.ServeHTTP(serviceRestart, httptest.NewRequest(http.MethodPost, "/dashboard/api/service/restart", nil))
	if serviceRestart.Code != http.StatusOK || !strings.Contains(serviceRestart.Body.String(), "ERR_SERVICE_RESTART_NOT_SUPPORTED") {
		t.Fatalf("service restart status=%d body=%s", serviceRestart.Code, serviceRestart.Body.String())
	}

	systemPrompts := httptest.NewRecorder()
	handler.ServeHTTP(systemPrompts, httptest.NewRequest(http.MethodGet, "/dashboard/api/system-prompts", nil))
	if systemPrompts.Code != http.StatusOK || !strings.Contains(systemPrompts.Body.String(), `"legacy":true`) {
		t.Fatalf("system prompts status=%d body=%s", systemPrompts.Code, systemPrompts.Body.String())
	}

	manualProbe := httptest.NewRecorder()
	handler.ServeHTTP(manualProbe, httptest.NewRequest(http.MethodPost, "/dashboard/api/accounts/1/probe", nil))
	if manualProbe.Code != http.StatusNotFound || !strings.Contains(manualProbe.Body.String(), "account or direct client unavailable") {
		t.Fatalf("manual probe status=%d body=%s", manualProbe.Code, manualProbe.Body.String())
	}

	dynamicProxy := httptest.NewRecorder()
	handler.ServeHTTP(dynamicProxy, httptest.NewRequest(http.MethodGet, "/dashboard/api/dynamic-proxy/config", nil))
	if dynamicProxy.Code != http.StatusOK || !strings.Contains(dynamicProxy.Body.String(), `"success":true`) {
		t.Fatalf("dynamic proxy status=%d body=%s", dynamicProxy.Code, dynamicProxy.Body.String())
	}
}

func TestDashboardModelAccessCompatibilityAPIs(t *testing.T) {
	sqliteStore, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "windsurf.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqliteStore.Close() })
	am := account.NewManager(sqliteStore)
	access := modelaccess.NewManager(sqliteStore)
	handler := DashboardAPIHandler(am, access, nil, nil, reusepool.NewPool(), nil)

	allowlist := httptest.NewRecorder()
	handler.ServeHTTP(allowlist, httptest.NewRequest(http.MethodPut, "/dashboard/api/model-access", strings.NewReader(`{"mode":"allowlist","list":["claude-sonnet-4.6"]}`)))
	if allowlist.Code != http.StatusOK {
		t.Fatalf("allowlist status=%d body=%s", allowlist.Code, allowlist.Body.String())
	}
	if !access.IsVisible("claude-sonnet-4.6") || access.IsVisible("claude-opus-4.6") {
		t.Fatalf("allowlist visibility sonnet=%v opus=%v", access.IsVisible("claude-sonnet-4.6"), access.IsVisible("claude-opus-4.6"))
	}
	if !access.IsVisible("claude-sonnet-4.6-thinking") {
		t.Fatal("allowlist should inherit from base model to -thinking sibling")
	}
	var allowBody map[string]any
	if err := json.Unmarshal(allowlist.Body.Bytes(), &allowBody); err != nil {
		t.Fatal(err)
	}
	allowConfig := allowBody["config"].(map[string]any)
	if allowConfig["mode"] != "allowlist" || len(allowConfig["list"].([]any)) != 1 || allowConfig["list"].([]any)[0] != "claude-sonnet-4.6" {
		t.Fatalf("allowlist config=%v", allowConfig)
	}
	if ok, reason := access.IsEnabled("gpt-5"); ok || reason == "" {
		t.Fatalf("allowlist must not enable unsupported gpt-5 ok=%v reason=%q", ok, reason)
	}

	add := httptest.NewRecorder()
	handler.ServeHTTP(add, httptest.NewRequest(http.MethodPost, "/dashboard/api/model-access/add", strings.NewReader(`{"model":"claude-opus-4.6"}`)))
	if add.Code != http.StatusOK || !access.IsVisible("claude-opus-4.6") {
		t.Fatalf("add status=%d body=%s visible=%v", add.Code, add.Body.String(), access.IsVisible("claude-opus-4.6"))
	}

	remove := httptest.NewRecorder()
	handler.ServeHTTP(remove, httptest.NewRequest(http.MethodPost, "/dashboard/api/model-access/remove", strings.NewReader(`{"model":"claude-sonnet-4.6"}`)))
	if remove.Code != http.StatusOK || access.IsVisible("claude-sonnet-4.6") {
		t.Fatalf("remove status=%d body=%s visible=%v", remove.Code, remove.Body.String(), access.IsVisible("claude-sonnet-4.6"))
	}

	blocklist := httptest.NewRecorder()
	handler.ServeHTTP(blocklist, httptest.NewRequest(http.MethodPut, "/dashboard/api/model-access", strings.NewReader(`{"mode":"blocklist","list":["claude-opus-4.6"]}`)))
	if blocklist.Code != http.StatusOK || access.IsVisible("claude-opus-4.6") || access.IsVisible("claude-opus-4.6-thinking") || !access.IsVisible("claude-sonnet-4.6") {
		t.Fatalf("blocklist status=%d body=%s opus=%v opusThinking=%v sonnet=%v", blocklist.Code, blocklist.Body.String(), access.IsVisible("claude-opus-4.6"), access.IsVisible("claude-opus-4.6-thinking"), access.IsVisible("claude-sonnet-4.6"))
	}
	var blockBody map[string]any
	if err := json.Unmarshal(blocklist.Body.Bytes(), &blockBody); err != nil {
		t.Fatal(err)
	}
	blockConfig := blockBody["config"].(map[string]any)
	if blockConfig["mode"] != "blocklist" || len(blockConfig["list"].([]any)) != 1 || blockConfig["list"].([]any)[0] != "claude-opus-4.6" {
		t.Fatalf("blocklist config=%v", blockConfig)
	}
}

func TestDashboardModelsDefaultToPublicCatalog(t *testing.T) {
	sqliteStore, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "windsurf.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqliteStore.Close() })
	handler := DashboardAPIHandler(account.NewManager(sqliteStore), modelaccess.NewManager(sqliteStore), nil, nil, reusepool.NewPool(), nil)

	publicRec := httptest.NewRecorder()
	handler.ServeHTTP(publicRec, httptest.NewRequest(http.MethodGet, "/dashboard/api/models", nil))
	if publicRec.Code != http.StatusOK {
		t.Fatalf("public status=%d body=%s", publicRec.Code, publicRec.Body.String())
	}
	var publicBody struct {
		Scope string                  `json:"scope"`
		Data  []models.DashboardModel `json:"data"`
	}
	if err := json.Unmarshal(publicRec.Body.Bytes(), &publicBody); err != nil {
		t.Fatal(err)
	}
	if publicBody.Scope != "public" || len(publicBody.Data) != len(models.PublicModelIDs) {
		t.Fatalf("public scope=%q len=%d body=%s", publicBody.Scope, len(publicBody.Data), publicRec.Body.String())
	}
	for _, item := range publicBody.Data {
		if strings.Contains(item.ID, "-medium") || strings.Contains(item.ID, "-xhigh") || strings.Contains(item.ID, "-max") {
			t.Fatalf("public catalog leaked internal variant: %#v", item)
		}
	}

	allRec := httptest.NewRecorder()
	handler.ServeHTTP(allRec, httptest.NewRequest(http.MethodGet, "/dashboard/api/models?scope=all", nil))
	var allBody struct {
		Scope string                  `json:"scope"`
		Data  []models.DashboardModel `json:"data"`
	}
	if err := json.Unmarshal(allRec.Body.Bytes(), &allBody); err != nil {
		t.Fatal(err)
	}
	if allBody.Scope != "all" || len(allBody.Data) <= len(publicBody.Data) {
		t.Fatalf("all scope=%q len=%d public=%d", allBody.Scope, len(allBody.Data), len(publicBody.Data))
	}
}

func TestDashboardNodeProxyAndStatusCompatibilityAPIs(t *testing.T) {
	cfg := &config.Config{
		Server:    config.ServerConfig{Port: 3456, APIKeys: []string{"sk-old"}, MaxRequestBodyBytes: 26214400},
		Dashboard: config.DashboardConfig{Password: "dash-old"},
		Direct:    config.DirectConfig{Hosts: []string{"server.one", "server.two"}, TimeoutSeconds: 30},
		Health:    config.HealthConfig{Enabled: true, IntervalSeconds: 300, TimeoutSeconds: 20, Model: "claude-sonnet-4.6"},
		Scheduler: config.SchedulerConfig{MaxInflightPerAccount: 4, ReservationTTLSeconds: 180},
		Proxy:     config.ProxyConfig{TestURL: "https://server.codeium.com/", CooldownSeconds: 120, AllowPrivate: true},
	}
	rc := runtimeconfig.NewManager(cfg)
	am := testHandlerAccountManager(t)
	id, err := am.AddAccount("proxy-account@example.com", "devin-session-token$abc", "u", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := am.UpdateAccount(int(id), map[string]interface{}{"quota_daily_percent": 2, "quota_weekly_percent": 2}); err != nil {
		t.Fatal(err)
	}
	pp := proxypool.NewManager(proxypool.Config{TestURL: "https://server.codeium.com/", Cooldown: 2 * time.Minute, AllowPrivate: true})
	handler := DashboardAPIHandler(am, nil, rc, nil, reusepool.NewPool(), nil, pp)

	global := httptest.NewRecorder()
	handler.ServeHTTP(global, httptest.NewRequest(http.MethodPut, "/dashboard/api/proxy/global", strings.NewReader(`{"url":"http://user:secret@proxy.local:8080"}`)))
	if global.Code != http.StatusOK || strings.Contains(global.Body.String(), "secret") || !strings.Contains(global.Body.String(), "user:%2A%2A%2A@proxy.local") {
		t.Fatalf("global status=%d body=%s", global.Code, global.Body.String())
	}
	if pp.Default() != "http://user:secret@proxy.local:8080" || rc.Snapshot().Proxy.Default != "http://user:secret@proxy.local:8080" {
		t.Fatalf("default proxy not set pp=%q rc=%q", pp.Default(), rc.Snapshot().Proxy.Default)
	}

	badProxy := httptest.NewRecorder()
	handler.ServeHTTP(badProxy, httptest.NewRequest(http.MethodPut, fmt.Sprintf("/dashboard/api/proxy/accounts/%d", id), strings.NewReader(`{"url":"not-a-url"}`)))
	if badProxy.Code != http.StatusBadRequest {
		t.Fatalf("bad account proxy status=%d body=%s", badProxy.Code, badProxy.Body.String())
	}

	accountProxy := httptest.NewRecorder()
	handler.ServeHTTP(accountProxy, httptest.NewRequest(http.MethodPut, fmt.Sprintf("/dashboard/api/proxy/accounts/%d", id), strings.NewReader(`{"url":"http://acct:pass@proxy.local:9000"}`)))
	if accountProxy.Code != http.StatusOK || strings.Contains(accountProxy.Body.String(), "devin-session-token") {
		t.Fatalf("account proxy status=%d body=%s", accountProxy.Code, accountProxy.Body.String())
	}
	acc, _ := am.GetAccount(int(id))
	if acc == nil || acc.ProxyURL != "http://acct:pass@proxy.local:9000" {
		t.Fatalf("account proxy=%+v", acc)
	}

	clearAccountProxy := httptest.NewRecorder()
	handler.ServeHTTP(clearAccountProxy, httptest.NewRequest(http.MethodDelete, fmt.Sprintf("/dashboard/api/proxy/accounts/%d", id), nil))
	if clearAccountProxy.Code != http.StatusOK {
		t.Fatalf("account proxy delete status=%d body=%s", clearAccountProxy.Code, clearAccountProxy.Body.String())
	}
	acc, _ = am.GetAccount(int(id))
	if acc == nil || acc.ProxyURL != "" {
		t.Fatalf("account proxy not cleared: %+v", acc)
	}

	drought := httptest.NewRecorder()
	handler.ServeHTTP(drought, httptest.NewRequest(http.MethodGet, "/dashboard/api/drought", nil))
	if drought.Code != http.StatusOK || !strings.Contains(drought.Body.String(), `"drought_accounts":1`) {
		t.Fatalf("drought status=%d body=%s", drought.Code, drought.Body.String())
	}

	upstream := httptest.NewRecorder()
	handler.ServeHTTP(upstream, httptest.NewRequest(http.MethodGet, "/dashboard/api/upstream-endpoints", nil))
	if upstream.Code != http.StatusOK || !strings.Contains(upstream.Body.String(), "GetChatMessage") || !strings.Contains(upstream.Body.String(), "application/grpc") {
		t.Fatalf("upstream status=%d body=%s", upstream.Code, upstream.Body.String())
	}

	tierAccess := httptest.NewRecorder()
	handler.ServeHTTP(tierAccess, httptest.NewRequest(http.MethodGet, "/dashboard/api/tier-access", nil))
	if tierAccess.Code != http.StatusOK || !strings.Contains(tierAccess.Body.String(), `"free":["gemini-2.5-flash"]`) || !strings.Contains(tierAccess.Body.String(), `"direct_supported"`) {
		t.Fatalf("tier-access status=%d body=%s", tierAccess.Code, tierAccess.Body.String())
	}
}

func TestDashboardProxySettersRejectPrivateHostsByDefault(t *testing.T) {
	cfg := &config.Config{
		Proxy:     config.ProxyConfig{TestURL: "https://server.codeium.com/", CooldownSeconds: 120},
		Health:    config.HealthConfig{IntervalSeconds: 300, TimeoutSeconds: 20, Model: "claude-sonnet-4.6"},
		Scheduler: config.SchedulerConfig{ReservationTTLSeconds: 180},
	}
	rc := runtimeconfig.NewManager(cfg)
	am := testHandlerAccountManager(t)
	id, err := am.AddAccount("proxy-private@example.com", "devin-session-token$abc", "u", "", "")
	if err != nil {
		t.Fatal(err)
	}
	pp := proxypool.NewManager(proxypool.Config{TestURL: "https://server.codeium.com/", Cooldown: 2 * time.Minute})
	handler := DashboardAPIHandler(am, nil, rc, nil, reusepool.NewPool(), nil, pp)

	global := httptest.NewRecorder()
	handler.ServeHTTP(global, httptest.NewRequest(http.MethodPut, "/dashboard/api/proxy/global", strings.NewReader(`{"url":"http://127.0.0.1:8080"}`)))
	if global.Code != http.StatusBadRequest || !strings.Contains(global.Body.String(), "ERR_PROXY_PRIVATE") {
		t.Fatalf("global status=%d body=%s", global.Code, global.Body.String())
	}

	accountProxy := httptest.NewRecorder()
	handler.ServeHTTP(accountProxy, httptest.NewRequest(http.MethodPut, fmt.Sprintf("/dashboard/api/proxy/accounts/%d", id), strings.NewReader(`{"url":"http://10.0.0.1:9000"}`)))
	if accountProxy.Code != http.StatusBadRequest || !strings.Contains(accountProxy.Body.String(), "ERR_PROXY_PRIVATE") {
		t.Fatalf("account proxy status=%d body=%s", accountProxy.Code, accountProxy.Body.String())
	}
	acc, _ := am.GetAccount(int(id))
	if acc == nil || acc.ProxyURL != "" {
		t.Fatalf("private proxy should not be persisted: %+v", acc)
	}
}

func TestDashboardAccountCompatibilityPatchAndDelete(t *testing.T) {
	am := testHandlerAccountManager(t)
	id, err := am.AddAccount("compat-account@example.com", "devin-session-token$abc", "u", "", "")
	if err != nil {
		t.Fatal(err)
	}
	res := &account.Reservation{Account: &account.Account{ID: int(id), Email: "compat-account@example.com"}, ModelID: "claude-sonnet-4.6"}
	am.RecordFailure(res, account.ErrorTransport, fmt.Errorf("dial timeout"))

	handler := DashboardAPIHandler(am, nil, nil, nil, reusepool.NewPool(), nil)
	body := `{"status":"disabled","label":"node-label","tier":"pro","blockedModels":["claude-opus-4.6","claude-opus-4.6"],"resetErrors":true}`
	patch := httptest.NewRecorder()
	handler.ServeHTTP(patch, httptest.NewRequest(http.MethodPatch, fmt.Sprintf("/dashboard/api/accounts/%d", id), strings.NewReader(body)))
	if patch.Code != http.StatusOK {
		t.Fatalf("patch status=%d body=%s", patch.Code, patch.Body.String())
	}
	acc, err := am.GetAccount(int(id))
	if err != nil {
		t.Fatal(err)
	}
	if acc == nil || acc.Enabled || acc.Banned || acc.Notes != "node-label" || acc.Tier != "pro" {
		t.Fatalf("patched account=%+v", acc)
	}
	blocked, err := am.GetBlockedModels(int(id))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(blocked, ",") != "claude-opus-4.6" {
		t.Fatalf("blocked=%v", blocked)
	}
	for _, row := range am.Snapshot().Accounts {
		if row.ID == int(id) && row.RecentErrors != nil {
			t.Fatalf("resetErrors did not clear recent errors: %+v", row)
		}
	}

	reactivate := httptest.NewRecorder()
	handler.ServeHTTP(reactivate, httptest.NewRequest(http.MethodPatch, fmt.Sprintf("/dashboard/api/accounts/%d", id), strings.NewReader(`{"status":"active"}`)))
	if reactivate.Code != http.StatusOK {
		t.Fatalf("reactivate status=%d body=%s", reactivate.Code, reactivate.Body.String())
	}
	acc, _ = am.GetAccount(int(id))
	if acc == nil || !acc.Enabled || acc.Banned {
		t.Fatalf("reactivated account=%+v", acc)
	}

	del := httptest.NewRecorder()
	handler.ServeHTTP(del, httptest.NewRequest(http.MethodDelete, fmt.Sprintf("/dashboard/api/accounts/%d", id), nil))
	if del.Code != http.StatusOK {
		t.Fatalf("delete status=%d body=%s", del.Code, del.Body.String())
	}
	acc, _ = am.GetAccount(int(id))
	if acc != nil {
		t.Fatalf("account not deleted: %+v", acc)
	}
}

func TestDashboardLogsExport(t *testing.T) {
	old := globalRequestStats
	globalRequestStats = &requestStatsStore{}
	t.Cleanup(func() { globalRequestStats = old })
	recordRequestEvent(RequestEvent{RequestID: "req_export", Route: "chat", Model: "claude-sonnet-4.6", Status: "ok", HTTPStatus: 200})
	recordRequestEvent(RequestEvent{RequestID: "req_filtered", Route: "messages", Model: "claude-sonnet-4.6", Status: "error", HTTPStatus: 429, ErrorClass: account.ErrorRateLimit})

	rec := httptest.NewRecorder()
	dashboardLogsExport(rec, httptest.NewRequest(http.MethodGet, "/dashboard/api/logs/export?format=csv", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "req_export") || rec.Header().Get("Content-Type") != "text/csv; charset=utf-8" {
		t.Fatalf("csv status=%d content-type=%q body=%s", rec.Code, rec.Header().Get("Content-Type"), rec.Body.String())
	}

	ndjson := httptest.NewRecorder()
	dashboardLogsExport(ndjson, httptest.NewRequest(http.MethodGet, "/dashboard/api/logs/export?format=ndjson", nil))
	if ndjson.Code != http.StatusOK || !strings.Contains(ndjson.Body.String(), `"req_id":"req_export"`) {
		t.Fatalf("ndjson status=%d body=%s", ndjson.Code, ndjson.Body.String())
	}

	filtered := httptest.NewRecorder()
	dashboardLogsExport(filtered, httptest.NewRequest(http.MethodGet, "/dashboard/api/logs/export?format=csv&status=error&error_class=rate_limit", nil))
	if filtered.Code != http.StatusOK || !strings.Contains(filtered.Body.String(), "req_filtered") || strings.Contains(filtered.Body.String(), "req_export") {
		t.Fatalf("filtered status=%d body=%s", filtered.Code, filtered.Body.String())
	}
}

func TestDashboardCacheSnapshot(t *testing.T) {
	rp := reusepool.NewPool()
	rp.Checkin("fp", &reusepool.Entry{AccountID: 7, APIKeyHash: "hash", ModelID: "m", CallerKey: "secret-caller", CascadeID: "cascade"}, time.Minute)
	snap := dashboardCacheSnapshot(rp)
	if snap["enabled"] != true || snap["entries"] != 1 {
		t.Fatalf("snapshot=%+v", snap)
	}
	stats, ok := snap["stats"].(reusepool.Stats)
	if !ok || stats.Stores != 1 {
		t.Fatalf("stats=%T %+v", snap["stats"], snap["stats"])
	}
	items, ok := snap["items"].([]cacheEntryView)
	if !ok || len(items) != 1 || items[0].AccountID != 7 {
		t.Fatalf("items=%T %+v", snap["items"], snap["items"])
	}
	if items[0].CallerKeyHash == "" || strings.Contains(fmt.Sprintf("%+v", items[0]), "secret-caller") {
		t.Fatalf("caller key leaked: %+v", items[0])
	}
}

func TestDashboardCacheAPIDeleteClearsReusePool(t *testing.T) {
	rp := reusepool.NewPool()
	rp.Checkin("fp", &reusepool.Entry{AccountID: 7, APIKeyHash: "hash", ModelID: "m", CallerKey: "caller"}, time.Minute)
	rec := httptest.NewRecorder()
	dashboardCacheAPI(rec, httptest.NewRequest(http.MethodDelete, "/dashboard/api/cache", nil), rp)
	if rec.Code != http.StatusOK {
		t.Fatalf("delete status=%d body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "caller") {
		t.Fatalf("caller key leaked: %s", rec.Body.String())
	}
	var body struct {
		Success bool `json:"success"`
		Cleared int  `json:"cleared"`
		Cache   struct {
			Entries int `json:"entries"`
		} `json:"cache"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !body.Success || body.Cleared != 1 || body.Cache.Entries != 0 || len(rp.Snapshot()) != 0 {
		t.Fatalf("body=%+v entries=%+v", body, rp.Snapshot())
	}
}

func TestDebugSchedulerHandlerMasksReuseCallerKey(t *testing.T) {
	rp := reusepool.NewPool()
	rp.Checkin("fp", &reusepool.Entry{AccountID: 7, APIKeyHash: "hash", ModelID: "m", CallerKey: "secret-caller", CascadeID: "cascade"}, time.Minute)
	rec := httptest.NewRecorder()
	DebugSchedulerHandler(testHandlerAccountManager(t), rp).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/debug/scheduler", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "secret-caller") || strings.Contains(rec.Body.String(), `"caller_key"`) {
		t.Fatalf("caller key leaked: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"caller_key_hash"`) {
		t.Fatalf("caller key hash missing: %s", rec.Body.String())
	}
}

func TestDashboardManualAvailabilityProbeUsesDirectChatAndClearsState(t *testing.T) {
	am := testHandlerAccountManager(t)
	id, err := am.AddAccount("probe@example.com", "devin-session-token$probe", "u", "http://proxy.example:8080", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := am.MarkCooldown(int(id), "claude-sonnet-4.6", time.Now().Add(time.Minute), "rate limit"); err != nil {
		t.Fatal(err)
	}
	res := &account.Reservation{Account: &account.Account{ID: int(id), Email: "probe@example.com"}, ModelID: "claude-sonnet-4.6"}
	for i := 0; i < 3; i++ {
		am.RecordFailure(res, account.ErrorTransport, fmt.Errorf("dial timeout"))
	}
	dc := &fakeDashboardDirectClient{}
	handler := DashboardAPIHandler(am, nil, nil, dc, reusepool.NewPool(), nil)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, fmt.Sprintf("/dashboard/api/availability/accounts/%d/models/claude-sonnet-4.6/probe", id), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("probe status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(dc.probes) != 1 || dc.probes[0].ProxyURL != "http://proxy.example:8080" || dc.probes[0].Model.ID != "claude-sonnet-4.6" {
		t.Fatalf("probe request=%+v", dc.probes)
	}
	for _, row := range am.Snapshot().Accounts {
		if row.ID == int(id) {
			if row.ModelCooldowns != nil || row.ModelBreakers != nil || row.RecentErrors != nil {
				t.Fatalf("availability state was not cleared: %+v", row)
			}
			return
		}
	}
	t.Fatal("account not found in snapshot")
}

func TestDashboardProxyAPI(t *testing.T) {
	pp := proxypool.NewManager(proxypool.Config{Default: "http://default:8080", RotateOnError: true, AllowPrivate: true})

	add := httptest.NewRecorder()
	dashboardProxyAPI(add, httptest.NewRequest(http.MethodPost, "/dashboard/api/proxy", strings.NewReader(`{"url":"http://user:secret@proxy.local:8080"}`)), pp)
	if add.Code != http.StatusOK || strings.Contains(add.Body.String(), "secret") {
		t.Fatalf("add status=%d body=%s", add.Code, add.Body.String())
	}
	var added map[string]any
	if err := json.Unmarshal(add.Body.Bytes(), &added); err != nil {
		t.Fatal(err)
	}
	item := added["proxy"].(map[string]any)
	id, _ := item["id"].(string)
	if id == "" || item["url"] != "" {
		t.Fatalf("proxy item=%v", item)
	}

	enabled := false
	patchBody, _ := json.Marshal(map[string]any{"id": id, "enabled": enabled, "cooldown_seconds": 30})
	patch := httptest.NewRecorder()
	dashboardProxyAPI(patch, httptest.NewRequest(http.MethodPatch, "/dashboard/api/proxy", bytes.NewReader(patchBody)), pp)
	if patch.Code != http.StatusOK || !strings.Contains(patch.Body.String(), "cooldown_until") {
		t.Fatalf("patch status=%d body=%s", patch.Code, patch.Body.String())
	}

	del := httptest.NewRecorder()
	dashboardProxyAPI(del, httptest.NewRequest(http.MethodDelete, "/dashboard/api/proxy?id="+id, nil), pp)
	if del.Code != http.StatusOK || !strings.Contains(del.Body.String(), `"success":true`) {
		t.Fatalf("delete status=%d body=%s", del.Code, del.Body.String())
	}
}

func TestDashboardDynamicProxyBindingActionsClearAvailability(t *testing.T) {
	verify := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ip":"203.0.113.10","country":"US","region":"NJ","city":"Newark","org":"AS test"}`))
	}))
	defer verify.Close()

	sqliteStore, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "windsurf.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqliteStore.Close() })
	am := account.NewManager(sqliteStore)
	id64, err := am.AddAccount("proxy-bind@example.com", "devin-session-token$abc", "u", "", "")
	if err != nil {
		t.Fatal(err)
	}
	id := int(id64)
	if err := am.MarkCooldown(id, "claude-sonnet-4.6", time.Now().Add(time.Hour), "old ip limit"); err != nil {
		t.Fatal(err)
	}
	res := &account.Reservation{Account: &account.Account{ID: id, Email: "proxy-bind@example.com"}, ModelID: "claude-sonnet-4.6"}
	am.RecordFailure(res, account.ErrorTransport, fmt.Errorf("old proxy transport failure"))

	pp := proxypool.NewManager(proxypool.Config{
		DB:               sqliteStore.DB,
		AccountBinding:   true,
		TestURL:          verify.URL,
		AllowPrivate:     true,
		Host:             "127.0.0.1",
		Port:             1000,
		UsernameTemplate: "user-sid-{sid}-ttl-{ttl}",
		Password:         "secret",
	})
	handler := DashboardAPIHandler(am, nil, nil, nil, reusepool.NewPool(), nil, pp)

	bind := httptest.NewRecorder()
	handler.ServeHTTP(bind, httptest.NewRequest(http.MethodPost, fmt.Sprintf("/dashboard/api/proxy/accounts/%d/bind", id), nil))
	if bind.Code != http.StatusOK || strings.Contains(bind.Body.String(), "secret") {
		t.Fatalf("bind status=%d body=%s", bind.Code, bind.Body.String())
	}
	for _, row := range am.Snapshot().Accounts {
		if row.ID == id {
			if row.ModelCooldowns != nil || row.ModelBreakers != nil || row.RecentErrors != nil || row.Inflight != 0 || row.RPMUsed != 0 {
				t.Fatalf("availability not cleared after bind: %+v", row)
			}
			break
		}
	}

	list := httptest.NewRecorder()
	handler.ServeHTTP(list, httptest.NewRequest(http.MethodGet, "/dashboard/api/proxy/bindings", nil))
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), `"account_id":`) || strings.Contains(list.Body.String(), "secret") {
		t.Fatalf("bindings status=%d body=%s", list.Code, list.Body.String())
	}

	clear := httptest.NewRecorder()
	handler.ServeHTTP(clear, httptest.NewRequest(http.MethodPost, fmt.Sprintf("/dashboard/api/proxy/accounts/%d/clear", id), nil))
	if clear.Code != http.StatusOK {
		t.Fatalf("clear status=%d body=%s", clear.Code, clear.Body.String())
	}
	binding, err := pp.Binding(id)
	if err != nil {
		t.Fatal(err)
	}
	if binding != nil {
		t.Fatalf("binding should be cleared: %+v", binding)
	}
}

func TestDashboardDynamicProxyCompatBatchActionsClearAvailability(t *testing.T) {
	verify := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ip":"203.0.113.20","country":"US","region":"CA","city":"San Jose","org":"AS test"}`))
	}))
	defer verify.Close()

	sqliteStore, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "windsurf.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqliteStore.Close() })
	am := account.NewManager(sqliteStore)
	id64, err := am.AddAccount("proxy-batch@example.com", "devin-session-token$abc", "u", "", "")
	if err != nil {
		t.Fatal(err)
	}
	id := int(id64)
	if err := am.MarkCooldown(id, "claude-sonnet-4.6", time.Now().Add(time.Hour), "old ip limit"); err != nil {
		t.Fatal(err)
	}
	pp := proxypool.NewManager(proxypool.Config{
		DB:               sqliteStore.DB,
		AccountBinding:   true,
		TestURL:          verify.URL,
		AllowPrivate:     true,
		Host:             "127.0.0.1",
		Port:             1000,
		UsernameTemplate: "user-sid-{sid}-ttl-{ttl}",
	})
	handler := DashboardAPIHandler(am, nil, nil, nil, reusepool.NewPool(), nil, pp)

	reqBody := fmt.Sprintf(`{"accountIds":[%d],"force":true}`, id)
	batch := httptest.NewRecorder()
	handler.ServeHTTP(batch, httptest.NewRequest(http.MethodPost, "/dashboard/api/dynamic-proxy/batch/bind", strings.NewReader(reqBody)))
	if batch.Code != http.StatusOK || !strings.Contains(batch.Body.String(), `"success":true`) {
		t.Fatalf("batch status=%d body=%s", batch.Code, batch.Body.String())
	}
	for _, row := range am.Snapshot().Accounts {
		if row.ID == id {
			if row.ModelCooldowns != nil {
				t.Fatalf("cooldown not cleared after compat bind: %+v", row)
			}
			return
		}
	}
	t.Fatal("account not found")
}

func testHandlerAccountManager(t *testing.T) *account.Manager {
	t.Helper()
	sqliteStore, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "windsurf.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	t.Cleanup(func() { _ = sqliteStore.Close() })
	return account.NewManager(sqliteStore)
}
