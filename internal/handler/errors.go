package handler

import (
	"errors"
	"net"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/zhangyu/windsurfapi-go/internal/account"
)

var (
	resetsInRe   = regexp.MustCompile(`(?i)\b(?:resets?|retry(?: |_)?after)\s*(?:in|after|:)?\s*([0-9]+(?:\.[0-9]+)?)\s*(milliseconds?|ms|seconds?|secs?|s|minutes?|mins?|m|hours?|hrs?|h)\b`)
	durationLike = regexp.MustCompile(`(?i)\b([0-9]+h)?([0-9]+m)?([0-9]+s)?\b`)
)

func classifyError(err error) account.ErrorClass {
	if err == nil {
		return ""
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "invalid token"),
		strings.Contains(msg, "invalid devin token"),
		strings.Contains(msg, "failed to validate devin token"),
		strings.Contains(msg, "logging out and logging in again"),
		strings.Contains(msg, "auth token"),
		strings.Contains(msg, "authentication failed"):
		return account.ErrorBanSignal
	case strings.Contains(msg, "rate limit"),
		strings.Contains(msg, "rate_limit"),
		strings.Contains(msg, "too many requests"),
		strings.Contains(msg, "quota"),
		strings.Contains(msg, "capacity"),
		strings.Contains(msg, "retry_after"),
		strings.Contains(msg, "retry-after"):
		return account.ErrorRateLimit
	case strings.Contains(msg, "model not"),
		strings.Contains(msg, "not entitled"),
		strings.Contains(msg, "permission_denied"),
		strings.Contains(msg, "does not have access"),
		strings.Contains(msg, "not available"):
		return account.ErrorModelNotAvailable
	case strings.Contains(msg, "policy"),
		strings.Contains(msg, "blocked"),
		strings.Contains(msg, "safety"),
		strings.Contains(msg, "content violation"):
		return account.ErrorPolicyBlocked
	case strings.Contains(msg, "suspended"),
		strings.Contains(msg, "banned"),
		strings.Contains(msg, "account disabled"):
		return account.ErrorBanSignal
	case strings.Contains(msg, "timeout"),
		strings.Contains(msg, "context deadline"),
		strings.Contains(msg, "temporarily unavailable"),
		strings.Contains(msg, "internal error"),
		strings.Contains(msg, "stalled"):
		return account.ErrorUpstreamTransient
	default:
		var nerr net.Error
		if errors.As(err, &nerr) {
			return account.ErrorTransport
		}
		if strings.Contains(msg, "connection refused") ||
			strings.Contains(msg, "unexpected eof") ||
			strings.Contains(msg, "http2") ||
			strings.Contains(msg, "dial tcp") {
			return account.ErrorTransport
		}
		return account.ErrorFatal
	}
}

func retryableClass(class account.ErrorClass) bool {
	switch class {
	case account.ErrorRateLimit, account.ErrorModelNotAvailable, account.ErrorBanSignal, account.ErrorUpstreamTransient, account.ErrorTransport:
		return true
	default:
		return false
	}
}

func retryablePreSend(class account.ErrorClass) bool {
	switch class {
	case account.ErrorTransport, account.ErrorUpstreamTransient:
		return true
	default:
		return false
	}
}

func proxyFailureClass(class account.ErrorClass) bool {
	switch class {
	case account.ErrorTransport, account.ErrorUpstreamTransient:
		return true
	default:
		return false
	}
}

func statusForClass(class account.ErrorClass) int {
	switch class {
	case account.ErrorRateLimit:
		return 429
	case account.ErrorPolicyBlocked:
		return 451
	case account.ErrorModelNotAvailable:
		return 403
	case account.ErrorTransport, account.ErrorUpstreamTransient:
		return 502
	default:
		return 502
	}
}

func cooldownUntilForError(now time.Time, class account.ErrorClass, err error) time.Time {
	defaultDuration := 5 * time.Minute
	if class == account.ErrorRateLimit {
		if parsed, ok := parseCooldownDuration(err); ok {
			if parsed < time.Second {
				parsed = time.Second
			}
			if parsed > 2*time.Hour {
				parsed = 2 * time.Hour
			}
			return now.Add(parsed)
		}
	}
	return now.Add(defaultDuration)
}

func parseCooldownDuration(err error) (time.Duration, bool) {
	if err == nil {
		return 0, false
	}
	msg := err.Error()
	lower := strings.ToLower(msg)
	for _, marker := range []string{"resets in:", "resets in ", "reset in:", "reset in ", "retry after:", "retry after "} {
		idx := strings.Index(lower, marker)
		if idx < 0 {
			continue
		}
		rest := strings.TrimSpace(msg[idx+len(marker):])
		token := strings.Fields(rest)
		if len(token) == 0 {
			continue
		}
		if d, err := time.ParseDuration(strings.Trim(token[0], ".,;")); err == nil && d > 0 {
			return d, true
		}
		if len(token) >= 2 {
			if d, ok := durationFromNumberAndUnit(token[0], token[1]); ok {
				return d, true
			}
		}
	}
	match := resetsInRe.FindStringSubmatch(msg)
	if len(match) == 3 {
		if d, ok := durationFromNumberAndUnit(match[1], match[2]); ok {
			return d, true
		}
	}
	for _, token := range strings.Fields(msg) {
		token = strings.Trim(token, ".,;()[]")
		if token == "" || !durationLike.MatchString(token) {
			continue
		}
		if d, err := time.ParseDuration(token); err == nil && d > 0 {
			return d, true
		}
	}
	return 0, false
}

func durationFromNumberAndUnit(rawN, rawUnit string) (time.Duration, bool) {
	n, err := strconv.ParseFloat(strings.TrimSpace(rawN), 64)
	if err != nil || n <= 0 {
		return 0, false
	}
	unit := strings.ToLower(strings.Trim(strings.TrimSpace(rawUnit), ".,;"))
	switch unit {
	case "ms", "millisecond", "milliseconds":
		return time.Duration(n * float64(time.Millisecond)), true
	case "s", "sec", "secs", "second", "seconds":
		return time.Duration(n * float64(time.Second)), true
	case "m", "min", "mins", "minute", "minutes":
		return time.Duration(n * float64(time.Minute)), true
	case "h", "hr", "hrs", "hour", "hours":
		return time.Duration(n * float64(time.Hour)), true
	default:
		return 0, false
	}
}
