package config

import "testing"

func TestApplyRuntimeDefaultsPrefersEnvLSBinaryPath(t *testing.T) {
	t.Setenv("LS_BINARY_PATH", "/tmp/test-language-server")

	cfg := &Config{}
	applyRuntimeDefaults(cfg)

	if cfg.LS.BinaryPath != "/tmp/test-language-server" {
		t.Fatalf("BinaryPath = %q, want env value", cfg.LS.BinaryPath)
	}
}

func TestApplyRuntimeDefaultsKeepsConfiguredLSBinaryPath(t *testing.T) {
	t.Setenv("LS_BINARY_PATH", "/tmp/env-language-server")

	cfg := &Config{LS: LSConfig{BinaryPath: "/tmp/config-language-server"}}
	applyRuntimeDefaults(cfg)

	if cfg.LS.BinaryPath != "/tmp/config-language-server" {
		t.Fatalf("BinaryPath = %q, want configured value", cfg.LS.BinaryPath)
	}
}

func TestApplyRuntimeDefaultsAppliesEnvOverrides(t *testing.T) {
	t.Setenv("WINDSURFAPI_PORT", "4567")
	t.Setenv("WINDSURFAPI_API_KEYS", "sk-a, sk-b")
	t.Setenv("WINDSURFAPI_MAX_REQUEST_BODY_BYTES", "123456")
	t.Setenv("WINDSURFAPI_DB_PATH", "/tmp/windsurf.db")
	t.Setenv("WINDSURFAPI_DIRECT_HOSTS", "server.one,server.two")
	t.Setenv("WINDSURFAPI_DIRECT_TIMEOUT_SECONDS", "45")
	t.Setenv("WINDSURFAPI_DIRECT_NATIVE_CHAT_PROMPTS", "true")
	t.Setenv("WINDSURFAPI_SCHEDULER_REDIS_ENABLED", "true")
	t.Setenv("WINDSURFAPI_SCHEDULER_REDIS_FAIL_CLOSED", "true")
	t.Setenv("WINDSURFAPI_MAX_INFLIGHT_PER_ACCOUNT", "8")
	t.Setenv("WINDSURFAPI_PROXY_DEFAULT", "http://proxy.local:8080")
	t.Setenv("WINDSURFAPI_PROXY_DYNAMIC", "http://proxy-a:8080,http://proxy-b:8080")
	t.Setenv("WINDSURFAPI_PROXY_TEST_URL", "https://example.test/")
	t.Setenv("WINDSURFAPI_PROXY_COOLDOWN_SECONDS", "77")
	t.Setenv("WINDSURFAPI_PROXY_ROTATE_ON_ERROR", "false")
	t.Setenv("WINDSURFAPI_PROXY_ALLOW_PRIVATE", "true")

	cfg := &Config{}
	applyRuntimeDefaults(cfg)

	if cfg.Server.Port != 4567 {
		t.Fatalf("port=%d", cfg.Server.Port)
	}
	if len(cfg.Server.APIKeys) != 2 || cfg.Server.APIKeys[0] != "sk-a" || cfg.Server.APIKeys[1] != "sk-b" {
		t.Fatalf("api keys=%v", cfg.Server.APIKeys)
	}
	if cfg.Server.MaxRequestBodyBytes != 123456 {
		t.Fatalf("max request body=%d", cfg.Server.MaxRequestBodyBytes)
	}
	if cfg.SQLite.Path != "/tmp/windsurf.db" {
		t.Fatalf("db path=%q", cfg.SQLite.Path)
	}
	if len(cfg.Direct.Hosts) != 2 || cfg.Direct.Hosts[1] != "server.two" {
		t.Fatalf("direct hosts=%v", cfg.Direct.Hosts)
	}
	if cfg.Direct.TimeoutSeconds != 45 {
		t.Fatalf("direct timeout=%d", cfg.Direct.TimeoutSeconds)
	}
	if !cfg.Direct.NativeChatPrompts {
		t.Fatal("expected direct native chat prompts enabled")
	}
	if !cfg.Scheduler.RedisEnabled || !cfg.Scheduler.RedisFailClosed || cfg.Scheduler.MaxInflightPerAccount != 8 {
		t.Fatalf("scheduler=%+v", cfg.Scheduler)
	}
	if cfg.Proxy.Default != "http://proxy.local:8080" {
		t.Fatalf("proxy=%q", cfg.Proxy.Default)
	}
	if len(cfg.Proxy.Dynamic) != 2 || cfg.Proxy.Dynamic[1] != "http://proxy-b:8080" || cfg.Proxy.TestURL != "https://example.test/" || cfg.Proxy.CooldownSeconds != 77 || cfg.Proxy.RotateOnError || !cfg.Proxy.AllowPrivate {
		t.Fatalf("proxy=%+v", cfg.Proxy)
	}
}
