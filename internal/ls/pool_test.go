package ls

import (
	"context"
	"net"
	"os"
	"testing"
	"time"

	"github.com/zhangyu/windsurfapi-go/internal/account"
)

func TestBuildChildEnvFiltersAndProxy(t *testing.T) {
	parent := []string{
		"HOME=/tmp/home",
		"PATH=/bin",
		"SECRET=do-not-leak",
		"AWS_ACCESS_KEY_ID=nope",
	}
	env := BuildChildEnv(parent, "http://proxy.example:8080")
	found := map[string]string{}
	for _, kv := range env {
		for i := 0; i < len(kv); i++ {
			if kv[i] == '=' {
				found[kv[:i]] = kv[i+1:]
				break
			}
		}
	}
	if found["HOME"] != "/tmp/home" || found["PATH"] != "/bin" {
		t.Fatalf("allowlist missing: %+v", found)
	}
	if _, leak := found["SECRET"]; leak {
		t.Fatal("SECRET must be filtered out")
	}
	if _, leak := found["AWS_ACCESS_KEY_ID"]; leak {
		t.Fatal("AWS_ACCESS_KEY_ID must be filtered out")
	}
	if found["HTTPS_PROXY"] != "http://proxy.example:8080" || found["http_proxy"] != "http://proxy.example:8080" {
		t.Fatalf("proxy not set: %+v", found)
	}
}

func TestPortInUseAndWaitReady(t *testing.T) {
	// 起一个临时 TCP listener，验证 portInUse + waitPortReady
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer l.Close()
	port := l.Addr().(*net.TCPAddr).Port
	if !portInUse(port) {
		t.Fatalf("portInUse should be true")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := waitPortReady(ctx, port, 2*time.Second); err != nil {
		t.Fatalf("waitPortReady: %v", err)
	}
}

func TestEnsureDefaultErrorsWithoutBinary(t *testing.T) {
	p := NewPool(Config{}) // BinaryPath 空
	_, err := p.EnsureDefault(context.Background())
	if err == nil {
		t.Fatal("expected error when binary path missing")
	}
}

func TestProxyKeySeparatesProxyAndDefault(t *testing.T) {
	if ProxyKey("") != "default" {
		t.Fatal("empty proxy should map to default")
	}
	a := ProxyKey("http://user-a:pass@proxy.example:8080")
	b := ProxyKey("http://user-b:pass@proxy.example:8080")
	if a == b {
		t.Fatalf("sticky users should produce different keys: %s", a)
	}
	if RedactProxy("http://user:pass@proxy.example:8080") != "http://%2A%2A%2A@proxy.example:8080" {
		t.Fatalf("proxy redaction changed")
	}
}

func TestAccountKeySeparatesAccountsWithoutProxy(t *testing.T) {
	a := &account.Account{ID: 1}
	b := &account.Account{ID: 2}
	if AccountKey(a) == AccountKey(b) {
		t.Fatalf("different accounts without proxy should use different LS keys")
	}
	if AccountKey(a) != "acct_1" {
		t.Fatalf("unexpected account key: %s", AccountKey(a))
	}
}

func TestAccountKeyKeepsProxyForChildEnv(t *testing.T) {
	a := &account.Account{ID: 9, ProxyURL: "http://user:pass@proxy.example:8080"}
	key := AccountKey(a)
	if key != "acct_9" {
		t.Fatalf("unexpected account key: %s", key)
	}
	if got := proxyURLFromKey(key); got != a.ProxyURL {
		t.Fatalf("proxyURLFromKey(%s)=%q want %q", key, got, a.ProxyURL)
	}
}

func TestSpawnRealBinaryIfAvailable(t *testing.T) {
	// 仅在 LS_TEST_BINARY 指定真机二进制时跑，CI 默认 skip
	bin := os.Getenv("LS_TEST_BINARY")
	if bin == "" {
		t.Skip("set LS_TEST_BINARY to run real LS spawn test")
	}
	tmp := t.TempDir()
	p := NewPool(Config{BinaryPath: bin, DataRoot: tmp, ReadyTimeout: 30 * time.Second})
	defer p.StopAll()
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	entry, err := p.EnsureDefault(ctx)
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if entry.Port == 0 || entry.CSRFToken == "" || entry.SessionID == "" {
		t.Fatalf("missing fields: %+v", entry)
	}
	if !portInUse(entry.Port) {
		t.Fatalf("LS should listen on port %d", entry.Port)
	}
}
