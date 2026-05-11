package proxy

import (
	"context"
	"crypto/rand"
	"crypto/sha1"
	"crypto/tls"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/zhangyu/windsurfapi-go/internal/redact"
)

type Config struct {
	Default           string
	Dynamic           []string
	RotateOnError     bool
	TestURL           string
	Cooldown          time.Duration
	HTTPClient        *http.Client
	DB                *sql.DB
	AllowPrivate      bool
	AccountBinding    bool
	AutoBindNew       bool
	RenewBefore       time.Duration
	MaxBindRetries    int
	WorkerInterval    time.Duration
	WorkerBatchSize   int
	WorkerConcurrency int
	Provider          string
	Protocol          string
	Host              string
	Port              int
	UsernameTemplate  string
	Password          string
	Region            string
	State             string
	TTLMinutes        int
}

type Manager struct {
	mu                sync.Mutex
	db                *sql.DB
	defaultURL        string
	rotateOnError     bool
	testURL           string
	cooldown          time.Duration
	timeout           time.Duration
	allowPrivate      bool
	accountBinding    bool
	autoBindNew       bool
	renewBefore       time.Duration
	maxBindRetries    int
	workerInterval    time.Duration
	workerBatchSize   int
	workerConcurrency int
	provider          string
	protocol          string
	host              string
	port              int
	usernameTemplate  string
	password          string
	region            string
	state             string
	ttlMinutes        int
	items             map[string]*Entry
	order             []string
	next              int
}

type Entry struct {
	ID                string    `json:"id"`
	URL               string    `json:"url"`
	MaskedURL         string    `json:"masked_url"`
	Enabled           bool      `json:"enabled"`
	Inflight          int       `json:"inflight"`
	Successes         int       `json:"successes"`
	Failures          int       `json:"failures"`
	LastError         string    `json:"last_error,omitempty"`
	LastTestStatus    string    `json:"last_test_status,omitempty"`
	LastTestLatencyMS int64     `json:"last_test_latency_ms,omitempty"`
	LastTestAt        time.Time `json:"last_test_at,omitempty"`
	CooldownUntil     time.Time `json:"cooldown_until,omitempty"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}

type Reservation struct {
	ProxyURL  string
	ID        string
	dynamic   bool
	accountID int
	binding   bool
}

type Snapshot struct {
	Enabled             bool             `json:"enabled"`
	Persistent          bool             `json:"persistent"`
	Default             string           `json:"default"`
	RotateOnError       bool             `json:"rotate_on_error"`
	TestURL             string           `json:"test_url"`
	AccountBinding      bool             `json:"account_binding"`
	AutoBindNewAccounts bool             `json:"auto_bind_new_accounts"`
	RenewBeforeMS       int64            `json:"renew_before_ms"`
	MaxBindRetries      int              `json:"max_bind_retries"`
	WorkerIntervalMS    int64            `json:"worker_interval_ms"`
	WorkerBatchSize     int              `json:"worker_batch_size"`
	WorkerConcurrency   int              `json:"worker_concurrency"`
	Provider            string           `json:"provider"`
	Protocol            string           `json:"protocol"`
	Host                string           `json:"host"`
	Port                int              `json:"port"`
	UsernameTemplate    string           `json:"username_template"`
	PasswordSet         bool             `json:"password_set"`
	Region              string           `json:"region"`
	State               string           `json:"state"`
	TTLMinutes          int              `json:"ttl_minutes"`
	Entries             []Entry          `json:"entries"`
	Bindings            []AccountBinding `json:"bindings"`
	Summary             BindingSummary   `json:"summary"`
}

const (
	BindingActive    = "active"
	BindingVerifying = "verifying"
	BindingRotating  = "rotating"
	BindingFailed    = "failed"
	BindingExpired   = "expired"
	BindingSuspended = "suspended"
)

type AccountBinding struct {
	AccountID      int       `json:"account_id"`
	Provider       string    `json:"provider"`
	Protocol       string    `json:"protocol"`
	Host           string    `json:"host"`
	Port           int       `json:"port"`
	Username       string    `json:"username,omitempty"`
	Password       string    `json:"-"`
	SessionID      string    `json:"session_id,omitempty"`
	EgressIP       string    `json:"egress_ip,omitempty"`
	Country        string    `json:"country,omitempty"`
	Region         string    `json:"region,omitempty"`
	City           string    `json:"city,omitempty"`
	ISPOrg         string    `json:"isp_org,omitempty"`
	Status         string    `json:"status"`
	ExpiresAt      time.Time `json:"expires_at,omitempty"`
	RemainingMS    int64     `json:"remaining_ms"`
	LastVerifiedAt time.Time `json:"last_verified_at,omitempty"`
	VerifyError    string    `json:"verify_error,omitempty"`
	FailCount      int       `json:"fail_count"`
	HasPassword    bool      `json:"has_password"`
	MaskedURL      string    `json:"masked_url,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

type BindingSummary struct {
	Bound        int `json:"bound"`
	ExpiringSoon int `json:"expiring_soon"`
	Failed       int `json:"failed"`
	Suspended    int `json:"suspended"`
	Unbound      int `json:"unbound"`
}

type BindingResult struct {
	Success  bool            `json:"success"`
	Binding  *AccountBinding `json:"binding,omitempty"`
	Attempts int             `json:"attempts,omitempty"`
	Error    string          `json:"error,omitempty"`
}

type WorkerPlanItem struct {
	AccountID int    `json:"account_id"`
	Reason    string `json:"reason"`
	Priority  int    `json:"priority"`
}

type AccountRef struct {
	ID      int
	Active  bool
	Enabled bool
	Banned  bool
}

type VerifyInfo struct {
	EgressIP  string
	Country   string
	Region    string
	City      string
	ISPOrg    string
	LatencyMS int64
}

func NewManager(cfg Config) *Manager {
	if cfg.Cooldown <= 0 {
		cfg.Cooldown = 2 * time.Minute
	}
	if cfg.RenewBefore <= 0 {
		cfg.RenewBefore = 15 * time.Minute
	}
	if cfg.MaxBindRetries <= 0 {
		cfg.MaxBindRetries = 3
	}
	if cfg.WorkerInterval <= 0 {
		cfg.WorkerInterval = time.Minute
	}
	if cfg.WorkerBatchSize <= 0 {
		cfg.WorkerBatchSize = 20
	}
	if cfg.WorkerConcurrency <= 0 {
		cfg.WorkerConcurrency = 3
	}
	if strings.TrimSpace(cfg.TestURL) == "" {
		cfg.TestURL = "https://ipinfo.io/json"
	}
	cfg.Provider = firstNonEmpty(cfg.Provider, "novproxy")
	cfg.Protocol = normalizeProtocol(cfg.Protocol)
	cfg.Host = firstNonEmpty(cfg.Host, "us.novproxy.io")
	if cfg.Port <= 0 {
		cfg.Port = 1000
	}
	cfg.UsernameTemplate = firstNonEmpty(cfg.UsernameTemplate, "nfgr68136-region-{region}-st-{state}-sid-{sid}-t-{ttl}")
	cfg.Region = firstNonEmpty(cfg.Region, "US")
	cfg.State = firstNonEmpty(cfg.State, "New Jersey")
	if cfg.TTLMinutes <= 0 {
		cfg.TTLMinutes = 120
	}
	timeout := 10 * time.Second
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: timeout}
	}
	if client.Timeout > 0 {
		timeout = client.Timeout
	}
	m := &Manager{
		db:                cfg.DB,
		defaultURL:        strings.TrimSpace(cfg.Default),
		rotateOnError:     cfg.RotateOnError,
		testURL:           strings.TrimSpace(cfg.TestURL),
		cooldown:          cfg.Cooldown,
		timeout:           timeout,
		allowPrivate:      cfg.AllowPrivate,
		accountBinding:    cfg.AccountBinding,
		autoBindNew:       cfg.AutoBindNew,
		renewBefore:       cfg.RenewBefore,
		maxBindRetries:    cfg.MaxBindRetries,
		workerInterval:    cfg.WorkerInterval,
		workerBatchSize:   cfg.WorkerBatchSize,
		workerConcurrency: cfg.WorkerConcurrency,
		provider:          strings.ToLower(strings.TrimSpace(cfg.Provider)),
		protocol:          cfg.Protocol,
		host:              strings.TrimSpace(cfg.Host),
		port:              cfg.Port,
		usernameTemplate:  strings.TrimSpace(cfg.UsernameTemplate),
		password:          cfg.Password,
		region:            strings.TrimSpace(cfg.Region),
		state:             strings.TrimSpace(cfg.State),
		ttlMinutes:        cfg.TTLMinutes,
		items:             map[string]*Entry{},
	}
	if cfg.DB != nil {
		_ = m.load()
	}
	for _, raw := range cfg.Dynamic {
		_, _ = m.Add(raw)
	}
	return m
}

