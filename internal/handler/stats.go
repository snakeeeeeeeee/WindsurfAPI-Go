package handler

import (
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"io"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/zhangyu/windsurfapi-go/internal/account"
	"github.com/zhangyu/windsurfapi-go/internal/redact"
)

const maxRequestEvents = 500

type RequestEvent struct {
	Time            time.Time          `json:"time"`
	RequestID       string             `json:"req_id"`
	Route           string             `json:"route"`
	Model           string             `json:"model"`
	CallerKeyHash   string             `json:"caller_key_hash,omitempty"`
	AccountID       int                `json:"account_id,omitempty"`
	Attempt         int                `json:"attempt"`
	Status          string             `json:"status"`
	HTTPStatus      int                `json:"http_status"`
	ErrorClass      account.ErrorClass `json:"error_class,omitempty"`
	Error           string             `json:"error,omitempty"`
	Retry           bool               `json:"retry"`
	Stream          bool               `json:"stream"`
	LatencyMS       int64              `json:"latency_ms"`
	SendMS          int64              `json:"send_ms"`
	FirstTextMS     int64              `json:"first_text_ms,omitempty"`
	UsageInput      uint64             `json:"usage_input"`
	UsageOutput     uint64             `json:"usage_output"`
	UsageCacheRead  uint64             `json:"usage_cache_read"`
	ToolCallCount   int                `json:"tool_call_count"`
	ReuseHit        bool               `json:"reuse_hit"`
	ReuseMissReason string             `json:"reuse_miss_reason,omitempty"`
}

type requestStatsStore struct {
	mu          sync.Mutex
	events      []RequestEvent
	db          *sql.DB
	lastDBError string
	subscribers map[chan RequestEvent]struct{}
}

var globalRequestStats = &requestStatsStore{}

func ConfigureRequestStatsDB(db *sql.DB) {
	globalRequestStats.ConfigureDB(db)
}

func recordRequestEvent(ev RequestEvent) {
	globalRequestStats.Record(ev)
}

func requestStatsSnapshot() map[string]any {
	return globalRequestStats.Snapshot()
}

func resetRequestStats() {
	globalRequestStats.Reset()
}

func requestLogsSnapshot(limit int) []RequestEvent {
	return globalRequestStats.Recent(limit)
}

func requestLogsSnapshotFiltered(limit int, filter RequestLogFilter) []RequestEvent {
	return globalRequestStats.RecentFiltered(limit, filter)
}

type RequestLogFilter struct {
	Query      string
	Route      string
	Model      string
	Status     string
	ErrorClass string
	AccountID  int
	HTTPStatus int
	Stream     *bool
	Retry      *bool
}

func (s *requestStatsStore) Record(ev RequestEvent) {
	if ev.Time.IsZero() {
		ev.Time = time.Now()
	}
	ev = redactRequestEvent(ev)
	s.mu.Lock()
	s.events = append(s.events, ev)
	if len(s.events) > maxRequestEvents {
		s.events = append([]RequestEvent(nil), s.events[len(s.events)-maxRequestEvents:]...)
	}
	db := s.db
	var subscribers []chan RequestEvent
	for ch := range s.subscribers {
		subscribers = append(subscribers, ch)
	}
	s.mu.Unlock()
	if db != nil {
		if err := insertRequestEvent(db, ev); err != nil {
			s.mu.Lock()
			s.lastDBError = err.Error()
			s.mu.Unlock()
		}
	}
	for _, ch := range subscribers {
		select {
		case ch <- ev:
		default:
		}
	}
}

func (s *requestStatsStore) ConfigureDB(db *sql.DB) {
	s.mu.Lock()
	s.db = db
	if s.subscribers == nil {
		s.subscribers = map[chan RequestEvent]struct{}{}
	}
	if db != nil {
		s.events = loadRecentRequestEvents(db, maxRequestEvents)
	}
	s.lastDBError = ""
	s.mu.Unlock()
}

func (s *requestStatsStore) Reset() {
	s.mu.Lock()
	s.events = nil
	s.lastDBError = ""
	s.mu.Unlock()
}

