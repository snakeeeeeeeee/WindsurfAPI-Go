package redact

import (
	"strings"
	"testing"
)

func TestTextRedactsCommonSecrets(t *testing.T) {
	input := `Authorization: Bearer sk-test_abcdefghijklmnopqrstuvwxyz
Cookie: session=devin-session-token$abc123456789xyz; other=value
proxy=http://user:secret@proxy.example.com:8080
email=user@example.com
jwt=eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.signaturepart
aws=AKIAABCDEFGHIJKLMNOP
api_key=sk-other_abcdefghijklmnopqrstuvwxyz`
	got := Text(input)
	for _, forbidden := range []string{
		"sk-test_abcdefghijklmnopqrstuvwxyz",
		"devin-session-token$abc123456789xyz",
		"user@example.com",
		"secret@proxy",
		"eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9",
		"AKIAABCDEFGHIJKLMNOP",
		"sk-other_abcdefghijklmnopqrstuvwxyz",
	} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("redacted text still contains %q: %s", forbidden, got)
		}
	}
	if !strings.Contains(got, "[REDACTED]") || !strings.Contains(got, "[REDACTED_EMAIL]") || !strings.Contains(got, "http://user:***@proxy.example.com:8080") {
		t.Fatalf("missing redaction markers: %s", got)
	}
}

func TestTextRedactsJSONCredentialFields(t *testing.T) {
	got := Text(`{"firebase_token":"devin-session-token$abc123","caller_key":"caller-secret","message":"ok"}`)
	if strings.Contains(got, "devin-session-token") || strings.Contains(got, "caller-secret") {
		t.Fatalf("secret leaked: %s", got)
	}
	if !strings.Contains(got, `"message":"ok"`) {
		t.Fatalf("non-secret text was damaged: %s", got)
	}
}
