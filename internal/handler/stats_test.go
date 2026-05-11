package handler

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhangyu/windsurfapi-go/internal/account"
	"github.com/zhangyu/windsurfapi-go/internal/store"
)

func TestRequestStatsPersistsAndReloads(t *testing.T) {
	sqliteStore, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "windsurf.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer sqliteStore.Close()

	stats := &requestStatsStore{}
	stats.ConfigureDB(sqliteStore.DB)
	stats.Record(RequestEvent{
		RequestID:     "req_test",
		Route:         "chat",
		Model:         "claude-sonnet-4.6",
		CallerKeyHash: "caller",
		AccountID:     7,
		Attempt:       2,
		Status:        "error",
		HTTPStatus:    429,
		ErrorClass:    account.ErrorRateLimit,
		Error:         "rate limit",
		Retry:         true,
		LatencyMS:     123,
		SendMS:        111,
		UsageInput:    10,
		UsageOutput:   2,
		ToolCallCount: 1,
	})

	reloaded := &requestStatsStore{}
	reloaded.ConfigureDB(sqliteStore.DB)
	recent := reloaded.Recent(10)
	if len(recent) != 1 {
		t.Fatalf("recent len=%d", len(recent))
	}
	got := recent[0]
	if got.RequestID != "req_test" || got.ErrorClass != account.ErrorRateLimit || !got.Retry || got.UsageInput != 10 {
		t.Fatalf("reloaded event=%+v", got)
	}
	snap := reloaded.Snapshot()
	if snap["persistent"] != true || snap["total"] != 1 {
		t.Fatalf("snapshot=%+v", snap)
	}
}

func TestRequestStatsRedactsSensitiveErrorsBeforePersistingAndExport(t *testing.T) {
	sqliteStore, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "windsurf.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer sqliteStore.Close()

	secretErr := `Authorization: Bearer sk-test_abcdefghijklmnopqrstuvwxyz user@example.com proxy=http://user:secret@proxy.example.com:8080 token=devin-session-token$abc123`
	stats := &requestStatsStore{}
	stats.ConfigureDB(sqliteStore.DB)
	stats.Record(RequestEvent{
		RequestID:       "req_secret",
		Route:           "chat",
		Model:           "claude-sonnet-4.6",
		Status:          "error",
		HTTPStatus:      502,
		ErrorClass:      account.ErrorTransport,
		Error:           secretErr,
		ReuseMissReason: `caller_key=raw-caller-secret user@example.com`,
	})

	checkNoSecrets := func(t *testing.T, label, got string) {
		t.Helper()
		for _, forbidden := range []string{"sk-test_abcdefghijklmnopqrstuvwxyz", "user@example.com", "secret@proxy", "devin-session-token$abc123", "raw-caller-secret"} {
			if strings.Contains(got, forbidden) {
				t.Fatalf("%s leaked %q: %s", label, forbidden, got)
			}
		}
	}

	recent := stats.Recent(1)
	if len(recent) != 1 {
		t.Fatalf("recent len=%d", len(recent))
	}
	checkNoSecrets(t, "recent error", recent[0].Error+" "+recent[0].ReuseMissReason)

	reloaded := &requestStatsStore{}
	reloaded.ConfigureDB(sqliteStore.DB)
	if got := reloaded.Recent(1); len(got) != 1 {
		t.Fatalf("reloaded len=%d", len(got))
	} else {
		checkNoSecrets(t, "persisted error", got[0].Error+" "+got[0].ReuseMissReason)
	}

	var csvBuf strings.Builder
	if err := writeRequestEventsCSV(&csvBuf, recent); err != nil {
		t.Fatal(err)
	}
	checkNoSecrets(t, "csv", csvBuf.String())
	checkNoSecrets(t, "json", string(requestEventJSON(recent[0])))
}