func (m *Manager) Reserve(accountProxy string) Reservation {
	if m == nil {
		return Reservation{ProxyURL: strings.TrimSpace(accountProxy)}
	}
	accountProxy = strings.TrimSpace(accountProxy)
	if accountProxy != "" {
		return Reservation{ProxyURL: accountProxy}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	if len(m.order) > 0 {
		for i := 0; i < len(m.order); i++ {
			idx := (m.next + i) % len(m.order)
			item := m.items[m.order[idx]]
			if item == nil || !item.Enabled || item.CooldownUntil.After(now) {
				continue
			}
			m.next = (idx + 1) % len(m.order)
			item.Inflight++
			item.UpdatedAt = now
			return Reservation{ProxyURL: item.URL, ID: item.ID, dynamic: true}
		}
	}
	return Reservation{ProxyURL: m.defaultURL}
}

func (m *Manager) ReserveForAccount(accountID int, accountProxy string) Reservation {
	if m == nil {
		return Reservation{ProxyURL: strings.TrimSpace(accountProxy)}
	}
	if binding := m.ActiveBinding(accountID); binding != nil {
		return Reservation{ProxyURL: binding.proxyURL(), accountID: accountID, binding: true}
	}
	res := m.Reserve(accountProxy)
	res.accountID = accountID
	return res
}

func (m *Manager) EffectiveProxyURL(accountID int, accountProxy string) string {
	if m == nil {
		return strings.TrimSpace(accountProxy)
	}
	if binding := m.ActiveBinding(accountID); binding != nil {
		return binding.proxyURL()
	}
	if accountProxy = strings.TrimSpace(accountProxy); accountProxy != "" {
		return accountProxy
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.defaultURL
}

func (m *Manager) SetDefault(raw string) error {
	if m == nil {
		return nil
	}
	raw = strings.TrimSpace(raw)
	if raw != "" {
		if err := validateProxyURL(raw, m.allowPrivate); err != nil {
			return err
		}
	}
	m.mu.Lock()
	m.defaultURL = raw
	m.mu.Unlock()
	return nil
}

func (m *Manager) Default() string {
	if m == nil {
		return ""
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.defaultURL
}

func (m *Manager) URL(id string) (string, bool) {
	if m == nil {
		return "", false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	item := m.items[strings.TrimSpace(id)]
	if item == nil {
		return "", false
	}
	return item.URL, true
}

func (m *Manager) Release(res Reservation) {
	if m == nil {
		return
	}
	if res.binding {
		return
	}
	if !res.dynamic || res.ID == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if item := m.items[res.ID]; item != nil {
		if item.Inflight > 0 {
			item.Inflight--
		}
		item.UpdatedAt = time.Now()
	}
}

func (m *Manager) RecordSuccess(res Reservation) {
	if m == nil {
		return
	}
	if res.binding {
		_ = m.markBindingSuccess(res.accountID)
		return
	}
	if !res.dynamic || res.ID == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if item := m.items[res.ID]; item != nil {
		item.Successes++
		item.LastError = ""
		item.UpdatedAt = time.Now()
		_ = m.persistEntryLocked(item)
	}
}

func (m *Manager) RecordFailure(res Reservation, err error) {
	if m == nil {
		return
	}
	if res.binding {
		_, _ = m.MarkBindingFailure(res.accountID, err, true)
		return
	}
	if !res.dynamic || res.ID == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if item := m.items[res.ID]; item != nil {
		item.Failures++
		if err != nil {
			item.LastError = redact.Text(err.Error())
		}
		if m.rotateOnError {
			item.CooldownUntil = time.Now().Add(m.cooldown)
		}
		item.UpdatedAt = time.Now()
		_ = m.persistEntryLocked(item)
	}
}

func (m *Manager) Add(raw string) (*Entry, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, fmt.Errorf("proxy url required")
	}
	if err := validateProxyURL(raw, m.allowPrivate); err != nil {
		return nil, err
	}
	id := ID(raw)
	now := time.Now()
	m.mu.Lock()
	defer m.mu.Unlock()
	if item := m.items[id]; item != nil {
		item.URL = raw
		item.MaskedURL = Mask(raw)
		item.Enabled = true
		item.UpdatedAt = now
		if err := m.persistEntryLocked(item); err != nil {
			return nil, err
		}
		return cloneEntry(item), nil
	}
	item := &Entry{ID: id, URL: raw, MaskedURL: Mask(raw), Enabled: true, CreatedAt: now, UpdatedAt: now}
	m.items[id] = item
	m.order = append(m.order, id)
	if err := m.persistEntryLocked(item); err != nil {
		delete(m.items, id)
		m.order = m.order[:len(m.order)-1]
		return nil, err
	}
	return cloneEntry(item), nil
}

// GenerateProviderProxy creates a provider-specific proxy URL and adds it to
// the dynamic pool. The first supported provider is novproxy because that is
// what the Node dashboard's dynamic proxy flow used.
func (m *Manager) GenerateProviderProxy() (*Entry, string, error) {
	if m == nil {
		return nil, "", fmt.Errorf("proxy manager unavailable")
	}
	m.mu.Lock()
	provider := m.provider
	protocol := m.protocol
	host := m.host
	port := m.port
	tpl := m.usernameTemplate
	password := m.password
	region := m.region
	state := m.state
	ttl := m.ttlMinutes
	m.mu.Unlock()
	if provider == "" || provider == "novproxy" {
		raw, err := buildNovproxyURL(protocol, host, port, tpl, password, region, state, ttl)
		if err != nil {
			return nil, "", err
		}
		item, err := m.Add(raw)
		return item, raw, err
	}
	return nil, "", fmt.Errorf("unsupported dynamic proxy provider: %s", provider)
}

func (m *Manager) generatedBinding(accountID int, status string) (*AccountBinding, error) {
	if m == nil {
		return nil, fmt.Errorf("proxy manager unavailable")
	}
	m.mu.Lock()
	provider := m.provider
	protocol := m.protocol
	host := m.host
	port := m.port
	tpl := m.usernameTemplate
	password := m.password
	region := m.region
	state := m.state
	ttl := m.ttlMinutes
	m.mu.Unlock()
	if provider != "" && provider != "novproxy" {
		return nil, fmt.Errorf("unsupported dynamic proxy provider: %s", provider)
	}
	b, err := buildNovproxyBinding(accountID, protocol, host, port, tpl, password, region, state, ttl)
	if err != nil {
		return nil, err
	}
	b.Provider = firstNonEmpty(provider, "novproxy")
	b.Status = firstNonEmpty(status, BindingVerifying)
	now := time.Now()
	b.CreatedAt = now
	b.UpdatedAt = now
	return b, nil
}

func (m *Manager) BindAccount(ctx context.Context, accountID int, force bool) (*BindingResult, error) {
	return m.bindAccount(ctx, accountID, force, false)
}

func (m *Manager) RotateAccount(ctx context.Context, accountID int, force bool) (*BindingResult, error) {
	return m.bindAccount(ctx, accountID, force, true)
}

func (m *Manager) bindAccount(ctx context.Context, accountID int, force bool, rotating bool) (*BindingResult, error) {
	if m == nil {
		return nil, fmt.Errorf("proxy manager unavailable")
	}
	if accountID <= 0 {
		return nil, fmt.Errorf("account id required")
	}
	if !force && !m.AccountBindingEnabled() {
		return nil, fmt.Errorf("ERR_DYNAMIC_PROXY_DISABLED")
	}
	maxAttempts := m.MaxBindRetries()
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		status := BindingVerifying
		if rotating {
			status = BindingRotating
		}
		base, err := m.generatedBinding(accountID, status)
		if err != nil {
			return nil, err
		}
		if err := m.saveBinding(base); err != nil {
			return nil, err
		}
		info, err := m.verifyProxyURL(ctx, base.proxyURL())
		if err == nil {
			base.Status = BindingActive
			base.VerifyError = ""
			base.FailCount = 0
			base.LastVerifiedAt = time.Now()
			base.UpdatedAt = time.Now()
			if info != nil {
				base.EgressIP = info.EgressIP
				base.Country = info.Country
				base.Region = info.Region
				base.City = info.City
				base.ISPOrg = info.ISPOrg
			}
			if err := m.saveBinding(base); err != nil {
				return nil, err
			}
			out := base.safe()
			return &BindingResult{Success: true, Binding: &out, Attempts: attempt}, nil
		}
		lastErr = err
		base.Status = BindingFailed
		base.VerifyError = redact.Text(err.Error())
		base.FailCount = attempt
		base.ExpiresAt = time.Time{}
		base.UpdatedAt = time.Now()
		_ = m.saveBinding(base)
	}
	msg := "ERR_DYNAMIC_PROXY_BIND_FAILED"
	if lastErr != nil {
		msg = redact.Text(lastErr.Error())
	}
	return &BindingResult{Success: false, Attempts: maxAttempts, Error: msg}, fmt.Errorf("%s", msg)
}

func (m *Manager) VerifyAccount(ctx context.Context, accountID int, force bool) (*BindingResult, error) {
	b, err := m.Binding(accountID)
	if err != nil {
		return nil, err
	}
	if b == nil {
		return nil, fmt.Errorf("ERR_DYNAMIC_PROXY_NOT_BOUND")
	}
	if !force && blockedBindingStatus(b.Status) {
		return nil, fmt.Errorf("ERR_DYNAMIC_PROXY_STATUS_%s", strings.ToUpper(b.Status))
	}
	info, err := m.verifyProxyURL(ctx, b.proxyURL())
	if err != nil {
		b.Status = BindingFailed
		b.VerifyError = redact.Text(err.Error())
		b.FailCount++
		b.UpdatedAt = time.Now()
		_ = m.saveBinding(b)
		out := b.safe()
		return &BindingResult{Success: false, Binding: &out, Error: b.VerifyError}, err
	}
	b.Status = BindingActive
	b.VerifyError = ""
	b.LastVerifiedAt = time.Now()
	b.UpdatedAt = time.Now()
	if info != nil {
		b.EgressIP = info.EgressIP
		b.Country = info.Country
		b.Region = info.Region
		b.City = info.City
		b.ISPOrg = info.ISPOrg
	}
	if err := m.saveBinding(b); err != nil {
		return nil, err
	}
	out := b.safe()
	return &BindingResult{Success: true, Binding: &out, Attempts: 1}, nil
}

func (m *Manager) ClearAccount(accountID int) (bool, error) {
	if m == nil || m.db == nil || accountID <= 0 {
		return false, nil
	}
	result, err := m.db.Exec(`DELETE FROM account_proxy_bindings WHERE account_id = ?`, accountID)
	if err != nil {
		return false, err
	}
	n, _ := result.RowsAffected()
	return n > 0, nil
}

func (m *Manager) SuspendAccount(accountID int, reason string) (*AccountBinding, error) {
	b, err := m.Binding(accountID)
	if err != nil || b == nil {
		return b, err
	}
	b.Status = BindingSuspended
	b.VerifyError = redact.Text(firstNonEmpty(reason, "account_disabled"))
	b.UpdatedAt = time.Now()
	if err := m.saveBinding(b); err != nil {
		return nil, err
	}
	out := b.safe()
	return &out, nil
}

func (m *Manager) ResumeAccount(ctx context.Context, accountID int) (*BindingResult, error) {
	b, err := m.Binding(accountID)
	if err != nil {
		return nil, err
	}
	if b == nil {
		return m.BindAccount(ctx, accountID, true)
	}
	if b.ExpiresAt.After(time.Now()) && b.Status != BindingFailed {
		b.Status = BindingActive
		b.VerifyError = ""
		b.UpdatedAt = time.Now()
		if err := m.saveBinding(b); err != nil {
			return nil, err
		}
		out := b.safe()
		return &BindingResult{Success: true, Binding: &out}, nil
	}
	return m.RotateAccount(ctx, accountID, true)
}

func (m *Manager) MarkBindingFailure(accountID int, err error, autoRebind bool) (*AccountBinding, error) {
	b, loadErr := m.Binding(accountID)
	if loadErr != nil || b == nil {
		return b, loadErr
	}
	msg := "dynamic_proxy_failure"
	if err != nil {
		msg = redact.Text(err.Error())
	}
	b.Status = BindingFailed
	b.VerifyError = msg
	b.FailCount++
	b.UpdatedAt = time.Now()
	if saveErr := m.saveBinding(b); saveErr != nil {
		return nil, saveErr
	}
	out := b.safe()
	if autoRebind && m.AccountBindingEnabled() && m.rotateOnError {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), m.timeout)
			defer cancel()
			_, _ = m.RotateAccount(ctx, accountID, true)
		}()
	}
	return &out, nil
}

