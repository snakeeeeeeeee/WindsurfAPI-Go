package reuse

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/zhangyu/windsurfapi-go/internal/windsurf"
)

const (
	DefaultTTL = 30 * time.Minute
	maxEntries = 500
)

type Entry struct {
	AccountID    int       `json:"account_id"`
	APIKeyHash   string    `json:"api_key_hash"`
	LSPort       int       `json:"ls_port"`
	LSGeneration string    `json:"ls_generation"`
	CascadeID    string    `json:"cascade_id"`
	SessionID    string    `json:"session_id,omitempty"`
	ModelID      string    `json:"model_id"`
	CallerKey    string    `json:"caller_key"`
	CreatedAt    time.Time `json:"created_at"`
	LastUsedAt   time.Time `json:"last_used_at"`
	ExpiresAt    time.Time `json:"expires_at"`
}

type Pool struct {
	mu      sync.Mutex
	entries map[string]*Entry
	stats   Stats
}

type Stats struct {
	Hits      int `json:"hits"`
	Misses    int `json:"misses"`
	Stores    int `json:"stores"`
	Evictions int `json:"evictions"`
}

func NewPool() *Pool {
	return &Pool{entries: map[string]*Entry{}}
}

func (p *Pool) Checkout(fingerprint, callerKey, modelID string) (*Entry, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	key := poolKey(fingerprint, callerKey, modelID)
	e := p.entries[key]
	if e == nil || time.Now().After(e.ExpiresAt) {
		delete(p.entries, key)
		p.stats.Misses++
		return nil, false
	}
	delete(p.entries, key)
	e.LastUsedAt = time.Now()
	p.stats.Hits++
	return e, true
}

func (p *Pool) Checkin(fingerprint string, entry *Entry, ttl time.Duration) {
	if entry == nil || fingerprint == "" {
		return
	}
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	now := time.Now()
	entry.LastUsedAt = now
	if entry.CreatedAt.IsZero() {
		entry.CreatedAt = now
	}
	entry.ExpiresAt = now.Add(ttl)
	key := poolKey(fingerprint, entry.CallerKey, entry.ModelID)
	p.mu.Lock()
	defer p.mu.Unlock()
	p.entries[key] = entry
	p.stats.Stores++
	p.evictLocked()
}

func (p *Pool) InvalidateLS(lsPort int, generation string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for k, e := range p.entries {
		if e.LSPort == lsPort && (generation == "" || e.LSGeneration == generation) {
			delete(p.entries, k)
		}
	}
}

func (p *Pool) Clear() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := len(p.entries)
	p.entries = map[string]*Entry{}
	if n > 0 {
		p.stats.Evictions += n
	}
	return n
}

func (p *Pool) Stats() Stats {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.stats
}

func (p *Pool) Snapshot() []Entry {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]Entry, 0, len(p.entries))
	for _, e := range p.entries {
		out = append(out, *e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LastUsedAt.After(out[j].LastUsedAt) })
	return out
}

func (p *Pool) evictLocked() {
	if len(p.entries) <= maxEntries {
		return
	}
	type kv struct {
		k string
		t time.Time
	}
	items := make([]kv, 0, len(p.entries))
	for k, e := range p.entries {
		items = append(items, kv{k: k, t: e.LastUsedAt})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].t.Before(items[j].t) })
	for len(p.entries) > maxEntries && len(items) > 0 {
		delete(p.entries, items[0].k)
		items = items[1:]
		p.stats.Evictions++
	}
}

func Fingerprint(modelID, callerKey string, messages []windsurf.ChatMessage) string {
	return FingerprintWithOptions(modelID, callerKey, "", "", "", messages)
}

func FingerprintWithOptions(modelID, callerKey, route, toolsDigest, toolChoiceDigest string, messages []windsurf.ChatMessage) string {
	return fingerprintMessages(modelID, callerKey, route, toolsDigest, toolChoiceDigest, messages)
}

// FingerprintBeforeWithOptions returns the lookup key for a continuation
// request. It hashes the stable conversation before the newest inbound turn.
// For tool continuations, the newest inbound turn can be a contiguous group of
// trailing tool results, so they are dropped together.
func FingerprintBeforeWithOptions(modelID, callerKey, route, toolsDigest, toolChoiceDigest string, messages []windsurf.ChatMessage) string {
	prior := priorTurnsForBefore(messages)
	if len(prior) == 0 {
		return ""
	}
	return fingerprintMessages(modelID, callerKey, route, toolsDigest, toolChoiceDigest, prior)
}

// FingerprintAfterWithOptions returns the key that the next continuation will
// look up after the current assistant result is applied to messages.
func FingerprintAfterWithOptions(modelID, callerKey, route, toolsDigest, toolChoiceDigest string, messages []windsurf.ChatMessage, result *windsurf.ChatResult) string {
	assistant, ok := assistantTurnFromResult(result)
	if !ok {
		return ""
	}
	after := append(append([]windsurf.ChatMessage(nil), messages...), assistant)
	return fingerprintMessages(modelID, callerKey, route, toolsDigest, toolChoiceDigest, after)
}

