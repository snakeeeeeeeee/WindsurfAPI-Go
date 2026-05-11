package handler

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/zhangyu/windsurfapi-go/internal/account"
	"github.com/zhangyu/windsurfapi-go/internal/config"
	"github.com/zhangyu/windsurfapi-go/internal/health"
	"github.com/zhangyu/windsurfapi-go/internal/ls"
	"github.com/zhangyu/windsurfapi-go/internal/modelaccess"
	"github.com/zhangyu/windsurfapi-go/internal/models"
	proxypool "github.com/zhangyu/windsurfapi-go/internal/proxy"
	"github.com/zhangyu/windsurfapi-go/internal/redact"
	reusepool "github.com/zhangyu/windsurfapi-go/internal/reuse"
	runtimeconfig "github.com/zhangyu/windsurfapi-go/internal/runtimeconfig"
	usagepkg "github.com/zhangyu/windsurfapi-go/internal/usage"
	"github.com/zhangyu/windsurfapi-go/internal/windsurf"
	"github.com/zhangyu/windsurfapi-go/internal/windsurf/direct"
)

type accountImportRequest struct {
	Email         string                 `json:"email"`
	Token         string                 `json:"token"`
	APIKey        string                 `json:"api_key"`
	FirebaseToken string                 `json:"firebase_token"`
	UserID        string                 `json:"user_id"`
	ProxyURL      string                 `json:"proxy_url"`
	Proxy         string                 `json:"proxy"`
	Notes         string                 `json:"notes"`
	Label         string                 `json:"label"`
	Tier          string                 `json:"tier"`
	Enabled       *bool                  `json:"enabled"`
	Banned        *bool                  `json:"banned"`
	BlockedModels []string               `json:"blocked_models"`
	Extra         map[string]interface{} `json:"-"`
}

type dashboardDirectClient interface {
	GetUserStatusWithProxy(context.Context, string, string) (*direct.UserStatus, error)
	CheckMessageRateLimitWithProxy(context.Context, string, string) (*direct.RateLimit, error)
	GetCascadeModelConfigsWithProxy(context.Context, string, string) (*direct.ModelConfigs, error)
	ProbeChat(context.Context, string, string, *models.Model, string) (*windsurf.ChatResult, error)
	Snapshot() direct.Stats
}

func effectiveDashboardProxy(pp *proxypool.Manager, acc *account.Account) string {
	if acc == nil {
		return ""
	}
	if pp == nil {
		return acc.ProxyURL
	}
	return pp.EffectiveProxyURL(acc.ID, acc.ProxyURL)
}

func validateDashboardProxyURL(rc *runtimeconfig.Manager, proxyURL string) error {
	proxyURL = strings.TrimSpace(proxyURL)
	if proxyURL == "" {
		return nil
	}
	allowPrivate := false
	if rc != nil {
		allowPrivate = rc.Snapshot().Proxy.AllowPrivate
	}
	return proxypool.ValidateURLWithPrivate(proxyURL, allowPrivate)
}

type authLoginRequest struct {
	Accounts []accountImportRequest `json:"accounts"`
	Text     string                 `json:"text"`
	Raw      string                 `json:"raw"`
	ParseAs  string                 `json:"parseAs"`
	accountImportRequest
}

func AuthStatusHandler(am *account.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		snap := am.Snapshot()
		writeJSON(w, http.StatusOK, map[string]any{
			"authenticated": len(snap.Accounts) > 0,
			"counts":        accountCounts(snap.Accounts),
			"health":        snap.Health,
			"coordinator":   snap.Coordinator,
		})
	}
}

func AuthLoginHandler(am *account.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		var req authLoginRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid request: "+err.Error())
			return
		}
		items := req.Accounts
		if len(items) == 0 {
			items = []accountImportRequest{req.accountImportRequest}
		}
		results := make([]map[string]any, 0, len(items))
		for _, item := range items {
			acc, err := importAccount(am, item)
			if err != nil {
				results = append(results, map[string]any{"success": false, "email": item.Email, "error": err.Error()})
				continue
			}
			results = append(results, map[string]any{"success": true, "account": safeAccount(acc)})
		}
		status := http.StatusOK
		if len(results) == 1 {
			if ok, _ := results[0]["success"].(bool); !ok {
				status = http.StatusBadRequest
			}
		}
		snap := am.Snapshot()
		writeJSON(w, status, map[string]any{"results": results, "counts": accountCounts(snap.Accounts)})
	}
}

func AuthAccountsHandler(am *account.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			snap := am.Snapshot()
			writeJSON(w, http.StatusOK, map[string]any{"accounts": snap.Accounts, "counts": accountCounts(snap.Accounts)})
		case http.MethodPost:
			AuthLoginHandler(am).ServeHTTP(w, r)
		default:
			writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
	}
}

func AuthAccountByIDHandler(am *account.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := accountIDFromPath(r.URL.Path)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		switch r.Method {
		case http.MethodDelete:
			if err := am.DeleteAccount(id); err != nil {
				writeJSONError(w, http.StatusInternalServerError, err.Error())
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"success": true})
		case http.MethodPatch, http.MethodPut:
			raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
			if err != nil {
				writeJSONError(w, http.StatusBadRequest, "invalid request: "+err.Error())
				return
			}
			var patch accountImportRequest
			if err := json.NewDecoder(bytes.NewReader(raw)).Decode(&patch); err != nil {
				writeJSONError(w, http.StatusBadRequest, "invalid request: "+err.Error())
				return
			}
			present := map[string]json.RawMessage{}
			_ = json.Unmarshal(raw, &present)
			fields := accountPatchFieldsWithPresence(patch, present)
			if proxy, ok := fields["proxy_url"].(string); ok {
				if err := validateDashboardProxyURL(nil, proxy); err != nil {
					writeJSONError(w, http.StatusBadRequest, err.Error())
					return
				}
			}
			if len(fields) == 0 {
				writeJSONError(w, http.StatusBadRequest, "no supported account fields")
				return
			}
			if err := am.UpdateAccount(id, fields); err != nil {
				writeJSONError(w, http.StatusInternalServerError, err.Error())
				return
			}
			acc, err := am.GetAccount(id)
			if err != nil {
				writeJSONError(w, http.StatusInternalServerError, err.Error())
				return
			}
			if acc == nil {
				writeJSONError(w, http.StatusNotFound, "account not found")
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"success": true, "account": safeAccount(acc)})
		default:
			writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
	}
}

func AuthAccountModelsHandler(am *account.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := accountIDFromPath(strings.TrimSuffix(r.URL.Path, "/models"))
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		switch r.Method {
		case http.MethodGet:
			blocked, err := am.GetBlockedModels(id)
			if err != nil {
				writeJSONError(w, http.StatusInternalServerError, err.Error())
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"account_id": id, "blocked_models": blocked})
		case http.MethodPut, http.MethodPatch:
			var body struct {
				BlockedModels []string `json:"blocked_models"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				writeJSONError(w, http.StatusBadRequest, "invalid request: "+err.Error())
				return
			}
			if err := am.SetBlockedModels(id, body.BlockedModels); err != nil {
				writeJSONError(w, http.StatusInternalServerError, err.Error())
				return
			}
			blocked, _ := am.GetBlockedModels(id)
			writeJSON(w, http.StatusOK, map[string]any{"success": true, "account_id": id, "blocked_models": blocked})
		default:
			writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
	}
}

func AuthModelAccessHandler(access *modelaccess.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		modelID, err := modelIDFromAccessPath(r.URL.Path)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		switch r.Method {
		case http.MethodGet:
			item, err := access.Get(modelID)
			if err != nil {
				writeJSONError(w, http.StatusInternalServerError, err.Error())
				return
			}
			writeJSON(w, http.StatusOK, item)
		case http.MethodPatch, http.MethodPut:
			var patch modelaccess.Patch
			if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
				writeJSONError(w, http.StatusBadRequest, "invalid request: "+err.Error())
				return
			}
			item, err := access.Upsert(modelID, patch)
			if err != nil {
				writeJSONError(w, http.StatusBadRequest, err.Error())
				return
			}
			writeJSON(w, http.StatusOK, item)
		case http.MethodDelete:
			if err := access.Reset(modelID); err != nil {
				writeJSONError(w, http.StatusBadRequest, err.Error())
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"success": true, "model_id": modelID})
		default:
			writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
	}
}

func DashboardAPIHandler(am *account.Manager, access *modelaccess.Manager, rc *runtimeconfig.Manager, dc dashboardDirectClient, rp *reusepool.Pool, pool *ls.Pool, extras ...any) http.HandlerFunc {
	var proxyPool *proxypool.Manager
	var usageMgr *usagepkg.Manager
	for _, item := range extras {
		switch v := item.(type) {
		case *proxypool.Manager:
			proxyPool = v
		case *usagepkg.Manager:
			usageMgr = v
		}
	}
	return func(w http.ResponseWriter, r *http.Request) {
		subpath := strings.TrimPrefix(r.URL.Path, "/dashboard/api")
		if subpath == "" {
			subpath = "/overview"
		}
		if subpath == "/auth" {
			dashboardAuthProbeAPI(w, r, rc)
			return
		}
		if subpath == "/experimental" {
			dashboardExperimentalAPI(w, r, rc, rp)
			return
		}
		if subpath == "/settings/credentials" {
			dashboardCredentialsAPI(w, r, rc)
			return
		}
		if subpath == "/settings/env" {
			dashboardEnvAPI(w, r, rc)
			return
		}
		if subpath == "/import-accounts" {
			dashboardImportAccountsAPI(w, r, am)
			return
		}
		if subpath == "/experimental/conversation-pool" && r.Method == http.MethodDelete {
			dashboardExperimentalConversationPoolAPI(w, r, rp)
			return
		}
		if subpath == "/config" {
			dashboardConfigAPI(w, r, rc, proxyPool, usageMgr)
			return
		}
		if subpath == "/availability/config" {
			dashboardAvailabilityConfigAPI(w, r, rc)
			return
		}
		if subpath == "/availability/prune" {
			dashboardAvailabilityPruneAPI(w, r, am)
			return
		}
		if subpath == "/availability/worker/run" || subpath == "/accounts/probe-all" || subpath == "/accounts/refresh-credits" {
			dashboardAccountsRefreshAPI(w, r, am, dc, rc, proxyPool)
			return
		}
		if strings.HasPrefix(subpath, "/availability/models/") {
			dashboardAvailabilityModelAPI(w, r, am, dc, rc, proxyPool, subpath)
			return
		}
		if strings.HasPrefix(subpath, "/availability/accounts/") {
			dashboardAvailabilityAccountModelAPI(w, r, am, dc, rc, proxyPool, subpath)
			return
		}
		if strings.HasPrefix(subpath, "/accounts/") && strings.HasSuffix(subpath, "/refresh-credits") {
			dashboardAccountRefreshAPI(w, r, am, dc, rc, proxyPool)
			return
		}
		if strings.HasPrefix(subpath, "/accounts/") && strings.HasSuffix(subpath, "/probe") {
			dashboardAccountRefreshAPI(w, r, am, dc, rc, proxyPool)
			return
		}
		if strings.HasPrefix(subpath, "/accounts/") && strings.HasSuffix(subpath, "/rate-limit") {
			dashboardAccountRateLimitAPI(w, r, am, dc, rc, proxyPool)
			return
		}
		if subpath == "/stats" && r.Method == http.MethodDelete {
			resetRequestStats()
			writeJSON(w, http.StatusOK, map[string]any{"success": true})
			return
		}
		if subpath == "/logs/export" {
			dashboardLogsExport(w, r)
			return
		}
		if subpath == "/logs/stream" {
			dashboardLogsStream(w, r)
			return
		}
		if subpath == "/proxy" {
			dashboardProxyAPI(w, r, proxyPool)
			return
		}
		if subpath == "/proxy/bindings" {
			dashboardProxyBindingsAPI(w, r, proxyPool, am)
			return
		}
		if subpath == "/proxy/generate" {
			dashboardProxyGenerateAPI(w, r, proxyPool)
			return
		}
		if subpath == "/proxy/bind-accounts" {
			dashboardProxyBindAccountsAPI(w, r, am, proxyPool, rc)
			return
		}
		if subpath == "/tier-access" {
			dashboardTierAccessAPI(w, r)
			return
		}
		if subpath == "/proxy/global" {
			dashboardProxyGlobalAPI(w, r, rc, proxyPool)
			return
		}
		if strings.HasPrefix(subpath, "/proxy/accounts/") {
			dashboardProxyAccountAPI(w, r, am, rc, proxyPool)
			return
		}
		if strings.HasPrefix(subpath, "/dynamic-proxy/") {
			dashboardDynamicProxyCompatAPI(w, r, proxyPool, am)
			return
		}
		if subpath == "/model-access" && (r.Method == http.MethodPut || r.Method == http.MethodPatch) {
			dashboardModelAccessCompatAPI(w, r, access)
			return
		}
		if subpath == "/model-access/add" || subpath == "/model-access/remove" {
			dashboardModelAccessListMutationAPI(w, r, access, strings.TrimPrefix(subpath, "/model-access/"))
			return
		}
		if dashboardLegacyUnavailableAPI(w, r, subpath) {
			return
		}
		if subpath == "/cache" && (r.Method == http.MethodGet || r.Method == http.MethodDelete) {
			dashboardCacheAPI(w, r, rp)
			return
		}
		if strings.HasPrefix(subpath, "/accounts/") {
			dashboardAccountCompatAPI(w, r, am)
			return
		}
		if r.Method != http.MethodGet {
			writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		snap := am.Snapshot()
		switch subpath {
		case "/overview":
			writeJSON(w, http.StatusOK, dashboardOverview(snap, dc, rp))
		case "/accounts":
			writeJSON(w, http.StatusOK, map[string]any{"accounts": snap.Accounts, "counts": accountCounts(snap.Accounts)})
		case "/scheduler":
			writeJSON(w, http.StatusOK, map[string]any{"events": snap.Events, "health": snap.Health, "coordinator": snap.Coordinator, "reuse": dashboardReuseStats(rp), "entries": reuseDebugEntries(rp)})
		case "/direct":
			writeJSON(w, http.StatusOK, dashboardDirectSnapshot(dc))
		case "/proxy":
			writeJSON(w, http.StatusOK, dashboardProxySnapshot(proxyPool))
		case "/availability":
			writeJSON(w, http.StatusOK, map[string]any{"health": snap.Health, "accounts": snap.Accounts, "coordinator": snap.Coordinator})
		case "/drought":
			writeJSON(w, http.StatusOK, dashboardDroughtSummary(snap))
		case "/upstream-endpoints":
			writeJSON(w, http.StatusOK, dashboardUpstreamEndpoints(dc))
		case "/models":
			scope := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("scope")))
			full := scope == "all" || scope == "full" || scope == "legacy"
			writeJSON(w, http.StatusOK, map[string]any{"object": "list", "scope": dashboardModelScope(full), "data": dashboardModelList(access, full)})
		case "/model-access":
			if access == nil {
				writeJSON(w, http.StatusOK, map[string]any{"models": dashboardModelList(access, false), "overrides": []any{}, "config": map[string]any{"mode": "all", "list": []string{}}})
				return
			}
			all, err := access.List()
			if err != nil {
				writeJSONError(w, http.StatusInternalServerError, err.Error())
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"models": dashboardModelList(access, false), "overrides": all, "config": dashboardModelAccessConfig(access)})
		case "/legacy", "/ls":
			if pool == nil {
				writeJSON(w, http.StatusOK, map[string]any{"entries": []any{}, "legacy": true})
				return
			}
			writeJSON(w, http.StatusOK, pool.Snapshot())
		case "/stats":
			writeJSON(w, http.StatusOK, requestStatsSnapshot())
		case "/logs":
			limit := intQuery(r, "limit", 100)
			if limit <= 0 {
				limit = 100
			}
			if limit > 1000 {
				limit = 1000
			}
			writeJSON(w, http.StatusOK, map[string]any{"requests": requestLogsSnapshotFiltered(limit, requestLogFilterFromQuery(r)), "events": snap.Events})
		default:
			writeJSONError(w, http.StatusNotFound, "dashboard api not found")
		}
	}
}

func dashboardModelAccessCompatAPI(w http.ResponseWriter, r *http.Request, access *modelaccess.Manager) {
	if access == nil {
		writeJSONError(w, http.StatusNotFound, "model access manager unavailable")
		return
	}
	var body struct {
		Mode string   `json:"mode"`
		List []string `json:"list"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid request: "+err.Error())
		return
	}
	mode := strings.ToLower(strings.TrimSpace(body.Mode))
	if mode == "" {
		mode = "all"
	}
	if err := applyDashboardModelAccessList(access, mode, body.List); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "config": dashboardModelAccessConfig(access), "models": dashboardModelList(access, false)})
}