func (m *Manager) AutoBindNewAccount(ctx context.Context, accountID int) (*BindingResult, error) {
	if m == nil || !m.AccountBindingEnabled() || !m.AutoBindNewEnabled() {
		return nil, nil
	}
	return m.BindAccount(ctx, accountID, false)
}

func (m *Manager) ActiveBinding(accountID int) *AccountBinding {
	b, err := m.Binding(accountID)
	if err != nil || b == nil || b.Status != BindingActive {
		return nil
	}
	if !b.ExpiresAt.IsZero() && !b.ExpiresAt.After(time.Now()) {
		b.Status = BindingExpired
		b.VerifyError = "binding_expired"
		b.UpdatedAt = time.Now()
		_ = m.saveBinding(b)
		return nil
	}
	out := b.safe()
	out.Password = b.Password
	out.Username = b.Username
	return &out
}

func (m *Manager) Binding(accountID int) (*AccountBinding, error) {
	if m == nil || m.db == nil || accountID <= 0 {
		return nil, nil
	}
	row := m.db.QueryRow(`SELECT account_id, provider, protocol, host, port, username, password, session_id, egress_ip, country, region, city, isp_org, status, expires_at, last_verified_at, verify_error, fail_count, created_at, updated_at FROM account_proxy_bindings WHERE account_id = ?`, accountID)
	b, err := scanBinding(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return b, err
}

func (m *Manager) Bindings() ([]AccountBinding, error) {
	if m == nil || m.db == nil {
		return nil, nil
	}
	rows, err := m.db.Query(`SELECT account_id, provider, protocol, host, port, username, password, session_id, egress_ip, country, region, city, isp_org, status, expires_at, last_verified_at, verify_error, fail_count, created_at, updated_at FROM account_proxy_bindings ORDER BY updated_at DESC, account_id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AccountBinding
	for rows.Next() {
		b, err := scanBinding(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b.safe())
	}
	return out, rows.Err()
}

func (m *Manager) WorkerPlan(accounts []AccountRef) ([]WorkerPlanItem, error) {
	cfgEnabled := m.AccountBindingEnabled()
	refs := map[int]AccountRef{}
	for _, a := range accounts {
		refs[a.ID] = a
	}
	bindings, err := m.Bindings()
	if err != nil {
		return nil, err
	}
	bound := map[int]bool{}
	now := time.Now()
	var out []WorkerPlanItem
	for _, b := range bindings {
		bound[b.AccountID] = true
		ref := refs[b.AccountID]
		activeAccount := ref.Active || (ref.Enabled && !ref.Banned)
		if !activeAccount || b.Status == BindingSuspended {
			continue
		}
		if b.Status == BindingFailed || b.Status == BindingExpired {
			out = append(out, WorkerPlanItem{AccountID: b.AccountID, Reason: b.Status, Priority: 1})
			continue
		}
		if b.Status == BindingActive && !b.ExpiresAt.IsZero() && b.ExpiresAt.Before(now.Add(m.RenewBefore())) {
			out = append(out, WorkerPlanItem{AccountID: b.AccountID, Reason: "expiring_soon", Priority: 2})
		}
	}
	if cfgEnabled && m.AutoBindNewEnabled() {
		for _, ref := range accounts {
			activeAccount := ref.Active || (ref.Enabled && !ref.Banned)
			if activeAccount && !bound[ref.ID] {
				out = append(out, WorkerPlanItem{AccountID: ref.ID, Reason: "unbound", Priority: 3})
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Priority != out[j].Priority {
			return out[i].Priority < out[j].Priority
		}
		return out[i].AccountID < out[j].AccountID
	})
	if m.workerBatchSize >= 0 && len(out) > m.workerBatchSize {
		out = out[:m.workerBatchSize]
	}
	return out, nil
}

func (m *Manager) RunMaintenance(ctx context.Context, accounts []AccountRef) (map[string]any, error) {
	plan, err := m.WorkerPlan(accounts)
	if err != nil {
		return nil, err
	}
	concurrency := m.workerConcurrencyValue()
	result := map[string]any{"planned": len(plan), "processed": 0, "failed": 0, "concurrency": concurrency, "items": plan}
	if len(plan) == 0 {
		return result, nil
	}
	jobs := make(chan WorkerPlanItem)
	var wg sync.WaitGroup
	var mu sync.Mutex
	worker := func() {
		defer wg.Done()
		for item := range jobs {
			if ctx.Err() != nil {
				mu.Lock()
				result["failed"] = result["failed"].(int) + 1
				mu.Unlock()
				continue
			}
			_, err := m.RotateAccount(ctx, item.AccountID, true)
			mu.Lock()
			result["processed"] = result["processed"].(int) + 1
			if err != nil {
				result["failed"] = result["failed"].(int) + 1
			}
			mu.Unlock()
		}
	}
	if concurrency > len(plan) {
		concurrency = len(plan)
	}
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go worker()
	}
	for _, item := range plan {
		select {
		case <-ctx.Done():
			close(jobs)
			wg.Wait()
			return result, ctx.Err()
		case jobs <- item:
		}
	}
	close(jobs)
	wg.Wait()
	return result, nil
}

func (m *Manager) Delete(id string) (bool, error) {
	id = strings.TrimSpace(id)
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.items[id]; !ok {
		return false, nil
	}
	delete(m.items, id)
	for i, existing := range m.order {
		if existing == id {
			m.order = append(m.order[:i], m.order[i+1:]...)
			break
		}
	}
	if len(m.order) == 0 {
		m.next = 0
	} else if m.next >= len(m.order) {
		m.next = 0
	}
	if m.db != nil {
		if _, err := m.db.Exec(`DELETE FROM proxy_pool WHERE id = ?`, id); err != nil {
			return false, err
		}
	}
	return true, nil
}

func (m *Manager) DeleteAccountBinding(accountID int) (bool, error) {
	return m.ClearAccount(accountID)
}

func (m *Manager) Patch(id string, enabled *bool, cooldownSeconds *int) (*Entry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	item := m.items[strings.TrimSpace(id)]
	if item == nil {
		return nil, fmt.Errorf("proxy not found")
	}
	if enabled != nil {
		item.Enabled = *enabled
	}
	if cooldownSeconds != nil {
		if *cooldownSeconds <= 0 {
			item.CooldownUntil = time.Time{}
		} else {
			item.CooldownUntil = time.Now().Add(time.Duration(*cooldownSeconds) * time.Second)
		}
	}
	item.UpdatedAt = time.Now()
	if err := m.persistEntryLocked(item); err != nil {
		return nil, err
	}
	return cloneEntry(item), nil
}

func (m *Manager) Test(ctx context.Context, id string) (*Entry, error) {
	m.mu.Lock()
	item := m.items[strings.TrimSpace(id)]
	if item == nil {
		m.mu.Unlock()
		return nil, fmt.Errorf("proxy not found")
	}
	proxyURL := item.URL
	testURL := m.testURL
	timeout := m.timeout
	m.mu.Unlock()

	start := time.Now()
	status := "ok"
	err := testProxy(ctx, timeout, testURL, proxyURL)
	if err != nil {
		status = "failed"
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	item = m.items[strings.TrimSpace(id)]
	if item == nil {
		return nil, fmt.Errorf("proxy not found")
	}
	item.LastTestStatus = status
	item.LastTestLatencyMS = time.Since(start).Milliseconds()
	item.LastTestAt = time.Now()
	if err != nil {
		item.Failures++
		item.LastError = redact.Text(err.Error())
		if m.rotateOnError {
			item.CooldownUntil = time.Now().Add(m.cooldown)
		}
		item.UpdatedAt = time.Now()
		_ = m.persistEntryLocked(item)
		return cloneEntry(item), err
	}
	item.Successes++
	item.LastError = ""
	item.UpdatedAt = time.Now()
	_ = m.persistEntryLocked(item)
	return cloneEntry(item), nil
}

func (m *Manager) AccountBindingEnabled() bool {
	if m == nil {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.accountBinding
}

func (m *Manager) AutoBindNewEnabled() bool {
	if m == nil {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.autoBindNew
}

func (m *Manager) RenewBefore() time.Duration {
	if m == nil {
		return 15 * time.Minute
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.renewBefore
}

func (m *Manager) MaxBindRetries() int {
	if m == nil {
		return 3
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.maxBindRetries <= 0 {
		return 3
	}
	return m.maxBindRetries
}

func (m *Manager) WorkerInterval() time.Duration {
	if m == nil {
		return time.Minute
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.workerInterval
}

func (m *Manager) workerConcurrencyValue() int {
	if m == nil {
		return 1
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.workerConcurrency <= 0 {
		return 1
	}
	return m.workerConcurrency
}

func (m *Manager) Reconfigure(cfg Config) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.defaultURL = strings.TrimSpace(cfg.Default)
	m.rotateOnError = cfg.RotateOnError
	if strings.TrimSpace(cfg.TestURL) != "" {
		m.testURL = strings.TrimSpace(cfg.TestURL)
	}
	if cfg.Cooldown > 0 {
		m.cooldown = cfg.Cooldown
	}
	m.allowPrivate = cfg.AllowPrivate
	m.accountBinding = cfg.AccountBinding
	m.autoBindNew = cfg.AutoBindNew
	if cfg.RenewBefore > 0 {
		m.renewBefore = cfg.RenewBefore
	}
	if cfg.MaxBindRetries > 0 {
		m.maxBindRetries = cfg.MaxBindRetries
	}
	if cfg.WorkerInterval > 0 {
		m.workerInterval = cfg.WorkerInterval
	}
	if cfg.WorkerBatchSize >= 0 {
		m.workerBatchSize = cfg.WorkerBatchSize
	}
	if cfg.WorkerConcurrency > 0 {
		m.workerConcurrency = cfg.WorkerConcurrency
	}
	if strings.TrimSpace(cfg.Provider) != "" {
		m.provider = strings.ToLower(strings.TrimSpace(cfg.Provider))
	}
	if strings.TrimSpace(cfg.Protocol) != "" {
		m.protocol = normalizeProtocol(cfg.Protocol)
	}
	if strings.TrimSpace(cfg.Host) != "" {
		m.host = strings.TrimSpace(cfg.Host)
	}
	if cfg.Port > 0 {
		m.port = cfg.Port
	}
	if strings.TrimSpace(cfg.UsernameTemplate) != "" {
		m.usernameTemplate = strings.TrimSpace(cfg.UsernameTemplate)
	}
	if cfg.Password != "" {
		m.password = cfg.Password
	}
	if strings.TrimSpace(cfg.Region) != "" {
		m.region = strings.TrimSpace(cfg.Region)
	}
	if strings.TrimSpace(cfg.State) != "" {
		m.state = strings.TrimSpace(cfg.State)
	}
	if cfg.TTLMinutes > 0 {
		m.ttlMinutes = cfg.TTLMinutes
	}
}

func (m *Manager) Snapshot() Snapshot {
	m.mu.Lock()
	out := Snapshot{
		Enabled:             len(m.order) > 0 || m.defaultURL != "" || m.accountBinding,
		Persistent:          m.db != nil,
		Default:             Mask(m.defaultURL),
		RotateOnError:       m.rotateOnError,
		TestURL:             m.testURL,
		AccountBinding:      m.accountBinding,
		AutoBindNewAccounts: m.autoBindNew,
		RenewBeforeMS:       m.renewBefore.Milliseconds(),
		MaxBindRetries:      m.maxBindRetries,
		WorkerIntervalMS:    m.workerInterval.Milliseconds(),
		WorkerBatchSize:     m.workerBatchSize,
		WorkerConcurrency:   m.workerConcurrency,
		Provider:            m.provider,
		Protocol:            m.protocol,
		Host:                m.host,
		Port:                m.port,
		UsernameTemplate:    m.usernameTemplate,
		PasswordSet:         strings.TrimSpace(m.password) != "",
		Region:              m.region,
		State:               m.state,
		TTLMinutes:          m.ttlMinutes,
		Entries:             make([]Entry, 0, len(m.order)),
	}
	for _, id := range m.order {
		if item := m.items[id]; item != nil {
			out.Entries = append(out.Entries, *cloneEntry(item))
		}
	}
	renewBefore := m.renewBefore
	m.mu.Unlock()
	bindings, _ := m.Bindings()
	out.Bindings = bindings
	out.Summary = summarizeBindings(bindings, nil, renewBefore)
	return out
}

func (m *Manager) load() error {
	rows, err := m.db.Query(`SELECT id, url, enabled, successes, failures, last_error, last_test_status, last_test_latency_ms, last_test_at, cooldown_until, created_at, updated_at FROM proxy_pool ORDER BY created_at, id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var item Entry
		var lastTestAt, cooldownUntil sql.NullTime
		if err := rows.Scan(&item.ID, &item.URL, &item.Enabled, &item.Successes, &item.Failures, &item.LastError, &item.LastTestStatus, &item.LastTestLatencyMS, &lastTestAt, &cooldownUntil, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return err
		}
		if err := validateProxyURL(item.URL, m.allowPrivate); err != nil {
			continue
		}
		if item.ID == "" {
			item.ID = ID(item.URL)
		}
		item.MaskedURL = Mask(item.URL)
		if lastTestAt.Valid {
			item.LastTestAt = lastTestAt.Time
		}
		if cooldownUntil.Valid {
			item.CooldownUntil = cooldownUntil.Time
		}
		m.items[item.ID] = &item
		m.order = append(m.order, item.ID)
	}
	return rows.Err()
}

func (m *Manager) persistEntryLocked(item *Entry) error {
	if m.db == nil || item == nil {
		return nil
	}
	var lastTestAt any
	if !item.LastTestAt.IsZero() {
		lastTestAt = item.LastTestAt
	}
	var cooldownUntil any
	if !item.CooldownUntil.IsZero() {
		cooldownUntil = item.CooldownUntil
	}
	createdAt := item.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now()
		item.CreatedAt = createdAt
	}
	updatedAt := item.UpdatedAt
	if updatedAt.IsZero() {
		updatedAt = time.Now()
		item.UpdatedAt = updatedAt
	}
	_, err := m.db.Exec(
		`INSERT INTO proxy_pool (id, url, enabled, successes, failures, last_error, last_test_status, last_test_latency_ms, last_test_at, cooldown_until, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET
		   url = excluded.url,
		   enabled = excluded.enabled,
		   successes = excluded.successes,
		   failures = excluded.failures,
		   last_error = excluded.last_error,
		   last_test_status = excluded.last_test_status,
		   last_test_latency_ms = excluded.last_test_latency_ms,
		   last_test_at = excluded.last_test_at,
		   cooldown_until = excluded.cooldown_until,
		   updated_at = excluded.updated_at`,
		item.ID, item.URL, item.Enabled, item.Successes, item.Failures, item.LastError, item.LastTestStatus, item.LastTestLatencyMS, lastTestAt, cooldownUntil, createdAt, updatedAt,
	)
	return err
}

