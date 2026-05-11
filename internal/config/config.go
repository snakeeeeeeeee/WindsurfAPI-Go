package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

type ServerConfig struct {
	Port                int      `yaml:"port"`
	APIKeys             []string `yaml:"api_keys"`
	MaxRequestBodyBytes int64    `yaml:"max_request_body_bytes"`
}

type SQLiteConfig struct {
	Path string `yaml:"path"`
}

type RedisConfig struct {
	Addr     string `yaml:"addr"`
	Password string `yaml:"password"`
	DB       int    `yaml:"db"`
}

type LSConfig struct {
	BinaryPath       string `yaml:"binary_path"`
	ExtensionVersion string `yaml:"extension_version"`
	MaxInstances     int    `yaml:"max_instances"`
	WarmupOnStart    bool   `yaml:"warmup_on_start"`

	// B5 新增：把 LS 启动参数显式化，默认值见 internal/ls 包常量。
	DataRoot     string `yaml:"data_root"`      // LS 的 codeium_dir / database_dir 根
	APIServerURL string `yaml:"api_server_url"` // 上游 Windsurf 主站
	RegisterURL  string `yaml:"register_url"`   // 注册地址
	CSRFToken    string `yaml:"csrf_token"`
	DefaultPort  int    `yaml:"default_port"`
	ReadySeconds int    `yaml:"ready_seconds"` // 等 LS 端口就绪的秒数
}

type DashboardConfig struct {
	Enabled  bool   `yaml:"enabled"`
	Port     int    `yaml:"port"`
	Username string `yaml:"username"`
	Password string `yaml:"password"`
}

type ProxyConfig struct {
	Default           string   `yaml:"default"`
	Dynamic           []string `yaml:"dynamic"`
	RotateOnError     bool     `yaml:"rotate_on_error"`
	TestURL           string   `yaml:"test_url"`
	CooldownSeconds   int      `yaml:"cooldown_seconds"`
	AllowPrivate      bool     `yaml:"allow_private"`
	AccountBinding    bool     `yaml:"account_binding"`
	AutoBindNew       bool     `yaml:"auto_bind_new_accounts"`
	RenewBeforeMS     int      `yaml:"renew_before_ms"`
	MaxBindRetries    int      `yaml:"max_bind_retries"`
	WorkerIntervalMS  int      `yaml:"worker_interval_ms"`
	WorkerBatchSize   int      `yaml:"worker_batch_size"`
	WorkerConcurrency int      `yaml:"worker_concurrency"`
	Provider          string   `yaml:"provider"`
	Protocol          string   `yaml:"protocol"`
	Host              string   `yaml:"host"`
	Port              int      `yaml:"port"`
	UsernameTemplate  string   `yaml:"username_template"`
	Password          string   `yaml:"password"`
	Region            string   `yaml:"region"`
	State             string   `yaml:"state"`
	TTLMinutes        int      `yaml:"ttl_minutes"`
}

// WindsurfConfig 目前仅用于 chat handler 的占位上游地址。
// 真正的 Windsurf 协议是本地 LS 的 gRPC，后续接入 langserver + gRPC 后
// 这个结构体会被替换/扩展（见 PLAN.md）。
type WindsurfConfig struct {
	BaseURL string `yaml:"base_url"`
}

type ChatConfig struct {
	Backend string `yaml:"backend"`
}

type DirectConfig struct {
	Hosts             []string `yaml:"hosts"`
	TimeoutSeconds    int      `yaml:"timeout_seconds"`
	NativeChatPrompts bool     `yaml:"native_chat_prompts"`
}

type HealthConfig struct {
	Enabled           bool   `yaml:"enabled"`
	IntervalSeconds   int    `yaml:"interval_seconds"`
	TimeoutSeconds    int    `yaml:"timeout_seconds"`
	MarkInvalidBanned bool   `yaml:"mark_invalid_banned"`
	CheckModelConfigs bool   `yaml:"check_model_configs"`
	ReadyRequireCheck bool   `yaml:"ready_require_check"`
	Model             string `yaml:"model"`
}

type SchedulerConfig struct {
	RedisEnabled          bool `yaml:"redis_enabled"`
	RedisFailClosed       bool `yaml:"redis_fail_closed"`
	MaxInflightPerAccount int  `yaml:"max_inflight_per_account"`
	ReservationTTLSeconds int  `yaml:"reservation_ttl_seconds"`
}