func dashboardModelAccessListMutationAPI(w http.ResponseWriter, r *http.Request, access *modelaccess.Manager, action string) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if access == nil {
		writeJSONError(w, http.StatusNotFound, "model access manager unavailable")
		return
	}
	var body struct {
		Model string `json:"model"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid request: "+err.Error())
		return
	}
	modelID := models.NormalizeModelID(body.Model)
	if modelID == "" || models.GetModelByID(modelID) == nil {
		writeJSONError(w, http.StatusBadRequest, "model is required")
		return
	}
	cfg := access.Config()
	if cfg.Mode == "" || cfg.Mode == "all" {
		cfg.Mode = "allowlist"
	}
	list := append([]string(nil), cfg.List...)
	if action == "add" {
		if !containsString(list, modelID) {
			list = append(list, modelID)
		}
	} else {
		filtered := list[:0]
		for _, item := range list {
			if item != modelID {
				filtered = append(filtered, item)
			}
		}
		list = filtered
	}
	if err := applyDashboardModelAccessList(access, cfg.Mode, list); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "config": dashboardModelAccessConfig(access), "model": modelID})
}

func dashboardExperimentalAPI(w http.ResponseWriter, r *http.Request, rc *runtimeconfig.Manager, rp *reusepool.Pool) {
	if rc == nil {
		writeJSONError(w, http.StatusNotFound, "runtime config unavailable")
		return
	}
	switch r.Method {
	case http.MethodGet:
		snap := rc.Snapshot()
		poolStats := reusepool.Stats{}
		if rp != nil {
			poolStats = rp.Stats()
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"flags": map[string]any{
				"cascadeConversationReuse": true,
				"directNativeChatPrompts":  snap.Direct.NativeChatPrompts,
				"directOnly":               true,
			},
			"conversationPool": poolStats,
		})
	case http.MethodPut, http.MethodPatch:
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid request: "+err.Error())
			return
		}
		if raw, ok := body["directNativeChatPrompts"]; ok {
			snap := rc.Snapshot()
			patch := runtimeconfig.Patch{Direct: &runtimeconfig.DirectView{
				Hosts:             snap.Direct.Hosts,
				TimeoutSeconds:    snap.Direct.TimeoutSeconds,
				NativeChatPrompts: truthy(raw),
			}}
			if _, err := rc.Patch(patch); err != nil {
				writeJSONError(w, http.StatusBadRequest, err.Error())
				return
			}
		}
		if raw, ok := body["cascadeConversationReuse"]; ok && !truthy(raw) && rp != nil {
			rp.Clear()
		}
		snap := rc.Snapshot()
		poolStats := reusepool.Stats{}
		if rp != nil {
			poolStats = rp.Stats()
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"flags": map[string]any{
				"cascadeConversationReuse": true,
				"directNativeChatPrompts":  snap.Direct.NativeChatPrompts,
				"directOnly":               true,
			},
			"conversationPool": poolStats,
		})
	default:
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func dashboardAvailabilityConfigAPI(w http.ResponseWriter, r *http.Request, rc *runtimeconfig.Manager) {
	if rc == nil {
		writeJSONError(w, http.StatusNotFound, "runtime config unavailable")
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "config": rc.Snapshot().Health})
	case http.MethodPut, http.MethodPatch:
		var body runtimeconfig.HealthView
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid request: "+err.Error())
			return
		}
		snap := rc.Snapshot()
		merged := snap.Health
		if body.IntervalSeconds != 0 {
			merged.IntervalSeconds = body.IntervalSeconds
		}
		if body.TimeoutSeconds != 0 {
			merged.TimeoutSeconds = body.TimeoutSeconds
		}
		if strings.TrimSpace(body.Model) != "" {
			merged.Model = body.Model
		}
		merged.Enabled = body.Enabled
		merged.MarkInvalidBanned = body.MarkInvalidBanned
		merged.CheckModelConfigs = body.CheckModelConfigs
		merged.ReadyRequireCheck = body.ReadyRequireCheck
		next, err := rc.Patch(runtimeconfig.Patch{Health: &merged})
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "config": next.Health})
	default:
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func dashboardAvailabilityPruneAPI(w http.ResponseWriter, r *http.Request, am *account.Manager) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if am == nil {
		writeJSONError(w, http.StatusNotFound, "account manager unavailable")
		return
	}
	pruned := am.PruneAvailability()
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "pruned": pruned, "snapshot": am.Snapshot()})
}

func dashboardAccountsRefreshAPI(w http.ResponseWriter, r *http.Request, am *account.Manager, dc dashboardDirectClient, rc *runtimeconfig.Manager, pp *proxypool.Manager) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if am == nil || dc == nil {
		writeJSONError(w, http.StatusNotFound, "account or direct client unavailable")
		return
	}
	accounts, err := am.GetAllAccounts()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	var body struct {
		AccountIDs      []int  `json:"account_ids"`
		IDs             []int  `json:"ids"`
		IncludeDisabled bool   `json:"include_disabled"`
		CheckModels     *bool  `json:"check_models"`
		Model           string `json:"model"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	ids := append([]int(nil), body.AccountIDs...)
	ids = append(ids, body.IDs...)
	allow := intSet(ids)
	results := []map[string]any{}
	for i := range accounts {
		a := accounts[i]
		if len(allow) > 0 && !allow[a.ID] {
			continue
		}
		if !body.IncludeDisabled && (!a.Enabled || a.Banned) {
			continue
		}
		results = append(results, dashboardRefreshOneAccount(r.Context(), am, dc, rc, pp, &a, body.CheckModels == nil || *body.CheckModels))
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "total": len(results), "results": results, "snapshot": am.Snapshot()})
}

