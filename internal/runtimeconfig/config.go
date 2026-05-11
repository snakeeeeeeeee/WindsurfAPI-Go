package runtimeconfig

import (
	"fmt"
	"strings"
	"sync"

	"github.com/zhangyu/windsurfapi-go/internal/config"
)

type Manager struct {
	mu  sync.RWMutex
	cfg *config.Config
}

type Snapshot struct {
	Server    ServerView    `json:"server"`
	SQLite    SQLiteView    `json:"sqlite"`
	Redis     RedisView     `json:"redis"`
	Chat      ChatView      `json:"chat"`
	Direct    DirectView    `json:"direct"`
	Health    HealthView    `json:"health"`
	Scheduler SchedulerView `json:"scheduler"`
	Usage     UsageView     `json:"usage"`
	Dashboard DashboardView `json:"dashboard"`
	Proxy     ProxyView     `json:"proxy"`
	Log       LogView       `json:"log"`
	Security  SecurityView  `json:"security"`
}

type ServerView struct {
	Port                int   `json:"port"`
	APIKeyCount         int   `json:"api_key_count"`
	MaxRequestBodyBytes int64 `json:"max_request_body_bytes"`
}

type SQLiteView struct {
	Path string `json:"path"`
}

type RedisView struct {
	Addr        string `json:"addr"`
	DB          int    `json:"db"`
	PasswordSet bool   `json:"password_set"`
}

type ChatView struct {
	Backend string `json:"backend"`
}

type DirectView struct {
	Hosts             []string `json:"hosts"`
	TimeoutSeconds    int      `json:"timeout_seconds"`
	NativeChatPrompts bool     `json:"native_chat_prompts"`
}

type HealthView struct {
	Enabled           bool   `json:"enabled"`
	IntervalSeconds   int    `json:"interval_seconds"`
	TimeoutSeconds    int    `json:"timeout_seconds"`
	MarkInvalidBanned bool   `json:"mark_invalid_banned"`
	CheckModelConfigs bool   `json:"check_model_configs"`
	ReadyRequireCheck bool   `json:"ready_require_check"`
	Model             string `json:"model"`
}

type SchedulerView struct {
	RedisEnabled          bool `json:"redis_enabled"`
	RedisFailClosed       bool `json:"redis_fail_closed"`
	MaxInflightPerAccount int  `json:"max_inflight_per_account"`
	ReservationTTLSeconds int  `json:"reservation_ttl_seconds"`
}

type UsageView struct {
	VirtualCache VirtualCacheView `json:"virtual_cache"`
}

type VirtualCacheView struct {
	Enabled             bool    `json:"enabled"`
	Mode                string  `json:"mode"`
	DefaultTTL          string  `json:"default_ttl"`
	UncachedInputTokens int     `json:"uncached_input_tokens"`
	MinInputTokens      int     `json:"min_input_tokens"`
	MaxInputTokens      int     `json:"max_input_tokens"`
	WarmupTokens        int     `json:"warmup_tokens"`
	MinCreationTokens   int     `json:"min_creation_tokens"`
	MaxCreationTokens   int     `json:"max_creation_tokens"`
	CreationJitterRatio float64 `json:"creation_jitter_ratio"`
	BurstEveryTurns     int     `json:"burst_every_turns"`
	BurstMinTokens      int     `json:"burst_min_tokens"`
	BurstMaxTokens      int     `json:"burst_max_tokens"`
}

type DashboardView struct {
	Enabled     bool `json:"enabled"`
	Port        int  `json:"port"`
	PasswordSet bool `json:"password_set"`
}

