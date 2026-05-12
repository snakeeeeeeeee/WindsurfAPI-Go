package account

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/zhangyu/windsurfapi-go/internal/redact"
	"github.com/zhangyu/windsurfapi-go/internal/store"
)

const (
	rpmWindow         = time.Minute
	defaultCooldown   = 5 * time.Minute
	queueRetryEvery   = 100 * time.Millisecond
	defaultQueueWait  = 30 * time.Second
	unknownQuotaScore = 100

	recentErrorWindow      = 5 * time.Minute
	modelBreakerThreshold  = 3
	modelBreakerCooldown   = 2 * time.Minute
	droughtQuotaThreshold  = 10
	severeDroughtThreshold = 5
)

var tierRPM = map[string]int{
	"free":    10,
	"unknown": 20,
	"pro":     60,
}

// ErrorClass is the scheduler-facing error class for cooldown/health updates.
type ErrorClass string

const (
	ErrorRateLimit         ErrorClass = "rate_limit"
	ErrorModelNotAvailable ErrorClass = "model_not_available"
	ErrorPolicyBlocked     ErrorClass = "policy_blocked"
	ErrorBanSignal         ErrorClass = "ban_signal"
	ErrorUpstreamTransient ErrorClass = "upstream_transient"
	ErrorTransport         ErrorClass = "transport"
	ErrorFatal             ErrorClass = "fatal"
)

// Account 表示一个 Windsurf 账号。
type Account struct {
	ID                 int        `json:"id"`
	Email              string     `json:"email"`
	FirebaseToken      string     `json:"firebase_token"`
	UserID             string     `json:"user_id"`
	ProxyURL           string     `json:"proxy_url"`
	Tier               string     `json:"tier"`
	PlanName           string     `json:"plan_name,omitempty"`
	ModelConfigCount   int        `json:"model_config_count,omitempty"`
	RateLimitedUntil   *time.Time `json:"rate_limited_until,omitempty"`
	QuotaDailyPercent  *float64   `json:"quota_daily_percent,omitempty"`
	QuotaWeeklyPercent *float64   `json:"quota_weekly_percent,omitempty"`
	QuotaDailyResetAt  *time.Time `json:"quota_daily_reset_at,omitempty"`
	QuotaWeeklyResetAt *time.Time `json:"quota_weekly_reset_at,omitempty"`
	PromptLimit        *float64   `json:"prompt_limit,omitempty"`
	PromptUsed         *float64   `json:"prompt_used,omitempty"`
	PromptRemaining    *float64   `json:"prompt_remaining,omitempty"`
	FlexLimit          *float64   `json:"flex_limit,omitempty"`
	FlexUsed           *float64   `json:"flex_used,omitempty"`
	FlexRemaining      *float64   `json:"flex_remaining,omitempty"`
	OverageBalance     *float64   `json:"overage_balance,omitempty"`
	PlanStart          string     `json:"plan_start,omitempty"`
	PlanEnd            string     `json:"plan_end,omitempty"`
	HealthCheckedAt    *time.Time `json:"health_checked_at,omitempty"`
	LastUsedAt         *time.Time `json:"last_used_at,omitempty"`
	Enabled            bool       `json:"enabled"`
	Banned             bool       `json:"banned"`
	Notes              string     `json:"notes"`
	CreatedAt          time.Time  `json:"created_at"`
	UpdatedAt          time.Time  `json:"updated_at"`
}

// MaskProxyURL hides proxy credentials for debug/admin API responses. Runtime
// paths must use Account.ProxyURL directly, never this masked value.
func MaskProxyURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "***"
	}
	if u.User != nil {
		username := u.User.Username()
		if username != "" {
			u.User = url.UserPassword(username, "***")
		} else {
			u.User = url.User("***")
		}
	}
	return u.String()
}

// AccountModel 账号-模型关联。
type AccountModel struct {
	ID            int        `json:"id"`
	AccountID     int        `json:"account_id"`
	ModelName     string     `json:"model_name"`
	Enabled       bool       `json:"enabled"`
	CooldownUntil *time.Time `json:"cooldown_until,omitempty"`
}

// Reservation is a checked-out account. Callers must Release or Refund it.
type Reservation struct {
	Account              *Account
	ModelID              string
	ReservationTimestamp time.Time
	refunded             bool
	released             bool
	coordRelease         func()
}

type runtimeState struct {
	inflight      int
	rpmHistory    []time.Time
	lastUsed      time.Time
	modelCooldown map[string]time.Time
	modelErrors   map[string]*modelErrorState
	modelBreakers map[string]breakerState
}

type failureRecord struct {
	At      time.Time
	Class   ErrorClass
	Message string
}

type breakerState struct {
	Until  time.Time
	Reason string
}

type modelErrorState struct {
	Failures  []failureRecord
	LastClass ErrorClass
	LastError string
}

// Manager 账号管理器。
type Manager struct {
	db *sql.DB

	mu                    sync.Mutex
	states                map[int]*runtimeState
	events                []DebugEvent
	health                HealthSummary
	coord                 Coordinator
	maxInflightPerAccount int
}

// DebugEvent is a compact scheduler event for debug APIs.
type DebugEvent struct {
	Time     time.Time  `json:"time"`
	Account  int        `json:"account_id"`
	Model    string     `json:"model"`
	Class    ErrorClass `json:"class"`
	Message  string     `json:"message"`
	Cooldown int64      `json:"cooldown_ms,omitempty"`
}

type DebugAccount struct {
	ID                 int               `json:"id"`
	Email              string            `json:"email"`
	UserID             string            `json:"user_id"`
	ProxyURL           string            `json:"proxy_url"`
	ProxyURLSet        bool              `json:"proxy_url_set"`
	Tier               string            `json:"tier"`
	PlanName           string            `json:"plan_name,omitempty"`
	ModelConfigCount   int               `json:"model_config_count,omitempty"`
	Enabled            bool              `json:"enabled"`
	Banned             bool              `json:"banned"`
	Notes              string            `json:"notes"`
	CreatedAt          time.Time         `json:"created_at"`
	UpdatedAt          time.Time         `json:"updated_at"`
	TokenSet           bool              `json:"token_set"`
	Inflight           int               `json:"inflight"`
	RPMUsed            int               `json:"rpm_used"`
	RPMLimit           int               `json:"rpm_limit"`
	QuotaDailyPercent  *float64          `json:"quota_daily_percent,omitempty"`
	QuotaWeeklyPercent *float64          `json:"quota_weekly_percent,omitempty"`
	QuotaDailyResetAt  string            `json:"quota_daily_reset_at,omitempty"`
	QuotaWeeklyResetAt string            `json:"quota_weekly_reset_at,omitempty"`
	QuotaScore         float64           `json:"quota_score"`
	Prompt             CreditSnapshot    `json:"prompt,omitempty"`
	Flex               CreditSnapshot    `json:"flex,omitempty"`
	OverageBalance     *float64          `json:"overage_balance,omitempty"`
	PlanStart          string            `json:"plan_start,omitempty"`
	PlanEnd            string            `json:"plan_end,omitempty"`
	HealthCheckedAt    string            `json:"health_checked_at,omitempty"`
	Drought            bool              `json:"drought"`
	DroughtPenalty     int               `json:"drought_penalty"`
	RateLimitedUntil   string            `json:"rate_limited_until,omitempty"`
	ModelCooldowns     map[string]string `json:"model_cooldowns,omitempty"`
	ModelBreakers      map[string]string `json:"model_breakers,omitempty"`
	RecentErrors       map[string]int    `json:"recent_errors,omitempty"`
	BlockedModels      []string          `json:"blocked_models,omitempty"`
}

