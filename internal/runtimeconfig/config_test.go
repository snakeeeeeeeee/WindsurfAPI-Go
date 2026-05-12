package runtimeconfig

import (
	"testing"

	"github.com/zhangyu/windsurfapi-go/internal/config"
)

func TestSnapshotMasksSensitiveFields(t *testing.T) {
	mgr := NewManager(&config.Config{
		Server: config.ServerConfig{Port: 3456, APIKeys: []string{"sk-a", "sk-b"}},
		Redis:  config.RedisConfig{Addr: "redis:6379", Password: "secret", DB: 2},
	})
	snap := mgr.Snapshot()
	if snap.Server.APIKeyCount != 2 || !snap.Redis.PasswordSet {
		t.Fatalf("snapshot=%+v", snap)
	}
	if !snap.Security.APIKeys.Safe || !snap.Security.RedisPassword.Set || snap.Security.RedisPassword.Environment != "WINDSURFAPI_REDIS_PASSWORD" {
		t.Fatalf("security snapshot=%+v", snap.Security)
	}
}

func TestSecuritySnapshotFlagsDefaults(t *testing.T) {
	mgr := NewManager(&config.Config{
		Server:    config.ServerConfig{APIKeys: []string{"sk-windsurf-default"}},
		Dashboard: config.DashboardConfig{Password: "admin"},
	})
	snap := mgr.Snapshot()
	if snap.Security.APIKeys.Safe || snap.Security.DashboardPassword.Safe {
		t.Fatalf("defaults should be unsafe: %+v", snap.Security)
	}
	if snap.Security.APIKeys.Environment != "WINDSURFAPI_API_KEYS" || snap.Security.DashboardPassword.Environment != "WINDSURFAPI_DASHBOARD_PASSWORD" {
		t.Fatalf("env hints missing: %+v", snap.Security)
	}
}

