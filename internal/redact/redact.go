package redact

import (
	"regexp"
	"strings"
)

const marker = "[REDACTED]"

var replacements = []struct {
	re   *regexp.Regexp
	repl string
}{
	{regexp.MustCompile(`(?i)\b((?:https?|socks5h?|socks5|socks4)://)([^/\s:@]+):([^@\s/]+)@`), `${1}${2}:***@`},
	{regexp.MustCompile(`(?i)("?(?:authorization|cookie|set-cookie)"?\s*:\s*")[^"]+(")`), `${1}` + marker + `${2}`},
	{regexp.MustCompile(`(?im)(\b(?:Authorization|Cookie|Set-Cookie)\s*[:=]\s*)[^\r\n]+`), `${1}` + marker},
	{regexp.MustCompile(`(?i)\bBearer\s+[A-Za-z0-9._~+/=-]{10,}`), `Bearer ` + marker},
	{regexp.MustCompile(`(?i)("?(?:api[_-]?key|firebase[_-]?token|access[_-]?token|refresh[_-]?token|id[_-]?token|session[_-]?token|devin[_-]?token|dashboard[_-]?password|caller[_-]?key|password|secret)"?\s*:\s*")[^"]+(")`), `${1}` + marker + `${2}`},
	{regexp.MustCompile(`(?i)\b(api[_-]?key|firebase[_-]?token|access[_-]?token|refresh[_-]?token|id[_-]?token|session[_-]?token|devin[_-]?token|dashboard[_-]?password|caller[_-]?key|password|secret)(\s*[:=]\s*)(["']?)[^\s,"';&]+`), `${1}${2}${3}` + marker},
	{regexp.MustCompile(`\bsk-[A-Za-z0-9][A-Za-z0-9_-]{8,}\b`), marker},
	{regexp.MustCompile(`\bdevin-session-token\$?[A-Za-z0-9._$-]*\b`), marker},
	{regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`), marker},
	{regexp.MustCompile(`\b[A-Za-z0-9_-]{20,}\.[A-Za-z0-9_-]{20,}\.[A-Za-z0-9_-]{10,}\b`), marker},
	{regexp.MustCompile(`\b[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}\b`), `[REDACTED_EMAIL]`},
}

// Text removes tokens, credentials, and user identifiers from text that may be
// exposed through logs, debug APIs, SSE errors, or persisted request events.
func Text(s string) string {
	if s == "" {
		return ""
	}
	out := strings.TrimSpace(s)
	for _, item := range replacements {
		out = item.re.ReplaceAllString(out, item.repl)
	}
	return out
}
