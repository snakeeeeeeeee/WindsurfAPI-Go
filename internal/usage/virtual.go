package usage

import (
	"crypto/sha256"
	"encoding/binary"
	"strings"
	"sync"
	"time"

	"github.com/zhangyu/windsurfapi-go/internal/config"
	"github.com/zhangyu/windsurfapi-go/internal/windsurf"
)

type VirtualCacheConfig = config.VirtualCacheUsageConfig

type Route string

const (
	RouteOpenAI    Route = "openai"
	RouteAnthropic Route = "anthropic"
	RouteResponses Route = "responses"
)

type Input struct {
	AccountID             int
	Model                 string
	CallerKeyHash         string
	Route                 string
	ObservedInputTokens   uint64
	EstimatedInputTokens  uint64
	OutputTokens          uint64
	RequestedCacheTTL     string
	EstimatedUserDeltaTok uint64
}

type Usage struct {
	InputTokens              uint64
	OutputTokens             uint64
	CacheReadInputTokens     uint64
	CacheCreationInputTokens uint64
	Ephemeral5mInputTokens   uint64
	Ephemeral1hInputTokens   uint64
	Virtual                  bool
}

type key struct {
	AccountID     int
	Model         string
	CallerKeyHash string
	Route         string
}

type entry struct {
	Cached5mTokens          uint64
	Cached5mExpiresAt       time.Time
	Cached1hTokens          uint64
	Cached1hExpiresAt       time.Time
	LastObservedInputTokens uint64
	TurnCount               uint64
}

type Manager struct {
	mu      sync.Mutex
	cfg     VirtualCacheConfig
	ledgers map[key]entry
}

func NewManager(cfg VirtualCacheConfig) *Manager {
	return &Manager{cfg: normalizeConfig(cfg), ledgers: map[key]entry{}}
}

func (m *Manager) SetConfig(cfg VirtualCacheConfig) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.ledgers == nil {
		m.ledgers = map[key]entry{}
	}
	m.cfg = normalizeConfig(cfg)
	if !m.cfg.Enabled {
		m.ledgers = map[key]entry{}
	}
}

func (m *Manager) Config() VirtualCacheConfig {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cfg
}