func fingerprintMessages(modelID, callerKey, route, toolsDigest, toolChoiceDigest string, messages []windsurf.ChatMessage) string {
	var b strings.Builder
	b.WriteString("v2\n")
	b.WriteString(modelID)
	b.WriteString("\n")
	b.WriteString(callerKey)
	b.WriteString("\n")
	b.WriteString(route)
	b.WriteString("\n")
	b.WriteString(toolsDigest)
	b.WriteString("\n")
	b.WriteString(toolChoiceDigest)
	b.WriteString("\n")
	for _, m := range messages {
		role := strings.ToLower(strings.TrimSpace(m.Role))
		b.WriteString(role)
		b.WriteString(":")
		content := m.Content
		if role == "assistant" && len(m.ToolCalls) > 0 {
			// Tool-call assistant turns are replayed inconsistently by clients:
			// some preserve a preamble, others replay content as ""/null. The
			// stable state is the tool call list, so narration is ignored here.
			content = ""
		}
		b.WriteString(normalize(content))
		for _, call := range m.ToolCalls {
			b.WriteString("|tool:")
			b.WriteString(call.Name)
			b.WriteString(":")
			b.WriteString(normalize(canonicalToolArgs(call.ArgumentsJSON)))
		}
		if m.ToolCallID != "" {
			b.WriteString("|tool_result:")
			b.WriteString(m.ToolCallID)
		}
		b.WriteString("\n")
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}

func priorTurnsForBefore(messages []windsurf.ChatMessage) []windsurf.ChatMessage {
	if len(messages) == 0 {
		return nil
	}
	newest := -1
	for i := len(messages) - 1; i >= 0; i-- {
		role := strings.ToLower(strings.TrimSpace(messages[i].Role))
		if role == "user" || role == "tool" {
			newest = i
			break
		}
	}
	if newest <= 0 {
		return nil
	}
	if strings.ToLower(strings.TrimSpace(messages[newest].Role)) != "tool" {
		return append([]windsurf.ChatMessage(nil), messages[:newest]...)
	}
	firstTrailingTool := newest
	for firstTrailingTool > 0 && strings.ToLower(strings.TrimSpace(messages[firstTrailingTool-1].Role)) == "tool" {
		firstTrailingTool--
	}
	if firstTrailingTool <= 0 {
		return nil
	}
	return append([]windsurf.ChatMessage(nil), messages[:firstTrailingTool]...)
}

func assistantTurnFromResult(result *windsurf.ChatResult) (windsurf.ChatMessage, bool) {
	if result == nil {
		return windsurf.ChatMessage{}, false
	}
	if len(result.ToolCalls) > 0 {
		return windsurf.ChatMessage{Role: "assistant", ToolCalls: append([]windsurf.ToolCall(nil), result.ToolCalls...)}, true
	}
	if strings.TrimSpace(result.Text) != "" {
		return windsurf.ChatMessage{Role: "assistant", Content: result.Text}, true
	}
	return windsurf.ChatMessage{}, false
}

func canonicalToolArgs(args string) string {
	args = strings.TrimSpace(args)
	if args == "" {
		return ""
	}
	var v any
	if err := json.Unmarshal([]byte(args), &v); err != nil {
		return args
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return args
	}
	return string(raw)
}

func APIKeyHash(apiKey string) string {
	sum := sha256.Sum256([]byte(apiKey))
	return hex.EncodeToString(sum[:])[:16]
}

func poolKey(fp, callerKey, modelID string) string {
	return modelID + "|" + callerKey + "|" + fp
}

var (
	uuidRe = regexp.MustCompile(`[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}`)
	dateRe = regexp.MustCompile(`\b\d{4}-\d{2}-\d{2}\b`)
	cwdRe  = regexp.MustCompile(`(?im)^\s*(?:[-*]\s*)?(?:working directory|cwd|current working directory)\s*[:：].*$`)
)

func normalize(s string) string {
	out := stripDynamicTags(s)
	out = uuidRe.ReplaceAllString(out, "<uuid>")
	out = dateRe.ReplaceAllString(out, "<date>")
	out = cwdRe.ReplaceAllString(out, "cwd:<cwd>")
	out = strings.TrimSpace(out)
	return strings.Join(strings.Fields(out), " ")
}

func stripDynamicTags(s string) string {
	out := s
	for _, tag := range []string{
		"system-reminder",
		"command-message",
		"command-name",
		"command-args",
		"local-command-stdout",
		"local-command-stderr",
		"user-prompt-submit-hook",
		"analysis",
		"summary",
	} {
		re := regexp.MustCompile(`(?is)<` + regexp.QuoteMeta(tag) + `[^>]*>.*?</` + regexp.QuoteMeta(tag) + `>`)
		out = re.ReplaceAllString(out, "")
	}
	return out
}