func dashboardAccountRefreshAPI(w http.ResponseWriter, r *http.Request, am *account.Manager, dc dashboardDirectClient, rc *runtimeconfig.Manager, pp *proxypool.Manager) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if am == nil || dc == nil {
		writeJSONError(w, http.StatusNotFound, "account or direct client unavailable")
		return
	}
	id, err := dashboardAccountIDFromPath(r.URL.Path)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	acc, err := am.GetAccount(id)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if acc == nil {
		writeJSONError(w, http.StatusNotFound, "account not found")
		return
	}
	var body struct {
		CheckModels *bool `json:"check_models"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	result := dashboardRefreshOneAccount(r.Context(), am, dc, rc, pp, acc, body.CheckModels == nil || *body.CheckModels)
	status := http.StatusOK
	if ok, _ := result["success"].(bool); !ok {
		status = http.StatusBadRequest
	}
	writeJSON(w, status, result)
}

func dashboardAccountRateLimitAPI(w http.ResponseWriter, r *http.Request, am *account.Manager, dc dashboardDirectClient, rc *runtimeconfig.Manager, pp *proxypool.Manager) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if am == nil || dc == nil {
		writeJSONError(w, http.StatusNotFound, "account or direct client unavailable")
		return
	}
	id, err := dashboardAccountIDFromPath(r.URL.Path)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	acc, err := am.GetAccount(id)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if acc == nil {
		writeJSONError(w, http.StatusNotFound, "account not found")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), dashboardHealthTimeout(rc))
	defer cancel()
	rl, err := dc.CheckMessageRateLimitWithProxy(ctx, acc.FirebaseToken, effectiveDashboardProxy(pp, acc))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, redact.Text(err.Error()))
		return
	}
	var retryUntil *time.Time
	if !rl.HasCapacity && rl.RetryAfterMS != nil && *rl.RetryAfterMS > 0 {
		until := time.Now().Add(time.Duration(*rl.RetryAfterMS) * time.Millisecond)
		retryUntil = &until
		_ = am.UpdateHealthDetails(acc.ID, account.HealthUpdate{RateLimitedUntil: retryUntil, HealthCheckedAt: time.Now(), Note: "manual rate-limit check"})
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "account_id": acc.ID, "rate_limit": rl, "rate_limited_until": retryUntil})
}

func dashboardAvailabilityModelAPI(w http.ResponseWriter, r *http.Request, am *account.Manager, dc dashboardDirectClient, rc *runtimeconfig.Manager, pp *proxypool.Manager, subpath string) {
	modelID, action, err := parseAvailabilityModelPath(subpath)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	switch {
	case action == "probe" && r.Method == http.MethodPost:
		dashboardProbeModelAPI(w, r, am, dc, rc, pp, modelID)
	case action == "breaker" && r.Method == http.MethodDelete:
		cleared := am.ClearModelBreakers(modelID)
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "model": modelID, "cleared": cleared, "snapshot": am.Snapshot()})
	default:
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func dashboardAvailabilityAccountModelAPI(w http.ResponseWriter, r *http.Request, am *account.Manager, dc dashboardDirectClient, rc *runtimeconfig.Manager, pp *proxypool.Manager, subpath string) {
	accountID, modelID, action, err := parseAvailabilityAccountModelPath(subpath)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	switch {
	case action == "probe" && r.Method == http.MethodPost:
		acc, err := am.GetAccount(accountID)
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if acc == nil {
			writeJSONError(w, http.StatusNotFound, "account not found")
			return
		}
		result := dashboardProbeOneAccountModel(r.Context(), am, dc, rc, pp, acc, modelID, "")
		status := http.StatusOK
		if ok, _ := result["success"].(bool); !ok {
			status = http.StatusBadRequest
		}
		writeJSON(w, status, result)
	case action == "cooldown" && r.Method == http.MethodDelete:
		if err := am.ClearCooldown(accountID, modelID); err != nil {
			writeJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "account_id": accountID, "model": modelID, "snapshot": am.Snapshot()})
	case action == "breaker" && r.Method == http.MethodDelete:
		am.ClearModelBreaker(accountID, modelID)
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "account_id": accountID, "model": modelID, "snapshot": am.Snapshot()})
	default:
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func dashboardProbeModelAPI(w http.ResponseWriter, r *http.Request, am *account.Manager, dc dashboardDirectClient, rc *runtimeconfig.Manager, pp *proxypool.Manager, modelID string) {
	if am == nil || dc == nil {
		writeJSONError(w, http.StatusNotFound, "account or direct client unavailable")
		return
	}
	accounts, err := am.GetAllAccounts()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	var body struct {
		AccountIDs      []int  `json:"account_ids"`
		IDs             []int  `json:"ids"`
		IncludeDisabled bool   `json:"include_disabled"`
		Limit           int    `json:"limit"`
		Prompt          string `json:"prompt"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	ids := append([]int(nil), body.AccountIDs...)
	ids = append(ids, body.IDs...)
	allow := intSet(ids)
	limit := body.Limit
	if limit <= 0 && len(allow) == 0 {
		limit = 1
	}
	results := []map[string]any{}
	success := false
	for i := range accounts {
		a := accounts[i]
		if len(allow) > 0 && !allow[a.ID] {
			continue
		}
		if !body.IncludeDisabled && (!a.Enabled || a.Banned) {
			continue
		}
		result := dashboardProbeOneAccountModel(r.Context(), am, dc, rc, pp, &a, modelID, body.Prompt)
		results = append(results, result)
		if ok, _ := result["success"].(bool); ok {
			success = true
			break
		}
		if limit > 0 && len(results) >= limit {
			break
		}
	}
	status := http.StatusOK
	if !success {
		status = http.StatusBadRequest
	}
	writeJSON(w, status, map[string]any{
		"success":  success,
		"model":    modelID,
		"total":    len(results),
		"results":  results,
		"snapshot": am.Snapshot(),
	})
}

func dashboardProbeOneAccountModel(ctx context.Context, am *account.Manager, dc dashboardDirectClient, rc *runtimeconfig.Manager, pp *proxypool.Manager, acc *account.Account, modelID, prompt string) map[string]any {
	if acc == nil {
		return map[string]any{"success": false, "error": "account missing"}
	}
	model := models.GetModelByID(modelID)
	if model == nil {
		return map[string]any{"success": false, "account_id": acc.ID, "email": acc.Email, "model": modelID, "error": "unknown model"}
	}
	if !model.DirectSupported {
		return map[string]any{"success": false, "account_id": acc.ID, "email": acc.Email, "model": model.ID, "error": "model unsupported on direct backend: " + modelUnsupportedReason(model)}
	}
	if strings.TrimSpace(acc.FirebaseToken) == "" {
		return map[string]any{"success": false, "account_id": acc.ID, "email": acc.Email, "model": model.ID, "error": "token missing"}
	}
	start := time.Now()
	checkCtx, cancel := context.WithTimeout(ctx, dashboardHealthTimeout(rc))
	defer cancel()
	proxyURL := effectiveDashboardProxy(pp, acc)
	result, err := dc.ProbeChat(checkCtx, acc.FirebaseToken, proxyURL, model, prompt)
	if err != nil {
		class := classifyError(err)
		res := &account.Reservation{Account: acc, ModelID: model.ID}
		am.RecordFailure(res, class, err)
		switch class {
		case account.ErrorRateLimit, account.ErrorModelNotAvailable:
			_ = am.MarkCooldown(acc.ID, model.ID, cooldownUntilForError(time.Now(), class, err), err.Error())
		case account.ErrorBanSignal:
			_ = am.MarkBanned(acc.ID)
		}
		return map[string]any{
			"success":    false,
			"account_id": acc.ID,
			"email":      acc.Email,
			"model":      model.ID,
			"class":      class,
			"error":      redact.Text(err.Error()),
			"elapsed_ms": time.Since(start).Milliseconds(),
		}
	}
	res := &account.Reservation{Account: acc, ModelID: model.ID}
	am.RecordSuccess(res, result.Usage)
	_ = am.ClearCooldown(acc.ID, model.ID)
	am.ClearModelBreaker(acc.ID, model.ID)
	return map[string]any{
		"success":       true,
		"account_id":    acc.ID,
		"email":         acc.Email,
		"model":         model.ID,
		"text":          truncateDebugText(result.Text, 120),
		"thinking":      truncateDebugText(result.Thinking, 120),
		"tool_calls":    len(result.ToolCalls),
		"finish_reason": result.FinishReason,
		"elapsed_ms":    time.Since(start).Milliseconds(),
	}
}

func truncateDebugText(text string, limit int) string {
	text = strings.TrimSpace(redact.Text(text))
	if limit <= 0 || len(text) <= limit {
		return text
	}
	return text[:limit] + "..."
}

func dashboardRefreshOneAccount(ctx context.Context, am *account.Manager, dc dashboardDirectClient, rc *runtimeconfig.Manager, pp *proxypool.Manager, acc *account.Account, checkModels bool) map[string]any {
	if acc == nil {
		return map[string]any{"success": false, "error": "account missing"}
	}
	if strings.TrimSpace(acc.FirebaseToken) == "" {
		return map[string]any{"success": false, "account_id": acc.ID, "email": acc.Email, "error": "token missing"}
	}
	start := time.Now()
	checkCtx, cancel := context.WithTimeout(ctx, dashboardHealthTimeout(rc))
	defer cancel()
	proxyURL := effectiveDashboardProxy(pp, acc)
	status, err := dc.GetUserStatusWithProxy(checkCtx, acc.FirebaseToken, proxyURL)
	if err != nil {
		if dashboardInvalidToken(err) {
			_ = am.MarkBanned(acc.ID)
		}
		return map[string]any{"success": false, "account_id": acc.ID, "email": acc.Email, "invalid": dashboardInvalidToken(err), "error": redact.Text(err.Error()), "elapsed_ms": time.Since(start).Milliseconds()}
	}
	rl, err := dc.CheckMessageRateLimitWithProxy(checkCtx, acc.FirebaseToken, proxyURL)
	if err != nil {
		return map[string]any{"success": false, "account_id": acc.ID, "email": acc.Email, "error": redact.Text(err.Error()), "stage": "rate_limit", "elapsed_ms": time.Since(start).Milliseconds()}
	}
	configCount := 0
	if checkModels {
		cfgs, err := dc.GetCascadeModelConfigsWithProxy(checkCtx, acc.FirebaseToken, proxyURL)
		if err != nil {
			return map[string]any{"success": false, "account_id": acc.ID, "email": acc.Email, "error": redact.Text(err.Error()), "stage": "model_configs", "elapsed_ms": time.Since(start).Milliseconds()}
		}
		configCount = len(cfgs.Configs)
	}
	var retryUntil *time.Time
	if !rl.HasCapacity && rl.RetryAfterMS != nil && *rl.RetryAfterMS > 0 {
		until := time.Now().Add(time.Duration(*rl.RetryAfterMS) * time.Millisecond)
		retryUntil = &until
	}
	note := fmt.Sprintf("dashboard refresh plan=%s checked_at=%s", status.PlanName, time.Now().Format(time.RFC3339))
	update := health.AccountHealthUpdate(health.TierFromPlan(status.PlanName), status, retryUntil, note)
	update.ModelConfigCount = configCount
	if err := am.UpdateHealthDetails(acc.ID, update); err != nil {
		return map[string]any{"success": false, "account_id": acc.ID, "email": acc.Email, "error": redact.Text(err.Error()), "stage": "update_health", "elapsed_ms": time.Since(start).Milliseconds()}
	}
	return map[string]any{
		"success":            true,
		"account_id":         acc.ID,
		"email":              acc.Email,
		"tier":               health.TierFromPlan(status.PlanName),
		"plan_name":          status.PlanName,
		"daily_percent":      status.DailyPercent,
		"weekly_percent":     status.WeeklyPercent,
		"prompt":             status.Prompt,
		"flex":               status.Flex,
		"overage_balance":    status.OverageBalance,
		"rate_capacity":      rl.HasCapacity,
		"rate_limited_until": retryUntil,
		"model_configs":      configCount,
		"elapsed_ms":         time.Since(start).Milliseconds(),
	}
}

func dashboardHealthTimeout(rc *runtimeconfig.Manager) time.Duration {
	seconds := 20
	if rc != nil {
		if v := rc.Snapshot().Health.TimeoutSeconds; v > 0 {
			seconds = v
		}
	}
	return time.Duration(seconds) * time.Second
}

func intSet(ids []int) map[int]bool {
	out := map[int]bool{}
	for _, id := range ids {
		if id > 0 {
			out[id] = true
		}
	}
	return out
}

func dashboardInvalidToken(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "invalid token") ||
		strings.Contains(msg, "invalid devin token") ||
		strings.Contains(msg, "failed to validate devin token") ||
		strings.Contains(msg, "logging out and logging in again") ||
		strings.Contains(msg, "authentication failed")
}

func parseAvailabilityModelPath(subpath string) (string, string, error) {
	parts := strings.Split(strings.Trim(subpath, "/"), "/")
	if len(parts) < 3 || parts[0] != "availability" || parts[1] != "models" {
		return "", "", fmt.Errorf("invalid availability model path")
	}
	modelID := models.NormalizeModelID(parts[2])
	if modelID == "" {
		return "", "", fmt.Errorf("model is required")
	}
	action := ""
	if len(parts) > 3 {
		action = parts[3]
	}
	return modelID, action, nil
}

func parseAvailabilityAccountModelPath(subpath string) (int, string, string, error) {
	parts := strings.Split(strings.Trim(subpath, "/"), "/")
	if len(parts) < 5 || parts[0] != "availability" || parts[1] != "accounts" || parts[3] != "models" {
		return 0, "", "", fmt.Errorf("invalid availability account-model path")
	}
	accountID, err := strconv.Atoi(parts[2])
	if err != nil || accountID <= 0 {
		return 0, "", "", fmt.Errorf("invalid account id")
	}
	modelID := models.NormalizeModelID(parts[4])
	if modelID == "" {
		return 0, "", "", fmt.Errorf("model is required")
	}
	action := ""
	if len(parts) > 5 {
		action = parts[5]
	}
	return accountID, modelID, action, nil
}