func TestPatchUpdatesMutableRuntimeConfig(t *testing.T) {
	cfg := &config.Config{
		Server:    config.ServerConfig{MaxRequestBodyBytes: 100, APIKeys: []string{"sk-old"}},
		Redis:     config.RedisConfig{Password: "redis-old"},
		Dashboard: config.DashboardConfig{Password: "dash-old"},
		Models:    config.ModelsConfig{DefaultEffort: "medium"},
		Direct:    config.DirectConfig{Hosts: []string{"old"}, TimeoutSeconds: 30},
		Health:    config.HealthConfig{Enabled: true, IntervalSeconds: 300, TimeoutSeconds: 20, Model: "claude-sonnet-4.6"},
		Scheduler: config.SchedulerConfig{MaxInflightPerAccount: 4, ReservationTTLSeconds: 180},
	}
	mgr := NewManager(cfg)
	snap, err := mgr.Patch(Patch{
		Server:    &ServerView{MaxRequestBodyBytes: 200},
		Models:    &ModelsView{DefaultEffort: "xhigh"},
		Direct:    &DirectView{Hosts: []string{" one ", "two"}, TimeoutSeconds: 45, NativeChatPrompts: true},
		Health:    &HealthView{Enabled: false, IntervalSeconds: 60, TimeoutSeconds: 10, Model: "claude-opus-4.6"},
		Scheduler: &SchedulerView{RedisEnabled: true, RedisFailClosed: true, MaxInflightPerAccount: 8, ReservationTTLSeconds: 90},
		Usage:     &UsageView{VirtualCache: VirtualCacheView{Enabled: true, Mode: "dynamic", DefaultTTL: "1h", UncachedInputTokens: 80, MinInputTokens: 2, MaxInputTokens: 5000, WarmupTokens: 100, MinCreationTokens: 5, MaxCreationTokens: 9000, CreationJitterRatio: 0.2, BurstEveryTurns: 4, BurstMinTokens: 50, BurstMaxTokens: 100}},
		Proxy:     &ProxyView{Default: " http://proxy.local:8080 ", Dynamic: []string{" http://proxy-a:8080 "}, RotateOnError: true, TestURL: "https://example.test/", CooldownSeconds: 90, AllowPrivate: true, Provider: "novproxy", Protocol: "http", Host: "proxy.vendor.test", Port: 1000, UsernameTemplate: "user-{region}-{state}-{sid}-{ttl}", Password: "proxy-secret", Region: "US", State: "New Jersey", TTLMinutes: 60},
		Log:       &LogView{Level: "DEBUG"},
		Secrets:   &SecretsPatch{APIKeys: []string{" sk-new ", "sk-next"}, DashboardPassword: " dash-new ", RedisPassword: "redis-new"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if snap.Server.MaxRequestBodyBytes != 200 || snap.Direct.TimeoutSeconds != 45 || snap.Direct.Hosts[0] != "one" || !snap.Scheduler.RedisEnabled || !snap.Scheduler.RedisFailClosed {
		t.Fatalf("snapshot=%+v", snap)
	}
	if snap.Models.DefaultEffort != "xhigh" || mgr.DefaultModelEffort() != "xhigh" || cfg.Models.DefaultEffort != "xhigh" {
		t.Fatalf("models config not patched snap=%+v cfg=%+v", snap.Models, cfg.Models)
	}
	if !snap.Direct.NativeChatPrompts || !cfg.Direct.NativeChatPrompts {
		t.Fatalf("native prompts not patched snap=%+v cfg=%+v", snap.Direct, cfg.Direct)
	}
	if !snap.Usage.VirtualCache.Enabled || snap.Usage.VirtualCache.Mode != "dynamic" || cfg.Usage.VirtualCache.DefaultTTL != "1h" || cfg.Usage.VirtualCache.UncachedInputTokens != 80 {
		t.Fatalf("usage not patched snap=%+v cfg=%+v", snap.Usage, cfg.Usage)
	}
	if cfg.Proxy.Default != "http://proxy.local:8080" || len(cfg.Proxy.Dynamic) != 1 || cfg.Proxy.Dynamic[0] != "http://proxy-a:8080" || cfg.Proxy.TestURL != "https://example.test/" || cfg.Proxy.CooldownSeconds != 90 || !cfg.Proxy.AllowPrivate || cfg.Log.Level != "debug" {
		t.Fatalf("cfg=%+v", cfg)
	}
	if cfg.Proxy.Provider != "novproxy" || cfg.Proxy.Host != "proxy.vendor.test" || cfg.Proxy.Password != "proxy-secret" || !snap.Proxy.PasswordSet || snap.Proxy.Password != "" {
		t.Fatalf("proxy provider config not patched/masked cfg=%+v snap=%+v", cfg.Proxy, snap.Proxy)
	}
	if got := mgr.APIKeys(); len(got) != 2 || got[0] != "sk-new" || got[1] != "sk-next" || mgr.DashboardPassword() != "dash-new" || cfg.Redis.Password != "redis-new" {
		t.Fatalf("secrets not patched cfg=%+v keys=%v pass=%q", cfg, mgr.APIKeys(), mgr.DashboardPassword())
	}
	if snap.Server.APIKeyCount != 2 || !snap.Redis.PasswordSet || !snap.Security.APIKeys.Safe || !snap.Security.DashboardPassword.Safe || !snap.Security.RedisPassword.Set {
		t.Fatalf("secret snapshot=%+v", snap)
	}
}

func TestPatchRejectsInvalidValues(t *testing.T) {
	mgr := NewManager(&config.Config{})
	if _, err := mgr.Patch(Patch{Direct: &DirectView{TimeoutSeconds: 0}}); err == nil {
		t.Fatal("expected direct timeout error")
	}
	if _, err := mgr.Patch(Patch{Secrets: &SecretsPatch{APIKeys: []string{" ", ""}}}); err == nil {
		t.Fatal("expected api keys error")
	}
	if _, err := mgr.Patch(Patch{Usage: &UsageView{VirtualCache: VirtualCacheView{Enabled: true, Mode: "bad", DefaultTTL: "5m", UncachedInputTokens: 1, MinInputTokens: 1, MaxInputTokens: 2}}}); err == nil {
		t.Fatal("expected virtual cache mode error")
	}
}

func TestMaxRequestBodyBytesReflectsRuntimePatch(t *testing.T) {
	cfg := &config.Config{Server: config.ServerConfig{MaxRequestBodyBytes: 100}}
	mgr := NewManager(cfg)
	if got := mgr.MaxRequestBodyBytes(); got != 100 {
		t.Fatalf("initial max body=%d", got)
	}
	if _, err := mgr.Patch(Patch{Server: &ServerView{MaxRequestBodyBytes: 200}}); err != nil {
		t.Fatal(err)
	}
	if got := mgr.MaxRequestBodyBytes(); got != 200 {
		t.Fatalf("patched max body=%d", got)
	}
}