type UsageConfig struct {
	VirtualCache VirtualCacheUsageConfig `yaml:"virtual_cache"`
}

type VirtualCacheUsageConfig struct {
	Enabled             bool    `yaml:"enabled"`
	Mode                string  `yaml:"mode"`
	DefaultTTL          string  `yaml:"default_ttl"`
	UncachedInputTokens int     `yaml:"uncached_input_tokens"`
	MinInputTokens      int     `yaml:"min_input_tokens"`
	MaxInputTokens      int     `yaml:"max_input_tokens"`
	WarmupTokens        int     `yaml:"warmup_tokens"`
	MinCreationTokens   int     `yaml:"min_creation_tokens"`
	MaxCreationTokens   int     `yaml:"max_creation_tokens"`
	CreationJitterRatio float64 `yaml:"creation_jitter_ratio"`
	BurstEveryTurns     int     `yaml:"burst_every_turns"`
	BurstMinTokens      int     `yaml:"burst_min_tokens"`
	BurstMaxTokens      int     `yaml:"burst_max_tokens"`
}

type LogConfig struct {
	Level string `yaml:"level"`
}

type Config struct {
	Server    ServerConfig    `yaml:"server"`
	SQLite    SQLiteConfig    `yaml:"sqlite"`
	Redis     RedisConfig     `yaml:"redis"`
	LS        LSConfig        `yaml:"ls"`
	Windsurf  WindsurfConfig  `yaml:"windsurf"`
	Chat      ChatConfig      `yaml:"chat"`
	Direct    DirectConfig    `yaml:"direct"`
	Health    HealthConfig    `yaml:"health"`
	Scheduler SchedulerConfig `yaml:"scheduler"`
	Usage     UsageConfig     `yaml:"usage"`
	Dashboard DashboardConfig `yaml:"dashboard"`
	Proxy     ProxyConfig     `yaml:"proxy"`
	Log       LogConfig       `yaml:"log"`
}

func Load(path string) (*Config, error) {
	if path == "" {
		path = "configs/default.yaml"
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config file: %w", err)
	}

	cfg := &Config{
		Server: ServerConfig{
			Port:                3456,
			APIKeys:             []string{"sk-windsurf-default"},
			MaxRequestBodyBytes: 25 * 1024 * 1024,
		},
		SQLite: SQLiteConfig{
			Path: "./data/windsurf.db",
		},
		Redis: RedisConfig{
			Addr: "localhost:6379",
			DB:   0,
		},
		LS: LSConfig{
			ExtensionVersion: "1.5.7",
			MaxInstances:     16,
			WarmupOnStart:    true,
		},
		Dashboard: DashboardConfig{
			Enabled:  true,
			Port:     3457,
			Username: "admin",
			Password: "admin",
		},
		Windsurf: WindsurfConfig{
			BaseURL: "http://127.0.0.1:50051",
		},
		Chat: ChatConfig{
			Backend: "direct",
		},
		Direct: DirectConfig{
			TimeoutSeconds: 30,
		},
		Health: HealthConfig{
			Enabled:           true,
			IntervalSeconds:   300,
			TimeoutSeconds:    20,
			MarkInvalidBanned: true,
			CheckModelConfigs: true,
			ReadyRequireCheck: false,
			Model:             "claude-sonnet-4.6",
		},
		Scheduler: SchedulerConfig{
			RedisEnabled:          true,
			RedisFailClosed:       false,
			MaxInflightPerAccount: 4,
			ReservationTTLSeconds: 180,
		},
		Usage: UsageConfig{VirtualCache: VirtualCacheUsageConfig{
			Enabled:             false,
			Mode:                "conservative",
			DefaultTTL:          "5m",
			UncachedInputTokens: 64,
			MinInputTokens:      1,
			MaxInputTokens:      4096,
			WarmupTokens:        0,
			MinCreationTokens:   0,
			MaxCreationTokens:   8192,
			CreationJitterRatio: 0,
			BurstEveryTurns:     0,
			BurstMinTokens:      0,
			BurstMaxTokens:      0,
		}},
		Proxy: ProxyConfig{
			RotateOnError:     true,
			TestURL:           "https://ipinfo.io/json",
			CooldownSeconds:   120,
			AccountBinding:    true,
			AutoBindNew:       false,
			RenewBeforeMS:     900000,
			MaxBindRetries:    3,
			WorkerIntervalMS:  60000,
			WorkerBatchSize:   20,
			WorkerConcurrency: 3,
			Provider:          "novproxy",
			Protocol:          "http",
			Host:              "us.novproxy.io",
			Port:              1000,
			UsernameTemplate:  "nfgr68136-region-{region}-st-{state}-sid-{sid}-t-{ttl}",
			Region:            "US",
			State:             "New Jersey",
			TTLMinutes:        120,
		},
		Log: LogConfig{
			Level: "info",
		},
	}

	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	applyRuntimeDefaults(cfg)

	// 确保数据目录存在
	dbDir := filepath.Dir(cfg.SQLite.Path)
	if err := os.MkdirAll(dbDir, 0755); err != nil {
		return nil, fmt.Errorf("create db dir: %w", err)
	}

	return cfg, nil
}