func dashboardLegacyUnavailableAPI(w http.ResponseWriter, r *http.Request, subpath string) bool {
	switch {
	case subpath == "/system-prompts" && r.Method == http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]any{
			"prompts": map[string]any{},
			"legacy":  true,
			"message": "Direct-only Go backend uses compiled protocol prompts; Node runtime system prompt editing is not active.",
		})
		return true
	case subpath == "/system-prompts" && (r.Method == http.MethodPut || r.Method == http.MethodPatch):
		writeJSON(w, http.StatusOK, map[string]any{
			"success": false,
			"legacy":  true,
			"error":   "ERR_SYSTEM_PROMPTS_NOT_RUNTIME_EDITABLE",
		})
		return true
	case strings.HasPrefix(subpath, "/system-prompts/") && r.Method == http.MethodDelete:
		writeJSON(w, http.StatusOK, map[string]any{
			"success": false,
			"legacy":  true,
			"error":   "ERR_SYSTEM_PROMPTS_NOT_RUNTIME_EDITABLE",
		})
		return true
	case subpath == "/auto-update/quiet-window" && r.Method == http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":      true,
			"enabled": false,
			"legacy":  true,
			"reason":  "node_quiet_window_update_not_used_by_go_runtime",
		})
		return true
	case subpath == "/auto-update/quiet-window" && (r.Method == http.MethodPut || r.Method == http.MethodPatch):
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":      false,
			"enabled": false,
			"legacy":  true,
			"error":   "ERR_QUIET_WINDOW_UPDATE_NOT_SUPPORTED",
		})
		return true
	case subpath == "/auto-update/quiet-window/run" && r.Method == http.MethodPost:
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":     false,
			"legacy": true,
			"error":  "ERR_QUIET_WINDOW_UPDATE_NOT_SUPPORTED",
		})
		return true
	case subpath == "/service/restart" && r.Method == http.MethodPost:
		writeJSON(w, http.StatusOK, map[string]any{
			"success":    false,
			"restarting": false,
			"error":      "ERR_SERVICE_RESTART_NOT_SUPPORTED",
			"message":    "Restart Go service through systemd/docker/orchestrator instead of Dashboard API.",
		})
		return true
	case subpath == "/batch-import" && r.Method == http.MethodPost:
		writeJSON(w, http.StatusNotImplemented, map[string]any{
			"success": false,
			"error":   "ERR_BATCH_LOGIN_NOT_IMPLEMENTED",
			"message": "Use /dashboard/api/import-accounts with already obtained tokens.",
		})
		return true
	case strings.HasSuffix(subpath, "/refresh-token") && strings.HasPrefix(subpath, "/accounts/") && r.Method == http.MethodPost:
		writeJSON(w, http.StatusNotImplemented, map[string]any{
			"success": false,
			"error":   "ERR_REFRESH_TOKEN_NOT_IMPLEMENTED_DIRECT_ONLY",
		})
		return true
	case strings.HasPrefix(subpath, "/account/") && strings.HasSuffix(subpath, "/reveal-key") && r.Method == http.MethodPost:
		writeJSON(w, http.StatusForbidden, map[string]any{
			"success": false,
			"error":   "ERR_REVEAL_KEY_DISABLED",
			"message": "Go Dashboard never returns raw account tokens.",
		})
		return true
	case subpath == "/test-proxy" && r.Method == http.MethodPost:
		writeJSON(w, http.StatusNotImplemented, map[string]any{
			"ok":      false,
			"error":   "ERR_USE_DYNAMIC_PROXY_API",
			"message": "Use /dashboard/api/proxy dynamic proxy test action in Go Dashboard.",
		})
		return true
	case strings.HasPrefix(subpath, "/dynamic-proxy/"):
		writeJSON(w, http.StatusNotImplemented, map[string]any{
			"success": false,
			"error":   "ERR_USE_GO_PROXY_API",
			"message": "Use /dashboard/api/proxy and /dashboard/api/proxy/accounts/:id in the Go Dashboard.",
		})
		return true
	case subpath == "/accounts/import-local-availability" && r.Method == http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]any{
			"available": false,
			"reason":    "direct_only_go_backend",
			"hint":      "Go production backend does not read local Windsurf desktop state; import tokens through /dashboard/api/import-accounts.",
		})
		return true
	case subpath == "/accounts/import-local" && r.Method == http.MethodGet:
		writeJSON(w, http.StatusForbidden, map[string]any{
			"success": false,
			"error":   "ERR_LOCAL_IMPORT_NOT_AVAILABLE_DIRECT_ONLY",
			"message": "Direct-only Go backend does not import local Windsurf desktop credentials.",
		})
		return true
	case subpath == "/self-update/check" && r.Method == http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":        false,
			"available": false,
			"mode":      "external_deployment",
			"reason":    "self_update_not_supported_in_go_runtime",
		})
		return true
	case subpath == "/self-update" && r.Method == http.MethodPost:
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":        false,
			"available": false,
			"error":     "ERR_SELF_UPDATE_NOT_SUPPORTED",
			"reason":    "manage Go deployments with systemd/docker/CI instead of Node dashboard self-update",
		})
		return true
	case strings.HasPrefix(subpath, "/langserver/"):
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":     false,
			"legacy": true,
			"error":  "ERR_LEGACY_LS_NOT_ON_MAIN_PATH",
			"reason": "Go production chat/messages/responses are Direct-only; LS routes are disabled except /dashboard/api/ls debug snapshot.",
		})
		return true
	case subpath == "/windsurf-login" || subpath == "/windsurf-login/batch" || subpath == "/oauth-login":
		if r.Method == http.MethodPost {
			writeJSON(w, http.StatusNotImplemented, map[string]any{
				"success": false,
				"error":   "ERR_WINDSURF_LOGIN_NOT_IMPLEMENTED",
				"message": "Use existing Windsurf tokens via /dashboard/api/import-accounts for the Direct-only Go backend.",
			})
			return true
		}
	}
	return false
}

func dashboardAccountCompatAPI(w http.ResponseWriter, r *http.Request, am *account.Manager) {
	if am == nil {
		writeJSONError(w, http.StatusNotFound, "account manager unavailable")
		return
	}
	id, err := dashboardAccountIDFromPath(r.URL.Path)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	switch r.Method {
	case http.MethodPatch, http.MethodPut:
		raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid request: "+err.Error())
			return
		}
		var body map[string]any
		if err := json.NewDecoder(bytes.NewReader(raw)).Decode(&body); err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid request: "+err.Error())
			return
		}
		fields, blocked, resetErrors, err := dashboardAccountCompatPatch(body)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		if len(fields) > 0 {
			if err := am.UpdateAccount(id, fields); err != nil {
				writeJSONError(w, http.StatusInternalServerError, err.Error())
				return
			}
		}
		if blocked != nil {
			if err := am.SetBlockedModels(id, blocked); err != nil {
				writeJSONError(w, http.StatusInternalServerError, err.Error())
				return
			}
		}
		if resetErrors {
			am.ResetAccountErrors(id)
		}
		acc, err := am.GetAccount(id)
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if acc == nil {
			writeJSONError(w, http.StatusNotFound, "account not found")
			return
		}
		out := safeAccount(acc)
		if blocked != nil {
			out["blocked_models"] = blocked
		} else if existing, err := am.GetBlockedModels(id); err == nil {
			out["blocked_models"] = existing
		}
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "account": out})
	case http.MethodDelete:
		if err := am.DeleteAccount(id); err != nil {
			writeJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"success": true})
	default:
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func dashboardAccountCompatPatch(body map[string]any) (map[string]interface{}, []string, bool, error) {
	fields := map[string]interface{}{}
	var blocked []string
	resetErrors := false
	if raw, ok := body["status"]; ok {
		switch strings.ToLower(strings.TrimSpace(fmt.Sprint(raw))) {
		case "active", "enabled":
			fields["enabled"] = true
			fields["banned"] = false
		case "disabled", "inactive":
			fields["enabled"] = false
		case "error", "banned", "invalid":
			fields["enabled"] = false
			fields["banned"] = true
		case "":
		default:
			return nil, nil, false, fmt.Errorf("unsupported status %q", raw)
		}
	}
	if raw, ok := body["label"]; ok {
		fields["notes"] = strings.TrimSpace(fmt.Sprint(raw))
	}
	if raw, ok := body["notes"]; ok {
		fields["notes"] = fmt.Sprint(raw)
	}
	if raw, ok := body["tier"]; ok {
		fields["tier"] = strings.ToLower(strings.TrimSpace(fmt.Sprint(raw)))
	}
	if raw, ok := body["proxy_url"]; ok {
		proxyURL := strings.TrimSpace(fmt.Sprint(raw))
		if err := validateDashboardProxyURL(nil, proxyURL); err != nil {
			return nil, nil, false, err
		}
		fields["proxy_url"] = proxyURL
	}
	if raw, ok := body["proxy"]; ok {
		proxyURL := strings.TrimSpace(fmt.Sprint(raw))
		if err := validateDashboardProxyURL(nil, proxyURL); err != nil {
			return nil, nil, false, err
		}
		fields["proxy_url"] = proxyURL
	}
	if raw, ok := body["blockedModels"]; ok {
		blocked = stringSliceFromAny(raw)
	}
	if raw, ok := body["blocked_models"]; ok {
		blocked = stringSliceFromAny(raw)
	}
	if raw, ok := body["resetErrors"]; ok {
		resetErrors = truthy(raw)
	}
	if raw, ok := body["reset_errors"]; ok {
		resetErrors = truthy(raw)
	}
	return fields, blocked, resetErrors, nil
}

func dashboardImportAccountsAPI(w http.ResponseWriter, r *http.Request, am *account.Manager) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req authLoginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid request: "+err.Error())
		return
	}
	items, warnings := expandImportRequest(req)
	if len(items) == 0 {
		writeJSONError(w, http.StatusBadRequest, "no valid accounts to import")
		return
	}
	results := make([]map[string]any, 0, len(items))
	imported := 0
	for i, item := range items {
		acc, err := importAccount(am, item)
		row := map[string]any{
			"success": false,
			"index":   i,
			"email":   item.Email,
			"label":   item.Label,
		}
		if err != nil {
			row["error"] = err.Error()
			results = append(results, row)
			continue
		}
		imported++
		row["success"] = true
		row["account"] = safeAccount(acc)
		results = append(results, row)
	}
	snap := am.Snapshot()
	status := http.StatusOK
	if imported == 0 {
		status = http.StatusBadRequest
	}
	writeJSON(w, status, map[string]any{
		"success":       imported > 0,
		"imported":      imported,
		"failed":        len(results) - imported,
		"total":         len(results),
		"results":       results,
		"warnings":      warnings,
		"counts":        accountCounts(snap.Accounts),
		"parse_as":      strings.TrimSpace(req.ParseAs),
		"compatibility": "node-dashboard-import",
	})
}

func dashboardTierAccessAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	writeJSON(w, http.StatusOK, models.TierAccessSnapshot())
}

func dashboardAuthProbeAPI(w http.ResponseWriter, r *http.Request, rc *runtimeconfig.Manager) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	snap := runtimeconfig.Snapshot{}
	if rc != nil {
		snap = rc.Snapshot()
	}
	required := snap.Security.DashboardPassword.Set || snap.Security.APIKeys.Set
	writeJSON(w, http.StatusOK, map[string]any{
		"required": required,
		"valid":    true,
		"locked":   false,
		"security": snap.Security,
	})
}