type bindingScanner interface {
	Scan(dest ...any) error
}

func scanBinding(row bindingScanner) (*AccountBinding, error) {
	var b AccountBinding
	var expiresAt, lastVerifiedAt, createdAt, updatedAt sql.NullTime
	err := row.Scan(
		&b.AccountID, &b.Provider, &b.Protocol, &b.Host, &b.Port, &b.Username, &b.Password, &b.SessionID,
		&b.EgressIP, &b.Country, &b.Region, &b.City, &b.ISPOrg, &b.Status, &expiresAt,
		&lastVerifiedAt, &b.VerifyError, &b.FailCount, &createdAt, &updatedAt,
	)
	if err != nil {
		return nil, err
	}
	if b.Provider == "" {
		b.Provider = "novproxy"
	}
	b.Protocol = normalizeProtocol(b.Protocol)
	if b.Status == "" {
		b.Status = BindingActive
	}
	if expiresAt.Valid {
		b.ExpiresAt = expiresAt.Time
	}
	if lastVerifiedAt.Valid {
		b.LastVerifiedAt = lastVerifiedAt.Time
	}
	if createdAt.Valid {
		b.CreatedAt = createdAt.Time
	}
	if updatedAt.Valid {
		b.UpdatedAt = updatedAt.Time
	}
	return &b, nil
}