func applyRuntimeDefaults(cfg *Config) {
	applyEnvOverrides(cfg)
	if cfg.Chat.Backend == "" {
		cfg.Chat.Backend = "direct"
	}
	if cfg.Server.MaxRequestBodyBytes <= 0 {
		cfg.Server.MaxRequestBodyBytes = 25 * 1024 * 1024
	}
	if cfg.Direct.TimeoutSeconds <= 0 {
		cfg.Direct.TimeoutSeconds = 30
	}
	if cfg.Health.IntervalSeconds <= 0 {
		cfg.Health.IntervalSeconds = 300
	}
	if cfg.Health.TimeoutSeconds <= 0 {
		cfg.Health.TimeoutSeconds = 20
	}
	if cfg.Health.Model == "" {
		cfg.Health.Model = "claude-sonnet-4.6"
	}
	if cfg.Scheduler.MaxInflightPerAccount < 0 {
		cfg.Scheduler.MaxInflightPerAccount = 0
	}
	if cfg.Scheduler.ReservationTTLSeconds <= 0 {
		cfg.Scheduler.ReservationTTLSeconds = 180
	}
	if cfg.Usage.VirtualCache.Mode == "" {
		cfg.Usage.VirtualCache.Mode = "conservative"
	}
	if cfg.Usage.VirtualCache.DefaultTTL == "" {
		cfg.Usage.VirtualCache.DefaultTTL = "5m"
	}
	if cfg.Usage.VirtualCache.UncachedInputTokens <= 0 {
		cfg.Usage.VirtualCache.UncachedInputTokens = 64
	}
	if cfg.Usage.VirtualCache.MinInputTokens <= 0 {
		cfg.Usage.VirtualCache.MinInputTokens = 1
	}
	if cfg.Usage.VirtualCache.MaxInputTokens <= 0 {
		cfg.Usage.VirtualCache.MaxInputTokens = 4096
	}
	if cfg.Usage.VirtualCache.MaxCreationTokens <= 0 {
		cfg.Usage.VirtualCache.MaxCreationTokens = 8192
	}
	if cfg.Proxy.CooldownSeconds <= 0 {
		cfg.Proxy.CooldownSeconds = 120
	}
	if cfg.Proxy.RenewBeforeMS <= 0 {
		cfg.Proxy.RenewBeforeMS = 900000
	}
	if cfg.Proxy.MaxBindRetries <= 0 {
		cfg.Proxy.MaxBindRetries = 3
	}
	if cfg.Proxy.WorkerIntervalMS <= 0 {
		cfg.Proxy.WorkerIntervalMS = 60000
	}
	if cfg.Proxy.WorkerBatchSize < 0 {
		cfg.Proxy.WorkerBatchSize = 20
	}
	if cfg.Proxy.WorkerConcurrency <= 0 {
		cfg.Proxy.WorkerConcurrency = 3
	}
	if cfg.Proxy.TestURL == "" {
		cfg.Proxy.TestURL = "https://ipinfo.io/json"
	}
	if cfg.Proxy.Provider == "" {
		cfg.Proxy.Provider = "novproxy"
	}
	if cfg.Proxy.Protocol == "" {
		cfg.Proxy.Protocol = "http"
	}
	if cfg.Proxy.Host == "" {
		cfg.Proxy.Host = "us.novproxy.io"
	}
	if cfg.Proxy.Port <= 0 {
		cfg.Proxy.Port = 1000
	}
	if cfg.Proxy.UsernameTemplate == "" {
		cfg.Proxy.UsernameTemplate = "nfgr68136-region-{region}-st-{state}-sid-{sid}-t-{ttl}"
	}
	if cfg.Proxy.Region == "" {
		cfg.Proxy.Region = "US"
	}
	if cfg.Proxy.State == "" {
		cfg.Proxy.State = "New Jersey"
	}
	if cfg.Proxy.TTLMinutes <= 0 {
		cfg.Proxy.TTLMinutes = 120
	}
	if cfg.LS.BinaryPath != "" {
		return
	}
	if v := os.Getenv("LS_BINARY_PATH"); v != "" {
		cfg.LS.BinaryPath = v
		return
	}
	if home, err := os.UserHomeDir(); err == nil {
		path := filepath.Join(home, ".windsurf", "language_server_macos_arm64")
		if _, statErr := os.Stat(path); statErr == nil {
			cfg.LS.BinaryPath = path
		}
	}
}