type ProxyView struct {
	Default             string   `json:"default"`
	Dynamic             []string `json:"dynamic"`
	RotateOnError       bool     `json:"rotate_on_error"`
	TestURL             string   `json:"test_url"`
	CooldownSeconds     int      `json:"cooldown_seconds"`
	AllowPrivate        bool     `json:"allow_private"`
	AccountBinding      bool     `json:"account_binding"`
	AutoBindNewAccounts bool     `json:"auto_bind_new_accounts"`
	RenewBeforeMS       int      `json:"renew_before_ms"`
	MaxBindRetries      int      `json:"max_bind_retries"`
	WorkerIntervalMS    int      `json:"worker_interval_ms"`
	WorkerBatchSize     int      `json:"worker_batch_size"`
	WorkerConcurrency   int      `json:"worker_concurrency"`
	Provider            string   `json:"provider"`
	Protocol            string   `json:"protocol"`
	Host                string   `json:"host"`
	Port                int      `json:"port"`
	UsernameTemplate    string   `json:"username_template"`
	PasswordSet         bool     `json:"password_set"`
	Password            string   `json:"password,omitempty"`
	Region              string   `json:"region"`
	State               string   `json:"state"`
	TTLMinutes          int      `json:"ttl_minutes"`
}

type LogView struct {
	Level string `json:"level"`
}

type SecretStatus struct {
	Set         bool   `json:"set"`
	Safe        bool   `json:"safe"`
	Source      string `json:"source"`
	Environment string `json:"environment"`
	Message     string `json:"message"`
}

type SecurityView struct {
	APIKeys           SecretStatus `json:"api_keys"`
	DashboardPassword SecretStatus `json:"dashboard_password"`
	RedisPassword     SecretStatus `json:"redis_password"`
}

type Patch struct {
	Server    *ServerView    `json:"server,omitempty"`
	Direct    *DirectView    `json:"direct,omitempty"`
	Health    *HealthView    `json:"health,omitempty"`
	Scheduler *SchedulerView `json:"scheduler,omitempty"`
	Usage     *UsageView     `json:"usage,omitempty"`
	Proxy     *ProxyView     `json:"proxy,omitempty"`
	Log       *LogView       `json:"log,omitempty"`
	Secrets   *SecretsPatch  `json:"secrets,omitempty"`
}

type SecretsPatch struct {
	APIKeys           []string `json:"api_keys,omitempty"`
	DashboardPassword string   `json:"dashboard_password,omitempty"`
	RedisPassword     string   `json:"redis_password,omitempty"`
}

func NewManager(cfg *config.Config) *Manager {
	return &Manager{cfg: cfg}
}

func (m *Manager) MaxRequestBodyBytes() int64 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.cfg == nil || m.cfg.Server.MaxRequestBodyBytes <= 0 {
		return 25 * 1024 * 1024
	}
	return m.cfg.Server.MaxRequestBodyBytes
}

func (m *Manager) APIKeys() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.cfg == nil {
		return nil
	}
	return append([]string(nil), m.cfg.Server.APIKeys...)
}

func (m *Manager) DashboardPassword() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.cfg == nil {
		return ""
	}
	return strings.TrimSpace(m.cfg.Dashboard.Password)
}

func (m *Manager) CredentialsSnapshot() map[string]any {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.cfg == nil {
		return map[string]any{
			"apiKey_masked":           "",
			"apiKeySource":            "unset",
			"dashboardPasswordSet":    false,
			"dashboardPasswordSource": "unset",
		}
	}
	apiKey := ""
	for _, key := range m.cfg.Server.APIKeys {
		if key = strings.TrimSpace(key); key != "" {
			apiKey = key
			break
		}
	}
	dashboardPassword := strings.TrimSpace(m.cfg.Dashboard.Password)
	return map[string]any{
		"apiKey_masked":           maskSecret(apiKey),
		"apiKeySource":            sourceForSecret(apiKey),
		"dashboardPasswordSet":    dashboardPassword != "",
		"dashboardPasswordSource": sourceForSecret(dashboardPassword),
	}
}

