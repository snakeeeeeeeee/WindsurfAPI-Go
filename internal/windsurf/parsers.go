package windsurf

import (
	"encoding/binary"
	"math"

	p "github.com/zhangyu/windsurfapi-go/internal/proto"
)

// ─── StartCascadeResponse ─────────────────────────────────

// ParseStartCascadeResponse 对齐 windsurf.js:758。返回 cascade_id；找不到时返回空串。
func ParseStartCascadeResponse(buf []byte) (string, error) {
	fields, err := p.ParseFields(buf)
	if err != nil {
		return "", err
	}
	if f := p.GetField(fields, 1, p.WireLenDelim); f != nil {
		return f.String(), nil
	}
	return "", nil
}

// ─── GeneratorMetadata ────────────────────────────────────

// Usage 是聚合后的 token 用量。对齐 Node parseGeneratorMetadata 返回值。
type Usage struct {
	InputTokens        uint64
	OutputTokens       uint64
	CacheReadTokens    uint64
	CacheWriteTokens   uint64
	CacheWrite5mTokens uint64
	CacheWrite1hTokens uint64
	EntryCount         int
}

// ExtractUserStatusBytes returns top-level field 1 from GetUserStatusResponse.
// Newer LS builds require this raw message to be copied into panel state before
// InitializeCascadePanelState; parsing it and rebuilding is not equivalent.
func ExtractUserStatusBytes(buf []byte) ([]byte, error) {
	if len(buf) == 0 {
		return nil, nil
	}
	fields, err := p.ParseFields(buf)
	if err != nil {
		return nil, err
	}
	if f := p.GetField(fields, 1, p.WireLenDelim); f != nil {
		return f.Bytes(), nil
	}
	return nil, nil
}

// ParseGeneratorMetadata 对齐 windsurf.js:713。
// 没有任何 usage 时返回 nil。
func ParseGeneratorMetadata(buf []byte) (*Usage, error) {
	fields, err := p.ParseFields(buf)
	if err != nil {
		return nil, err
	}
	entries := p.GetAllFields(fields, 1)
	var u Usage
	found := false
	for _, entry := range entries {
		if entry.WireType != p.WireLenDelim {
			continue
		}
		gm, err := p.ParseFields(entry.BytesValue)
		if err != nil {
			continue
		}
		cm := p.GetField(gm, 1, p.WireLenDelim) // chat_model
		if cm == nil {
			continue
		}
		cmFields, err := p.ParseFields(cm.BytesValue)
		if err != nil {
			continue
		}
		usageField := p.GetField(cmFields, 4, p.WireLenDelim)
		if usageField == nil {
			continue
		}
		us, err := p.ParseFields(usageField.BytesValue)
		if err != nil {
			continue
		}
		in := p.GetField(us, 2, p.WireVarint).Uint()
		out := p.GetField(us, 3, p.WireVarint).Uint()
		cw := p.GetField(us, 4, p.WireVarint).Uint()
		cr := p.GetField(us, 5, p.WireVarint).Uint()
		if in|out|cw|cr == 0 {
			continue
		}
		u.InputTokens += in
		u.OutputTokens += out
		u.CacheWriteTokens += cw
		u.CacheWrite5mTokens += cw
		u.CacheReadTokens += cr
		found = true
	}
	if !found {
		return nil, nil
	}
	u.EntryCount = len(entries)
	return &u, nil
}

// ─── GetCascadeTrajectoryStepsResponse ────────────────────

// TrajectoryStep 是 parseTrajectorySteps 的输出。本轮只保留文本 + 状态，
// Node 版的 tool_calls / native-bridge 字段略去。
type TrajectoryStep struct {
	Type     uint64
	Status   uint64 // 3=DONE, 8=GENERATING
	Text     string // 优先 modified_response，否则 response
	Thinking string
	ErrorMsg string
	Usage    *Usage
}