func dashboardCredentialsAPI(w http.ResponseWriter, r *http.Request, rc *runtimeconfig.Manager) {
	if rc == nil {
		writeJSONError(w, http.StatusNotFound, "runtime config unavailable")
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, rc.CredentialsSnapshot())
	case http.MethodPut, http.MethodPatch:
		var body struct {
			APIKey            string `json:"apiKey"`
			APIKeySnake       string `json:"api_key"`
			DashboardPassword string `json:"dashboardPassword"`
			DashboardSnake    string `json:"dashboard_password"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid request: "+err.Error())
			return
		}
		patch := runtimeconfig.Patch{Secrets: &runtimeconfig.SecretsPatch{}}
		touched := false
		if apiKey := firstNonEmpty(body.APIKey, body.APIKeySnake); apiKey != "" {
			patch.Secrets.APIKeys = []string{apiKey}
			touched = true
		}
		if password := firstNonEmpty(body.DashboardPassword, body.DashboardSnake); password != "" {
			patch.Secrets.DashboardPassword = password
			touched = true
		}
		if !touched {
			writeJSONError(w, http.StatusBadRequest, "provide apiKey/api_key or dashboardPassword/dashboard_password")
			return
		}
		if _, err := rc.Patch(patch); err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		out := rc.CredentialsSnapshot()
		out["success"] = true
		writeJSON(w, http.StatusOK, out)
	default:
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func dashboardEnvAPI(w http.ResponseWriter, r *http.Request, rc *runtimeconfig.Manager) {
	if rc == nil {
		writeJSONError(w, http.StatusNotFound, "runtime config unavailable")
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]any{"env": rc.EnvSnapshot()})
	case http.MethodPut, http.MethodPatch:
		var patch runtimeconfig.Patch
		if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid request: "+err.Error())
			return
		}
		snap, err := rc.Patch(patch)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "env": rc.EnvSnapshot(), "config": snap})
	default:
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func dashboardExperimentalConversationPoolAPI(w http.ResponseWriter, r *http.Request, rp *reusepool.Pool) {
	if r.Method != http.MethodDelete {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	cleared := 0
	if rp != nil {
		cleared = rp.Clear()
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "cleared": cleared})
}

func dashboardCacheAPI(w http.ResponseWriter, r *http.Request, rp *reusepool.Pool) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, dashboardCacheSnapshot(rp))
	case http.MethodDelete:
		cleared := 0
		if rp != nil {
			cleared = rp.Clear()
		}
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "cleared": cleared, "cache": dashboardCacheSnapshot(rp)})
	default:
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func dashboardProxyAPI(w http.ResponseWriter, r *http.Request, pp *proxypool.Manager) {
	if pp == nil {
		writeJSON(w, http.StatusOK, dashboardProxySnapshot(pp))
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, dashboardProxySnapshot(pp))
	case http.MethodPost:
		var body struct {
			URL string `json:"url"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid request: "+err.Error())
			return
		}
		item, err := pp.Add(body.URL)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "proxy": item, "snapshot": pp.Snapshot()})
	case http.MethodPatch, http.MethodPut:
		var body struct {
			ID              string `json:"id"`
			Enabled         *bool  `json:"enabled"`
			CooldownSeconds *int   `json:"cooldown_seconds"`
			Test            bool   `json:"test"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid request: "+err.Error())
			return
		}
		if strings.TrimSpace(body.ID) == "" {
			writeJSONError(w, http.StatusBadRequest, "id required")
			return
		}
		var item *proxypool.Entry
		var err error
		if body.Test {
			item, err = pp.Test(r.Context(), body.ID)
		} else {
			item, err = pp.Patch(body.ID, body.Enabled, body.CooldownSeconds)
		}
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "proxy": item, "snapshot": pp.Snapshot()})
	case http.MethodDelete:
		id := strings.TrimSpace(r.URL.Query().Get("id"))
		if id == "" {
			var body struct {
				ID string `json:"id"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			id = strings.TrimSpace(body.ID)
		}
		if id == "" {
			writeJSONError(w, http.StatusBadRequest, "id required")
			return
		}
		deleted, err := pp.Delete(id)
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"success": deleted, "snapshot": pp.Snapshot()})
	default:
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func dashboardProxySnapshot(pp *proxypool.Manager) any {
	if pp == nil {
		return map[string]any{"enabled": false, "entries": []proxypool.Entry{}}
	}
	return pp.Snapshot()
}

func dashboardProxyBindingsAPI(w http.ResponseWriter, r *http.Request, pp *proxypool.Manager, am *account.Manager) {
	if pp == nil {
		writeJSONError(w, http.StatusNotFound, "proxy manager unavailable")
		return
	}
	switch r.Method {
	case http.MethodGet:
		bindings, err := pp.Bindings()
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "bindings": bindings, "snapshot": pp.Snapshot(), "accounts": proxyBindingAccounts(am)})
	case http.MethodPost:
		accounts := proxyBindingRefs(am)
		result, err := pp.RunMaintenance(r.Context(), accounts)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "result": result, "snapshot": pp.Snapshot()})
	default:
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func dashboardProxyGenerateAPI(w http.ResponseWriter, r *http.Request, pp *proxypool.Manager) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if pp == nil {
		writeJSONError(w, http.StatusNotFound, "proxy manager unavailable")
		return
	}
	item, raw, err := pp.GenerateProviderProxy()
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "proxy": item, "proxy_url": account.MaskProxyURL(raw), "snapshot": pp.Snapshot()})
}

func dashboardProxyBindAccountsAPI(w http.ResponseWriter, r *http.Request, am *account.Manager, pp *proxypool.Manager, rc *runtimeconfig.Manager) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if am == nil {
		writeJSONError(w, http.StatusNotFound, "account manager unavailable")
		return
	}
	var body struct {
		AccountIDs []int  `json:"account_ids"`
		ProxyID    string `json:"proxy_id"`
		ProxyURL   string `json:"proxy_url"`
		Action     string `json:"action"`
		Clear      bool   `json:"clear"`
		Generate   bool   `json:"generate"`
		Rotate     bool   `json:"rotate"`
		Dynamic    bool   `json:"dynamic"`
		Verify     bool   `json:"verify"`
		Suspend    bool   `json:"suspend"`
		Resume     bool   `json:"resume"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid request: "+err.Error())
		return
	}
	if len(body.AccountIDs) == 0 {
		writeJSONError(w, http.StatusBadRequest, "account_ids required")
		return
	}
	if body.Dynamic {
		if pp == nil {
			writeJSONError(w, http.StatusNotFound, "proxy manager unavailable")
			return
		}
		action := strings.ToLower(strings.TrimSpace(body.Action))
		switch {
		case body.Clear:
			action = "clear"
		case body.Rotate:
			action = "rotate"
		case body.Verify:
			action = "verify"
		case body.Suspend:
			action = "suspend"
		case body.Resume:
			action = "resume"
		case action == "":
			action = "bind"
		}
		if action != "bind" && action != "rotate" && action != "verify" && action != "clear" && action != "suspend" && action != "resume" {
			writeJSONError(w, http.StatusBadRequest, "unknown dynamic proxy action")
			return
		}
		results := []any{}
		updated := 0
		failed := 0
		for _, id := range uniqueInts(body.AccountIDs) {
			result := dashboardRunProxyBindingAction(r.Context(), am, pp, id, action, true)
			if ok, _ := result["success"].(bool); ok {
				updated++
			} else {
				failed++
			}
			results = append(results, result)
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"success":       failed == 0,
			"action":        action,
			"updated":       updated,
			"failed":        failed,
			"dynamic_bound": updated,
			"results":       results,
			"proxy":         dashboardProxySnapshot(pp),
		})
		return
	}
	proxyURL := strings.TrimSpace(body.ProxyURL)
	if body.Clear {
		proxyURL = ""
	} else if body.Generate || body.Rotate {
		if pp == nil {
			writeJSONError(w, http.StatusNotFound, "proxy manager unavailable")
			return
		}
	} else if proxyURL == "" && strings.TrimSpace(body.ProxyID) != "" && pp != nil {
		if raw, ok := pp.URL(body.ProxyID); ok {
			proxyURL = raw
		}
	}
	updated := 0
	generated := 0
	dynamicBound := 0
	for _, id := range body.AccountIDs {
		if id <= 0 {
			continue
		}
		nextProxyURL := proxyURL
		if body.Generate || body.Rotate {
			_, raw, err := pp.GenerateProviderProxy()
			if err != nil {
				writeJSONError(w, http.StatusBadRequest, err.Error())
				return
			}
			nextProxyURL = raw
			generated++
		}
		if err := validateDashboardProxyURL(rc, nextProxyURL); err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := am.UpdateAccount(id, map[string]interface{}{"proxy_url": nextProxyURL}); err != nil {
			writeJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		clearAccountAvailabilityAfterProxyChange(am, id, "static_proxy_changed")
		updated++
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "updated": updated, "generated": generated, "dynamic_bound": dynamicBound, "proxy_url_set": proxyURL != "" || generated > 0 || dynamicBound > 0, "snapshot": am.Snapshot(), "proxy": dashboardProxySnapshot(pp)})
}

func dashboardProxyGlobalAPI(w http.ResponseWriter, r *http.Request, rc *runtimeconfig.Manager, pp *proxypool.Manager) {
	if pp == nil {
		writeJSONError(w, http.StatusNotFound, "proxy manager unavailable")
		return
	}
	switch r.Method {
	case http.MethodPut, http.MethodPatch:
		raw, err := proxyURLFromRequest(r)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := validateDashboardProxyURL(rc, raw); err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := pp.SetDefault(raw); err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		if rc != nil {
			_ = rc.SetProxyDefault(raw)
		}
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "config": pp.Snapshot()})
	case http.MethodDelete:
		if err := pp.SetDefault(""); err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		if rc != nil {
			_ = rc.SetProxyDefault("")
		}
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "config": pp.Snapshot()})
	default:
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func dashboardProxyAccountAPI(w http.ResponseWriter, r *http.Request, am *account.Manager, rc *runtimeconfig.Manager, pp ...*proxypool.Manager) {
	if am == nil {
		writeJSONError(w, http.StatusNotFound, "account manager unavailable")
		return
	}
	var proxyPool *proxypool.Manager
	if len(pp) > 0 {
		proxyPool = pp[0]
	}
	path := strings.Trim(strings.TrimPrefix(r.URL.Path, "/dashboard/api/proxy/accounts/"), "/")
	parts := strings.Split(path, "/")
	id, err := strconv.Atoi(parts[0])
	if err != nil || id <= 0 {
		writeJSONError(w, http.StatusBadRequest, "invalid account id")
		return
	}
	action := ""
	if len(parts) > 1 {
		action = strings.TrimSpace(parts[1])
	}
	if action != "" {
		dashboardProxyAccountBindingActionAPI(w, r, am, proxyPool, id, action)
		return
	}
	switch r.Method {
	case http.MethodPut, http.MethodPatch:
		raw, err := proxyURLFromRequest(r)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := validateDashboardProxyURL(rc, raw); err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := am.UpdateAccount(id, map[string]interface{}{"proxy_url": raw}); err != nil {
			writeJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		clearAccountAvailabilityAfterProxyChange(am, id, "static_proxy_changed")
		acc, _ := am.GetAccount(id)
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "account": safeAccount(acc)})
	case http.MethodDelete:
		if proxyPool != nil {
			if deleted, err := proxyPool.ClearAccount(id); err != nil {
				writeJSONError(w, http.StatusInternalServerError, err.Error())
				return
			} else if deleted {
				clearAccountAvailabilityAfterProxyChange(am, id, "dynamic_proxy_cleared")
				writeJSON(w, http.StatusOK, map[string]any{"success": true, "binding_cleared": true, "snapshot": proxyPool.Snapshot()})
				return
			}
		}
		if err := am.UpdateAccount(id, map[string]interface{}{"proxy_url": ""}); err != nil {
			writeJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		clearAccountAvailabilityAfterProxyChange(am, id, "static_proxy_cleared")
		acc, _ := am.GetAccount(id)
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "account": safeAccount(acc)})
	default:
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func dashboardDynamicProxyCompatAPI(w http.ResponseWriter, r *http.Request, pp *proxypool.Manager, am *account.Manager) {
	subpath := strings.Trim(strings.TrimPrefix(r.URL.Path, "/dashboard/api/dynamic-proxy/"), "/")
	if subpath == "config" {
		if r.Method == http.MethodGet {
			writeJSON(w, http.StatusOK, map[string]any{"success": true, "config": dashboardProxySnapshot(pp)})
			return
		}
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if strings.HasPrefix(subpath, "batch/") && r.Method == http.MethodPost {
		action := strings.TrimPrefix(subpath, "batch/")
		var body struct {
			AccountIDs      []int `json:"accountIds"`
			AccountIDsSnake []int `json:"account_ids"`
			Force           bool  `json:"force"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		ids := append(body.AccountIDs, body.AccountIDsSnake...)
		out := map[string]any{"success": true, "action": action, "results": []any{}}
		results := []any{}
		for _, id := range uniqueInts(ids) {
			result := dashboardRunProxyBindingAction(r.Context(), am, pp, id, action, body.Force)
			results = append(results, result)
		}
		out["results"] = results
		out["snapshot"] = dashboardProxySnapshot(pp)
		writeJSON(w, http.StatusOK, out)
		return
	}
	if strings.HasPrefix(subpath, "accounts/") {
		rest := strings.TrimPrefix(subpath, "accounts/")
		parts := strings.Split(strings.Trim(rest, "/"), "/")
		id, err := strconv.Atoi(parts[0])
		if err != nil || id <= 0 {
			writeJSONError(w, http.StatusBadRequest, "invalid account id")
			return
		}
		action := "clear"
		if len(parts) > 1 {
			action = parts[1]
		}
		if r.Method == http.MethodDelete {
			action = "clear"
		}
		if r.Method != http.MethodPost && r.Method != http.MethodDelete {
			writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		if am != nil {
			if acc, _ := am.GetAccount(id); acc == nil {
				writeJSONError(w, http.StatusNotFound, "account not found")
				return
			}
		}
		result := dashboardRunProxyBindingAction(r.Context(), am, pp, id, action, true)
		status := http.StatusOK
		if ok, _ := result["success"].(bool); !ok {
			status = http.StatusBadRequest
		}
		writeJSON(w, status, result)
		return
	}
	writeJSONError(w, http.StatusNotFound, "unknown dynamic proxy route")
}