type CreditSnapshot struct {
	Limit     *float64 `json:"limit,omitempty"`
	Used      *float64 `json:"used,omitempty"`
	Remaining *float64 `json:"remaining,omitempty"`
}

type HealthUpdate struct {
	Tier             string
	PlanName         string
	DailyPercent     *float64
	WeeklyPercent    *float64
	DailyResetAt     *time.Time
	WeeklyResetAt    *time.Time
	PromptLimit      *float64
	PromptUsed       *float64
	PromptRemaining  *float64
	FlexLimit        *float64
	FlexUsed         *float64
	FlexRemaining    *float64
	OverageBalance   *float64
	PlanStart        string
	PlanEnd          string
	ModelConfigCount int
	RateLimitedUntil *time.Time
	HealthCheckedAt  time.Time
	Note             string
}

type HealthSummary struct {
	LastRunAt      time.Time `json:"last_run_at,omitempty"`
	LastDurationMS int64     `json:"last_duration_ms,omitempty"`
	Checked        int       `json:"checked"`
	OK             int       `json:"ok"`
	Invalid        int       `json:"invalid"`
	Failed         int       `json:"failed"`
	LastError      string    `json:"last_error,omitempty"`
}

type SchedulerSnapshot struct {
	Accounts    []DebugAccount `json:"accounts"`
	Events      []DebugEvent   `json:"events"`
	Health      HealthSummary  `json:"health"`
	Coordinator map[string]any `json:"coordinator"`
}

// NewManager 创建账号管理器。
func NewManager(store *store.SQLiteStore) *Manager {
	return &Manager{db: store.DB, states: map[int]*runtimeState{}}
}

func (m *Manager) SetMaxInflightPerAccount(max int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if max < 0 {
		max = 0
	}
	m.maxInflightPerAccount = max
}

func (m *Manager) SetCoordinator(coord Coordinator) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.coord = coord
}

// Reserve selects and reserves an account for modelID. It waits briefly when
// every otherwise-eligible account has only transient RPM pressure.
func (m *Manager) Reserve(ctx context.Context, modelID string, excludeAccountIDs []int) (*Reservation, error) {
	return m.ReserveFrom(ctx, modelID, nil, excludeAccountIDs)
}

// ReserveFrom selects and reserves an account from an optional allow-list. When
// allowedAccountIDs is empty, it behaves like Reserve.
func (m *Manager) ReserveFrom(ctx context.Context, modelID string, allowedAccountIDs []int, excludeAccountIDs []int) (*Reservation, error) {
	deadline := time.Now().Add(defaultQueueWait)
	allowed := map[int]bool{}
	for _, id := range allowedAccountIDs {
		if id > 0 {
			allowed[id] = true
		}
	}
	exclude := map[int]bool{}
	for _, id := range excludeAccountIDs {
		exclude[id] = true
	}

	for {
		res, retryAfter, err := m.tryReserve(modelID, allowed, exclude)
		if err == nil {
			return res, nil
		}
		if !errors.Is(err, ErrNoAvailableAccount) || retryAfter <= 0 || time.Now().Add(retryAfter).After(deadline) {
			return nil, err
		}
		sleep := retryAfter
		if sleep > queueRetryEvery {
			sleep = queueRetryEvery
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(sleep):
		}
	}
}

var ErrNoAvailableAccount = errors.New("no available accounts")

func (m *Manager) ReserveAccount(ctx context.Context, modelID string, accountID int) (*Reservation, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	a, err := m.GetAccount(accountID)
	if err != nil {
		return nil, err
	}
	if a == nil || !a.Enabled || a.Banned || strings.TrimSpace(a.FirebaseToken) == "" {
		return nil, ErrNoAvailableAccount
	}

	now := time.Now()
	m.mu.Lock()
	defer m.mu.Unlock()
	st := m.stateLocked(a.ID)
	m.applyDBCooldownLocked(st, modelID, *a)
	m.pruneRuntimeHealthLocked(st, now)
	if inCooldown(st, modelID, now) || inBreaker(st, modelID, now) || rateLimited(*a, now) || m.modelDisabledLocked(a.ID, modelID) || modelUnavailableByHealth(*a, modelID) {
		if modelUnavailableByHealth(*a, modelID) {
			m.recordEventLocked(DebugEvent{Time: now, Account: a.ID, Model: modelID, Class: ErrorModelNotAvailable, Message: "model_unavailable_by_account_health"})
		}
		return nil, ErrNoAvailableAccount
	}
	if m.maxInflightPerAccount > 0 && st.inflight >= m.maxInflightPerAccount {
		m.recordEventLocked(DebugEvent{Time: now, Account: a.ID, Model: modelID, Class: ErrorRateLimit, Message: "local_inflight_full"})
		return nil, ErrNoAvailableAccount
	}
	if m.coord != nil {
		if ok, reason := m.coord.CanReserve(ctx, *a, modelID); !ok {
			m.recordEventLocked(DebugEvent{Time: now, Account: a.ID, Model: modelID, Class: ErrorRateLimit, Message: reason})
			return nil, ErrNoAvailableAccount
		}
	}
	limit := rpmLimit(a.Tier)
	if limit <= 0 || pruneRPM(st, now) >= limit {
		return nil, ErrNoAvailableAccount
	}

	st.inflight++
	st.lastUsed = now
	st.rpmHistory = append(st.rpmHistory, now)
	var coordRelease func()
	if m.coord != nil {
		release, ok, reason := m.coord.Reserve(ctx, *a, modelID, now)
		if !ok {
			st.inflight--
			st.rpmHistory = st.rpmHistory[:len(st.rpmHistory)-1]
			m.recordEventLocked(DebugEvent{Time: now, Account: a.ID, Model: modelID, Class: ErrorRateLimit, Message: reason})
			return nil, ErrNoAvailableAccount
		}
		coordRelease = release
	}
	a.LastUsedAt = &now
	m.persistLastUsedLocked(a.ID, now)
	return &Reservation{Account: a, ModelID: modelID, ReservationTimestamp: now, coordRelease: coordRelease}, nil
}