func (m *Manager) EnvSnapshot() map[string]any {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.cfg == nil {
		return map[string]any{}
	}
	return map[string]any{
		"server": map[string]any{
			"port":                   m.cfg.Server.Port,
			"max_request_body_bytes": m.cfg.Server.MaxRequestBodyBytes,
			"api_key_count":          len(m.cfg.Server.APIKeys),
		},
		"direct": map[string]any{
			"hosts":               append([]string(nil), m.cfg.Direct.Hosts...),
			"timeout_seconds":     m.cfg.Direct.TimeoutSeconds,
			"native_chat_prompts": m.cfg.Direct.NativeChatPrompts,
		},
		"health": map[string]any{
			"enabled":             m.cfg.Health.Enabled,
			"interval_seconds":    m.cfg.Health.IntervalSeconds,
			"timeout_seconds":     m.cfg.Health.TimeoutSeconds,
			"mark_invalid_banned": m.cfg.Health.MarkInvalidBanned,
			"check_model_configs": m.cfg.Health.CheckModelConfigs,
			"ready_require_check": m.cfg.Health.ReadyRequireCheck,
			"model":               m.cfg.Health.Model,
		},
		"scheduler": map[string]any{
			"redis_enabled":            m.cfg.Scheduler.RedisEnabled,
			"redis_fail_closed":        m.cfg.Scheduler.RedisFailClosed,
			"max_inflight_per_account": m.cfg.Scheduler.MaxInflightPerAccount,
			"reservation_ttl_seconds":  m.cfg.Scheduler.ReservationTTLSeconds,
		},
		"usage": map[string]any{
			"virtual_cache": map[string]any{
				"enabled":               m.cfg.Usage.VirtualCache.Enabled,
				"mode":                  m.cfg.Usage.VirtualCache.Mode,
				"default_ttl":           m.cfg.Usage.VirtualCache.DefaultTTL,
				"uncached_input_tokens": m.cfg.Usage.VirtualCache.UncachedInputTokens,
				"min_input_tokens":      m.cfg.Usage.VirtualCache.MinInputTokens,
				"max_input_tokens":      m.cfg.Usage.VirtualCache.MaxInputTokens,
				"warmup_tokens":         m.cfg.Usage.VirtualCache.WarmupTokens,
				"min_creation_tokens":   m.cfg.Usage.VirtualCache.MinCreationTokens,
				"max_creation_tokens":   m.cfg.Usage.VirtualCache.MaxCreationTokens,
				"creation_jitter_ratio": m.cfg.Usage.VirtualCache.CreationJitterRatio,
				"burst_every_turns":     m.cfg.Usage.VirtualCache.BurstEveryTurns,
				"burst_min_tokens":      m.cfg.Usage.VirtualCache.BurstMinTokens,
				"burst_max_tokens":      m.cfg.Usage.VirtualCache.BurstMaxTokens,
			},
		},
		"proxy": map[string]any{
			"default":                m.cfg.Proxy.Default,
			"dynamic":                append([]string(nil), m.cfg.Proxy.Dynamic...),
			"rotate_on_error":        m.cfg.Proxy.RotateOnError,
			"test_url":               m.cfg.Proxy.TestURL,
			"cooldown_seconds":       m.cfg.Proxy.CooldownSeconds,
			"allow_private":          m.cfg.Proxy.AllowPrivate,
			"account_binding":        m.cfg.Proxy.AccountBinding,
			"auto_bind_new_accounts": m.cfg.Proxy.AutoBindNew,
			"renew_before_ms":        m.cfg.Proxy.RenewBeforeMS,
			"max_bind_retries":       m.cfg.Proxy.MaxBindRetries,
			"worker_interval_ms":     m.cfg.Proxy.WorkerIntervalMS,
			"worker_batch_size":      m.cfg.Proxy.WorkerBatchSize,
			"worker_concurrency":     m.cfg.Proxy.WorkerConcurrency,
			"provider":               m.cfg.Proxy.Provider,
			"protocol":               m.cfg.Proxy.Protocol,
			"host":                   m.cfg.Proxy.Host,
			"port":                   m.cfg.Proxy.Port,
			"username_template":      m.cfg.Proxy.UsernameTemplate,
			"password_set":           strings.TrimSpace(m.cfg.Proxy.Password) != "",
			"region":                 m.cfg.Proxy.Region,
			"state":                  m.cfg.Proxy.State,
			"ttl_minutes":            m.cfg.Proxy.TTLMinutes,
		},
		"log": map[string]any{"level": m.cfg.Log.Level},
	}
}