func dashboardRunProxyBindingAction(ctx context.Context, am *account.Manager, pp *proxypool.Manager, accountID int, action string, force bool) map[string]any {
	if pp == nil {
		return map[string]any{"success": false, "account_id": accountID, "error": "proxy manager unavailable"}
	}
	clear := func(reason string, ok bool) {
		if ok {
			clearAccountAvailabilityAfterProxyChange(am, accountID, reason)
		}
	}
	switch action {
	case "bind":
		result, err := pp.BindAccount(ctx, accountID, force)
		clear("dynamic_proxy_bound", err == nil)
		return bindingActionResult(accountID, result, err)
	case "rotate":
		result, err := pp.RotateAccount(ctx, accountID, true)
		clear("dynamic_proxy_rotated", err == nil)
		return bindingActionResult(accountID, result, err)
	case "verify":
		result, err := pp.VerifyAccount(ctx, accountID, true)
		clear("dynamic_proxy_verified", err == nil)
		return bindingActionResult(accountID, result, err)
	case "clear":
		deleted, err := pp.ClearAccount(accountID)
		if err != nil {
			return map[string]any{"success": false, "account_id": accountID, "error": redact.Text(err.Error())}
		}
		clear("dynamic_proxy_cleared", true)
		return map[string]any{"success": true, "account_id": accountID, "deleted": deleted}
	case "suspend":
		b, err := pp.SuspendAccount(accountID, "manual_suspend")
		if err != nil {
			return map[string]any{"success": false, "account_id": accountID, "error": redact.Text(err.Error())}
		}
		clear("dynamic_proxy_suspended", true)
		return map[string]any{"success": true, "account_id": accountID, "binding": b}
	case "resume":
		result, err := pp.ResumeAccount(ctx, accountID)
		clear("dynamic_proxy_resumed", err == nil)
		return bindingActionResult(accountID, result, err)
	default:
		return map[string]any{"success": false, "account_id": accountID, "error": "unknown action"}
	}
}

func clearAccountAvailabilityAfterProxyChange(am *account.Manager, accountID int, reason string) {
	if am == nil || accountID <= 0 {
		return
	}
	if err := am.ClearAccountAvailability(accountID, reason); err != nil {
		am.ResetAccountErrors(accountID)
	}
}

func bindingActionResult(accountID int, result *proxypool.BindingResult, err error) map[string]any {
	if err != nil {
		out := map[string]any{"success": false, "account_id": accountID, "error": redact.Text(err.Error())}
		if result != nil && result.Binding != nil {
			out["binding"] = result.Binding
			out["attempts"] = result.Attempts
		}
		return out
	}
	return map[string]any{"success": true, "account_id": accountID, "result": result}
}

func proxyBindingRefs(am *account.Manager) []proxypool.AccountRef {
	if am == nil {
		return nil
	}
	accounts, err := am.GetAllAccounts()
	if err != nil {
		return nil
	}
	refs := make([]proxypool.AccountRef, 0, len(accounts))
	for _, a := range accounts {
		refs = append(refs, proxypool.AccountRef{ID: a.ID, Enabled: a.Enabled, Banned: a.Banned, Active: a.Enabled && !a.Banned})
	}
	return refs
}

func proxyBindingAccounts(am *account.Manager) []map[string]any {
	if am == nil {
		return nil
	}
	accounts, err := am.GetAllAccounts()
	if err != nil {
		return nil
	}
	out := make([]map[string]any, 0, len(accounts))
	for _, a := range accounts {
		out = append(out, map[string]any{"id": a.ID, "email": a.Email, "enabled": a.Enabled, "banned": a.Banned, "proxy_url_set": strings.TrimSpace(a.ProxyURL) != ""})
	}
	return out
}