func (m *Manager) tryReserve(modelID string, allowed map[int]bool, exclude map[int]bool) (*Reservation, time.Duration, error) {
	accounts, err := m.GetEnabledAccounts()
	if err != nil {
		return nil, 0, err
	}

	now := time.Now()
	m.mu.Lock()
	defer m.mu.Unlock()

	type candidate struct {
		account    Account
		state      *runtimeState
		used       int
		limit      int
		score      float64
		penalty    int
		effective  int
		headroom   float64
		lastUsed   time.Time
		retryAfter time.Duration
	}
	var candidates []candidate
	var shortestRetry time.Duration
	for _, a := range accounts {
		if len(allowed) > 0 && !allowed[a.ID] {
			continue
		}
		if exclude[a.ID] || strings.TrimSpace(a.FirebaseToken) == "" {
			continue
		}
		st := m.stateLocked(a.ID)
		m.applyDBCooldownLocked(st, modelID, a)
		m.pruneRuntimeHealthLocked(st, now)
		if inCooldown(st, modelID, now) || inBreaker(st, modelID, now) || rateLimited(a, now) || m.modelDisabledLocked(a.ID, modelID) || modelUnavailableByHealth(a, modelID) {
			if modelUnavailableByHealth(a, modelID) {
				m.recordEventLocked(DebugEvent{Time: now, Account: a.ID, Model: modelID, Class: ErrorModelNotAvailable, Message: "model_unavailable_by_account_health"})
			}
			continue
		}
		if m.maxInflightPerAccount > 0 && st.inflight >= m.maxInflightPerAccount {
			shortestRetry = queueRetryEvery
			continue
		}
		if m.coord != nil {
			if ok, reason := m.coord.CanReserve(context.Background(), a, modelID); !ok {
				if retry := coordinatorRetryAfter(reason); retry > 0 && (shortestRetry == 0 || retry < shortestRetry) {
					shortestRetry = retry
				}
				continue
			}
		}
		limit := rpmLimit(a.Tier)
		if limit <= 0 {
			continue
		}
		used := pruneRPM(st, now)
		if used >= limit {
			if len(st.rpmHistory) > 0 {
				retry := st.rpmHistory[0].Add(rpmWindow).Sub(now)
				if retry > 0 && (shortestRetry == 0 || retry < shortestRetry) {
					shortestRetry = retry
				}
			}
			continue
		}
		headroom := float64(limit-used) / float64(limit)
		score := quotaScore(a)
		penalty := droughtPenalty(score)
		candidates = append(candidates, candidate{
			account:   a,
			state:     st,
			used:      used,
			limit:     limit,
			score:     score,
			penalty:   penalty,
			effective: st.inflight + penalty,
			headroom:  headroom,
			lastUsed:  st.lastUsed,
		})
	}
	if len(candidates) == 0 {
		return nil, shortestRetry, ErrNoAvailableAccount
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		x, y := candidates[i], candidates[j]
		if x.effective != y.effective {
			return x.effective < y.effective
		}
		if bucket(x.score) != bucket(y.score) {
			return bucket(x.score) > bucket(y.score)
		}
		if x.state.inflight != y.state.inflight {
			return x.state.inflight < y.state.inflight
		}
		if x.headroom != y.headroom {
			return x.headroom > y.headroom
		}
		if !x.lastUsed.Equal(y.lastUsed) {
			if x.lastUsed.IsZero() {
				return true
			}
			if y.lastUsed.IsZero() {
				return false
			}
			return x.lastUsed.Before(y.lastUsed)
		}
		return x.account.ID < y.account.ID
	})

	chosen := candidates[0]
	chosen.state.inflight++
	chosen.state.lastUsed = now
	chosen.state.rpmHistory = append(chosen.state.rpmHistory, now)
	a := chosen.account
	var coordRelease func()
	if m.coord != nil {
		release, ok, reason := m.coord.Reserve(context.Background(), a, modelID, now)
		if !ok {
			chosen.state.inflight--
			chosen.state.rpmHistory = chosen.state.rpmHistory[:len(chosen.state.rpmHistory)-1]
			m.recordEventLocked(DebugEvent{Time: time.Now(), Account: a.ID, Model: modelID, Class: ErrorRateLimit, Message: reason})
			if retry := coordinatorRetryAfter(reason); retry > 0 {
				return nil, retry, ErrNoAvailableAccount
			}
			return nil, shortestRetry, ErrNoAvailableAccount
		}
		coordRelease = release
	}
	a.LastUsedAt = &now
	m.persistLastUsedLocked(a.ID, now)
	return &Reservation{Account: &a, ModelID: modelID, ReservationTimestamp: now, coordRelease: coordRelease}, 0, nil
}

// Release decrements in-flight state for a reservation.
func (m *Manager) Release(res *Reservation) {
	if res == nil || res.Account == nil || res.released {
		return
	}
	m.mu.Lock()
	st := m.stateLocked(res.Account.ID)
	if st.inflight > 0 {
		st.inflight--
	}
	res.released = true
	release := res.coordRelease
	res.coordRelease = nil
	m.mu.Unlock()
	if release != nil {
		release()
	}
}

// Refund releases the reservation and removes its RPM reservation token.
func (m *Manager) Refund(res *Reservation) {
	if res == nil || res.Account == nil || res.refunded {
		return
	}
	m.mu.Lock()
	st := m.stateLocked(res.Account.ID)
	for i, ts := range st.rpmHistory {
		if ts.Equal(res.ReservationTimestamp) {
			st.rpmHistory = append(st.rpmHistory[:i], st.rpmHistory[i+1:]...)
			break
		}
	}
	coord := m.coord
	m.mu.Unlock()
	if coord != nil {
		coord.Refund(context.Background(), res.Account.ID, res.ReservationTimestamp)
		res.coordRelease = nil
	}
	res.refunded = true
	m.Release(res)
}