func (m *Manager) SetProxyDefault(raw string) Snapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cfg != nil {
		m.cfg.Proxy.Default = strings.TrimSpace(raw)
	}
	return snapshot(m.cfg)
}

func (m *Manager) Snapshot() Snapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return snapshot(m.cfg)
}

func (m *Manager) Patch(p Patch) (Snapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if p.Server != nil {
		if p.Server.MaxRequestBodyBytes <= 0 {
			return Snapshot{}, fmt.Errorf("server.max_request_body_bytes must be positive")
		}
		m.cfg.Server.MaxRequestBodyBytes = p.Server.MaxRequestBodyBytes
	}
	if p.Direct != nil {
		if p.Direct.TimeoutSeconds <= 0 {
			return Snapshot{}, fmt.Errorf("direct.timeout_seconds must be positive")
		}
		if len(p.Direct.Hosts) > 0 {
			m.cfg.Direct.Hosts = cleanStrings(p.Direct.Hosts)
		}
		m.cfg.Direct.TimeoutSeconds = p.Direct.TimeoutSeconds
		m.cfg.Direct.NativeChatPrompts = p.Direct.NativeChatPrompts
	}
	if p.Health != nil {
		if p.Health.IntervalSeconds <= 0 || p.Health.TimeoutSeconds <= 0 {
			return Snapshot{}, fmt.Errorf("health interval/timeout must be positive")
		}
		m.cfg.Health.Enabled = p.Health.Enabled
		m.cfg.Health.IntervalSeconds = p.Health.IntervalSeconds
		m.cfg.Health.TimeoutSeconds = p.Health.TimeoutSeconds
		m.cfg.Health.MarkInvalidBanned = p.Health.MarkInvalidBanned
		m.cfg.Health.CheckModelConfigs = p.Health.CheckModelConfigs
		m.cfg.Health.ReadyRequireCheck = p.Health.ReadyRequireCheck
		if strings.TrimSpace(p.Health.Model) != "" {
			m.cfg.Health.Model = strings.TrimSpace(p.Health.Model)
		}
	}
	if p.Scheduler != nil {
		if p.Scheduler.MaxInflightPerAccount < 0 || p.Scheduler.ReservationTTLSeconds <= 0 {
			return Snapshot{}, fmt.Errorf("scheduler max_inflight must be >= 0 and reservation ttl must be positive")
		}
		m.cfg.Scheduler.RedisEnabled = p.Scheduler.RedisEnabled
		m.cfg.Scheduler.RedisFailClosed = p.Scheduler.RedisFailClosed
		m.cfg.Scheduler.MaxInflightPerAccount = p.Scheduler.MaxInflightPerAccount
		m.cfg.Scheduler.ReservationTTLSeconds = p.Scheduler.ReservationTTLSeconds
	}
	if p.Usage != nil {
		vc := p.Usage.VirtualCache
		if strings.TrimSpace(vc.Mode) == "" {
			vc.Mode = "conservative"
		}
		mode := strings.ToLower(strings.TrimSpace(vc.Mode))
		if mode != "conservative" && mode != "dynamic" {
			return Snapshot{}, fmt.Errorf("usage.virtual_cache.mode must be conservative or dynamic")
		}
		ttl := strings.ToLower(strings.TrimSpace(vc.DefaultTTL))
		if ttl == "" {
			ttl = "5m"
		}
		if ttl != "5m" && ttl != "1h" {
			return Snapshot{}, fmt.Errorf("usage.virtual_cache.default_ttl must be 5m or 1h")
		}
		if vc.UncachedInputTokens <= 0 || vc.MinInputTokens <= 0 || vc.MaxInputTokens <= 0 {
			return Snapshot{}, fmt.Errorf("usage virtual cache input token limits must be positive")
		}
		if vc.MinInputTokens > vc.MaxInputTokens {
			return Snapshot{}, fmt.Errorf("usage virtual cache min_input_tokens must be <= max_input_tokens")
		}
		if vc.WarmupTokens < 0 || vc.MinCreationTokens < 0 || vc.MaxCreationTokens < 0 || vc.BurstEveryTurns < 0 || vc.BurstMinTokens < 0 || vc.BurstMaxTokens < 0 || vc.CreationJitterRatio < 0 {
			return Snapshot{}, fmt.Errorf("usage virtual cache numeric settings must be >= 0")
		}
		m.cfg.Usage.VirtualCache = config.VirtualCacheUsageConfig{
			Enabled:             vc.Enabled,
			Mode:                mode,
			DefaultTTL:          ttl,
			UncachedInputTokens: vc.UncachedInputTokens,
			MinInputTokens:      vc.MinInputTokens,
			MaxInputTokens:      vc.MaxInputTokens,
			WarmupTokens:        vc.WarmupTokens,
			MinCreationTokens:   vc.MinCreationTokens,
			MaxCreationTokens:   vc.MaxCreationTokens,
			CreationJitterRatio: vc.CreationJitterRatio,
			BurstEveryTurns:     vc.BurstEveryTurns,
			BurstMinTokens:      vc.BurstMinTokens,
			BurstMaxTokens:      vc.BurstMaxTokens,
		}
	}
	if p.Proxy != nil {
		m.cfg.Proxy.Default = strings.TrimSpace(p.Proxy.Default)
		m.cfg.Proxy.Dynamic = cleanStrings(p.Proxy.Dynamic)
		m.cfg.Proxy.RotateOnError = p.Proxy.RotateOnError
		m.cfg.Proxy.AllowPrivate = p.Proxy.AllowPrivate
		m.cfg.Proxy.AccountBinding = p.Proxy.AccountBinding
		m.cfg.Proxy.AutoBindNew = p.Proxy.AutoBindNewAccounts
		if p.Proxy.RenewBeforeMS > 0 {
			m.cfg.Proxy.RenewBeforeMS = p.Proxy.RenewBeforeMS
		}
		if p.Proxy.MaxBindRetries > 0 {
			m.cfg.Proxy.MaxBindRetries = p.Proxy.MaxBindRetries
		}
		if p.Proxy.WorkerIntervalMS > 0 {
			m.cfg.Proxy.WorkerIntervalMS = p.Proxy.WorkerIntervalMS
		}
		if p.Proxy.WorkerBatchSize > 0 {
			m.cfg.Proxy.WorkerBatchSize = p.Proxy.WorkerBatchSize
		}
		if p.Proxy.WorkerConcurrency > 0 {
			m.cfg.Proxy.WorkerConcurrency = p.Proxy.WorkerConcurrency
		}
		if strings.TrimSpace(p.Proxy.Provider) != "" {
			m.cfg.Proxy.Provider = strings.ToLower(strings.TrimSpace(p.Proxy.Provider))
		}
		if strings.TrimSpace(p.Proxy.Protocol) != "" {
			m.cfg.Proxy.Protocol = strings.ToLower(strings.TrimSpace(p.Proxy.Protocol))
		}
		if strings.TrimSpace(p.Proxy.Host) != "" {
			m.cfg.Proxy.Host = strings.TrimSpace(p.Proxy.Host)
		}
		if p.Proxy.Port > 0 {
			m.cfg.Proxy.Port = p.Proxy.Port
		}
		if strings.TrimSpace(p.Proxy.UsernameTemplate) != "" {
			m.cfg.Proxy.UsernameTemplate = strings.TrimSpace(p.Proxy.UsernameTemplate)
		}
		if strings.TrimSpace(p.Proxy.Password) != "" {
			m.cfg.Proxy.Password = p.Proxy.Password
		}
		if strings.TrimSpace(p.Proxy.Region) != "" {
			m.cfg.Proxy.Region = strings.TrimSpace(p.Proxy.Region)
		}
		if strings.TrimSpace(p.Proxy.State) != "" {
			m.cfg.Proxy.State = strings.TrimSpace(p.Proxy.State)
		}
		if p.Proxy.TTLMinutes > 0 {
			m.cfg.Proxy.TTLMinutes = p.Proxy.TTLMinutes
		}
		if strings.TrimSpace(p.Proxy.TestURL) != "" {
			m.cfg.Proxy.TestURL = strings.TrimSpace(p.Proxy.TestURL)
		}
		if p.Proxy.CooldownSeconds > 0 {
			m.cfg.Proxy.CooldownSeconds = p.Proxy.CooldownSeconds
		}
	}
	if p.Log != nil && strings.TrimSpace(p.Log.Level) != "" {
		m.cfg.Log.Level = strings.ToLower(strings.TrimSpace(p.Log.Level))
	}
	if p.Secrets != nil {
		if p.Secrets.APIKeys != nil {
			keys := cleanStrings(p.Secrets.APIKeys)
			if len(keys) == 0 {
				return Snapshot{}, fmt.Errorf("secrets.api_keys must include at least one key")
			}
			m.cfg.Server.APIKeys = keys
		}
		if strings.TrimSpace(p.Secrets.DashboardPassword) != "" {
			m.cfg.Dashboard.Password = strings.TrimSpace(p.Secrets.DashboardPassword)
		}
		if p.Secrets.RedisPassword != "" {
			m.cfg.Redis.Password = p.Secrets.RedisPassword
		}
	}
	return snapshot(m.cfg), nil
}