func uniqueInts(ids []int) []int {
	seen := map[int]bool{}
	var out []int
	for _, id := range ids {
		if id <= 0 || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}

func dashboardProxyAccountBindingActionAPI(w http.ResponseWriter, r *http.Request, am *account.Manager, pp *proxypool.Manager, accountID int, action string) {
	if pp == nil {
		writeJSONError(w, http.StatusNotFound, "proxy manager unavailable")
		return
	}
	acc, err := am.GetAccount(accountID)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if acc == nil {
		writeJSONError(w, http.StatusNotFound, "account not found")
		return
	}
	switch action {
	case "bind":
		if r.Method != http.MethodPost {
			writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		result, err := pp.BindAccount(r.Context(), accountID, true)
		if err == nil {
			clearAccountAvailabilityAfterProxyChange(am, accountID, "dynamic_proxy_bound")
		}
		writeBindingResult(w, pp, result, err)
	case "rotate":
		if r.Method != http.MethodPost {
			writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		result, err := pp.RotateAccount(r.Context(), accountID, true)
		if err == nil {
			clearAccountAvailabilityAfterProxyChange(am, accountID, "dynamic_proxy_rotated")
		}
		writeBindingResult(w, pp, result, err)
	case "verify":
		if r.Method != http.MethodPost {
			writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		result, err := pp.VerifyAccount(r.Context(), accountID, true)
		if err == nil {
			clearAccountAvailabilityAfterProxyChange(am, accountID, "dynamic_proxy_verified")
		}
		writeBindingResult(w, pp, result, err)
	case "suspend":
		if r.Method != http.MethodPost {
			writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		b, err := pp.SuspendAccount(accountID, "manual_suspend")
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		clearAccountAvailabilityAfterProxyChange(am, accountID, "dynamic_proxy_suspended")
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "binding": b, "snapshot": pp.Snapshot()})
	case "resume":
		if r.Method != http.MethodPost {
			writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		result, err := pp.ResumeAccount(r.Context(), accountID)
		if err == nil {
			clearAccountAvailabilityAfterProxyChange(am, accountID, "dynamic_proxy_resumed")
		}
		writeBindingResult(w, pp, result, err)
	case "clear":
		if r.Method != http.MethodDelete && r.Method != http.MethodPost {
			writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		deleted, err := pp.ClearAccount(accountID)
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		clearAccountAvailabilityAfterProxyChange(am, accountID, "dynamic_proxy_cleared")
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "deleted": deleted, "snapshot": pp.Snapshot()})
	default:
		writeJSONError(w, http.StatusNotFound, "unknown proxy account action")
	}
}

func writeBindingResult(w http.ResponseWriter, pp *proxypool.Manager, result *proxypool.BindingResult, err error) {
	if err != nil {
		status := http.StatusBadRequest
		if result != nil && result.Binding != nil {
			writeJSON(w, status, map[string]any{"success": false, "binding": result.Binding, "error": redact.Text(err.Error()), "snapshot": pp.Snapshot()})
			return
		}
		writeJSONError(w, status, redact.Text(err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "result": result, "snapshot": pp.Snapshot()})
}

func proxyURLFromRequest(r *http.Request) (string, error) {
	var body map[string]any
	if r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil && err != io.EOF {
			return "", fmt.Errorf("invalid request: %w", err)
		}
	}
	raw := ""
	for _, key := range []string{"url", "proxy_url", "proxy", "host"} {
		if v, ok := body[key]; ok {
			raw = strings.TrimSpace(fmt.Sprint(v))
			break
		}
	}
	if raw == "" {
		raw = strings.TrimSpace(r.URL.Query().Get("url"))
	}
	return raw, nil
}

func dashboardDroughtSummary(snap account.SchedulerSnapshot) map[string]any {
	drought := 0
	enabled := 0
	lowest := 100.0
	for _, acc := range snap.Accounts {
		if acc.Enabled && !acc.Banned {
			enabled++
		}
		if acc.Drought {
			drought++
		}
		if acc.QuotaScore > 0 && acc.QuotaScore < lowest {
			lowest = acc.QuotaScore
		}
	}
	if lowest == 100.0 {
		lowest = 0
	}
	return map[string]any{
		"enabled_accounts": enabled,
		"drought_accounts": drought,
		"lowest_quota":     lowest,
		"active":           drought > 0,
	}
}

func dashboardUpstreamEndpoints(dc dashboardDirectClient) map[string]any {
	hosts := []string{"server.codeium.com", "server.self-serve.windsurf.com"}
	if dc != nil {
		if snap := dc.Snapshot(); len(snap.Hosts) > 0 {
			hosts = snap.Hosts
		}
	}
	primary := hosts[0]
	fallback := ""
	if len(hosts) > 1 {
		fallback = hosts[1]
	}
	return map[string]any{
		"getUserStatus": map[string]any{
			"primary":  primary + "/exa.seat_management_pb.SeatManagementService/GetUserStatus",
			"fallback": fallback + "/exa.seat_management_pb.SeatManagementService/GetUserStatus",
			"protocol": "Connect-RPC",
		},
		"getCascadeModelConfigs": map[string]any{
			"primary":  primary + "/exa.api_server_pb.ApiServerService/GetCascadeModelConfigs",
			"fallback": fallback + "/exa.api_server_pb.ApiServerService/GetCascadeModelConfigs",
			"protocol": "Connect-RPC",
		},
		"getChatMessage": map[string]any{
			"primary":  primary + "/exa.api_server_pb.ApiServerService/GetChatMessage",
			"fallback": fallback + "/exa.api_server_pb.ApiServerService/GetChatMessage",
			"protocol": "raw application/grpc",
		},
	}
}

func dashboardLogsExport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	limit := intQuery(r, "limit", 500)
	if limit <= 0 {
		limit = 500
	}
	if limit > 5000 {
		limit = 5000
	}
	events := requestLogsSnapshotFiltered(limit, requestLogFilterFromQuery(r))
	format := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("format")))
	if format == "" {
		format = "csv"
	}
	switch format {
	case "json":
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Disposition", `attachment; filename="windsurfapi-request-logs.json"`)
		writeJSONNoEscape(w, map[string]any{"requests": events})
	case "ndjson":
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.Header().Set("Content-Disposition", `attachment; filename="windsurfapi-request-logs.ndjson"`)
		for _, ev := range events {
			_, _ = w.Write(requestEventJSON(ev))
			_, _ = w.Write([]byte("\n"))
		}
	default:
		var buf bytes.Buffer
		if err := writeRequestEventsCSV(&buf, events); err != nil {
			writeJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="windsurfapi-request-logs.csv"`)
		_, _ = w.Write(buf.Bytes())
	}
}

func dashboardLogsStream(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSONError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Connection", "keep-alive")
	filter := requestLogFilterFromQuery(r)
	for _, ev := range requestLogsSnapshotFiltered(25, filter) {
		_, _ = w.Write([]byte("event: request\n"))
		_, _ = w.Write([]byte("data: "))
		_, _ = w.Write(requestEventJSON(ev))
		_, _ = w.Write([]byte("\n\n"))
	}
	flusher.Flush()
	ch, cancel := globalRequestStats.Subscribe(32)
	defer cancel()
	for {
		select {
		case <-r.Context().Done():
			return
		case ev, ok := <-ch:
			if !ok {
				return
			}
			if !requestEventMatches(ev, filter) {
				continue
			}
			_, _ = w.Write([]byte("event: request\n"))
			_, _ = w.Write([]byte("data: "))
			_, _ = w.Write(requestEventJSON(ev))
			_, _ = w.Write([]byte("\n\n"))
			flusher.Flush()
		}
	}
}

func requestLogFilterFromQuery(r *http.Request) RequestLogFilter {
	q := r.URL.Query()
	return RequestLogFilter{
		Query:      strings.TrimSpace(q.Get("q")),
		Route:      strings.TrimSpace(q.Get("route")),
		Model:      strings.TrimSpace(q.Get("model")),
		Status:     strings.TrimSpace(q.Get("status")),
		ErrorClass: strings.TrimSpace(q.Get("error_class")),
		AccountID:  intQuery(r, "account_id", 0),
		HTTPStatus: intQuery(r, "http_status", 0),
		Stream:     boolQuery(q.Get("stream")),
		Retry:      boolQuery(q.Get("retry")),
	}
}

func boolQuery(raw string) *bool {
	raw = strings.TrimSpace(strings.ToLower(raw))
	if raw == "" || raw == "all" {
		return nil
	}
	switch raw {
	case "1", "true", "yes", "y", "on":
		v := true
		return &v
	case "0", "false", "no", "n", "off":
		v := false
		return &v
	default:
		return nil
	}
}

func intQuery(r *http.Request, name string, fallback int) int {
	raw := strings.TrimSpace(r.URL.Query().Get(name))
	if raw == "" {
		return fallback
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return fallback
	}
	return v
}

func dashboardCacheSnapshot(rp *reusepool.Pool) map[string]any {
	if rp == nil {
		return map[string]any{"enabled": false, "entries": 0, "items": []cacheEntryView{}}
	}
	items := rp.Snapshot()
	views := make([]cacheEntryView, 0, len(items))
	for _, item := range items {
		views = append(views, cacheEntryView{
			AccountID:     item.AccountID,
			APIKeyHash:    item.APIKeyHash,
			LSPort:        item.LSPort,
			LSGeneration:  item.LSGeneration,
			CascadeID:     item.CascadeID,
			ModelID:       item.ModelID,
			CallerKeyHash: shortHash(item.CallerKey),
			CreatedAt:     item.CreatedAt,
			LastUsedAt:    item.LastUsedAt,
			ExpiresAt:     item.ExpiresAt,
		})
	}
	return map[string]any{
		"enabled": true,
		"entries": len(items),
		"stats":   rp.Stats(),
		"items":   views,
	}
}

type cacheEntryView struct {
	AccountID     int       `json:"account_id"`
	APIKeyHash    string    `json:"api_key_hash"`
	LSPort        int       `json:"ls_port,omitempty"`
	LSGeneration  string    `json:"ls_generation,omitempty"`
	CascadeID     string    `json:"cascade_id,omitempty"`
	ModelID       string    `json:"model_id"`
	CallerKeyHash string    `json:"caller_key_hash,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
	LastUsedAt    time.Time `json:"last_used_at"`
	ExpiresAt     time.Time `json:"expires_at"`
}

func dashboardConfigAPI(w http.ResponseWriter, r *http.Request, rc *runtimeconfig.Manager, extras ...any) {
	if rc == nil {
		writeJSONError(w, http.StatusNotFound, "runtime config unavailable")
		return
	}
	var proxyPool *proxypool.Manager
	var usageMgr *usagepkg.Manager
	for _, item := range extras {
		switch v := item.(type) {
		case *proxypool.Manager:
			proxyPool = v
		case *usagepkg.Manager:
			usageMgr = v
		}
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, rc.Snapshot())
	case http.MethodPatch, http.MethodPut:
		var patch runtimeconfig.Patch
		if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid request: "+err.Error())
			return
		}
		if patch.Proxy != nil {
			if err := validateDashboardProxyURL(rc, patch.Proxy.Default); err != nil {
				writeJSONError(w, http.StatusBadRequest, err.Error())
				return
			}
			for _, raw := range patch.Proxy.Dynamic {
				if err := validateDashboardProxyURL(rc, raw); err != nil {
					writeJSONError(w, http.StatusBadRequest, err.Error())
					return
				}
			}
		}
		snap, err := rc.Patch(patch)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		if patch.Proxy != nil {
			reconfigureProxyManagerFromView(proxyPool, snap.Proxy)
		}
		if patch.Usage != nil && usageMgr != nil {
			usageMgr.SetConfig(configFromVirtualCacheView(snap.Usage.VirtualCache))
		}
		writeJSON(w, http.StatusOK, snap)
	default:
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func configFromVirtualCacheView(view runtimeconfig.VirtualCacheView) config.VirtualCacheUsageConfig {
	return config.VirtualCacheUsageConfig{
		Enabled:             view.Enabled,
		Mode:                view.Mode,
		DefaultTTL:          view.DefaultTTL,
		UncachedInputTokens: view.UncachedInputTokens,
		MinInputTokens:      view.MinInputTokens,
		MaxInputTokens:      view.MaxInputTokens,
		WarmupTokens:        view.WarmupTokens,
		MinCreationTokens:   view.MinCreationTokens,
		MaxCreationTokens:   view.MaxCreationTokens,
		CreationJitterRatio: view.CreationJitterRatio,
		BurstEveryTurns:     view.BurstEveryTurns,
		BurstMinTokens:      view.BurstMinTokens,
		BurstMaxTokens:      view.BurstMaxTokens,
	}
}

func reconfigureProxyManagerFromView(pp *proxypool.Manager, view runtimeconfig.ProxyView) {
	if pp == nil {
		return
	}
	pp.Reconfigure(proxypool.Config{
		Default:           view.Default,
		Dynamic:           view.Dynamic,
		RotateOnError:     view.RotateOnError,
		TestURL:           view.TestURL,
		Cooldown:          time.Duration(view.CooldownSeconds) * time.Second,
		AllowPrivate:      view.AllowPrivate,
		AccountBinding:    view.AccountBinding,
		AutoBindNew:       view.AutoBindNewAccounts,
		RenewBefore:       time.Duration(view.RenewBeforeMS) * time.Millisecond,
		MaxBindRetries:    view.MaxBindRetries,
		WorkerInterval:    time.Duration(view.WorkerIntervalMS) * time.Millisecond,
		WorkerBatchSize:   view.WorkerBatchSize,
		WorkerConcurrency: view.WorkerConcurrency,
		Provider:          view.Provider,
		Protocol:          view.Protocol,
		Host:              view.Host,
		Port:              view.Port,
		UsernameTemplate:  view.UsernameTemplate,
		Password:          view.Password,
		Region:            view.Region,
		State:             view.State,
		TTLMinutes:        view.TTLMinutes,
	})
}

func dashboardOverview(snap account.SchedulerSnapshot, dc dashboardDirectClient, rp *reusepool.Pool) map[string]any {
	counts := accountCounts(snap.Accounts)
	rpmUsed, rpmLimit, inflight := 0, 0, 0
	quotaTotal := 0.0
	for _, a := range snap.Accounts {
		rpmUsed += a.RPMUsed
		rpmLimit += a.RPMLimit
		inflight += a.Inflight
		quotaTotal += a.QuotaScore
	}
	avgQuota := 0.0
	if len(snap.Accounts) > 0 {
		avgQuota = quotaTotal / float64(len(snap.Accounts))
	}
	return map[string]any{
		"counts":      counts,
		"rpm_used":    rpmUsed,
		"rpm_limit":   rpmLimit,
		"inflight":    inflight,
		"avg_quota":   avgQuota,
		"health":      snap.Health,
		"coordinator": snap.Coordinator,
		"direct":      dashboardDirectSnapshot(dc),
		"reuse":       dashboardReuseStats(rp),
	}
}

func dashboardDirectSnapshot(dc dashboardDirectClient) direct.Stats {
	if dc == nil {
		return direct.Stats{Protocol: "grpc"}
	}
	return dc.Snapshot()
}

func dashboardReuseStats(rp *reusepool.Pool) reusepool.Stats {
	if rp == nil {
		return reusepool.Stats{}
	}
	return rp.Stats()
}

func expandImportRequest(req authLoginRequest) ([]accountImportRequest, []string) {
	items := make([]accountImportRequest, 0, len(req.Accounts)+1)
	warnings := []string{}
	items = append(items, req.Accounts...)
	if singleImportCandidate(req.accountImportRequest) {
		items = append(items, req.accountImportRequest)
	}
	text := firstNonEmpty(req.Text, req.Raw)
	if strings.TrimSpace(text) != "" {
		parsed, parseWarnings := parseAccountImportText(text)
		items = append(items, parsed...)
		warnings = append(warnings, parseWarnings...)
	}
	out := make([]accountImportRequest, 0, len(items))
	seen := map[string]int{}
	for idx, item := range items {
		item = normalizeImportItem(item)
		token := firstNonEmpty(item.FirebaseToken, item.Token, item.APIKey)
		if token == "" {
			warnings = append(warnings, fmt.Sprintf("item %d skipped: token/api_key/firebase_token required", idx))
			continue
		}
		key := strings.ToLower(strings.TrimSpace(item.Email))
		if key == "" {
			key = shortHash(token)
		}
		if prev, ok := seen[key]; ok {
			warnings = append(warnings, fmt.Sprintf("item %d overrides duplicate account from item %d", idx, prev))
			out[prev] = item
			continue
		}
		seen[key] = len(out)
		out = append(out, item)
	}
	return out, warnings
}

func singleImportCandidate(item accountImportRequest) bool {
	return firstNonEmpty(item.FirebaseToken, item.Token, item.APIKey) != "" ||
		strings.TrimSpace(item.Email) != "" ||
		strings.TrimSpace(item.Label) != "" ||
		strings.TrimSpace(item.ProxyURL) != "" ||
		strings.TrimSpace(item.Proxy) != ""
}

func normalizeImportItem(item accountImportRequest) accountImportRequest {
	if strings.TrimSpace(item.Token) == "" {
		item.Token = firstNonEmpty(item.APIKey, item.FirebaseToken)
	}
	if strings.TrimSpace(item.APIKey) == "" {
		item.APIKey = firstNonEmpty(item.Token, item.FirebaseToken)
	}
	if strings.TrimSpace(item.Email) == "" && strings.Contains(item.Label, "@") {
		item.Email = strings.TrimSpace(item.Label)
	}
	if strings.TrimSpace(item.ProxyURL) == "" {
		item.ProxyURL = strings.TrimSpace(item.Proxy)
	}
	return item
}

func parseAccountImportText(raw string) ([]accountImportRequest, []string) {
	lines := strings.Split(raw, "\n")
	items := make([]accountImportRequest, 0, len(lines))
	warnings := []string{}
	for lineNo, line := range lines {
		item, ok, warning := parseAccountImportLine(line)
		if warning != "" {
			warnings = append(warnings, fmt.Sprintf("line %d: %s", lineNo+1, warning))
		}
		if ok {
			items = append(items, item)
		}
	}
	return items, warnings
}

func parseAccountImportLine(raw string) (accountImportRequest, bool, string) {
	line := strings.TrimSpace(raw)
	if line == "" {
		return accountImportRequest{}, false, ""
	}
	if strings.HasPrefix(line, "#") || strings.HasPrefix(line, "//") {
		return accountImportRequest{}, false, ""
	}
	item := accountImportRequest{}
	if strings.Contains(line, "----") {
		parts := strings.Split(line, "----")
		if len(parts) >= 1 {
			item.Email = emailInString(parts[0])
			if item.Email == "" {
				item.Label = strings.TrimSpace(parts[0])
			}
		}
		if len(parts) >= 3 {
			item.Token = tokenInString(parts[2])
		}
		if len(parts) >= 4 {
			if proxy := proxyInString(parts[3]); proxy != "" {
				item.ProxyURL = proxy
			}
		}
		if item.Token == "" {
			return item, false, "expected session token in third ---- field"
		}
		if item.Email == "" {
			item.Email = emailInString(line)
		}
		return item, true, ""
	}

	if strings.HasPrefix(line, "{") {
		var decoded accountImportRequest
		if err := json.Unmarshal([]byte(line), &decoded); err != nil {
			return item, false, "invalid JSON account line"
		}
		decoded = normalizeImportItem(decoded)
		if firstNonEmpty(decoded.Token, decoded.APIKey, decoded.FirebaseToken) == "" {
			return decoded, false, "JSON account line missing token/api_key/firebase_token"
		}
		return decoded, true, ""
	}

	item.Email = emailInString(line)
	item.Token = tokenInString(line)
	item.ProxyURL = proxyInString(line)
	if item.Token == "" {
		return item, false, "expected session token"
	}
	return item, true, ""
}

func emailInString(raw string) string {
	raw = strings.TrimSpace(raw)
	for _, field := range strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == '\t' || r == ' ' || r == ';' || r == '|'
	}) {
		field = strings.TrimSpace(field)
		if strings.Contains(field, "@") && strings.Contains(field, ".") {
			return field
		}
	}
	if strings.Contains(raw, "@") && strings.Contains(raw, ".") {
		return raw
	}
	return ""
}

func tokenInString(raw string) string {
	raw = strings.TrimSpace(raw)
	const prefix = "devin-session-token$"
	idx := strings.Index(raw, prefix)
	if idx < 0 {
		return ""
	}
	rest := raw[idx:]
	end := len(rest)
	for i, r := range rest {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '.' || r == '-' || r == '$' {
			continue
		}
		end = i
		break
	}
	return rest[:end]
}