func (m *Manager) MarkCooldown(accountID int, modelName string, until time.Time, reason string) error {
	m.mu.Lock()
	st := m.stateLocked(accountID)
	if st.modelCooldown == nil {
		st.modelCooldown = map[string]time.Time{}
	}
	key := modelName
	if key == "" {
		key = "*"
	}
	st.modelCooldown[key] = until
	coord := m.coord
	m.recordEventLocked(DebugEvent{Time: time.Now(), Account: accountID, Model: key, Class: ErrorRateLimit, Message: reason, Cooldown: time.Until(until).Milliseconds()})
	m.mu.Unlock()
	if coord != nil {
		coord.MarkCooldown(context.Background(), accountID, key, until)
	}
	if key == "*" {
		if _, err := m.db.Exec("UPDATE accounts SET rate_limited_until = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?", until, accountID); err != nil {
			return err
		}
	}
	return m.SetCooldown(accountID, key, until)
}

func (m *Manager) RecordSuccess(res *Reservation, usage any) {
	if res == nil || res.Account == nil {
		return
	}
	m.mu.Lock()
	st := m.stateLocked(res.Account.ID)
	key := modelKey(res.ModelID)
	if st.modelErrors != nil {
		delete(st.modelErrors, key)
	}
	if st.modelBreakers != nil {
		if br, ok := st.modelBreakers[key]; ok && !br.Until.After(time.Now()) {
			delete(st.modelBreakers, key)
		}
	}
	m.recordEventLocked(DebugEvent{Time: time.Now(), Account: res.Account.ID, Model: "", Class: "", Message: "success"})
	m.mu.Unlock()
}

func (m *Manager) ResetAccountErrors(accountID int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	st := m.stateLocked(accountID)
	st.modelErrors = map[string]*modelErrorState{}
	st.modelBreakers = map[string]breakerState{}
	m.recordEventLocked(DebugEvent{Time: time.Now(), Account: accountID, Message: "account errors reset"})
}

// ClearAccountAvailability removes account-local cooldown, breaker, recent
// error, RPM, and inflight state after a material identity change such as
// dynamic proxy rebinding. It intentionally leaves durable health/quota fields
// untouched because those describe the Windsurf account, not the current IP.
func (m *Manager) ClearAccountAvailability(accountID int, reason string) error {
	if accountID <= 0 {
		return nil
	}
	if _, err := m.db.Exec("UPDATE accounts SET rate_limited_until = NULL, updated_at = CURRENT_TIMESTAMP WHERE id = ?", accountID); err != nil {
		return err
	}
	if _, err := m.db.Exec("UPDATE account_models SET cooldown_until = NULL WHERE account_id = ?", accountID); err != nil {
		return err
	}
	m.mu.Lock()
	st := m.stateLocked(accountID)
	st.modelCooldown = map[string]time.Time{}
	st.modelErrors = map[string]*modelErrorState{}
	st.modelBreakers = map[string]breakerState{}
	st.rpmHistory = nil
	st.inflight = 0
	coord := m.coord
	m.recordEventLocked(DebugEvent{
		Time:    time.Now(),
		Account: accountID,
		Message: fmt.Sprintf("account availability cleared: %s", defaultString(reason, "account_identity_changed")),
	})
	m.mu.Unlock()
	if coord != nil {
		coord.ClearCooldown(context.Background(), accountID, "*")
	}
	return nil
}

func defaultString(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return strings.TrimSpace(value)
}

func (m *Manager) RecordFailure(res *Reservation, class ErrorClass, err error) {
	if res == nil || res.Account == nil {
		return
	}
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	m.mu.Lock()
	now := time.Now()
	st := m.stateLocked(res.Account.ID)
	key := modelKey(res.ModelID)
	m.recordRecentFailureLocked(st, key, now, class, msg)
	if opensModelBreaker(class) {
		if ew := st.modelErrors[key]; ew != nil && len(ew.Failures) >= modelBreakerThreshold {
			until := now.Add(modelBreakerCooldown)
			if st.modelBreakers == nil {
				st.modelBreakers = map[string]breakerState{}
			}
			st.modelBreakers[key] = breakerState{Until: until, Reason: string(class)}
			m.recordEventLocked(DebugEvent{
				Time:     now,
				Account:  res.Account.ID,
				Model:    key,
				Class:    class,
				Message:  fmt.Sprintf("model breaker open after %d recent failures", len(ew.Failures)),
				Cooldown: time.Until(until).Milliseconds(),
			})
		}
	}
	m.recordEventLocked(DebugEvent{Time: now, Account: res.Account.ID, Model: key, Class: class, Message: msg})
	m.mu.Unlock()
}

// SelectAccount keeps the old API for smoke helpers; production chat uses Reserve.
func (m *Manager) SelectAccount() (*Account, error) {
	res, err := m.Reserve(context.Background(), "", nil)
	if err != nil {
		return nil, err
	}
	m.Release(res)
	return res.Account, nil
}