func snapshot(cfg *config.Config) Snapshot {
	if cfg == nil {
		return Snapshot{}
	}
	return Snapshot{
		Server: ServerView{Port: cfg.Server.Port, APIKeyCount: len(cfg.Server.APIKeys), MaxRequestBodyBytes: cfg.Server.MaxRequestBodyBytes},
		SQLite: SQLiteView{Path: cfg.SQLite.Path},
		Redis:  RedisView{Addr: cfg.Redis.Addr, DB: cfg.Redis.DB, PasswordSet: strings.TrimSpace(cfg.Redis.Password) != ""},
		Chat:   ChatView{Backend: cfg.Chat.Backend},
		Direct: DirectView{
			Hosts:             append([]string(nil), cfg.Direct.Hosts...),
			TimeoutSeconds:    cfg.Direct.TimeoutSeconds,
			NativeChatPrompts: cfg.Direct.NativeChatPrompts,
		},
		Health: HealthView{
			Enabled:           cfg.Health.Enabled,
			IntervalSeconds:   cfg.Health.IntervalSeconds,
			TimeoutSeconds:    cfg.Health.TimeoutSeconds,
			MarkInvalidBanned: cfg.Health.MarkInvalidBanned,
			CheckModelConfigs: cfg.Health.CheckModelConfigs,
			ReadyRequireCheck: cfg.Health.ReadyRequireCheck,
			Model:             cfg.Health.Model,
		},
		Scheduler: SchedulerView{
			RedisEnabled:          cfg.Scheduler.RedisEnabled,
			RedisFailClosed:       cfg.Scheduler.RedisFailClosed,
			MaxInflightPerAccount: cfg.Scheduler.MaxInflightPerAccount,
			ReservationTTLSeconds: cfg.Scheduler.ReservationTTLSeconds,
		},
		Usage: UsageView{VirtualCache: VirtualCacheView{
			Enabled:             cfg.Usage.VirtualCache.Enabled,
			Mode:                cfg.Usage.VirtualCache.Mode,
			DefaultTTL:          cfg.Usage.VirtualCache.DefaultTTL,
			UncachedInputTokens: cfg.Usage.VirtualCache.UncachedInputTokens,
			MinInputTokens:      cfg.Usage.VirtualCache.MinInputTokens,
			MaxInputTokens:      cfg.Usage.VirtualCache.MaxInputTokens,
			WarmupTokens:        cfg.Usage.VirtualCache.WarmupTokens,
			MinCreationTokens:   cfg.Usage.VirtualCache.MinCreationTokens,
			MaxCreationTokens:   cfg.Usage.VirtualCache.MaxCreationTokens,
			CreationJitterRatio: cfg.Usage.VirtualCache.CreationJitterRatio,
			BurstEveryTurns:     cfg.Usage.VirtualCache.BurstEveryTurns,
			BurstMinTokens:      cfg.Usage.VirtualCache.BurstMinTokens,
			BurstMaxTokens:      cfg.Usage.VirtualCache.BurstMaxTokens,
		}},
		Dashboard: DashboardView{
			Enabled:     cfg.Dashboard.Enabled,
			Port:        cfg.Dashboard.Port,
			PasswordSet: strings.TrimSpace(cfg.Dashboard.Password) != "",
		},
		Proxy: ProxyView{
			Default:             cfg.Proxy.Default,
			Dynamic:             append([]string(nil), cfg.Proxy.Dynamic...),
			RotateOnError:       cfg.Proxy.RotateOnError,
			TestURL:             cfg.Proxy.TestURL,
			CooldownSeconds:     cfg.Proxy.CooldownSeconds,
			AllowPrivate:        cfg.Proxy.AllowPrivate,
			AccountBinding:      cfg.Proxy.AccountBinding,
			AutoBindNewAccounts: cfg.Proxy.AutoBindNew,
			RenewBeforeMS:       cfg.Proxy.RenewBeforeMS,
			MaxBindRetries:      cfg.Proxy.MaxBindRetries,
			WorkerIntervalMS:    cfg.Proxy.WorkerIntervalMS,
			WorkerBatchSize:     cfg.Proxy.WorkerBatchSize,
			WorkerConcurrency:   cfg.Proxy.WorkerConcurrency,
			Provider:            cfg.Proxy.Provider,
			Protocol:            cfg.Proxy.Protocol,
			Host:                cfg.Proxy.Host,
			Port:                cfg.Proxy.Port,
			UsernameTemplate:    cfg.Proxy.UsernameTemplate,
			PasswordSet:         strings.TrimSpace(cfg.Proxy.Password) != "",
			Region:              cfg.Proxy.Region,
			State:               cfg.Proxy.State,
			TTLMinutes:          cfg.Proxy.TTLMinutes,
		},
		Log: LogView{Level: cfg.Log.Level},
		Security: SecurityView{
			APIKeys:           apiKeysStatus(cfg.Server.APIKeys),
			DashboardPassword: dashboardPasswordStatus(cfg.Dashboard.Password),
			RedisPassword:     redisPasswordStatus(cfg.Redis.Password),
		},
	}
}