func proxyInString(raw string) string {
	raw = strings.TrimSpace(raw)
	fields := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == '\t' || r == ' ' || r == ';' || r == '|'
	})
	for _, field := range fields {
		field = strings.TrimSpace(field)
		lower := strings.ToLower(field)
		if strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") || strings.HasPrefix(lower, "socks5://") {
			return field
		}
	}
	lower := strings.ToLower(raw)
	if strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") || strings.HasPrefix(lower, "socks5://") {
		return raw
	}
	return ""
}

func dashboardModelList(access *modelaccess.Manager, full bool) []models.DashboardModel {
	if access == nil {
		if full {
			return models.ToDashboardModelList()
		}
		return models.ToPublicDashboardModelList()
	}
	raw, err := access.List()
	if err != nil {
		if full {
			return models.ToDashboardModelList()
		}
		return models.ToPublicDashboardModelList()
	}
	mapped := map[string]models.DashboardAccess{}
	for id, item := range raw {
		mapped[id] = models.DashboardAccess{
			Visible:           item.Visible,
			Enabled:           item.Enabled,
			Deprecated:        item.Deprecated,
			UnsupportedReason: item.UnsupportedReason,
			Notes:             item.Notes,
		}
	}
	if full {
		return models.ToDashboardModelListWithAccess(mapped)
	}
	return models.ToPublicDashboardModelListWithAccess(mapped)
}

func dashboardModelScope(full bool) string {
	if full {
		return "all"
	}
	return "public"
}

func dashboardModelAccessConfig(access *modelaccess.Manager) map[string]any {
	visible := []string{}
	hidden := []string{}
	mode := "all"
	list := []string{}
	if access == nil {
		return map[string]any{"mode": mode, "list": list, "visible": visible, "hidden": hidden}
	}
	cfg := access.Config()
	mode = cfg.Mode
	list = append(list, cfg.List...)
	if mode == "" {
		mode = "all"
	}
	for _, m := range models.AllModels() {
		item, err := access.Get(m.ID)
		if err != nil {
			continue
		}
		if item.Visible {
			visible = append(visible, m.ID)
		} else {
			hidden = append(hidden, m.ID)
		}
	}
	if mode == "all" && len(hidden) > 0 {
		mode = "allowlist"
		list = append([]string(nil), visible...)
	}
	return map[string]any{"mode": mode, "list": list, "visible": visible, "hidden": hidden}
}

func applyDashboardModelAccessList(access *modelaccess.Manager, mode string, list []string) error {
	if access == nil {
		return fmt.Errorf("model access manager unavailable")
	}
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode == "" {
		mode = "all"
	}
	targets := map[string]bool{}
	normalizedList := []string{}
	for _, raw := range list {
		modelID := models.NormalizeModelID(raw)
		if modelID == "" {
			continue
		}
		if models.GetModelByID(modelID) == nil {
			return fmt.Errorf("unknown model: %s", raw)
		}
		if !targets[modelID] {
			normalizedList = append(normalizedList, modelID)
		}
		targets[modelID] = true
	}
	switch mode {
	case "all", "default":
		for _, m := range models.AllModels() {
			_ = access.Reset(m.ID)
		}
	case "allowlist", "allow", "include":
		for _, m := range models.AllModels() {
			visible := targets[m.ID] || targets[modelThinkingSibling(m.ID)]
			if _, err := access.Upsert(m.ID, modelaccess.Patch{Visible: &visible}); err != nil {
				return err
			}
		}
	case "blocklist", "block", "exclude":
		for _, m := range models.AllModels() {
			visible := !(targets[m.ID] || targets[modelThinkingSibling(m.ID)])
			if _, err := access.Upsert(m.ID, modelaccess.Patch{Visible: &visible}); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("unsupported model access mode %q", mode)
	}
	return access.SetConfig(modelaccess.Config{Mode: mode, List: normalizedList})
}

func modelThinkingSibling(modelID string) string {
	modelID = models.NormalizeModelID(modelID)
	if strings.HasSuffix(modelID, "-thinking") {
		return strings.TrimSuffix(modelID, "-thinking")
	}
	return modelID + "-thinking"
}

func importAccount(am *account.Manager, item accountImportRequest) (*account.Account, error) {
	token := firstNonEmpty(item.FirebaseToken, item.Token, item.APIKey)
	if strings.TrimSpace(token) == "" {
		return nil, fmt.Errorf("token/api_key/firebase_token required")
	}
	email := strings.TrimSpace(item.Email)
	if email == "" {
		email = derivedAccountEmail(token, item.Label)
	}
	userID := firstNonEmpty(item.UserID, email)
	proxyURL := firstNonEmpty(item.ProxyURL, item.Proxy)
	if err := validateDashboardProxyURL(nil, proxyURL); err != nil {
		return nil, err
	}
	notes := item.Notes
	if notes == "" && item.Label != "" {
		notes = item.Label
	}
	if err := am.UpsertAccount(email, token, userID, proxyURL, notes); err != nil {
		return nil, err
	}
	acc, err := findAccountByEmail(am, email)
	if err != nil {
		return nil, err
	}
	if acc == nil {
		return nil, fmt.Errorf("imported account not found")
	}
	fields := accountPatchFields(item)
	delete(fields, "email")
	delete(fields, "firebase_token")
	delete(fields, "user_id")
	delete(fields, "proxy_url")
	delete(fields, "notes")
	if len(fields) > 0 {
		if err := am.UpdateAccount(acc.ID, fields); err != nil {
			return nil, err
		}
		acc, _ = am.GetAccount(acc.ID)
	}
	if item.BlockedModels != nil {
		if err := am.SetBlockedModels(acc.ID, item.BlockedModels); err != nil {
			return nil, err
		}
	}
	return acc, nil
}

func accountPatchFields(item accountImportRequest) map[string]interface{} {
	return accountPatchFieldsWithPresence(item, nil)
}

func accountPatchFieldsWithPresence(item accountImportRequest, present map[string]json.RawMessage) map[string]interface{} {
	fields := map[string]interface{}{}
	fieldPresent := func(name string) bool {
		if present == nil {
			return false
		}
		_, ok := present[name]
		return ok
	}
	if item.Email != "" || fieldPresent("email") {
		fields["email"] = strings.TrimSpace(item.Email)
	}
	if token := firstNonEmpty(item.FirebaseToken, item.Token, item.APIKey); token != "" {
		fields["firebase_token"] = token
	}
	if item.UserID != "" || fieldPresent("user_id") {
		fields["user_id"] = strings.TrimSpace(item.UserID)
	}
	if proxy := firstNonEmpty(item.ProxyURL, item.Proxy); proxy != "" {
		fields["proxy_url"] = strings.TrimSpace(proxy)
	} else if fieldPresent("proxy_url") || fieldPresent("proxy") {
		fields["proxy_url"] = ""
	}
	if item.Tier != "" || fieldPresent("tier") {
		fields["tier"] = strings.ToLower(strings.TrimSpace(item.Tier))
	}
	if item.Notes != "" || fieldPresent("notes") {
		fields["notes"] = item.Notes
	}
	if item.Enabled != nil {
		fields["enabled"] = *item.Enabled
	}
	if item.Banned != nil {
		fields["banned"] = *item.Banned
	}
	return fields
}

func safeAccount(acc *account.Account) map[string]any {
	if acc == nil {
		return nil
	}
	return map[string]any{
		"id":                    acc.ID,
		"email":                 acc.Email,
		"user_id":               acc.UserID,
		"proxy_url":             account.MaskProxyURL(acc.ProxyURL),
		"proxy_url_set":         strings.TrimSpace(acc.ProxyURL) != "",
		"tier":                  acc.Tier,
		"plan_name":             acc.PlanName,
		"model_config_count":    acc.ModelConfigCount,
		"rate_limited_until":    acc.RateLimitedUntil,
		"quota_daily_percent":   acc.QuotaDailyPercent,
		"quota_weekly_percent":  acc.QuotaWeeklyPercent,
		"quota_daily_reset_at":  acc.QuotaDailyResetAt,
		"quota_weekly_reset_at": acc.QuotaWeeklyResetAt,
		"prompt": account.CreditSnapshot{
			Limit:     acc.PromptLimit,
			Used:      acc.PromptUsed,
			Remaining: acc.PromptRemaining,
		},
		"flex": account.CreditSnapshot{
			Limit:     acc.FlexLimit,
			Used:      acc.FlexUsed,
			Remaining: acc.FlexRemaining,
		},
		"overage_balance":   acc.OverageBalance,
		"plan_start":        acc.PlanStart,
		"plan_end":          acc.PlanEnd,
		"health_checked_at": acc.HealthCheckedAt,
		"last_used_at":      acc.LastUsedAt,
		"enabled":           acc.Enabled,
		"banned":            acc.Banned,
		"notes":             acc.Notes,
		"token_set":         strings.TrimSpace(acc.FirebaseToken) != "",
		"created_at":        acc.CreatedAt,
		"updated_at":        acc.UpdatedAt,
	}
}

func accountCounts(accounts []account.DebugAccount) map[string]int {
	counts := map[string]int{"total": len(accounts)}
	for _, a := range accounts {
		if a.Enabled {
			counts["enabled"]++
		}
		if a.Banned {
			counts["banned"]++
		}
		if a.TokenSet {
			counts["token_set"]++
		}
	}
	return counts
}

func accountIDFromPath(path string) (int, error) {
	raw := strings.Trim(strings.TrimPrefix(path, "/auth/accounts/"), "/")
	if raw == "" {
		return 0, fmt.Errorf("account id required")
	}
	if strings.Contains(raw, "/") {
		raw = strings.Split(raw, "/")[0]
	}
	id, err := strconv.Atoi(raw)
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("invalid account id")
	}
	return id, nil
}

func modelIDFromAccessPath(path string) (string, error) {
	raw := strings.Trim(strings.TrimPrefix(path, "/auth/models/"), "/")
	raw = strings.TrimSuffix(raw, "/access")
	raw = strings.Trim(raw, "/")
	if raw == "" {
		return "", fmt.Errorf("model id required")
	}
	return models.NormalizeModelID(raw), nil
}

func dashboardAccountIDFromPath(path string) (int, error) {
	raw := strings.Trim(strings.TrimPrefix(path, "/dashboard/api/accounts/"), "/")
	if raw == "" {
		return 0, fmt.Errorf("account id required")
	}
	if strings.Contains(raw, "/") {
		raw = strings.Split(raw, "/")[0]
	}
	id, err := strconv.Atoi(raw)
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("invalid account id")
	}
	return id, nil
}

func findAccountByEmail(am *account.Manager, email string) (*account.Account, error) {
	accounts, err := am.GetAllAccounts()
	if err != nil {
		return nil, err
	}
	for _, acc := range accounts {
		if strings.EqualFold(acc.Email, email) {
			return &acc, nil
		}
	}
	return nil, nil
}

func stringSliceFromAny(raw any) []string {
	switch v := raw.(type) {
	case []string:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if item = strings.TrimSpace(item); item != "" {
				out = append(out, item)
			}
		}
		return out
	case []any:
		out := make([]string, 0, len(v))
		seen := map[string]bool{}
		for _, item := range v {
			s := strings.TrimSpace(fmt.Sprint(item))
			if s == "" || seen[s] {
				continue
			}
			seen[s] = true
			out = append(out, s)
		}
		return out
	case string:
		return parseCommaLines(v)
	default:
		return nil
	}
}

func parseCommaLines(raw string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, part := range strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == '\n' || r == '\r'
	}) {
		part = strings.TrimSpace(part)
		if part == "" || seen[part] {
			continue
		}
		seen[part] = true
		out = append(out, part)
	}
	return out
}

func truthy(raw any) bool {
	switch v := raw.(type) {
	case bool:
		return v
	case string:
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "1", "true", "yes", "y", "on":
			return true
		default:
			return false
		}
	case float64:
		return v != 0
	case int:
		return v != 0
	default:
		return false
	}
}

func derivedAccountEmail(token, label string) string {
	sum := sha256.Sum256([]byte(token))
	prefix := hex.EncodeToString(sum[:])[:12]
	if label = strings.TrimSpace(label); label != "" {
		label = strings.Map(func(r rune) rune {
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '.' || r == '-' || r == '_' {
				return r
			}
			return '-'
		}, label)
		label = strings.Trim(label, ".-_")
		if label != "" {
			return strings.ToLower(label) + "-" + prefix + "@local.invalid"
		}
	}
	return "account-" + prefix + "@local.invalid"
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func writeJSON(w http.ResponseWriter, code int, body any) {
	setNoStore(w)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	writeJSONNoEscape(w, body)
}