func (m *Manager) Snapshot() SchedulerSnapshot {
	accounts, _ := m.GetAllAccounts()
	now := time.Now()
	m.mu.Lock()
	defer m.mu.Unlock()
	events := append([]DebugEvent(nil), m.events...)
	for i := range events {
		events[i].Message = redact.Text(events[i].Message)
	}
	health := m.health
	health.LastError = redact.Text(health.LastError)
	out := SchedulerSnapshot{Accounts: make([]DebugAccount, 0, len(accounts)), Events: events, Health: health, Coordinator: map[string]any{"enabled": false}}
	if m.coord != nil {
		out.Coordinator = m.coord.Snapshot(context.Background())
	}
	for _, a := range accounts {
		st := m.stateLocked(a.ID)
		used := pruneRPM(st, now)
		cds := map[string]string{}
		for k, until := range m.activeDBCooldownsLocked(a.ID, now) {
			st.modelCooldown[k] = until
		}
		for k, until := range st.modelCooldown {
			if until.After(now) {
				cds[k] = until.Format(time.RFC3339)
			}
		}
		if len(cds) == 0 {
			cds = nil
		}
		breakers := map[string]string{}
		for k, br := range st.modelBreakers {
			if br.Until.After(now) {
				breakers[k] = br.Until.Format(time.RFC3339)
			}
		}
		if len(breakers) == 0 {
			breakers = nil
		}
		m.pruneRuntimeHealthLocked(st, now)
		recentErrors := map[string]int{}
		for k, ew := range st.modelErrors {
			if len(ew.Failures) > 0 {
				recentErrors[k] = len(ew.Failures)
			}
		}
		if len(recentErrors) == 0 {
			recentErrors = nil
		}
		rlUntil := ""
		if a.RateLimitedUntil != nil && a.RateLimitedUntil.After(now) {
			rlUntil = a.RateLimitedUntil.Format(time.RFC3339)
		}
		blocked, _ := m.GetBlockedModels(a.ID)
		score := quotaScore(a)
		out.Accounts = append(out.Accounts, DebugAccount{
			ID:                 a.ID,
			Email:              a.Email,
			UserID:             a.UserID,
			ProxyURL:           MaskProxyURL(a.ProxyURL),
			ProxyURLSet:        strings.TrimSpace(a.ProxyURL) != "",
			Tier:               a.Tier,
			PlanName:           a.PlanName,
			ModelConfigCount:   a.ModelConfigCount,
			Enabled:            a.Enabled,
			Banned:             a.Banned,
			Notes:              a.Notes,
			CreatedAt:          a.CreatedAt,
			UpdatedAt:          a.UpdatedAt,
			TokenSet:           strings.TrimSpace(a.FirebaseToken) != "",
			Inflight:           st.inflight,
			RPMUsed:            used,
			RPMLimit:           rpmLimit(a.Tier),
			QuotaDailyPercent:  a.QuotaDailyPercent,
			QuotaWeeklyPercent: a.QuotaWeeklyPercent,
			QuotaDailyResetAt:  formatTimePtr(a.QuotaDailyResetAt),
			QuotaWeeklyResetAt: formatTimePtr(a.QuotaWeeklyResetAt),
			QuotaScore:         score,
			Prompt: CreditSnapshot{
				Limit:     a.PromptLimit,
				Used:      a.PromptUsed,
				Remaining: a.PromptRemaining,
			},
			Flex: CreditSnapshot{
				Limit:     a.FlexLimit,
				Used:      a.FlexUsed,
				Remaining: a.FlexRemaining,
			},
			OverageBalance:   a.OverageBalance,
			PlanStart:        a.PlanStart,
			PlanEnd:          a.PlanEnd,
			HealthCheckedAt:  formatTimePtr(a.HealthCheckedAt),
			Drought:          inDrought(score),
			DroughtPenalty:   droughtPenalty(score),
			RateLimitedUntil: rlUntil,
			ModelCooldowns:   cds,
			ModelBreakers:    breakers,
			RecentErrors:     recentErrors,
			BlockedModels:    blocked,
		})
	}
	return out
}

// GetEnabledAccounts 获取所有启用且未被封禁的账号。
func (m *Manager) GetEnabledAccounts() ([]Account, error) {
	rows, err := m.db.Query(accountSelectSQL + " WHERE enabled = 1 AND banned = 0")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanAccounts(rows)
}