func applyEnvOverrides(cfg *Config) {
	if v := getenv("WINDSURFAPI_PORT", "PORT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.Server.Port = n
		}
	}
	if v := getenv("WINDSURFAPI_API_KEYS", "API_KEYS"); v != "" {
		if keys := splitCSV(v); len(keys) > 0 {
			cfg.Server.APIKeys = keys
		}
	}
	if v := getenv("WINDSURFAPI_MAX_REQUEST_BODY_BYTES", "MAX_REQUEST_BODY_BYTES"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			cfg.Server.MaxRequestBodyBytes = n
		}
	}
	if v := getenv("WINDSURFAPI_DB_PATH", "SQLITE_PATH"); v != "" {
		cfg.SQLite.Path = v
	}
	if v := getenv("WINDSURFAPI_REDIS_ADDR", "REDIS_ADDR"); v != "" {
		cfg.Redis.Addr = v
	}
	if v := getenv("WINDSURFAPI_REDIS_PASSWORD", "REDIS_PASSWORD"); v != "" {
		cfg.Redis.Password = v
	}
	if v := getenv("WINDSURFAPI_REDIS_DB", "REDIS_DB"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			cfg.Redis.DB = n
		}
	}
	if v := getenv("WINDSURFAPI_DIRECT_HOSTS", "DIRECT_HOSTS"); v != "" {
		if hosts := splitCSV(v); len(hosts) > 0 {
			cfg.Direct.Hosts = hosts
		}
	}
	if v := getenv("WINDSURFAPI_DIRECT_TIMEOUT_SECONDS", "DIRECT_TIMEOUT_SECONDS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.Direct.TimeoutSeconds = n
		}
	}
	if v := getenv("WINDSURFAPI_DIRECT_NATIVE_CHAT_PROMPTS", "DIRECT_NATIVE_CHAT_PROMPTS"); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			cfg.Direct.NativeChatPrompts = b
		}
	}
	if v := getenv("WINDSURFAPI_HEALTH_ENABLED", "HEALTH_ENABLED"); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			cfg.Health.Enabled = b
		}
	}
	if v := getenv("WINDSURFAPI_HEALTH_INTERVAL_SECONDS", "HEALTH_INTERVAL_SECONDS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.Health.IntervalSeconds = n
		}
	}
	if v := getenv("WINDSURFAPI_HEALTH_MODEL", "HEALTH_MODEL"); v != "" {
		cfg.Health.Model = v
	}
	if v := getenv("WINDSURFAPI_SCHEDULER_REDIS_ENABLED", "SCHEDULER_REDIS_ENABLED"); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			cfg.Scheduler.RedisEnabled = b
		}
	}
	if v := getenv("WINDSURFAPI_SCHEDULER_REDIS_FAIL_CLOSED", "SCHEDULER_REDIS_FAIL_CLOSED"); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			cfg.Scheduler.RedisFailClosed = b
		}
	}
	if v := getenv("WINDSURFAPI_MAX_INFLIGHT_PER_ACCOUNT", "MAX_INFLIGHT_PER_ACCOUNT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			cfg.Scheduler.MaxInflightPerAccount = n
		}
	}
	if v := getenv("WINDSURFAPI_RESERVATION_TTL_SECONDS", "RESERVATION_TTL_SECONDS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.Scheduler.ReservationTTLSeconds = n
		}
	}
	if v := getenv("WINDSURFAPI_VIRTUAL_CACHE_ENABLED", "VIRTUAL_CACHE_ENABLED"); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			cfg.Usage.VirtualCache.Enabled = b
		}
	}
	if v := getenv("WINDSURFAPI_VIRTUAL_CACHE_MODE", "VIRTUAL_CACHE_MODE"); v != "" {
		cfg.Usage.VirtualCache.Mode = v
	}
	if v := getenv("WINDSURFAPI_VIRTUAL_CACHE_DEFAULT_TTL", "VIRTUAL_CACHE_DEFAULT_TTL"); v != "" {
		cfg.Usage.VirtualCache.DefaultTTL = v
	}
	if v := getenv("WINDSURFAPI_VIRTUAL_CACHE_UNCACHED_INPUT_TOKENS", "VIRTUAL_CACHE_UNCACHED_INPUT_TOKENS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.Usage.VirtualCache.UncachedInputTokens = n
		}
	}
	if v := getenv("WINDSURFAPI_VIRTUAL_CACHE_MIN_INPUT_TOKENS", "VIRTUAL_CACHE_MIN_INPUT_TOKENS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.Usage.VirtualCache.MinInputTokens = n
		}
	}
	if v := getenv("WINDSURFAPI_VIRTUAL_CACHE_MAX_INPUT_TOKENS", "VIRTUAL_CACHE_MAX_INPUT_TOKENS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.Usage.VirtualCache.MaxInputTokens = n
		}
	}
	if v := getenv("WINDSURFAPI_VIRTUAL_CACHE_WARMUP_TOKENS", "VIRTUAL_CACHE_WARMUP_TOKENS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			cfg.Usage.VirtualCache.WarmupTokens = n
		}
	}
	if v := getenv("WINDSURFAPI_VIRTUAL_CACHE_MIN_CREATION_TOKENS", "VIRTUAL_CACHE_MIN_CREATION_TOKENS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			cfg.Usage.VirtualCache.MinCreationTokens = n
		}
	}
	if v := getenv("WINDSURFAPI_VIRTUAL_CACHE_MAX_CREATION_TOKENS", "VIRTUAL_CACHE_MAX_CREATION_TOKENS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			cfg.Usage.VirtualCache.MaxCreationTokens = n
		}
	}
	if v := getenv("WINDSURFAPI_VIRTUAL_CACHE_CREATION_JITTER_RATIO", "VIRTUAL_CACHE_CREATION_JITTER_RATIO"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f >= 0 {
			cfg.Usage.VirtualCache.CreationJitterRatio = f
		}
	}
	if v := getenv("WINDSURFAPI_VIRTUAL_CACHE_BURST_EVERY_TURNS", "VIRTUAL_CACHE_BURST_EVERY_TURNS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			cfg.Usage.VirtualCache.BurstEveryTurns = n
		}
	}
	if v := getenv("WINDSURFAPI_VIRTUAL_CACHE_BURST_MIN_TOKENS", "VIRTUAL_CACHE_BURST_MIN_TOKENS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			cfg.Usage.VirtualCache.BurstMinTokens = n
		}
	}
	if v := getenv("WINDSURFAPI_VIRTUAL_CACHE_BURST_MAX_TOKENS", "VIRTUAL_CACHE_BURST_MAX_TOKENS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			cfg.Usage.VirtualCache.BurstMaxTokens = n
		}
	}
	if v := getenv("WINDSURFAPI_DASHBOARD_PASSWORD", "DASHBOARD_PASSWORD"); v != "" {
		cfg.Dashboard.Password = v
	}
	if v := getenv("WINDSURFAPI_PROXY_DEFAULT", "HTTP_PROXY", "HTTPS_PROXY"); v != "" {
		cfg.Proxy.Default = v
	}
	if v := getenv("WINDSURFAPI_PROXY_DYNAMIC", "PROXY_DYNAMIC"); v != "" {
		cfg.Proxy.Dynamic = splitCSV(v)
	}
	if v := getenv("WINDSURFAPI_PROXY_TEST_URL", "PROXY_TEST_URL"); v != "" {
		cfg.Proxy.TestURL = v
	}
	if v := getenv("WINDSURFAPI_PROXY_COOLDOWN_SECONDS", "PROXY_COOLDOWN_SECONDS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.Proxy.CooldownSeconds = n
		}
	}
	if v := getenv("WINDSURFAPI_PROXY_ROTATE_ON_ERROR", "PROXY_ROTATE_ON_ERROR"); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			cfg.Proxy.RotateOnError = b
		}
	}
	if v := getenv("WINDSURFAPI_PROXY_ALLOW_PRIVATE", "ALLOW_PRIVATE_PROXY_HOSTS"); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			cfg.Proxy.AllowPrivate = b
		}
	}
	if v := getenv("WINDSURFAPI_DYNAMIC_PROXY_ENABLED", "DYNAMIC_PROXY_ENABLED"); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			cfg.Proxy.AccountBinding = b
		}
	}
	if v := getenv("WINDSURFAPI_DYNAMIC_PROXY_AUTO_BIND", "DYNAMIC_PROXY_AUTO_BIND"); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			cfg.Proxy.AutoBindNew = b
		}
	}
	if v := getenv("WINDSURFAPI_DYNAMIC_PROXY_RENEW_BEFORE_MS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			cfg.Proxy.RenewBeforeMS = n
		}
	}
	if v := getenv("WINDSURFAPI_DYNAMIC_PROXY_MAX_BIND_RETRIES"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.Proxy.MaxBindRetries = n
		}
	}
	if v := getenv("WINDSURFAPI_DYNAMIC_PROXY_WORKER_INTERVAL_MS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.Proxy.WorkerIntervalMS = n
		}
	}
	if v := getenv("WINDSURFAPI_DYNAMIC_PROXY_WORKER_BATCH_SIZE"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			cfg.Proxy.WorkerBatchSize = n
		}
	}
	if v := getenv("WINDSURFAPI_DYNAMIC_PROXY_WORKER_CONCURRENCY"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.Proxy.WorkerConcurrency = n
		}
	}
	if v := getenv("WINDSURFAPI_DYNAMIC_PROXY_PROVIDER"); v != "" {
		cfg.Proxy.Provider = v
	}
	if v := getenv("WINDSURFAPI_DYNAMIC_PROXY_PROTOCOL"); v != "" {
		cfg.Proxy.Protocol = v
	}
	if v := getenv("WINDSURFAPI_DYNAMIC_PROXY_HOST"); v != "" {
		cfg.Proxy.Host = v
	}
	if v := getenv("WINDSURFAPI_DYNAMIC_PROXY_PORT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.Proxy.Port = n
		}
	}
	if v := getenv("WINDSURFAPI_DYNAMIC_PROXY_USERNAME_TEMPLATE"); v != "" {
		cfg.Proxy.UsernameTemplate = v
	}
	if v := getenv("WINDSURFAPI_DYNAMIC_PROXY_PASSWORD"); v != "" {
		cfg.Proxy.Password = v
	}
	if v := getenv("WINDSURFAPI_DYNAMIC_PROXY_REGION"); v != "" {
		cfg.Proxy.Region = v
	}
	if v := getenv("WINDSURFAPI_DYNAMIC_PROXY_STATE"); v != "" {
		cfg.Proxy.State = v
	}
	if v := getenv("WINDSURFAPI_DYNAMIC_PROXY_TTL_MINUTES"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.Proxy.TTLMinutes = n
		}
	}
	if v := getenv("WINDSURFAPI_LOG_LEVEL", "LOG_LEVEL"); v != "" {
		cfg.Log.Level = v
	}
}

func getenv(names ...string) string {
	for _, name := range names {
		if v := strings.TrimSpace(os.Getenv(name)); v != "" {
			return v
		}
	}
	return ""
}

func splitCSV(raw string) []string {
	var out []string
	for _, part := range strings.Split(raw, ",") {
		if v := strings.TrimSpace(part); v != "" {
			out = append(out, v)
		}
	}
	return out
}