// ParseTrajectorySteps 对齐 windsurf.js:779（精简版，只取 planner_response / status / usage / error）。
func ParseTrajectorySteps(buf []byte) ([]TrajectoryStep, error) {
	fields, err := p.ParseFields(buf)
	if err != nil {
		return nil, err
	}
	steps := p.GetAllFields(fields, 1)
	var out []TrajectoryStep
	for _, step := range steps {
		if step.WireType != p.WireLenDelim {
			continue
		}
		sf, err := p.ParseFields(step.BytesValue)
		if err != nil {
			continue
		}
		entry := TrajectoryStep{
			Type:   p.GetField(sf, 1, p.WireVarint).Uint(),
			Status: p.GetField(sf, 4, p.WireVarint).Uint(),
		}
		// planner_response (field 20)
		if planner := p.GetField(sf, 20, p.WireLenDelim); planner != nil {
			pf, err := p.ParseFields(planner.BytesValue)
			if err == nil {
				responseText := p.GetField(pf, 1, p.WireLenDelim).String()
				modifiedText := p.GetField(pf, 8, p.WireLenDelim).String()
				thinkText := p.GetField(pf, 3, p.WireLenDelim).String()
				if modifiedText != "" {
					entry.Text = modifiedText
				} else {
					entry.Text = responseText
				}
				entry.Thinking = thinkText
			}
		}
		// metadata → model_usage (field 5 → field 9)
		if meta := p.GetField(sf, 5, p.WireLenDelim); meta != nil {
			mf, err := p.ParseFields(meta.BytesValue)
			if err == nil {
				if usageF := p.GetField(mf, 9, p.WireLenDelim); usageF != nil {
					us, err := p.ParseFields(usageF.BytesValue)
					if err == nil {
						u := Usage{
							InputTokens:        p.GetField(us, 2, p.WireVarint).Uint(),
							OutputTokens:       p.GetField(us, 3, p.WireVarint).Uint(),
							CacheWriteTokens:   p.GetField(us, 4, p.WireVarint).Uint(),
							CacheWrite5mTokens: p.GetField(us, 4, p.WireVarint).Uint(),
							CacheReadTokens:    p.GetField(us, 5, p.WireVarint).Uint(),
						}
						if u.InputTokens|u.OutputTokens|u.CacheReadTokens|u.CacheWriteTokens != 0 {
							entry.Usage = &u
						}
					}
				}
			}
		}
		// 错误信息：field 24（error_message step）或 field 31（generic error）
		if em := p.GetField(sf, 24, p.WireLenDelim); em != nil {
			emf, err := p.ParseFields(em.BytesValue)
			if err == nil {
				if inner := p.GetField(emf, 3, p.WireLenDelim); inner != nil {
					entry.ErrorMsg = readErrorDetails(inner.BytesValue)
				}
			}
		}
		if entry.ErrorMsg == "" {
			if ef := p.GetField(sf, 31, p.WireLenDelim); ef != nil {
				entry.ErrorMsg = readErrorDetails(ef.BytesValue)
			}
		}
		out = append(out, entry)
	}
	return out, nil
}

func readErrorDetails(buf []byte) string {
	fields, err := p.ParseFields(buf)
	if err != nil {
		return ""
	}
	for _, fn := range []int{1, 2, 3} {
		if f := p.GetField(fields, fn, p.WireLenDelim); f != nil {
			s := f.String()
			if s != "" {
				return truncateOneLine(s, 300)
			}
		}
	}
	return ""
}

func truncateOneLine(s string, max int) string {
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' || s[i] == '\r' {
			s = s[:i]
			break
		}
	}
	if len(s) > max {
		s = s[:max]
	}
	return s
}

// ─── GetUserStatusResponse ────────────────────────────────

// UserStatus 是 parseGetUserStatusResponse 的精简 Go 版。
// 扩展字段（allowedModels 等）按需再加。
type UserStatus struct {
	Pro                    bool
	TeamsTier              uint64
	TierName               string
	DisplayName            string
	Email                  string
	TeamID                 string
	PlanName               string
	UserUsedPromptCredits  uint64
	UserUsedFlowCredits    uint64
	MonthlyPromptCredits   uint64
	MonthlyFlowCredits     uint64
	MaxPremiumChatMessages uint64
	TrialEndMs             int64
	HasPaidFeatures        bool
	IsEnterprise           bool
	IsTeams                bool
	AllowedModels          []AllowedModel
}

