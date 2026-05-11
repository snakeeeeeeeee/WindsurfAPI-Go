package handler

import (
	"net/http"

	"github.com/zhangyu/windsurfapi-go/internal/account"
	"github.com/zhangyu/windsurfapi-go/internal/ls"
	reusepool "github.com/zhangyu/windsurfapi-go/internal/reuse"
	"github.com/zhangyu/windsurfapi-go/internal/windsurf/direct"
)

func DebugAccountsHandler(am *account.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		writeJSONNoEscape(w, am.Snapshot())
	}
}

func DebugLSHandler(pool *ls.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		writeJSONNoEscape(w, pool.Snapshot())
	}
}

func DebugSchedulerHandler(am *account.Manager, rp *reusepool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		snap := am.Snapshot()
		writeJSONNoEscape(w, map[string]any{
			"scheduler":   snap.Events,
			"health":      snap.Health,
			"coordinator": snap.Coordinator,
			"reuse":       rp.Stats(),
			"entries":     reuseDebugEntries(rp),
			"requests":    requestStatsSnapshot(),
		})
	}
}

func reuseDebugEntries(rp *reusepool.Pool) []cacheEntryView {
	if rp == nil {
		return []cacheEntryView{}
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
	return views
}

func DebugDirectHandler(dc *direct.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		writeJSONNoEscape(w, dc.Snapshot())
	}
}
