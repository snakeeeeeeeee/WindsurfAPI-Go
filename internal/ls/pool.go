// Package ls 管理本地 Windsurf language_server 二进制子进程。
// 参考 WindsurfAPI/src/langserver.js（667 行）；Go 版按账号/代理隔离 LS，
// 避免多个账号共用同一个 panel state。
package ls

import (
	"context"
	"crypto/rand"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/zhangyu/windsurfapi-go/internal/account"
)

const (
	// DefaultAPIServerURL 官方上游。
	DefaultAPIServerURL = "https://server.self-serve.windsurf.com"
	// DefaultRegisterURL 官方注册。
	DefaultRegisterURL = "https://api.codeium.com/register_user/"
	// DefaultCSRF 固定值；Node 版同样硬编码。
	DefaultCSRF = "windsurf-api-csrf-fixed-token"
	// DefaultPort 首个 LS 的端口。
	DefaultPort = 42100

	managerStableAfterStart = 6 * time.Second
)

// Config 描述 pool 的启动参数。留空则走各字段的默认值。
type Config struct {
	BinaryPath   string // 必填；空时 EnsureDefault 直接报错
	DataRoot     string // LS 的 codeium_dir / database_dir 根；空默认 ./data/ls
	APIServerURL string
	RegisterURL  string
	CSRFToken    string
	DefaultPort  int
	MaxInstances int
	ReadyTimeout time.Duration // 等待 LS 端口就绪的最长时间
}

func (c *Config) fillDefaults() {
	if c.APIServerURL == "" {
		c.APIServerURL = DefaultAPIServerURL
	}
	if c.RegisterURL == "" {
		c.RegisterURL = DefaultRegisterURL
	}
	if c.CSRFToken == "" {
		c.CSRFToken = DefaultCSRF
	}
	if c.DefaultPort == 0 {
		c.DefaultPort = DefaultPort
	}
	if c.DataRoot == "" {
		c.DataRoot = "./data/ls"
	}
	if c.MaxInstances <= 0 {
		c.MaxInstances = 16
	}
	if c.ReadyTimeout == 0 {
		c.ReadyTimeout = 25 * time.Second
	}
}

// Entry 代表池里某个 LS 实例的可见状态。只读字段，外部不要改。
type Entry struct {
	Key        string // "default" 或 proxy key（后续）
	Port       int
	CSRFToken  string
	DataDir    string
	SessionID  string // 按 LS 生命周期唯一；Node 里是 randomUUID，本处同
	Generation string
	ProxyURL   string

	startedAt time.Time
	ready     bool
	proc      *exec.Cmd

	// WorkspaceInit 保证每 LS 只做一次 panel init；sync.Once 的错误版本。
	initOnce sync.Once
	initErr  error
}

// RunInit 幂等地运行 workspace init 回调，只执行一次。
// 回调里可能需要调 gRPC，所以允许返回错误；失败的话下一次还会重试（sync.Once + 错误模式）。
func (e *Entry) RunInit(f func() error) error {
	e.initOnce.Do(func() {
		e.initErr = f()
	})
	// 一次失败想重试：重置 once。Node 版 workspaceInit 字段也是成功之后才缓存。
	if e.initErr != nil {
		// 尝试重置 once 让下次重跑
		e.initOnce = sync.Once{}
	}
	return e.initErr
}

// StartedAt 暴露启动时间，dashboard 用。
func (e *Entry) StartedAt() time.Time { return e.startedAt }

func (e *Entry) Ready() bool { return e.ready }

// Pool 管理一组 LS 实例。当前只有 default 实例；保留 map 方便扩展。
type Pool struct {
	cfg Config

	mu       sync.Mutex
	entries  map[string]*Entry
	pending  map[string]chan struct{} // 合并并发 ensure
	closed   bool
	nextPort int
}

// NewPool 创建池实例。不会启动任何 LS —— lazy，第一次 Ensure* 时才 spawn。
func NewPool(cfg Config) *Pool {
	cfg.fillDefaults()
	return &Pool{
		cfg:      cfg,
		entries:  map[string]*Entry{},
		pending:  map[string]chan struct{}{},
		nextPort: cfg.DefaultPort + 1,
	}
}

// Cfg 暴露只读的配置。
func (p *Pool) Cfg() Config { return p.cfg }

// EnsureDefault 确保 default LS 就绪并返回它。并发调用会合并到同一次 spawn。
func (p *Pool) EnsureDefault(ctx context.Context) (*Entry, error) {
	return p.ensure(ctx, "default")
}

func (p *Pool) EnsureForAccount(ctx context.Context, a *account.Account) (*Entry, error) {
	if a == nil {
		return p.EnsureDefault(ctx)
	}
	return p.ensure(ctx, AccountKey(a))
}

func (p *Pool) EnsureForProxy(ctx context.Context, proxyURL string) (*Entry, error) {
	return p.ensure(ctx, ProxyKey(proxyURL))
}