// AllowedModel 对齐 Node allowedModels 项。
type AllowedModel struct {
	ModelEnum  uint64
	Alias      uint64
	Multiplier float32
}

// MapTeamsTier 对齐 windsurf.js:1097。
func MapTeamsTier(t uint64) string {
	switch t {
	case 0, 6, 19:
		return "free"
	default:
		if t > 0 {
			return "pro"
		}
		return "unknown"
	}
}

// ParseGetUserStatusResponse 对齐 windsurf.js:1145。
func ParseGetUserStatusResponse(buf []byte) (*UserStatus, error) {
	out := &UserStatus{}
	if len(buf) == 0 {
		out.TierName = MapTeamsTier(0)
		return out, nil
	}
	top, err := p.ParseFields(buf)
	if err != nil {
		return nil, err
	}
	usField := p.GetField(top, 1, p.WireLenDelim)
	piField := p.GetField(top, 2, p.WireLenDelim)

	if usField != nil && len(usField.BytesValue) > 0 {
		us, err := p.ParseFields(usField.BytesValue)
		if err != nil {
			return nil, err
		}
		out.Pro = p.GetField(us, 1, p.WireVarint).Uint() == 1
		out.DisplayName = p.GetField(us, 3, p.WireLenDelim).String()
		out.TeamID = p.GetField(us, 5, p.WireLenDelim).String()
		out.Email = p.GetField(us, 7, p.WireLenDelim).String()
		out.TeamsTier = p.GetField(us, 10, p.WireVarint).Uint()
		out.UserUsedPromptCredits = p.GetField(us, 28, p.WireVarint).Uint()
		out.UserUsedFlowCredits = p.GetField(us, 29, p.WireVarint).Uint()
		out.MaxPremiumChatMessages = p.GetField(us, 35, p.WireVarint).Uint()
		if ts := p.GetField(us, 34, p.WireLenDelim); ts != nil {
			tsFields, err := p.ParseFields(ts.BytesValue)
			if err == nil {
				secs := p.GetField(tsFields, 1, p.WireVarint).Uint()
				out.TrialEndMs = int64(secs) * 1000
			}
		}
	}

	if piField != nil && len(piField.BytesValue) > 0 {
		pi, err := p.ParseFields(piField.BytesValue)
		if err != nil {
			return nil, err
		}
		if out.TeamsTier == 0 {
			out.TeamsTier = p.GetField(pi, 1, p.WireVarint).Uint()
		}
		out.PlanName = p.GetField(pi, 2, p.WireLenDelim).String()
		out.MonthlyPromptCredits = p.GetField(pi, 12, p.WireVarint).Uint()
		out.MonthlyFlowCredits = p.GetField(pi, 13, p.WireVarint).Uint()
		out.IsEnterprise = p.GetField(pi, 16, p.WireVarint).Uint() == 1
		out.IsTeams = p.GetField(pi, 17, p.WireVarint).Uint() == 1
		out.HasPaidFeatures = p.GetField(pi, 32, p.WireVarint).Uint() == 1

		for _, entry := range p.GetAllFields(pi, 21) {
			if entry.WireType != p.WireLenDelim {
				continue
			}
			ac, err := p.ParseFields(entry.BytesValue)
			if err != nil {
				continue
			}
			mul := float32(1.0)
			if cm := p.GetField(ac, 2, p.WireFixed32); cm != nil && len(cm.BytesValue) == 4 {
				bits := binary.LittleEndian.Uint32(cm.BytesValue)
				mul = math.Float32frombits(bits)
			}
			var modelEnum, alias uint64
			if moa := p.GetField(ac, 1, p.WireLenDelim); moa != nil {
				m, err := p.ParseFields(moa.BytesValue)
				if err == nil {
					modelEnum = p.GetField(m, 1, p.WireVarint).Uint()
					alias = p.GetField(m, 2, p.WireVarint).Uint()
				}
			}
			out.AllowedModels = append(out.AllowedModels, AllowedModel{
				ModelEnum: modelEnum, Alias: alias, Multiplier: mul,
			})
		}
	}

	out.TierName = MapTeamsTier(out.TeamsTier)
	return out, nil
}