func TestRequestStatsSubscribeAndCSV(t *testing.T) {
	stats := &requestStatsStore{}
	ch, cancel := stats.Subscribe(1)
	defer cancel()
	ev := RequestEvent{RequestID: "req_csv", Route: "chat", Model: "claude-sonnet-4.6", Status: "ok", HTTPStatus: 200, LatencyMS: 10}
	stats.Record(ev)
	got := <-ch
	if got.RequestID != ev.RequestID {
		t.Fatalf("subscriber got %+v", got)
	}
	var buf strings.Builder
	if err := writeRequestEventsCSV(&buf, []RequestEvent{ev}); err != nil {
		t.Fatal(err)
	}
	csv := buf.String()
	if !strings.Contains(csv, "req_csv") || !strings.Contains(csv, "latency_ms") {
		t.Fatalf("csv=%q", csv)
	}
}

func TestRequestStatsRecentFiltered(t *testing.T) {
	stats := &requestStatsStore{}
	stats.Record(RequestEvent{RequestID: "req_chat", Route: "chat", Model: "claude-sonnet-4.6", Status: "ok", HTTPStatus: 200, AccountID: 1})
	stats.Record(RequestEvent{RequestID: "req_messages", Route: "messages", Model: "claude-sonnet-4.6", Status: "error", HTTPStatus: 429, ErrorClass: account.ErrorRateLimit, AccountID: 2, Retry: true})
	stats.Record(RequestEvent{RequestID: "req_stream", Route: "responses", Model: "claude-sonnet-4.6", Status: "ok", HTTPStatus: 200, AccountID: 3, Stream: true})

	got := stats.RecentFiltered(10, RequestLogFilter{Route: "messages", Status: "error", ErrorClass: "rate_limit", AccountID: 2})
	if len(got) != 1 || got[0].RequestID != "req_messages" {
		t.Fatalf("route/status filtered=%+v", got)
	}
	stream := true
	got = stats.RecentFiltered(10, RequestLogFilter{Stream: &stream})
	if len(got) != 1 || got[0].RequestID != "req_stream" {
		t.Fatalf("stream filtered=%+v", got)
	}
	retry := true
	got = stats.RecentFiltered(10, RequestLogFilter{Retry: &retry, Query: "429"})
	if len(got) != 1 || got[0].RequestID != "req_messages" {
		t.Fatalf("retry/query filtered=%+v", got)
	}
}

func TestRequestStatsSnapshotAggregatesUsageCacheAndAccounts(t *testing.T) {
	stats := &requestStatsStore{}
	stats.Record(RequestEvent{RequestID: "req_1", Route: "chat", Model: "claude-sonnet-4.6", Status: "ok", HTTPStatus: 200, AccountID: 1, LatencyMS: 900, UsageInput: 100, UsageOutput: 20, UsageCacheRead: 40, ReuseHit: true, ToolCallCount: 1})
	stats.Record(RequestEvent{RequestID: "req_2", Route: "messages", Model: "claude-sonnet-4.6", Status: "ok", HTTPStatus: 200, AccountID: 1, LatencyMS: 2500, UsageInput: 50, UsageOutput: 10, Stream: true})
	stats.Record(RequestEvent{RequestID: "req_3", Route: "responses", Model: "claude-sonnet-4.6", Status: "error", HTTPStatus: 502, AccountID: 2, LatencyMS: 12000, ErrorClass: account.ErrorTransport})

	snap := stats.Snapshot()
	usage := snap["usage"].(map[string]any)
	if usage["input"].(uint64) != 150 || usage["output"].(uint64) != 30 || usage["cache_read"].(uint64) != 40 {
		t.Fatalf("usage=%+v", usage)
	}
	cache := snap["cache"].(map[string]any)
	if cache["reuse_hits"].(int) != 1 || cache["cache_read_tokens"].(uint64) != 40 {
		t.Fatalf("cache=%+v", cache)
	}
	byAccount := snap["by_account"].(map[string]int)
	if byAccount["1"] != 2 || byAccount["2"] != 1 {
		t.Fatalf("by_account=%+v", byAccount)
	}
	buckets := snap["latency_buckets"].(map[string]int)
	if buckets["0-1s"] != 1 || buckets["1-3s"] != 1 || buckets["10s+"] != 1 {
		t.Fatalf("buckets=%+v", buckets)
	}
	if snap["stream_count"].(int) != 1 || snap["tool_call_count"].(int) != 1 {
		t.Fatalf("snap=%+v", snap)
	}
}