func (p *Pool) GetByKey(key string) *Entry {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.entries[key]
}

func (p *Pool) Snapshot() []map[string]any {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]map[string]any, 0, len(p.entries))
	for _, e := range p.entries {
		out = append(out, map[string]any{
			"key":        e.Key,
			"port":       e.Port,
			"proxy":      RedactProxy(e.ProxyURL),
			"started_at": e.startedAt.Format(time.RFC3339),
			"generation": e.Generation,
			"ready":      e.ready,
		})
	}
	return out
}

func (p *Pool) RestartKey(ctx context.Context, key string) error {
	var old *Entry
	p.mu.Lock()
	if e, ok := p.entries[key]; ok {
		old = e
		delete(p.entries, key)
	}
	p.mu.Unlock()
	if old != nil && old.proc != nil && old.proc.Process != nil {
		_ = old.proc.Process.Signal(syscall.SIGTERM)
	}
	_, err := p.ensure(ctx, key)
	return err
}

func (p *Pool) ensure(ctx context.Context, key string) (*Entry, error) {
	if p.cfg.BinaryPath == "" {
		return nil, errors.New("ls.binary_path not configured (set ls.binary_path in configs/default.yaml or LS_BINARY_PATH)")
	}

	for {
		p.mu.Lock()
		if p.closed {
			p.mu.Unlock()
			return nil, errors.New("ls pool closed")
		}
		if e, ok := p.entries[key]; ok && e.ready {
			p.mu.Unlock()
			return e, nil
		}
		if wait, ok := p.pending[key]; ok {
			p.mu.Unlock()
			select {
			case <-wait:
				// 结果已落到 p.entries，下一轮循环取
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			continue
		}
		if len(p.entries)+len(p.pending) >= p.cfg.MaxInstances {
			p.mu.Unlock()
			return nil, fmt.Errorf("ls max_instances=%d reached", p.cfg.MaxInstances)
		}
		// 成为 spawner
		wait := make(chan struct{})
		p.pending[key] = wait
		p.mu.Unlock()

		entry, err := p.spawn(ctx, key)

		p.mu.Lock()
		delete(p.pending, key)
		if err == nil {
			p.entries[key] = entry
		}
		close(wait)
		p.mu.Unlock()

		if err != nil {
			return nil, err
		}
		return entry, nil
	}
}

func (p *Pool) spawn(ctx context.Context, key string) (*Entry, error) {
	isDefault := key == "default"
	var port int
	if isDefault {
		port = p.cfg.DefaultPort
		// 占用检测：有人占了 default port 就往后挪（NOT adopt，安全考量）
		if portInUse(port) {
			log.Printf("LS default port %d already in use; moving to next free port", port)
			port = p.claimNextPort()
		}
	} else {
		port = p.claimNextPort()
	}

	dataDir := filepath.Join(p.cfg.DataRoot, key)
	if err := os.MkdirAll(filepath.Join(dataDir, "db"), 0o755); err != nil {
		return nil, fmt.Errorf("mkdir %s: %w", dataDir, err)
	}

	// 绝对路径：LS 有些子模块会按 $PWD 解析路径，相对路径在 systemd 下会错位。
	absDataDir, _ := filepath.Abs(dataDir)
	binary := p.cfg.BinaryPath
	if _, err := os.Stat(binary); err != nil {
		return nil, fmt.Errorf("ls binary not found at %s: %w", binary, err)
	}

	args := []string{
		"--api_server_url=" + p.cfg.APIServerURL,
		"--server_port=" + strconv.Itoa(port),
		"--csrf_token=" + p.cfg.CSRFToken,
		"--register_user_url=" + p.cfg.RegisterURL,
		"--codeium_dir=" + absDataDir,
		"--database_dir=" + filepath.Join(absDataDir, "db"),
		"--detect_proxy=false",
	}

	proxyURL := proxyURLFromKey(key)
	cmd := exec.Command(binary, args...)
	cmd.Env = BuildChildEnv(os.Environ(), proxyURL)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	stdout, _ := cmd.StdoutPipe()
	stderr, _ := cmd.StderrPipe()

	log.Printf("Starting LS key=%s port=%d binary=%s", key, port, binary)
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("spawn ls: %w", err)
	}

	// 按行转发日志，对齐 Node 版 stdout/stderr.on('data')
	go streamLSLog(key, stdout, false)
	go streamLSLog(key, stderr, true)

	// 独立 goroutine 等退出，清理池
	entry := &Entry{
		Key:        key,
		Port:       port,
		CSRFToken:  p.cfg.CSRFToken,
		DataDir:    absDataDir,
		SessionID:  newSessionID(),
		Generation: newSessionID(),
		ProxyURL:   proxyURL,
		startedAt:  time.Now(),
		proc:       cmd,
	}
	go p.waitExit(key, entry)

	// 等待 TCP 端口可连
	if err := waitPortReady(ctx, port, p.cfg.ReadyTimeout); err != nil {
		_ = cmd.Process.Kill()
		return nil, fmt.Errorf("ls port %d not ready: %w", port, err)
	}
	if err := waitManagerStable(ctx, cmd, port, entry.startedAt); err != nil {
		_ = cmd.Process.Kill()
		return nil, err
	}
	entry.ready = true
	log.Printf("LS key=%s ready on port %d", key, port)
	return entry, nil
}