func apiKeysStatus(keys []string) SecretStatus {
	set := false
	safe := true
	for _, key := range keys {
		k := strings.TrimSpace(key)
		if k == "" {
			continue
		}
		set = true
		if k == "sk-windsurf-default" || strings.Contains(strings.ToLower(k), "default") {
			safe = false
		}
	}
	msg := "configured"
	if !set {
		msg = "missing API key"
		safe = false
	} else if !safe {
		msg = "replace default API key before remote deployment"
	}
	return SecretStatus{Set: set, Safe: safe, Source: "env_or_yaml", Environment: "WINDSURFAPI_API_KEYS", Message: msg}
}

func dashboardPasswordStatus(password string) SecretStatus {
	p := strings.TrimSpace(password)
	set := p != ""
	safe := set && p != "admin"
	msg := "configured"
	if !set {
		msg = "missing dashboard password; remote dashboard is fail-closed"
	} else if !safe {
		msg = "default dashboard password; remote dashboard is fail-closed"
	}
	return SecretStatus{Set: set, Safe: safe, Source: "env_or_yaml", Environment: "WINDSURFAPI_DASHBOARD_PASSWORD", Message: msg}
}

func redisPasswordStatus(password string) SecretStatus {
	set := strings.TrimSpace(password) != ""
	msg := "not required unless Redis requires AUTH"
	if set {
		msg = "configured"
	}
	return SecretStatus{Set: set, Safe: true, Source: "env_or_yaml", Environment: "WINDSURFAPI_REDIS_PASSWORD", Message: msg}
}

func maskSecret(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if len(value) <= 8 {
		return value[:1] + "***" + value[len(value)-1:]
	}
	return value[:4] + "..." + value[len(value)-4:]
}

func sourceForSecret(value string) string {
	if strings.TrimSpace(value) == "" {
		return "unset"
	}
	return "env_or_yaml"
}

func cleanStrings(values []string) []string {
	var out []string
	for _, v := range values {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}