func (m *Manager) saveBinding(b *AccountBinding) error {
	if m == nil || m.db == nil || b == nil || b.AccountID <= 0 {
		return nil
	}
	now := time.Now()
	if b.CreatedAt.IsZero() {
		if existing, _ := m.Binding(b.AccountID); existing != nil && !existing.CreatedAt.IsZero() {
			b.CreatedAt = existing.CreatedAt
		} else {
			b.CreatedAt = now
		}
	}
	if b.UpdatedAt.IsZero() {
		b.UpdatedAt = now
	}
	_, err := m.db.Exec(
		`INSERT INTO account_proxy_bindings (account_id, provider, protocol, host, port, username, password, session_id, egress_ip, country, region, city, isp_org, status, expires_at, last_verified_at, verify_error, fail_count, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(account_id) DO UPDATE SET
		   provider = excluded.provider,
		   protocol = excluded.protocol,
		   host = excluded.host,
		   port = excluded.port,
		   username = excluded.username,
		   password = excluded.password,
		   session_id = excluded.session_id,
		   egress_ip = excluded.egress_ip,
		   country = excluded.country,
		   region = excluded.region,
		   city = excluded.city,
		   isp_org = excluded.isp_org,
		   status = excluded.status,
		   expires_at = excluded.expires_at,
		   last_verified_at = excluded.last_verified_at,
		   verify_error = excluded.verify_error,
		   fail_count = excluded.fail_count,
		   updated_at = excluded.updated_at`,
		b.AccountID, firstNonEmpty(b.Provider, "novproxy"), normalizeProtocol(b.Protocol), b.Host, b.Port, b.Username, b.Password, b.SessionID,
		b.EgressIP, b.Country, b.Region, b.City, b.ISPOrg, firstNonEmpty(b.Status, BindingActive), nullableTime(b.ExpiresAt), nullableTime(b.LastVerifiedAt),
		redact.Text(b.VerifyError), b.FailCount, b.CreatedAt, b.UpdatedAt,
	)
	return err
}