func (s *requestStatsStore) Recent(limit int) []RequestEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	if limit <= 0 || limit > len(s.events) {
		limit = len(s.events)
	}
	start := len(s.events) - limit
	out := redactRequestEvents(append([]RequestEvent(nil), s.events[start:]...))
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

func (s *requestStatsStore) RecentFiltered(limit int, filter RequestLogFilter) []RequestEvent {
	s.mu.Lock()
	events := append([]RequestEvent(nil), s.events...)
	s.mu.Unlock()
	out := make([]RequestEvent, 0, minInt(limit, len(events)))
	for i := len(events) - 1; i >= 0; i-- {
		ev := events[i]
		if !requestEventMatches(ev, filter) {
			continue
		}
		out = append(out, redactRequestEvent(ev))
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out
}

func requestEventMatches(ev RequestEvent, filter RequestLogFilter) bool {
	if filter.Route != "" && !strings.EqualFold(ev.Route, filter.Route) {
		return false
	}
	if filter.Model != "" && !strings.EqualFold(ev.Model, filter.Model) {
		return false
	}
	if filter.Status != "" && !strings.EqualFold(ev.Status, filter.Status) {
		return false
	}
	if filter.ErrorClass != "" && !strings.EqualFold(string(ev.ErrorClass), filter.ErrorClass) {
		return false
	}
	if filter.AccountID > 0 && ev.AccountID != filter.AccountID {
		return false
	}
	if filter.HTTPStatus > 0 && ev.HTTPStatus != filter.HTTPStatus {
		return false
	}
	if filter.Stream != nil && ev.Stream != *filter.Stream {
		return false
	}
	if filter.Retry != nil && ev.Retry != *filter.Retry {
		return false
	}
	if q := strings.TrimSpace(strings.ToLower(filter.Query)); q != "" {
		haystack := strings.ToLower(strings.Join([]string{
			ev.Time.Format(time.RFC3339Nano),
			ev.RequestID,
			ev.Route,
			ev.Model,
			ev.CallerKeyHash,
			strconv.Itoa(ev.AccountID),
			strconv.Itoa(ev.Attempt),
			ev.Status,
			strconv.Itoa(ev.HTTPStatus),
			string(ev.ErrorClass),
			ev.Error,
		}, " "))
		if !strings.Contains(haystack, q) {
			return false
		}
	}
	return true
}

func (s *requestStatsStore) Subscribe(buffer int) (chan RequestEvent, func()) {
	if buffer <= 0 {
		buffer = 16
	}
	ch := make(chan RequestEvent, buffer)
	s.mu.Lock()
	if s.subscribers == nil {
		s.subscribers = map[chan RequestEvent]struct{}{}
	}
	s.subscribers[ch] = struct{}{}
	s.mu.Unlock()
	cancel := func() {
		s.mu.Lock()
		if _, ok := s.subscribers[ch]; ok {
			delete(s.subscribers, ch)
			close(ch)
		}
		s.mu.Unlock()
	}
	return ch, cancel
}

func (s *requestStatsStore) Snapshot() map[string]any {
	s.mu.Lock()
	events := append([]RequestEvent(nil), s.events...)
	persistent := s.db != nil
	lastDBError := redact.Text(s.lastDBError)
	s.mu.Unlock()

	byRoute := map[string]int{}
	byModel := map[string]int{}
	byClass := map[string]int{}
	byAccount := map[string]int{}
	latencyBuckets := map[string]int{"0-1s": 0, "1-3s": 0, "3-10s": 0, "10s+": 0}
	success, failed, retried := 0, 0, 0
	var usageInput, usageOutput, usageCacheRead uint64
	reuseHits, streamCount, toolCallCount := 0, 0, 0
	var latencies []int64
	for _, ev := range events {
		byRoute[ev.Route]++
		byModel[ev.Model]++
		if ev.AccountID > 0 {
			byAccount[strconv.Itoa(ev.AccountID)]++
		}
		if ev.ErrorClass != "" {
			byClass[string(ev.ErrorClass)]++
		}
		if ev.Status == "ok" {
			success++
		} else if ev.Status != "" {
			failed++
		}
		if ev.Retry {
			retried++
		}
		if ev.Stream {
			streamCount++
		}
		if ev.ReuseHit {
			reuseHits++
		}
		toolCallCount += ev.ToolCallCount
		usageInput += ev.UsageInput
		usageOutput += ev.UsageOutput
		usageCacheRead += ev.UsageCacheRead
		if ev.LatencyMS > 0 {
			latencies = append(latencies, ev.LatencyMS)
			switch {
			case ev.LatencyMS < 1000:
				latencyBuckets["0-1s"]++
			case ev.LatencyMS < 3000:
				latencyBuckets["1-3s"]++
			case ev.LatencyMS < 10000:
				latencyBuckets["3-10s"]++
			default:
				latencyBuckets["10s+"]++
			}
		}
	}
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	total := len(events)
	return map[string]any{
		"window":          maxRequestEvents,
		"total":           total,
		"success":         success,
		"failed":          failed,
		"retried":         retried,
		"error_rate":      ratio(failed, total),
		"p50_ms":          percentile(latencies, 0.50),
		"p95_ms":          percentile(latencies, 0.95),
		"p99_ms":          percentile(latencies, 0.99),
		"by_route":        byRoute,
		"by_model":        byModel,
		"by_class":        byClass,
		"by_account":      byAccount,
		"latency_buckets": latencyBuckets,
		"usage": map[string]any{
			"input":      usageInput,
			"output":     usageOutput,
			"cache_read": usageCacheRead,
			"total":      usageInput + usageOutput,
		},
		"cache": map[string]any{
			"reuse_hits":        reuseHits,
			"reuse_hit_rate":    ratio(reuseHits, total),
			"cache_read_tokens": usageCacheRead,
			"cache_read_ratio":  ratio64(usageCacheRead, usageInput),
		},
		"stream_count":    streamCount,
		"tool_call_count": toolCallCount,
		"recent":          redactRequestEvents(lastRequestEvents(events, 25)),
		"persistent":      persistent,
		"db_error":        lastDBError,
	}
}

func lastRequestEvents(events []RequestEvent, limit int) []RequestEvent {
	if limit > len(events) {
		limit = len(events)
	}
	if limit <= 0 {
		return nil
	}
	out := append([]RequestEvent(nil), events[len(events)-limit:]...)
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

func percentile(sorted []int64, p float64) int64 {
	if len(sorted) == 0 {
		return 0
	}
	if p <= 0 {
		return sorted[0]
	}
	if p >= 1 {
		return sorted[len(sorted)-1]
	}
	idx := int(float64(len(sorted)-1) * p)
	return sorted[idx]
}

func ratio(n, d int) float64 {
	if d == 0 {
		return 0
	}
	return float64(n) / float64(d)
}

func ratio64(n, d uint64) float64 {
	if d == 0 {
		return 0
	}
	return float64(n) / float64(d)
}

func writeRequestEventsCSV(w io.Writer, events []RequestEvent) error {
	cw := csv.NewWriter(w)
	if err := cw.Write([]string{
		"time", "req_id", "route", "model", "caller_key_hash", "account_id", "attempt", "status", "http_status",
		"error_class", "error", "retry", "stream", "latency_ms", "send_ms", "first_text_ms",
		"usage_input", "usage_output", "usage_cache_read", "tool_call_count",
		"reuse_hit", "reuse_miss_reason",
	}); err != nil {
		return err
	}
	for _, ev := range events {
		ev = redactRequestEvent(ev)
		if err := cw.Write([]string{
			ev.Time.Format(time.RFC3339Nano),
			ev.RequestID,
			ev.Route,
			ev.Model,
			ev.CallerKeyHash,
			strconv.Itoa(ev.AccountID),
			strconv.Itoa(ev.Attempt),
			ev.Status,
			strconv.Itoa(ev.HTTPStatus),
			string(ev.ErrorClass),
			ev.Error,
			strconv.FormatBool(ev.Retry),
			strconv.FormatBool(ev.Stream),
			strconv.FormatInt(ev.LatencyMS, 10),
			strconv.FormatInt(ev.SendMS, 10),
			strconv.FormatInt(ev.FirstTextMS, 10),
			strconv.FormatUint(ev.UsageInput, 10),
			strconv.FormatUint(ev.UsageOutput, 10),
			strconv.FormatUint(ev.UsageCacheRead, 10),
			strconv.Itoa(ev.ToolCallCount),
			strconv.FormatBool(ev.ReuseHit),
			ev.ReuseMissReason,
		}); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}

func requestEventJSON(ev RequestEvent) []byte {
	ev = redactRequestEvent(ev)
	raw, _ := json.Marshal(ev)
	return raw
}

func insertRequestEvent(db *sql.DB, ev RequestEvent) error {
	ev = redactRequestEvent(ev)
	_, err := db.Exec(
		`INSERT INTO request_events (
			time, req_id, route, model, caller_key_hash, account_id, attempt, status, http_status,
			error_class, error, retry, stream, latency_ms, send_ms, first_text_ms,
			usage_input, usage_output, usage_cache_read, tool_call_count, reuse_hit, reuse_miss_reason
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		ev.Time, ev.RequestID, ev.Route, ev.Model, ev.CallerKeyHash, ev.AccountID, ev.Attempt, ev.Status, ev.HTTPStatus,
		string(ev.ErrorClass), ev.Error, boolInt(ev.Retry), boolInt(ev.Stream), ev.LatencyMS, ev.SendMS, ev.FirstTextMS,
		ev.UsageInput, ev.UsageOutput, ev.UsageCacheRead, ev.ToolCallCount, boolInt(ev.ReuseHit), ev.ReuseMissReason,
	)
	return err
}

func loadRecentRequestEvents(db *sql.DB, limit int) []RequestEvent {
	if db == nil || limit <= 0 {
		return nil
	}
	rows, err := db.Query(
		`SELECT time, req_id, route, model, caller_key_hash, account_id, attempt, status, http_status,
			error_class, error, retry, stream, latency_ms, send_ms, first_text_ms,
			usage_input, usage_output, usage_cache_read, tool_call_count, reuse_hit, reuse_miss_reason
		 FROM request_events ORDER BY time DESC, id DESC LIMIT ?`,
		limit,
	)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var desc []RequestEvent
	for rows.Next() {
		var ev RequestEvent
		var errorClass string
		var retry, stream, reuseHit int
		if err := rows.Scan(
			&ev.Time, &ev.RequestID, &ev.Route, &ev.Model, &ev.CallerKeyHash, &ev.AccountID, &ev.Attempt, &ev.Status, &ev.HTTPStatus,
			&errorClass, &ev.Error, &retry, &stream, &ev.LatencyMS, &ev.SendMS, &ev.FirstTextMS,
			&ev.UsageInput, &ev.UsageOutput, &ev.UsageCacheRead, &ev.ToolCallCount, &reuseHit, &ev.ReuseMissReason,
		); err != nil {
			return nil
		}
		ev.ErrorClass = account.ErrorClass(errorClass)
		ev.Retry = retry != 0
		ev.Stream = stream != 0
		ev.ReuseHit = reuseHit != 0
		ev = redactRequestEvent(ev)
		desc = append(desc, ev)
	}
	for i, j := 0, len(desc)-1; i < j; i, j = i+1, j-1 {
		desc[i], desc[j] = desc[j], desc[i]
	}
	return desc
}

func redactRequestEvent(ev RequestEvent) RequestEvent {
	ev.Error = redact.Text(ev.Error)
	ev.ReuseMissReason = redact.Text(ev.ReuseMissReason)
	return ev
}

func redactRequestEvents(events []RequestEvent) []RequestEvent {
	for i := range events {
		events[i] = redactRequestEvent(events[i])
	}
	return events
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
