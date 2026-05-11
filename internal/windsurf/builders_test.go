package windsurf

import (
	"testing"

	p "github.com/zhangyu/windsurfapi-go/internal/proto"
)

func TestBuildMetadataShape(t *testing.T) {
	meta := BuildMetadata("sk-test", "sess-123")
	fields, err := p.ParseFields(meta)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	cases := map[int]string{
		1:  "windsurf",
		3:  "sk-test",
		4:  "en",
		10: "sess-123",
		12: "windsurf",
	}
	for fn, want := range cases {
		f := p.GetField(fields, fn, p.WireLenDelim)
		if f == nil {
			t.Errorf("missing field %d", fn)
			continue
		}
		if f.String() != want {
			t.Errorf("field %d: got %q want %q", fn, f.String(), want)
		}
	}
	if p.GetField(fields, 9, p.WireVarint) == nil {
		t.Error("missing request_id")
	}
}

func TestStartCascadeRoundTrip(t *testing.T) {
	// 构造一个假的 StartCascadeResponse：field 1 = cascade_id
	fake := p.WriteStringField(1, "cascade-xyz-42")
	id, err := ParseStartCascadeResponse(fake)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if id != "cascade-xyz-42" {
		t.Fatalf("got %q", id)
	}
}

func TestBuildStartCascadeHasAllFields(t *testing.T) {
	buf := BuildStartCascadeRequest("sk-test", "sess-1")
	fields, err := p.ParseFields(buf)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if p.GetField(fields, 1, p.WireLenDelim) == nil {
		t.Error("missing metadata field")
	}
	if p.GetField(fields, 4, p.WireVarint).Uint() != 1 {
		t.Error("source field should be 1")
	}
	if p.GetField(fields, 5, p.WireVarint).Uint() != 1 {
		t.Error("trajectory_type field should be 1")
	}
}

func TestBuildSendCascadeMessageLayout(t *testing.T) {
	buf := BuildSendCascadeMessageRequest("sk-test", "cas-1", "hello", "sess-1",
		SendOptions{ModelEnum: 42, ModelUID: "uid-foo"})
	fields, err := p.ParseFields(buf)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got := p.GetField(fields, 1, p.WireLenDelim).String(); got != "cas-1" {
		t.Errorf("cascade_id: %q", got)
	}
	item := p.GetField(fields, 2, p.WireLenDelim)
	if item == nil {
		t.Fatal("missing item")
	}
	inner, err := p.ParseFields(item.BytesValue)
	if err != nil {
		t.Fatalf("parse item: %v", err)
	}
	if got := p.GetField(inner, 1, p.WireLenDelim).String(); got != "hello" {
		t.Errorf("text: %q", got)
	}
	if p.GetField(fields, 3, p.WireLenDelim) == nil {
		t.Error("missing metadata")
	}
	cfg := p.GetField(fields, 5, p.WireLenDelim)
	if cfg == nil {
		t.Fatal("missing cascade_config")
	}
	// Drill into cascade_config → planner_config (field 1) → requested_model_uid (field 35)
	cfgFields, _ := p.ParseFields(cfg.BytesValue)
	planner := p.GetField(cfgFields, 1, p.WireLenDelim)
	if planner == nil {
		t.Fatal("missing planner_config")
	}
	plannerFields, _ := p.ParseFields(planner.BytesValue)
	if got := p.GetField(plannerFields, 35, p.WireLenDelim).String(); got != "uid-foo" {
		t.Errorf("requested_model_uid: %q", got)
	}
	// requested_model_deprecated (field 15) wraps { model=1 = 42 }
	rmd := p.GetField(plannerFields, 15, p.WireLenDelim)
	if rmd == nil {
		t.Fatal("missing requested_model_deprecated")
	}
	rmdFields, _ := p.ParseFields(rmd.BytesValue)
	if got := p.GetField(rmdFields, 1, p.WireVarint).Uint(); got != 42 {
		t.Errorf("model enum inside deprecated: %d", got)
	}
}

func TestBuildUpdatePanelStateWithUserStatusLayout(t *testing.T) {
	userStatus := p.Concat(
		p.WriteStringField(7, "x@y"),
		p.WriteVarintField(10, 2),
	)
	buf := BuildUpdatePanelStateWithUserStatusRequest("sk-test", "sess-1", userStatus)
	fields, err := p.ParseFields(buf)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if p.GetField(fields, 1, p.WireLenDelim) == nil {
		t.Fatal("missing metadata")
	}
	got := p.GetField(fields, 2, p.WireLenDelim)
	if got == nil {
		t.Fatal("missing user_status")
	}
	if string(got.Bytes()) != string(userStatus) {
		t.Fatalf("user_status bytes changed")
	}
}

func TestExtractUserStatusBytes(t *testing.T) {
	userStatus := p.Concat(
		p.WriteStringField(7, "x@y"),
		p.WriteVarintField(10, 2),
	)
	resp := p.WriteMessageField(1, userStatus)
	got, err := ExtractUserStatusBytes(resp)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if string(got) != string(userStatus) {
		t.Fatalf("got %x want %x", got, userStatus)
	}
}

func TestParseGetUserStatusFree(t *testing.T) {
	// 构造 UserStatus: pro=false, teams_tier=0, email=x@y
	us := p.Concat(
		p.WriteStringField(7, "x@y"),
		p.WriteVarintField(10, 0),
	)
	top := p.WriteMessageField(1, us)
	out, err := ParseGetUserStatusResponse(top)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if out.Email != "x@y" || out.TierName != "free" {
		t.Fatalf("unexpected: %+v", out)
	}
}

func TestParseGetUserStatusPro(t *testing.T) {
	us := p.Concat(
		p.WriteVarintField(1, 1),     // pro=true
		p.WriteStringField(7, "a@b"), // email
		p.WriteVarintField(10, 2),    // teams_tier=Pro
	)
	pi := p.Concat(
		p.WriteStringField(2, "Pro Plan"),
		p.WriteVarintField(32, 1), // has_paid_features
	)
	resp := p.Concat(p.WriteMessageField(1, us), p.WriteMessageField(2, pi))
	out, err := ParseGetUserStatusResponse(resp)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !out.Pro || out.TierName != "pro" || out.PlanName != "Pro Plan" || !out.HasPaidFeatures {
		t.Fatalf("unexpected: %+v", out)
	}
}