func (m *Manager) markBindingSuccess(accountID int) error {
	b, err := m.Binding(accountID)
	if err != nil || b == nil {
		return err
	}
	b.Status = BindingActive
	b.VerifyError = ""
	b.UpdatedAt = time.Now()
	return m.saveBinding(b)
}

func (m *Manager) verifyProxyURL(ctx context.Context, proxyURL string) (*VerifyInfo, error) {
	if m == nil {
		return nil, fmt.Errorf("proxy manager unavailable")
	}
	m.mu.Lock()
	timeout := m.timeout
	testURL := m.testURL
	allowPrivate := m.allowPrivate
	m.mu.Unlock()
	return verifyProxy(ctx, timeout, testURL, proxyURL, allowPrivate)
}

func (b AccountBinding) proxyURL() string {
	u := &url.URL{Scheme: normalizeProtocol(b.Protocol), Host: fmt.Sprintf("%s:%d", b.Host, b.Port)}
	if strings.TrimSpace(b.Username) != "" {
		u.User = url.UserPassword(b.Username, b.Password)
	}
	return u.String()
}

func (b AccountBinding) safe() AccountBinding {
	out := b
	out.Password = ""
	out.HasPassword = strings.TrimSpace(b.Password) != ""
	out.Username = maskUsername(b.Username)
	out.MaskedURL = Mask(b.proxyURL())
	if !out.ExpiresAt.IsZero() {
		out.RemainingMS = time.Until(out.ExpiresAt).Milliseconds()
		if out.RemainingMS < 0 {
			out.RemainingMS = 0
		}
	}
	out.VerifyError = redact.Text(out.VerifyError)
	return out
}

