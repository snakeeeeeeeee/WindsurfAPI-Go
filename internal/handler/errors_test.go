package handler

import (
	"errors"
	"testing"
	"time"

	"github.com/zhangyu/windsurfapi-go/internal/account"
)

func TestClassifyError(t *testing.T) {
	cases := []struct {
		msg  string
		want account.ErrorClass
	}{
		{"rate limit resets in 10m", account.ErrorRateLimit},
		{"permission_denied: model not available", account.ErrorModelNotAvailable},
		{"blocked by safety policy", account.ErrorPolicyBlocked},
		{"account suspended", account.ErrorBanSignal},
		{"failed to validate Devin token: Invalid token", account.ErrorBanSignal},
		{"try logging out and logging in again", account.ErrorBanSignal},
		{"context deadline exceeded", account.ErrorUpstreamTransient},
		{"dial tcp 127.0.0.1:42100: connection refused", account.ErrorTransport},
		{"weird permanent failure", account.ErrorFatal},
	}
	for _, tc := range cases {
		if got := classifyError(errors.New(tc.msg)); got != tc.want {
			t.Fatalf("classifyError(%q)=%s want %s", tc.msg, got, tc.want)
		}
	}
}

func TestCooldownUntilForRateLimitParsesUpstreamReset(t *testing.T) {
	now := time.Date(2026, 5, 11, 10, 17, 21, 0, time.UTC)
	err := errors.New("Reached message rate limit for this model. Please try again later. Resets in: 30m0s (trace ID: abc)")
	got := cooldownUntilForError(now, account.ErrorRateLimit, err)
	if got.Sub(now) != 30*time.Minute {
		t.Fatalf("cooldown=%s want 30m", got.Sub(now))
	}
}

func TestCooldownUntilForRateLimitParsesRetryAfterUnits(t *testing.T) {
	now := time.Date(2026, 5, 11, 10, 17, 21, 0, time.UTC)
	cases := []struct {
		msg  string
		want time.Duration
	}{
		{"retry_after: 90s", 90 * time.Second},
		{"retry after 2 minutes", 2 * time.Minute},
		{"resets in 1h", time.Hour},
	}
	for _, tc := range cases {
		got := cooldownUntilForError(now, account.ErrorRateLimit, errors.New(tc.msg))
		if got.Sub(now) != tc.want {
			t.Fatalf("%q cooldown=%s want %s", tc.msg, got.Sub(now), tc.want)
		}
	}
}

func TestCooldownUntilDefaultsForUnparseableErrors(t *testing.T) {
	now := time.Date(2026, 5, 11, 10, 17, 21, 0, time.UTC)
	got := cooldownUntilForError(now, account.ErrorRateLimit, errors.New("rate limit"))
	if got.Sub(now) != 5*time.Minute {
		t.Fatalf("cooldown=%s want 5m", got.Sub(now))
	}
	got = cooldownUntilForError(now, account.ErrorModelNotAvailable, errors.New("model unavailable"))
	if got.Sub(now) != 5*time.Minute {
		t.Fatalf("model cooldown=%s want 5m", got.Sub(now))
	}
}