func (m *Manager) Build(input Input) Usage {
	if m == nil {
		return baseUsage(input)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.ledgers == nil {
		m.ledgers = map[key]entry{}
	}
	cfg := normalizeConfig(m.cfg)
	if !cfg.Enabled {
		return baseUsage(input)
	}
	now := time.Now()
	observed := firstPositive(input.ObservedInputTokens, input.EstimatedInputTokens)
	output := input.OutputTokens
	ledgerKey := key{
		AccountID:     input.AccountID,
		Model:         strings.TrimSpace(input.Model),
		CallerKeyHash: strings.TrimSpace(input.CallerKeyHash),
		Route:         strings.TrimSpace(input.Route),
	}
	if ledgerKey.Model == "" {
		ledgerKey.Model = "unknown"
	}
	if ledgerKey.CallerKeyHash == "" {
		ledgerKey.CallerKeyHash = "anonymous"
	}
	if ledgerKey.Route == "" {
		ledgerKey.Route = "default"
	}

	e := m.ledgers[ledgerKey]
	expire(&e, now)
	read := e.Cached5mTokens + e.Cached1hTokens
	uncached := computeUncached(cfg, input, observed)
	creation := computeCreation(cfg, input, e, observed, uncached)
	ttl := normalizeTTL(firstNonEmpty(input.RequestedCacheTTL, cfg.DefaultTTL))

	if creation > 0 {
		switch ttl {
		case "1h":
			e.Cached1hTokens += creation
			e.Cached1hExpiresAt = now.Add(time.Hour)
		default:
			e.Cached5mTokens += creation
			e.Cached5mExpiresAt = now.Add(5 * time.Minute)
		}
	}
	if observed > e.LastObservedInputTokens {
		e.LastObservedInputTokens = observed
	}
	e.TurnCount++
	m.ledgers[ledgerKey] = e

	u := Usage{
		InputTokens:              uncached,
		OutputTokens:             output,
		CacheReadInputTokens:     read,
		CacheCreationInputTokens: creation,
		Virtual:                  true,
	}
	if ttl == "1h" {
		u.Ephemeral1hInputTokens = creation
	} else {
		u.Ephemeral5mInputTokens = creation
	}
	return u
}

func FromUpstream(u *windsurf.Usage, fallbackInput uint64) Usage {
	input := fallbackInput
	output := uint64(0)
	if u != nil {
		if u.InputTokens > 0 {
			input = u.InputTokens
		}
		output = u.OutputTokens
	}
	return Usage{InputTokens: input, OutputTokens: output}
}

func OpenAIMap(u Usage) map[string]any {
	out := map[string]any{
		"prompt_tokens":     u.InputTokens,
		"completion_tokens": u.OutputTokens,
		"total_tokens":      u.InputTokens + u.OutputTokens,
	}
	if u.Virtual {
		out["cache_read_input_tokens"] = u.CacheReadInputTokens
		out["cache_creation_input_tokens"] = u.CacheCreationInputTokens
		out["cache_creation"] = map[string]any{
			"ephemeral_5m_input_tokens": u.Ephemeral5mInputTokens,
			"ephemeral_1h_input_tokens": u.Ephemeral1hInputTokens,
		}
		if u.CacheReadInputTokens > 0 {
			out["prompt_tokens_details"] = map[string]any{"cached_tokens": u.CacheReadInputTokens}
		}
	}
	return out
}

func AnthropicMap(u Usage) map[string]any {
	out := map[string]any{
		"input_tokens":                u.InputTokens,
		"output_tokens":               u.OutputTokens,
		"cache_creation_input_tokens": uint64(0),
		"cache_read_input_tokens":     uint64(0),
		"cache_creation":              map[string]any{"ephemeral_5m_input_tokens": uint64(0), "ephemeral_1h_input_tokens": uint64(0)},
	}
	if u.Virtual {
		out["cache_creation_input_tokens"] = u.CacheCreationInputTokens
		out["cache_read_input_tokens"] = u.CacheReadInputTokens
		out["cache_creation"] = map[string]any{
			"ephemeral_5m_input_tokens": u.Ephemeral5mInputTokens,
			"ephemeral_1h_input_tokens": u.Ephemeral1hInputTokens,
		}
	}
	return out
}

func baseUsage(input Input) Usage {
	return Usage{InputTokens: firstPositive(input.ObservedInputTokens, input.EstimatedInputTokens), OutputTokens: input.OutputTokens}
}

func computeUncached(cfg VirtualCacheConfig, input Input, observed uint64) uint64 {
	if observed == 0 {
		return 0
	}
	if strings.EqualFold(strings.TrimSpace(cfg.Mode), "dynamic") {
		estimated := firstPositive(input.EstimatedUserDeltaTok, input.EstimatedInputTokens, uint64(cfg.UncachedInputTokens))
		return clamp(estimated, uint64(cfg.MinInputTokens), uint64(cfg.MaxInputTokens), observed)
	}
	return clamp(uint64(cfg.UncachedInputTokens), uint64(cfg.MinInputTokens), uint64(cfg.MaxInputTokens), observed)
}

func computeCreation(cfg VirtualCacheConfig, input Input, e entry, observed, uncached uint64) uint64 {
	if observed == 0 {
		return 0
	}
	var creation uint64
	if e.TurnCount == 0 {
		if observed > uncached {
			creation = observed - uncached
		}
		if warmup := uint64(cfg.WarmupTokens); creation < warmup {
			creation = warmup
		}
	} else {
		if observed > e.LastObservedInputTokens {
			creation = observed - e.LastObservedInputTokens
		}
		if strings.EqualFold(strings.TrimSpace(cfg.Mode), "dynamic") {
			if input.EstimatedUserDeltaTok > creation {
				creation = input.EstimatedUserDeltaTok
			}
			creation += input.OutputTokens / 2
			creation = applyJitter(cfg, input, e.TurnCount+1, creation)
			if cfg.BurstEveryTurns > 0 && cfg.BurstMaxTokens > 0 && (e.TurnCount+1)%uint64(cfg.BurstEveryTurns) == 0 {
				creation += deterministicRange(input, e.TurnCount+1, "burst", uint64(cfg.BurstMinTokens), uint64(cfg.BurstMaxTokens))
			}
		}
		if min := uint64(cfg.MinCreationTokens); creation < min {
			creation = min
		}
	}
	if max := uint64(cfg.MaxCreationTokens); max > 0 && creation > max {
		creation = max
	}
	return creation
}

func applyJitter(cfg VirtualCacheConfig, input Input, turn uint64, value uint64) uint64 {
	if value == 0 || cfg.CreationJitterRatio <= 0 {
		return value
	}
	ratio := cfg.CreationJitterRatio
	if ratio > 1 {
		ratio = 1
	}
	spread := uint64(float64(value)*ratio + 0.5)
	if spread == 0 {
		return value
	}
	offset := deterministicRange(input, turn, "jitter", 0, spread*2)
	if offset <= spread {
		if offset > value {
			return 0
		}
		return value - offset
	}
	return value + (offset - spread)
}

func expire(e *entry, now time.Time) {
	if !e.Cached5mExpiresAt.IsZero() && !e.Cached5mExpiresAt.After(now) {
		e.Cached5mTokens = 0
		e.Cached5mExpiresAt = time.Time{}
	}
	if !e.Cached1hExpiresAt.IsZero() && !e.Cached1hExpiresAt.After(now) {
		e.Cached1hTokens = 0
		e.Cached1hExpiresAt = time.Time{}
	}
}

func normalizeConfig(cfg VirtualCacheConfig) VirtualCacheConfig {
	if strings.TrimSpace(cfg.Mode) == "" {
		cfg.Mode = "conservative"
	}
	if strings.TrimSpace(cfg.DefaultTTL) == "" {
		cfg.DefaultTTL = "5m"
	}
	cfg.DefaultTTL = normalizeTTL(cfg.DefaultTTL)
	if cfg.UncachedInputTokens <= 0 {
		cfg.UncachedInputTokens = 64
	}
	if cfg.MinInputTokens <= 0 {
		cfg.MinInputTokens = 1
	}
	if cfg.MaxInputTokens <= 0 {
		cfg.MaxInputTokens = 4096
	}
	if cfg.MaxCreationTokens <= 0 {
		cfg.MaxCreationTokens = 8192
	}
	if cfg.CreationJitterRatio < 0 {
		cfg.CreationJitterRatio = 0
	}
	return cfg
}

func normalizeTTL(ttl string) string {
	switch strings.ToLower(strings.TrimSpace(ttl)) {
	case "1h", "hour", "one_hour":
		return "1h"
	default:
		return "5m"
	}
}

func clamp(v, min, max, observed uint64) uint64 {
	if min > 0 && v < min {
		v = min
	}
	if max > 0 && v > max {
		v = max
	}
	if observed > 0 && v > observed {
		v = observed
	}
	return v
}

func deterministicRange(input Input, turn uint64, salt string, min, max uint64) uint64 {
	if max <= min {
		return min
	}
	h := sha256.New()
	var buf [16]byte
	binary.LittleEndian.PutUint64(buf[:8], uint64(input.AccountID))
	binary.LittleEndian.PutUint64(buf[8:], turn)
	h.Write(buf[:])
	h.Write([]byte(input.Model))
	h.Write([]byte(input.CallerKeyHash))
	h.Write([]byte(input.Route))
	h.Write([]byte(salt))
	sum := h.Sum(nil)
	n := binary.LittleEndian.Uint64(sum[:8])
	return min + n%(max-min+1)
}

func firstPositive(values ...uint64) uint64 {
	for _, v := range values {
		if v > 0 {
			return v
		}
	}
	return 0
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