func buildNovproxyBinding(accountID int, protocol, host string, port int, tpl, password, region, state string, ttl int) (*AccountBinding, error) {
	raw, err := buildNovproxyURL(protocol, host, port, tpl, password, region, state, ttl)
	if err != nil {
		return nil, err
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	username := ""
	pass := ""
	if u.User != nil {
		username = u.User.Username()
		pass, _ = u.User.Password()
	}
	hostOnly := u.Hostname()
	portValue, _ := strconv.Atoi(u.Port())
	if portValue <= 0 {
		portValue = port
	}
	sessionID := extractSessionID(username)
	return &AccountBinding{
		AccountID: accountID,
		Provider:  "novproxy",
		Protocol:  normalizeProtocol(protocol),
		Host:      hostOnly,
		Port:      portValue,
		Username:  username,
		Password:  pass,
		SessionID: sessionID,
		ExpiresAt: time.Now().Add(time.Duration(ttl) * time.Minute),
	}, nil
}

func extractSessionID(username string) string {
	parts := strings.Split(username, "-")
	for i, part := range parts {
		if part == "sid" && i+1 < len(parts) {
			return parts[i+1]
		}
	}
	return ""
}

func blockedBindingStatus(status string) bool {
	switch status {
	case BindingFailed, BindingExpired, BindingSuspended, BindingRotating, BindingVerifying:
		return true
	default:
		return false
	}
}

func summarizeBindings(bindings []AccountBinding, accounts []AccountRef, renewBefore time.Duration) BindingSummary {
	now := time.Now()
	accountCount := len(accounts)
	seen := map[int]bool{}
	s := BindingSummary{}
	for _, b := range bindings {
		seen[b.AccountID] = true
		switch b.Status {
		case BindingActive:
			s.Bound++
			if !b.ExpiresAt.IsZero() && b.ExpiresAt.Before(now.Add(renewBefore)) {
				s.ExpiringSoon++
			}
		case BindingFailed, BindingExpired:
			s.Failed++
		case BindingSuspended:
			s.Suspended++
		}
	}
	if accountCount > 0 {
		s.Unbound = accountCount - len(seen)
		if s.Unbound < 0 {
			s.Unbound = 0
		}
	}
	return s
}

func maskUsername(username string) string {
	username = strings.TrimSpace(username)
	if username == "" {
		return ""
	}
	if len(username) <= 12 {
		return "***"
	}
	return username[:10] + "..." + username[len(username)-6:]
}

func nullableTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t
}

func verifyProxy(ctx context.Context, timeout time.Duration, targetURL, proxyURL string, allowPrivate bool) (*VerifyInfo, error) {
	if err := validateProxyURL(proxyURL, allowPrivate); err != nil {
		return nil, err
	}
	proxy, _ := url.Parse(proxyURL)
	target, err := url.Parse(targetURL)
	if err != nil || target.Scheme == "" || target.Host == "" {
		if err == nil {
			err = fmt.Errorf("missing scheme or host")
		}
		return nil, fmt.Errorf("invalid proxy test url: %w", err)
	}
	if isPrivateHost(target.Hostname()) && !allowPrivate {
		return nil, fmt.Errorf("ERR_PROXY_TEST_PRIVATE_HOST: proxy test target %q is private", target.Hostname())
	}
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	if allowPrivate && isPrivateHost(proxy.Hostname()) {
		info, err := verifyProxyDirect(ctx, timeout, target.String())
		if err != nil {
			return nil, err
		}
		return info, nil
	}
	start := time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json,text/plain;q=0.9")
	req.Header.Set("User-Agent", "windsurfapi-go/proxy-test")
	client := &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			Proxy:           http.ProxyURL(proxy),
			TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12},
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("ERR_VERIFY_HTTP:%d:%s", resp.StatusCode, redact.Text(string(body)))
	}
	info := parseVerifyBody(body)
	info.LatencyMS = time.Since(start).Milliseconds()
	if info.EgressIP == "" {
		return nil, fmt.Errorf("ERR_PROXY_VERIFY_NO_IP")
	}
	return info, nil
}