var proxyKeyMap sync.Map // key -> raw proxy URL

func AccountKey(a *account.Account) string {
	if a == nil {
		return "default"
	}
	if a.ID > 0 {
		key := "acct_" + strconv.Itoa(a.ID)
		if strings.TrimSpace(a.ProxyURL) != "" {
			proxyKeyMap.Store(key, strings.TrimSpace(a.ProxyURL))
		}
		return key
	}
	return ProxyKey(a.ProxyURL)
}

func ProxyKey(proxyURL string) string {
	proxyURL = strings.TrimSpace(proxyURL)
	if proxyURL == "" {
		return "default"
	}
	u, err := url.Parse(proxyURL)
	if err != nil || u.Host == "" {
		return "px_" + sanitizeKey(proxyURL)
	}
	host := sanitizeKey(u.Hostname())
	port := u.Port()
	if port == "" {
		port = "8080"
	}
	key := "px_" + host + "_" + sanitizeKey(port)
	if u.User != nil {
		user := u.User.Username()
		if user != "" {
			key += "_u" + sanitizeKey(user)
		}
	}
	if len(key) > 80 {
		sum := sha1.Sum([]byte(proxyURL))
		key = key[:48] + "_" + hex.EncodeToString(sum[:])[:16]
	}
	proxyKeyMap.Store(key, proxyURL)
	return key
}

func proxyURLFromKey(key string) string {
	if key == "default" {
		return ""
	}
	if v, ok := proxyKeyMap.Load(key); ok {
		return v.(string)
	}
	return ""
}

func sanitizeKey(s string) string {
	if s == "" {
		return "empty"
	}
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	return strings.Trim(b.String(), "_")
}

func RedactProxy(raw string) string {
	if raw == "" {
		return "none"
	}
	u, err := url.Parse(raw)
	if err != nil {
		return sanitizeKey(raw)
	}
	if u.User != nil {
		u.User = url.User("***")
	}
	return u.String()
}

func (p *Pool) claimNextPort() int {
	p.mu.Lock()
	for {
		port := p.nextPort
		p.nextPort++
		if !portInUse(port) {
			p.mu.Unlock()
			return port
		}
	}
}

func (p *Pool) waitExit(key string, entry *Entry) {
	err := entry.proc.Wait()
	p.mu.Lock()
	defer p.mu.Unlock()
	if cur, ok := p.entries[key]; ok && cur == entry {
		delete(p.entries, key)
	}
	if err != nil {
		log.Printf("LS key=%s exited: %v", key, err)
	} else {
		log.Printf("LS key=%s exited cleanly", key)
	}
}

// StopAll 终止池内所有 LS 进程。阻塞直到每个进程退出或 1.5s 超时后 SIGKILL。
func (p *Pool) StopAll() {
	p.mu.Lock()
	p.closed = true
	entries := make([]*Entry, 0, len(p.entries))
	for _, e := range p.entries {
		entries = append(entries, e)
	}
	p.entries = map[string]*Entry{}
	p.mu.Unlock()

	var wg sync.WaitGroup
	for _, e := range entries {
		wg.Add(1)
		go func(e *Entry) {
			defer wg.Done()
			done := make(chan struct{})
			go func() {
				_ = e.proc.Wait()
				close(done)
			}()
			_ = e.proc.Process.Signal(syscall.SIGTERM)
			select {
			case <-done:
			case <-time.After(1500 * time.Millisecond):
				_ = e.proc.Process.Kill()
			}
		}(e)
	}
	wg.Wait()
}

func portInUse(port int) bool {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 500*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

func waitPortReady(ctx context.Context, port int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timeout after %s", timeout)
		}
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 1*time.Second)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func waitManagerStable(ctx context.Context, cmd *exec.Cmd, port int, started time.Time) error {
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if cmd.ProcessState != nil && cmd.ProcessState.Exited() {
			return fmt.Errorf("ls manager exited during startup: %s", cmd.ProcessState.String())
		}
		if cmd.Process != nil {
			if err := cmd.Process.Signal(syscall.Signal(0)); err != nil {
				return fmt.Errorf("ls manager not alive during startup: %w", err)
			}
		}
		if time.Since(started) >= managerStableAfterStart {
			if !portInUse(port) {
				return fmt.Errorf("ls port %d closed during startup", port)
			}
			return nil
		}
		timer := time.NewTimer(250 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func newSessionID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("00000000-0000-4000-8000-%012d", time.Now().UnixNano()%1_000_000_000_000)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