// GetAllAccounts 获取所有账号。
func (m *Manager) GetAllAccounts() ([]Account, error) {
	rows, err := m.db.Query(accountSelectSQL + " ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanAccounts(rows)
}

const accountSelectSQL = `SELECT id, email, firebase_token, user_id, proxy_url, tier, plan_name, model_config_count, rate_limited_until, quota_daily_percent, quota_weekly_percent, quota_daily_reset_at, quota_weekly_reset_at, prompt_limit, prompt_used, prompt_remaining, flex_limit, flex_used, flex_remaining, overage_balance, plan_start, plan_end, health_checked_at, last_used_at, enabled, banned, notes, created_at, updated_at FROM accounts`

// GetAccount 获取单个账号。
func (m *Manager) GetAccount(id int) (*Account, error) {
	row := m.db.QueryRow(accountSelectSQL+" WHERE id = ?", id)
	a := &Account{}
	err := scanAccount(row, a)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return a, nil
}

// AddAccount 添加账号。
func (m *Manager) AddAccount(email, firebaseToken, userID, proxyURL, notes string) (int64, error) {
	result, err := m.db.Exec(
		"INSERT INTO accounts (email, firebase_token, user_id, proxy_url, tier, notes) VALUES (?, ?, ?, ?, 'unknown', ?)",
		email, firebaseToken, userID, proxyURL, notes,
	)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

// UpsertAccount imports or refreshes an account by email while preserving the
// raw token semantics used by the LS metadata field.
func (m *Manager) UpsertAccount(email, firebaseToken, userID, proxyURL, notes string) error {
	if userID == "" {
		userID = email
	}
	_, err := m.db.Exec(
		`INSERT INTO accounts (email, firebase_token, user_id, proxy_url, tier, notes, enabled, banned)
		 VALUES (?, ?, ?, ?, 'unknown', ?, 1, 0)
		 ON CONFLICT(email) DO UPDATE SET
		   firebase_token = excluded.firebase_token,
		   user_id = excluded.user_id,
		   proxy_url = excluded.proxy_url,
		   enabled = 1,
		   banned = 0,
		   notes = excluded.notes,
		   updated_at = CURRENT_TIMESTAMP`,
		email, firebaseToken, userID, proxyURL, notes,
	)
	return err
}

// UpdateAccount 更新账号字段。
func (m *Manager) UpdateAccount(id int, fields map[string]interface{}) error {
	allowed := map[string]bool{
		"email": true, "firebase_token": true, "user_id": true,
		"proxy_url": true, "tier": true, "plan_name": true, "model_config_count": true, "rate_limited_until": true,
		"quota_daily_percent": true, "quota_weekly_percent": true,
		"quota_daily_reset_at": true, "quota_weekly_reset_at": true,
		"prompt_limit": true, "prompt_used": true, "prompt_remaining": true,
		"flex_limit": true, "flex_used": true, "flex_remaining": true,
		"overage_balance": true, "plan_start": true, "plan_end": true,
		"health_checked_at": true, "last_used_at": true,
		"enabled": true, "banned": true, "notes": true,
	}
	var setClauses []string
	var args []interface{}
	for k, v := range fields {
		if !allowed[k] {
			continue
		}
		setClauses = append(setClauses, k+" = ?")
		args = append(args, v)
	}
	if len(setClauses) == 0 {
		return nil
	}
	setClauses = append(setClauses, "updated_at = CURRENT_TIMESTAMP")
	args = append(args, id)

	query := fmt.Sprintf("UPDATE accounts SET %s WHERE id = ?", strings.Join(setClauses, ", "))
	_, err := m.db.Exec(query, args...)
	return err
}

// DeleteAccount 删除账号。
func (m *Manager) DeleteAccount(id int) error {
	m.db.Exec("DELETE FROM account_models WHERE account_id = ?", id)
	_, err := m.db.Exec("DELETE FROM accounts WHERE id = ?", id)
	return err
}

// MarkBanned 标记账号被封。
func (m *Manager) MarkBanned(id int) error {
	_, err := m.db.Exec("UPDATE accounts SET banned = 1, enabled = 0, updated_at = CURRENT_TIMESTAMP WHERE id = ?", id)
	m.mu.Lock()
	m.recordEventLocked(DebugEvent{Time: time.Now(), Account: id, Class: ErrorBanSignal, Message: "account disabled"})
	m.mu.Unlock()
	return err
}

func (m *Manager) UpdateHealth(accountID int, tier string, dailyPercent, weeklyPercent *float64, rateLimitedUntil *time.Time, note string) error {
	return m.UpdateHealthDetails(accountID, HealthUpdate{
		Tier:             tier,
		DailyPercent:     dailyPercent,
		WeeklyPercent:    weeklyPercent,
		RateLimitedUntil: rateLimitedUntil,
		HealthCheckedAt:  time.Now(),
		Note:             note,
	})
}

func (m *Manager) UpdateHealthDetails(accountID int, update HealthUpdate) error {
	fields := map[string]interface{}{}
	if strings.TrimSpace(update.Tier) != "" {
		fields["tier"] = strings.ToLower(strings.TrimSpace(update.Tier))
	}
	if strings.TrimSpace(update.PlanName) != "" {
		fields["plan_name"] = strings.TrimSpace(update.PlanName)
	}
	if update.DailyPercent != nil {
		fields["quota_daily_percent"] = *update.DailyPercent
	}
	if update.WeeklyPercent != nil {
		fields["quota_weekly_percent"] = *update.WeeklyPercent
	}
	fields["quota_daily_reset_at"] = nullableTime(update.DailyResetAt)
	fields["quota_weekly_reset_at"] = nullableTime(update.WeeklyResetAt)
	fields["prompt_limit"] = nullableFloat(update.PromptLimit)
	fields["prompt_used"] = nullableFloat(update.PromptUsed)
	fields["prompt_remaining"] = nullableFloat(update.PromptRemaining)
	fields["flex_limit"] = nullableFloat(update.FlexLimit)
	fields["flex_used"] = nullableFloat(update.FlexUsed)
	fields["flex_remaining"] = nullableFloat(update.FlexRemaining)
	fields["overage_balance"] = nullableFloat(update.OverageBalance)
	fields["plan_start"] = strings.TrimSpace(update.PlanStart)
	fields["plan_end"] = strings.TrimSpace(update.PlanEnd)
	fields["model_config_count"] = update.ModelConfigCount
	if !update.HealthCheckedAt.IsZero() {
		fields["health_checked_at"] = update.HealthCheckedAt
	} else {
		fields["health_checked_at"] = time.Now()
	}
	if update.RateLimitedUntil != nil {
		fields["rate_limited_until"] = *update.RateLimitedUntil
	} else {
		fields["rate_limited_until"] = nil
	}
	if strings.TrimSpace(update.Note) != "" {
		fields["notes"] = update.Note
	}
	if err := m.UpdateAccount(accountID, fields); err != nil {
		return err
	}
	m.mu.Lock()
	st := m.stateLocked(accountID)
	if update.RateLimitedUntil != nil && update.RateLimitedUntil.After(time.Now()) {
		st.modelCooldown["*"] = *update.RateLimitedUntil
	} else {
		delete(st.modelCooldown, "*")
	}
	m.recordEventLocked(DebugEvent{Time: time.Now(), Account: accountID, Message: "health ok"})
	m.mu.Unlock()
	return nil
}

func (m *Manager) ClearCooldown(accountID int, modelName string) error {
	key := strings.TrimSpace(modelName)
	if key == "" {
		key = "*"
	}
	if key == "*" {
		if _, err := m.db.Exec("UPDATE accounts SET rate_limited_until = NULL, updated_at = CURRENT_TIMESTAMP WHERE id = ?", accountID); err != nil {
			return err
		}
	}
	if _, err := m.db.Exec("UPDATE account_models SET cooldown_until = NULL WHERE account_id = ? AND model_name = ?", accountID, key); err != nil {
		return err
	}
	m.mu.Lock()
	st := m.stateLocked(accountID)
	delete(st.modelCooldown, key)
	coord := m.coord
	m.recordEventLocked(DebugEvent{Time: time.Now(), Account: accountID, Model: key, Message: "cooldown cleared"})
	m.mu.Unlock()
	if coord != nil {
		coord.ClearCooldown(context.Background(), accountID, key)
	}
	return nil
}

func (m *Manager) ClearModelBreaker(accountID int, modelName string) {
	key := modelKey(modelName)
	m.mu.Lock()
	defer m.mu.Unlock()
	st := m.stateLocked(accountID)
	delete(st.modelBreakers, key)
	if st.modelErrors != nil {
		delete(st.modelErrors, key)
	}
	m.recordEventLocked(DebugEvent{Time: time.Now(), Account: accountID, Model: key, Message: "model breaker cleared"})
}

func (m *Manager) ClearModelBreakers(modelName string) int {
	key := modelKey(modelName)
	m.mu.Lock()
	defer m.mu.Unlock()
	cleared := 0
	for accountID, st := range m.states {
		if _, ok := st.modelBreakers[key]; ok {
			delete(st.modelBreakers, key)
			cleared++
		}
		if st.modelErrors != nil {
			delete(st.modelErrors, key)
		}
		m.recordEventLocked(DebugEvent{Time: time.Now(), Account: accountID, Model: key, Message: "model breaker cleared"})
	}
	return cleared
}

func (m *Manager) PruneAvailability() int {
	now := time.Now()
	m.mu.Lock()
	pruned := 0
	for _, st := range m.states {
		beforeCooldowns := len(st.modelCooldown)
		for key, until := range st.modelCooldown {
			if !until.After(now) {
				delete(st.modelCooldown, key)
			}
		}
		beforeBreakers := len(st.modelBreakers)
		for key, br := range st.modelBreakers {
			if !br.Until.After(now) {
				delete(st.modelBreakers, key)
			}
		}
		beforeErrors := len(st.modelErrors)
		m.pruneRuntimeHealthLocked(st, now)
		pruned += beforeCooldowns - len(st.modelCooldown)
		pruned += beforeBreakers - len(st.modelBreakers)
		pruned += beforeErrors - len(st.modelErrors)
	}
	m.recordEventLocked(DebugEvent{Time: now, Message: "availability pruned"})
	m.mu.Unlock()
	if _, err := m.db.Exec("UPDATE account_models SET cooldown_until = NULL WHERE cooldown_until IS NOT NULL AND cooldown_until <= datetime('now')"); err == nil {
		pruned++
	}
	if _, err := m.db.Exec("UPDATE accounts SET rate_limited_until = NULL WHERE rate_limited_until IS NOT NULL AND rate_limited_until <= datetime('now')"); err == nil {
		pruned++
	}
	return pruned
}

func (m *Manager) RecordHealthSummary(summary HealthSummary) {
	summary.LastError = redact.Text(summary.LastError)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.health = summary
}

// SetCooldown 设置账号模型冷却。
func (m *Manager) SetCooldown(accountID int, modelName string, until time.Time) error {
	_, err := m.db.Exec(
		`INSERT INTO account_models (account_id, model_name, cooldown_until)
		 VALUES (?, ?, ?)
		 ON CONFLICT(account_id, model_name) DO UPDATE SET cooldown_until = ?`,
		accountID, modelName, until, until,
	)
	return err
}

// IsInCooldown 检查模型是否在冷却中。
func (m *Manager) IsInCooldown(accountID int, modelName string) (bool, error) {
	var count int
	err := m.db.QueryRow(
		"SELECT COUNT(*) FROM account_models WHERE account_id = ? AND model_name = ? AND cooldown_until > datetime('now')",
		accountID, modelName,
	).Scan(&count)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

func (m *Manager) modelDisabledLocked(accountID int, modelName string) bool {
	if strings.TrimSpace(modelName) == "" {
		return false
	}
	var count int
	err := m.db.QueryRow("SELECT COUNT(*) FROM account_models WHERE account_id = ? AND model_name = ? AND enabled = 0", accountID, modelName).Scan(&count)
	return err == nil && count > 0
}

func (m *Manager) SetBlockedModels(accountID int, models []string) error {
	tx, err := m.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec("DELETE FROM account_models WHERE account_id = ? AND enabled = 0", accountID); err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, model := range models {
		model = strings.TrimSpace(model)
		if model == "" || seen[model] {
			continue
		}
		seen[model] = true
		if _, err := tx.Exec(
			`INSERT INTO account_models (account_id, model_name, enabled)
			 VALUES (?, ?, 0)
			 ON CONFLICT(account_id, model_name) DO UPDATE SET enabled = 0`,
			accountID, model,
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (m *Manager) GetBlockedModels(accountID int) ([]string, error) {
	rows, err := m.db.Query("SELECT model_name FROM account_models WHERE account_id = ? AND enabled = 0 ORDER BY model_name", accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var model string
		if err := rows.Scan(&model); err != nil {
			return nil, err
		}
		out = append(out, model)
	}
	return out, rows.Err()
}

func (m *Manager) stateLocked(id int) *runtimeState {
	st := m.states[id]
	if st == nil {
		st = &runtimeState{
			modelCooldown: map[string]time.Time{},
			modelErrors:   map[string]*modelErrorState{},
			modelBreakers: map[string]breakerState{},
		}
		m.states[id] = st
	}
	if st.modelCooldown == nil {
		st.modelCooldown = map[string]time.Time{}
	}
	if st.modelErrors == nil {
		st.modelErrors = map[string]*modelErrorState{}
	}
	if st.modelBreakers == nil {
		st.modelBreakers = map[string]breakerState{}
	}
	return st
}

func (m *Manager) applyDBCooldownLocked(st *runtimeState, modelID string, a Account) {
	if a.RateLimitedUntil != nil && a.RateLimitedUntil.After(time.Now()) {
		st.modelCooldown["*"] = *a.RateLimitedUntil
	}
	if modelID == "" {
		return
	}
	var until sql.NullTime
	err := m.db.QueryRow("SELECT cooldown_until FROM account_models WHERE account_id = ? AND model_name = ? AND cooldown_until > datetime('now')", a.ID, modelID).Scan(&until)
	if err == nil && until.Valid {
		st.modelCooldown[modelID] = until.Time
	}
}

func (m *Manager) activeDBCooldownsLocked(accountID int, now time.Time) map[string]time.Time {
	rows, err := m.db.Query(
		`SELECT model_name, cooldown_until
		   FROM account_models
		  WHERE account_id = ?
		    AND cooldown_until IS NOT NULL
		    AND cooldown_until > datetime('now')`,
		accountID,
	)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := map[string]time.Time{}
	for rows.Next() {
		var model string
		var until sql.NullTime
		if err := rows.Scan(&model, &until); err != nil {
			continue
		}
		if !until.Valid || !until.Time.After(now) {
			continue
		}
		model = strings.TrimSpace(model)
		if model == "" {
			model = "*"
		}
		out[model] = until.Time
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func (m *Manager) persistLastUsedLocked(id int, ts time.Time) {
	_, _ = m.db.Exec("UPDATE accounts SET last_used_at = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?", ts, id)
}

func (m *Manager) recordEventLocked(ev DebugEvent) {
	if ev.Time.IsZero() {
		ev.Time = time.Now()
	}
	ev.Message = redact.Text(ev.Message)
	m.events = append(m.events, ev)
	if len(m.events) > 200 {
		m.events = append([]DebugEvent(nil), m.events[len(m.events)-200:]...)
	}
}

func inCooldown(st *runtimeState, modelID string, now time.Time) bool {
	if st == nil {
		return false
	}
	if until, ok := st.modelCooldown["*"]; ok && until.After(now) {
		return true
	}
	if modelID != "" {
		if until, ok := st.modelCooldown[modelID]; ok && until.After(now) {
			return true
		}
	}
	return false
}

func inBreaker(st *runtimeState, modelID string, now time.Time) bool {
	if st == nil {
		return false
	}
	if br, ok := st.modelBreakers["*"]; ok && br.Until.After(now) {
		return true
	}
	if modelID != "" {
		if br, ok := st.modelBreakers[modelID]; ok && br.Until.After(now) {
			return true
		}
	}
	return false
}

func (m *Manager) recordRecentFailureLocked(st *runtimeState, key string, now time.Time, class ErrorClass, msg string) {
	msg = redact.Text(msg)
	if st.modelErrors == nil {
		st.modelErrors = map[string]*modelErrorState{}
	}
	ew := st.modelErrors[key]
	if ew == nil {
		ew = &modelErrorState{}
		st.modelErrors[key] = ew
	}
	ew.Failures = append(ew.Failures, failureRecord{At: now, Class: class, Message: msg})
	ew.LastClass = class
	ew.LastError = msg
	pruneErrorWindow(ew, now)
}

func (m *Manager) pruneRuntimeHealthLocked(st *runtimeState, now time.Time) {
	for k, ew := range st.modelErrors {
		pruneErrorWindow(ew, now)
		if len(ew.Failures) == 0 {
			delete(st.modelErrors, k)
		}
	}
	for k, br := range st.modelBreakers {
		if !br.Until.After(now) {
			delete(st.modelBreakers, k)
		}
	}
}

func pruneErrorWindow(ew *modelErrorState, now time.Time) {
	if ew == nil {
		return
	}
	cutoff := now.Add(-recentErrorWindow)
	n := 0
	for _, item := range ew.Failures {
		if item.At.After(cutoff) {
			ew.Failures[n] = item
			n++
		}
	}
	ew.Failures = ew.Failures[:n]
}

func opensModelBreaker(class ErrorClass) bool {
	switch class {
	case ErrorModelNotAvailable, ErrorUpstreamTransient, ErrorTransport:
		return true
	default:
		return false
	}
}

func coordinatorRetryAfter(reason string) time.Duration {
	reason = strings.ToLower(strings.TrimSpace(reason))
	if strings.Contains(reason, "inflight_full") {
		return queueRetryEvery
	}
	return 0
}

func rateLimited(a Account, now time.Time) bool {
	return a.RateLimitedUntil != nil && a.RateLimitedUntil.After(now)
}

func rpmLimit(tier string) int {
	tier = strings.ToLower(strings.TrimSpace(tier))
	if tier == "" {
		tier = "unknown"
	}
	if v, ok := tierRPM[tier]; ok {
		return v
	}
	return tierRPM["unknown"]
}

func pruneRPM(st *runtimeState, now time.Time) int {
	if st == nil {
		return 0
	}
	cutoff := now.Add(-rpmWindow)
	n := 0
	for _, ts := range st.rpmHistory {
		if ts.After(cutoff) {
			st.rpmHistory[n] = ts
			n++
		}
	}
	st.rpmHistory = st.rpmHistory[:n]
	return len(st.rpmHistory)
}

func quotaScore(a Account) float64 {
	daily := float64(unknownQuotaScore)
	weekly := float64(unknownQuotaScore)
	if a.QuotaDailyPercent != nil {
		daily = *a.QuotaDailyPercent
	}
	if a.QuotaWeeklyPercent != nil {
		weekly = *a.QuotaWeeklyPercent
	}
	if daily < weekly {
		return clampScore(daily)
	}
	return clampScore(weekly)
}

func clampScore(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return v
}

func bucket(v float64) int {
	return int(v / 5)
}

func modelKey(modelID string) string {
	if strings.TrimSpace(modelID) == "" {
		return "*"
	}
	return strings.TrimSpace(modelID)
}

func inDrought(score float64) bool {
	return score < droughtQuotaThreshold
}

func droughtPenalty(score float64) int {
	switch {
	case score <= 0:
		return 4
	case score < severeDroughtThreshold:
		return 3
	case score < droughtQuotaThreshold:
		return 2
	default:
		return 0
	}
}

func modelUnavailableByHealth(a Account, modelID string) bool {
	if a.ModelConfigCount <= 0 {
		return false
	}
	modelID = strings.ToLower(strings.TrimSpace(modelID))
	if modelID == "" {
		return false
	}
	if strings.Contains(modelID, "claude-opus-4-7") ||
		strings.Contains(modelID, "claude-opus-4.6") ||
		strings.Contains(modelID, "claude-opus-4-6") ||
		strings.Contains(modelID, "claude-sonnet-4.6") ||
		strings.Contains(modelID, "claude-sonnet-4-6") {
		return a.ModelConfigCount < 100
	}
	return false
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanAccounts(rows *sql.Rows) ([]Account, error) {
	var accounts []Account
	for rows.Next() {
		var a Account
		if err := scanAccount(rows, &a); err != nil {
			return nil, err
		}
		accounts = append(accounts, a)
	}
	return accounts, rows.Err()
}

func scanAccount(row rowScanner, a *Account) error {
	var tier, planName, planStart, planEnd sql.NullString
	var rateLimited, dailyReset, weeklyReset, healthChecked, lastUsed sql.NullTime
	var daily, weekly, promptLimit, promptUsed, promptRemaining, flexLimit, flexUsed, flexRemaining, overage sql.NullFloat64
	var modelConfigCount sql.NullInt64
	err := row.Scan(
		&a.ID, &a.Email, &a.FirebaseToken, &a.UserID, &a.ProxyURL, &tier, &planName, &modelConfigCount, &rateLimited,
		&daily, &weekly, &dailyReset, &weeklyReset,
		&promptLimit, &promptUsed, &promptRemaining, &flexLimit, &flexUsed, &flexRemaining, &overage,
		&planStart, &planEnd, &healthChecked, &lastUsed, &a.Enabled, &a.Banned, &a.Notes, &a.CreatedAt, &a.UpdatedAt,
	)
	if err != nil {
		return err
	}
	if tier.Valid && tier.String != "" {
		a.Tier = tier.String
	} else {
		a.Tier = "unknown"
	}
	if rateLimited.Valid {
		a.RateLimitedUntil = &rateLimited.Time
	}
	if planName.Valid {
		a.PlanName = planName.String
	}
	if modelConfigCount.Valid {
		a.ModelConfigCount = int(modelConfigCount.Int64)
	}
	if daily.Valid {
		a.QuotaDailyPercent = &daily.Float64
	}
	if weekly.Valid {
		a.QuotaWeeklyPercent = &weekly.Float64
	}
	if dailyReset.Valid {
		a.QuotaDailyResetAt = &dailyReset.Time
	}
	if weeklyReset.Valid {
		a.QuotaWeeklyResetAt = &weeklyReset.Time
	}
	if promptLimit.Valid {
		a.PromptLimit = &promptLimit.Float64
	}
	if promptUsed.Valid {
		a.PromptUsed = &promptUsed.Float64
	}
	if promptRemaining.Valid {
		a.PromptRemaining = &promptRemaining.Float64
	}
	if flexLimit.Valid {
		a.FlexLimit = &flexLimit.Float64
	}
	if flexUsed.Valid {
		a.FlexUsed = &flexUsed.Float64
	}
	if flexRemaining.Valid {
		a.FlexRemaining = &flexRemaining.Float64
	}
	if overage.Valid {
		a.OverageBalance = &overage.Float64
	}
	if planStart.Valid {
		a.PlanStart = planStart.String
	}
	if planEnd.Valid {
		a.PlanEnd = planEnd.String
	}
	if healthChecked.Valid {
		a.HealthCheckedAt = &healthChecked.Time
	}
	if lastUsed.Valid {
		a.LastUsedAt = &lastUsed.Time
	}
	return nil
}

func nullableFloat(v *float64) any {
	if v == nil {
		return nil
	}
	return *v
}

func nullableTime(v *time.Time) any {
	if v == nil || v.IsZero() {
		return nil
	}
	return *v
}

func formatTimePtr(v *time.Time) string {
	if v == nil || v.IsZero() {
		return ""
	}
	return v.Format(time.RFC3339)
}