func verifyProxyDirect(ctx context.Context, timeout time.Duration, targetURL string) (*VerifyInfo, error) {
	start := time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json,text/plain;q=0.9")
	req.Header.Set("User-Agent", "windsurfapi-go/proxy-test")
	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("ERR_VERIFY_HTTP:%d:%s", resp.StatusCode, redact.Text(string(body)))
	}
	info := parseVerifyBody(body)
	info.LatencyMS = time.Since(start).Milliseconds()
	if info.EgressIP == "" {
		return nil, fmt.Errorf("ERR_PROXY_VERIFY_NO_IP")
	}
	return info, nil
}

func parseVerifyBody(body []byte) *VerifyInfo {
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err == nil && raw != nil {
		return &VerifyInfo{
			EgressIP: stringValue(raw, "ip", "query", "origin"),
			Country:  stringValue(raw, "country"),
			Region:   stringValue(raw, "region"),
			City:     stringValue(raw, "city"),
			ISPOrg:   stringValue(raw, "org", "isp", "as"),
		}
	}
	return &VerifyInfo{EgressIP: strings.TrimSpace(string(body))}
}

func stringValue(raw map[string]any, keys ...string) string {
	for _, key := range keys {
		if v, ok := raw[key]; ok {
			if s := strings.TrimSpace(fmt.Sprint(v)); s != "" && s != "<nil>" {
				return s
			}
		}
	}
	return ""
}

func ID(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return sanitize(raw) + "_" + shortHash(raw)
	}
	return sanitize(u.Scheme+"_"+u.Host+"_"+u.User.Username()) + "_" + shortHash(raw)
}

func Mask(raw string) string {
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

func validateProxyURL(raw string, allowPrivate ...bool) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		if err == nil {
			err = fmt.Errorf("missing scheme or host")
		}
		return fmt.Errorf("invalid proxy_url %q: %w", Mask(raw), err)
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https", "socks5":
	default:
		return fmt.Errorf("unsupported proxy scheme: %s", u.Scheme)
	}
	if len(allowPrivate) > 0 && allowPrivate[0] {
		return nil
	}
	if isPrivateHost(u.Hostname()) {
		return fmt.Errorf("ERR_PROXY_PRIVATE_HOST: proxy host %q is private; set proxy.allow_private=true or WINDSURFAPI_PROXY_ALLOW_PRIVATE=1 to allow it", u.Hostname())
	}
	return nil
}

func ValidateURL(raw string) error {
	return validateProxyURL(raw)
}

func ValidateURLWithPrivate(raw string, allowPrivate bool) error {
	return validateProxyURL(raw, allowPrivate)
}

func buildNovproxyURL(protocol, host string, port int, tpl, password, region, state string, ttl int) (string, error) {
	protocol = normalizeProtocol(protocol)
	host = strings.TrimSpace(host)
	if host == "" {
		return "", fmt.Errorf("dynamic proxy host required")
	}
	if port <= 0 || port > 65535 {
		return "", fmt.Errorf("dynamic proxy port invalid")
	}
	if ttl <= 0 {
		ttl = 120
	}
	sessionID := randomSessionID()
	username := firstNonEmpty(tpl, "nfgr68136-region-{region}-st-{state}-sid-{sid}-t-{ttl}")
	replacements := map[string]string{
		"{region}": strings.TrimSpace(region),
		"{state}":  strings.TrimSpace(state),
		"{sid}":    sessionID,
		"{ttl}":    fmt.Sprintf("%d", ttl),
	}
	for from, to := range replacements {
		username = strings.ReplaceAll(username, from, to)
	}
	u := &url.URL{Scheme: protocol, Host: fmt.Sprintf("%s:%d", host, port)}
	if strings.TrimSpace(username) != "" {
		u.User = url.UserPassword(username, password)
	}
	return u.String(), nil
}

func normalizeProtocol(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "socks", "socks5", "socks5h":
		return "socks5"
	case "https":
		return "http"
	case "http":
		return "http"
	default:
		return "http"
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if v := strings.TrimSpace(value); v != "" {
			return v
		}
	}
	return ""
}

func randomSessionID() string {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return shortHash(fmt.Sprintf("%d", time.Now().UnixNano()))
	}
	return strings.TrimRight(hex.EncodeToString(buf[:]), "=")
}

func testProxy(ctx context.Context, timeout time.Duration, targetURL, proxyURL string) error {
	_, err := verifyProxy(ctx, timeout, targetURL, proxyURL, false)
	return err
}

func oldTestProxy(ctx context.Context, timeout time.Duration, targetURL, proxyURL string) error {
	if err := validateProxyURL(proxyURL); err != nil {
		return err
	}
	proxy, _ := url.Parse(proxyURL)
	target, err := url.Parse(targetURL)
	if err != nil || target.Scheme == "" || target.Host == "" {
		if err == nil {
			err = fmt.Errorf("missing scheme or host")
		}
		return fmt.Errorf("invalid proxy test url: %w", err)
	}
	if isPrivateHost(target.Hostname()) {
		return fmt.Errorf("ERR_PROXY_TEST_PRIVATE_HOST: proxy test target %q is private", target.Hostname())
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, target.String(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "windsurfapi-go/proxy-test")
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	client := &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			Proxy:           http.ProxyURL(proxy),
			TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12},
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 500 {
		return fmt.Errorf("proxy test target returned http %d", resp.StatusCode)
	}
	return nil
}

func isPrivateHost(host string) bool {
	host = strings.Trim(strings.TrimSpace(host), "[]")
	if host == "" || strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	if ip4 := ip.To4(); ip4 != nil {
		return ip4[0] == 0 ||
			ip4[0] == 10 ||
			ip4[0] == 127 ||
			(ip4[0] == 100 && ip4[1] >= 64 && ip4[1] <= 127) ||
			(ip4[0] == 169 && ip4[1] == 254) ||
			(ip4[0] == 172 && ip4[1] >= 16 && ip4[1] <= 31) ||
			(ip4[0] == 192 && ip4[1] == 168)
	}
	return ip.IsLoopback() || ip.IsUnspecified() || ip.IsLinkLocalUnicast() || ip.IsPrivate()
}

func sanitize(s string) string {
	s = strings.TrimSpace(strings.ToLower(s))
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			continue
		}
		b.WriteByte('_')
	}
	out := strings.Trim(b.String(), "_")
	if out == "" {
		return "proxy"
	}
	if len(out) > 96 {
		out = out[:96]
	}
	return out
}

func shortHash(raw string) string {
	sum := sha1.Sum([]byte(strings.TrimSpace(raw)))
	return hex.EncodeToString(sum[:])[:10]
}

func cloneEntry(in *Entry) *Entry {
	if in == nil {
		return nil
	}
	out := *in
	out.URL = ""
	if out.MaskedURL == "" {
		out.MaskedURL = Mask(in.URL)
	}
	return &out
}
