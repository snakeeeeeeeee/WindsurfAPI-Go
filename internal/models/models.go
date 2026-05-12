package models

import (
	"os"
	"strings"
	"sync"
)

// Model 表示一个支持的模型
type Model struct {
	ID          string `json:"id"`
	Object      string `json:"object"`
	Created     int64  `json:"created"`
	OwnedBy     string `json:"owned_by"`
	CascadeName string `json:"-"` // LS 实际发送的名称
	Family      string `json:"-"` // fallback 分组
	DisplayName string `json:"-"`

	// Windsurf Cascade 协议需要的两个字段。二者至少一个非零/非空：
	ModelUID  string `json:"-"` // 新模型走 uid（MODEL_PRIVATE_* 等），优先级更高
	ModelEnum uint64 `json:"-"` // 老模型走 enum（ModelEnumValue）

	Credit            float64 `json:"-"`
	Deprecated        bool    `json:"-"`
	DirectSupported   bool    `json:"-"`
	UnsupportedReason string  `json:"-"`
}

// OpenAIModel 用于 /v1/models 的返回格式
type OpenAIModel struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
}

type DashboardModel struct {
	ID                string  `json:"id"`
	Object            string  `json:"object"`
	Created           int64   `json:"created"`
	OwnedBy           string  `json:"owned_by"`
	Provider          string  `json:"provider"`
	ModelUID          string  `json:"model_uid,omitempty"`
	ModelEnum         uint64  `json:"model_enum,omitempty"`
	Credit            float64 `json:"credit,omitempty"`
	Family            string  `json:"family,omitempty"`
	DisplayName       string  `json:"display_name,omitempty"`
	Visible           bool    `json:"visible"`
	Deprecated        bool    `json:"deprecated"`
	Supported         bool    `json:"supported"`
	DirectSupported   bool    `json:"direct_supported"`
	UnsupportedReason string  `json:"unsupported_reason,omitempty"`
	Notes             string  `json:"notes,omitempty"`
}

// OpenAIModelList 用于 /v1/models 的列表返回
type OpenAIModelList struct {
	Object string        `json:"object"`
	Data   []OpenAIModel `json:"data"`
}

type TierAccess struct {
	Free            []string `json:"free"`
	Pro             []string `json:"pro"`
	Unknown         []string `json:"unknown"`
	Expired         []string `json:"expired"`
	AllModels       []string `json:"allModels"`
	DirectSupported []string `json:"direct_supported"`
	Unsupported     []string `json:"unsupported"`
}

var PublicModelIDs = []string{
	"claude-opus-4-7",
	"claude-opus-4-6",
	"claude-opus-4-6-thinking",
	"claude-sonnet-4-6",
	"claude-haiku-4-5",
	"claude-haiku-4-5-20251001",
}

// SupportedModels is intentionally Claude-only for the first Go milestone.
// Keeping the catalog small removes cross-provider routing/fallback variables
// while the Cascade protocol is still being nailed down.
var SupportedModels = []Model{
	{
		ID: "claude-4.5-haiku", Object: "model", Created: 1706745938,
		OwnedBy: "windsurf", CascadeName: "claude-4.5-haiku",
		Family: "claude", DisplayName: "Claude Haiku 4.5",
		ModelUID: "MODEL_PRIVATE_11",
	},
	{
		ID: "claude-4.5-sonnet", Object: "model", Created: 1706745938,
		OwnedBy: "windsurf", CascadeName: "claude-4.5-sonnet",
		Family: "claude", DisplayName: "Claude Sonnet 4.5",
		ModelUID: "MODEL_PRIVATE_2", ModelEnum: 353,
	},
	{
		ID: "claude-sonnet-4.6", Object: "model", Created: 1706745938,
		OwnedBy: "windsurf", CascadeName: "claude-sonnet-4.6",
		Family: "claude", DisplayName: "Claude Sonnet 4.6",
		ModelUID: "claude-sonnet-4-6",
	},
	{
		ID: "claude-sonnet-4.6-thinking", Object: "model", Created: 1706745938,
		OwnedBy: "windsurf", CascadeName: "claude-sonnet-4.6-thinking",
		Family: "claude", DisplayName: "Claude Sonnet 4.6 Thinking",
		ModelUID: "claude-sonnet-4-6-thinking",
	},
	{
		ID: "claude-opus-4.6", Object: "model", Created: 1706745938,
		OwnedBy: "windsurf", CascadeName: "claude-opus-4.6",
		Family: "claude", DisplayName: "Claude Opus 4.6",
		ModelUID: "claude-opus-4-6",
	},
	{
		ID: "claude-opus-4.6-thinking", Object: "model", Created: 1706745938,
		OwnedBy: "windsurf", CascadeName: "claude-opus-4.6-thinking",
		Family: "claude", DisplayName: "Claude Opus 4.6 Thinking",
		ModelUID: "claude-opus-4-6-thinking",
	},
	{
		ID: "claude-opus-4-7-medium", Object: "model", Created: 1706745938,
		OwnedBy: "windsurf", CascadeName: "claude-opus-4-7-medium",
		Family: "claude", DisplayName: "Claude Opus 4.7 Medium",
		ModelUID: "claude-opus-4-7-medium",
	},
	{
		ID: "claude-opus-4-7-low", Object: "model", Created: 1706745938,
		OwnedBy: "windsurf", CascadeName: "claude-opus-4-7-low",
		Family: "claude", DisplayName: "Claude Opus 4.7 Low",
		ModelUID: "claude-opus-4-7-low",
	},
	{
		ID: "claude-opus-4-7-high", Object: "model", Created: 1706745938,
		OwnedBy: "windsurf", CascadeName: "claude-opus-4-7-high",
		Family: "claude", DisplayName: "Claude Opus 4.7 High",
		ModelUID: "claude-opus-4-7-high",
	},
	{
		ID: "claude-opus-4-7-xhigh", Object: "model", Created: 1706745938,
		OwnedBy: "windsurf", CascadeName: "claude-opus-4-7-xhigh",
		Family: "claude", DisplayName: "Claude Opus 4.7 XHigh",
		ModelUID: "claude-opus-4-7-xhigh",
	},
	{
		ID: "claude-opus-4-7-max", Object: "model", Created: 1706745938,
		OwnedBy: "windsurf", CascadeName: "claude-opus-4-7-max",
		Family: "claude", DisplayName: "Claude Opus 4.7 Max",
		ModelUID: "claude-opus-4-7-max",
	},
	{
		ID: "claude-opus-4-7-medium-thinking", Object: "model", Created: 1706745938,
		OwnedBy: "windsurf", CascadeName: "claude-opus-4-7-medium-thinking",
		Family: "claude", DisplayName: "Claude Opus 4.7 Medium Thinking",
		ModelUID: "claude-opus-4-7-medium-thinking",
	},
	{
		ID: "claude-opus-4-7-high-thinking", Object: "model", Created: 1706745938,
		OwnedBy: "windsurf", CascadeName: "claude-opus-4-7-high-thinking",
		Family: "claude", DisplayName: "Claude Opus 4.7 High Thinking",
		ModelUID: "claude-opus-4-7-high-thinking",
	},
	{
		ID: "claude-opus-4-7-xhigh-thinking", Object: "model", Created: 1706745938,
		OwnedBy: "windsurf", CascadeName: "claude-opus-4-7-xhigh-thinking",
		Family: "claude", DisplayName: "Claude Opus 4.7 XHigh Thinking",
		ModelUID: "claude-opus-4-7-xhigh-thinking",
	},
}

var modelAliases = map[string]string{
	"claude-haiku-4.5":           "claude-4.5-haiku",
	"claude-haiku-4-5":           "claude-4.5-haiku",
	"claude-haiku-4-5-latest":    "claude-4.5-haiku",
	"claude-haiku-4.5-latest":    "claude-4.5-haiku",
	"claude-sonnet-4.5":          "claude-4.5-sonnet",
	"claude-sonnet-4-5":          "claude-4.5-sonnet",
	"claude-sonnet-4-6":          "claude-sonnet-4.6",
	"claude-sonnet-4-6-thinking": "claude-sonnet-4.6-thinking",
	"claude-4.6":                 "claude-sonnet-4.6",
	"claude-4.6-thinking":        "claude-sonnet-4.6-thinking",
	"claude-opus-4-6":            "claude-opus-4.6",
	"claude-opus-4-6-thinking":   "claude-opus-4.6-thinking",
	"claude-opus-4.7":            "claude-opus-4-7-medium",
	"claude-opus-4-7":            "claude-opus-4-7-medium",
	"claude-opus-4.7-medium":     "claude-opus-4-7-medium",
	"claude-opus-4.7-high":       "claude-opus-4-7-high",
	"claude-opus-4.7-xhigh":      "claude-opus-4-7-xhigh",
	"claude-opus-4.7-max":        "claude-opus-4-7-max",
}

type ResolverOptions struct {
	DefaultEffort       string
	DefaultOpus47Effort string
}

var (
	catalogOnce   sync.Once
	catalogModels []Model
	catalogLookup map[string]int
)

// GetModelByID 按 ID 查找模型
func GetModelByID(id string) *Model {
	id = NormalizeModelID(id)
	if id == "" {
		return nil
	}
	catalogOnce.Do(initCatalog)
	if idx, ok := catalogLookup[strings.ToLower(id)]; ok {
		model := catalogModels[idx]
		return &model
	}
	return nil
}

// NormalizeModelID maps Node-compatible aliases to canonical catalog IDs.
func NormalizeModelID(id string) string {
	key := strings.TrimSpace(strings.ToLower(id))
	if key == "" {
		return ""
	}
	catalogOnce.Do(initCatalog)
	if idx, ok := catalogLookup[key]; ok {
		return catalogModels[idx].ID
	}
	return key
}

// ResolveModelForRequest normalizes public model aliases and applies a
// separate reasoning effort knob to Windsurf's per-effort model IDs.
func ResolveModelForRequest(id, effort string) *Model {
	id = ResolveModelIDForRequest(id, effort)
	if id == "" {
		return nil
	}
	return GetModelByID(id)
}

func ResolveModelForRequestWithOptions(id, effort string, opts ResolverOptions) *Model {
	id = ResolveModelIDForRequestWithOptions(id, effort, opts)
	if id == "" {
		return nil
	}
	return GetModelByID(id)
}

func ResolveModelIDForRequest(id, effort string) string {
	return ResolveModelIDForRequestWithOptions(id, effort, ResolverOptions{})
}

func ResolveModelIDForRequestWithOptions(id, effort string, opts ResolverOptions) string {
	canonical := NormalizeModelID(id)
	if canonical == "" {
		return ""
	}
	effort = NormalizeReasoningEffort(effort)
	if effort == "" {
		effort = defaultEffortFor(id, canonical, firstNonEmpty(opts.DefaultEffort, opts.DefaultOpus47Effort))
	}
	if effort == "" {
		return canonical
	}
	if routed := resolveEffortVariant(canonical, effort); routed != "" {
		return routed
	}
	return canonical
}

func defaultEffortFor(publicID, canonical, configured string) string {
	configured = NormalizeReasoningEffort(configured)
	if configured == "" {
		return ""
	}
	if !hasExplicitEffort(publicID) && effortVariantBase(canonical) != "" {
		return configured
	}
	return ""
}

func hasExplicitEffort(key string) bool {
	key = strings.ToLower(strings.TrimSpace(key))
	for _, effort := range []string{"low", "medium", "high", "xhigh", "max"} {
		if strings.HasSuffix(key, "-"+effort) || strings.Contains(key, "-"+effort+"-") || strings.Contains(key, "."+effort+"-") {
			return true
		}
	}
	return false
}

func NormalizeReasoningEffort(effort string) string {
	switch strings.ToLower(strings.TrimSpace(effort)) {
	case "none", "off", "disabled", "false":
		return ""
	case "minimal":
		return "low"
	case "low", "medium", "high", "xhigh", "max":
		return strings.ToLower(strings.TrimSpace(effort))
	default:
		return ""
	}
}

func IsReasoningEffortEnabled(effort string) bool {
	return NormalizeReasoningEffort(effort) != ""
}

// PickRateLimitFallback returns a same-provider lower-cost model sibling for
// rate-limit remediation. It mirrors the Node catalog helper and only reads
// literal catalog suffixes, so it never falls across providers.
func PickRateLimitFallback(modelID string) string {
	modelID = NormalizeModelID(modelID)
	if modelID == "" {
		return ""
	}
	effort := []string{"low", "medium", "high", "xhigh", "max"}
	for i := len(effort) - 1; i >= 1; i-- {
		suffix := "-" + effort[i]
		if !strings.HasSuffix(modelID, suffix) {
			continue
		}
		base := strings.TrimSuffix(modelID, suffix)
		for j := i - 1; j >= 0; j-- {
			candidate := base + "-" + effort[j]
			if sameProviderFallback(modelID, candidate) {
				return candidate
			}
		}
	}
	if strings.HasSuffix(modelID, "-1m") {
		candidate := strings.TrimSuffix(modelID, "-1m")
		if sameProviderFallback(modelID, candidate) {
			return candidate
		}
	}
	return ""
}

func resolveEffortVariant(canonical, effort string) string {
	base := effortVariantBase(canonical)
	if base == "" {
		return ""
	}
	thinking := strings.HasSuffix(canonical, "-thinking")
	targetEffort := effort
	if thinking && targetEffort == "max" {
		targetEffort = "xhigh"
	}
	target := base + "-" + targetEffort
	if thinking {
		target += "-thinking"
	}
	if model := GetModelByID(target); model != nil {
		return model.ID
	}
	return ""
}

func effortVariantBase(canonical string) string {
	id := NormalizeModelID(canonical)
	if id == "" {
		return ""
	}
	baseID := strings.TrimSuffix(id, "-thinking")
	if hasEffortSibling(baseID) {
		return baseID
	}
	if base, ok := trimEffortSuffix(baseID); ok && hasEffortSibling(base) {
		return base
	}
	model := GetModelByID(id)
	if model == nil {
		return ""
	}
	uidBase := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(model.ModelUID)), "-thinking")
	if base, ok := trimEffortSuffix(uidBase); ok {
		if modelByUID := GetModelByID(base + "-medium"); modelByUID != nil {
			if baseID2, ok2 := trimEffortSuffix(strings.TrimSuffix(modelByUID.ID, "-thinking")); ok2 && hasEffortSibling(baseID2) {
				return baseID2
			}
		}
	}
	return ""
}

func trimEffortSuffix(id string) (string, bool) {
	id = strings.ToLower(strings.TrimSpace(id))
	for _, effort := range []string{"low", "medium", "high", "xhigh", "max"} {
		suffix := "-" + effort
		if strings.HasSuffix(id, suffix) {
			return strings.TrimSuffix(id, suffix), true
		}
	}
	return "", false
}

func hasEffortSibling(base string) bool {
	count := 0
	for _, effort := range []string{"low", "medium", "high", "xhigh", "max"} {
		if GetModelByID(base+"-"+effort) != nil {
			count++
		}
	}
	return count > 0
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if v := strings.TrimSpace(value); v != "" {
			return v
		}
	}
	return ""
}

// AllModels returns the full Node-parity catalog, including models that are
// visible in Dashboard but not yet enabled on the Direct-only runtime path.
func AllModels() []Model {
	catalogOnce.Do(initCatalog)
	out := make([]Model, len(catalogModels))
	copy(out, catalogModels)
	return out
}

// ToOpenAIModelList 转换为 OpenAI 格式模型列表
func ToOpenAIModelList() OpenAIModelList {
	return ToOpenAIModelListFiltered(func(Model) bool { return true })
}

func ToOpenAIModelListFiltered(include func(Model) bool) OpenAIModelList {
	data := make([]OpenAIModel, 0, len(PublicModelIDs))
	for _, publicID := range PublicModelIDs {
		m := ResolveModelForRequest(publicID, "")
		if m == nil || !m.DirectSupported || m.Deprecated {
			continue
		}
		if include != nil && !include(*m) {
			continue
		}
		data = append(data, OpenAIModel{
			ID:      publicID,
			Object:  m.Object,
			Created: m.Created,
			OwnedBy: m.OwnedBy,
		})
	}
	return OpenAIModelList{
		Object: "list",
		Data:   data,
	}
}

func ToDashboardModelList() []DashboardModel {
	return ToDashboardModelListWithAccess(nil)
}

func ToPublicDashboardModelList() []DashboardModel {
	return ToPublicDashboardModelListWithAccess(nil)
}

func ToPublicDashboardModelListWithAccess(access map[string]DashboardAccess) []DashboardModel {
	all := dashboardModelListForIDs(PublicModelIDs, access, true)
	deduped := make([]DashboardModel, 0, len(all))
	seen := map[string]bool{}
	for _, m := range all {
		if seen[m.ID] {
			continue
		}
		seen[m.ID] = true
		deduped = append(deduped, m)
	}
	return deduped
}

func TierAccessSnapshot() TierAccess {
	all := AllModels()
	pro := make([]string, 0, len(all))
	free := []string{}
	direct := []string{}
	unsupported := []string{}
	for _, m := range all {
		pro = append(pro, m.ID)
		if m.DirectSupported && !m.Deprecated {
			direct = append(direct, m.ID)
		} else {
			unsupported = append(unsupported, m.ID)
		}
	}
	if GetModelByID("gemini-2.5-flash") != nil {
		free = append(free, "gemini-2.5-flash")
	}
	return TierAccess{
		Free:            free,
		Pro:             pro,
		Unknown:         append([]string(nil), pro...),
		Expired:         []string{},
		AllModels:       append([]string(nil), pro...),
		DirectSupported: direct,
		Unsupported:     unsupported,
	}
}

type DashboardAccess struct {
	Visible           bool
	Enabled           bool
	Deprecated        bool
	UnsupportedReason string
	Notes             string
}

func ToDashboardModelListWithAccess(access map[string]DashboardAccess) []DashboardModel {
	return dashboardModelListForIDs(nil, access, false)
}

func dashboardModelListForIDs(ids []string, access map[string]DashboardAccess, publicIDs bool) []DashboardModel {
	var source []Model
	if publicIDs {
		source = make([]Model, 0, len(ids))
		for _, id := range ids {
			m := ResolveModelForRequest(id, "")
			if m == nil {
				continue
			}
			cp := *m
			cp.ID = id
			cp.DisplayName = id
			cp.CascadeName = id
			source = append(source, cp)
		}
	} else {
		source = AllModels()
	}
	data := make([]DashboardModel, len(source))
	for i, m := range source {
		provider := m.Family
		if provider == "" {
			provider = "windsurf"
		}
		visible, enabled, deprecated := true, true, m.Deprecated
		unsupportedReason, notes := m.UnsupportedReason, ""
		if a, ok := access[m.ID]; ok {
			visible = a.Visible
			enabled = a.Enabled
			deprecated = deprecated || a.Deprecated
			if strings.TrimSpace(a.UnsupportedReason) != "" {
				unsupportedReason = a.UnsupportedReason
			}
			notes = a.Notes
		}
		if deprecated && strings.TrimSpace(unsupportedReason) == "" {
			unsupportedReason = "deprecated model"
		}
		data[i] = DashboardModel{
			ID:                m.ID,
			Object:            m.Object,
			Created:           m.Created,
			OwnedBy:           m.OwnedBy,
			Provider:          provider,
			ModelUID:          m.ModelUID,
			ModelEnum:         m.ModelEnum,
			Credit:            m.Credit,
			Family:            m.Family,
			DisplayName:       m.DisplayName,
			Visible:           visible,
			Deprecated:        deprecated,
			Supported:         enabled && m.DirectSupported && !deprecated,
			DirectSupported:   m.DirectSupported,
			UnsupportedReason: unsupportedReason,
			Notes:             notes,
		}
	}
	return data
}

func initCatalog() {
	byID := map[string]Model{}
	order := make([]string, 0, len(nodeCatalogModels)+len(SupportedModels))
	for _, m := range nodeCatalogModels {
		normalizeModelDefaults(&m)
		byID[m.ID] = m
		order = append(order, m.ID)
	}
	for _, m := range SupportedModels {
		normalizeModelDefaults(&m)
		m.DirectSupported = true
		m.UnsupportedReason = ""
		m.Deprecated = false
		if m.Credit == 0 {
			if existing, ok := byID[m.ID]; ok && existing.Credit > 0 {
				m.Credit = existing.Credit
			} else {
				m.Credit = 1
			}
		}
		if existing, ok := byID[m.ID]; ok {
			if strings.TrimSpace(m.OwnedBy) == "" {
				m.OwnedBy = existing.OwnedBy
			}
			if strings.TrimSpace(m.Family) == "" {
				m.Family = existing.Family
			}
		} else {
			order = append(order, m.ID)
		}
		byID[m.ID] = m
	}
	catalogModels = make([]Model, 0, len(byID))
	seen := map[string]bool{}
	for _, id := range order {
		if seen[id] {
			continue
		}
		seen[id] = true
		catalogModels = append(catalogModels, byID[id])
	}
	catalogLookup = buildModelLookup(catalogModels)
}

func normalizeModelDefaults(m *Model) {
	if m.Object == "" {
		m.Object = "model"
	}
	if m.Created == 0 {
		m.Created = 1706745938
	}
	if m.CascadeName == "" {
		m.CascadeName = m.ID
	}
	if m.DisplayName == "" {
		m.DisplayName = m.ID
	}
	if m.Family == "" {
		m.Family = m.OwnedBy
	}
	if m.OwnedBy == "" {
		m.OwnedBy = m.Family
	}
	if m.Credit == 0 {
		m.Credit = 1
	}
	if !m.DirectSupported && strings.TrimSpace(m.UnsupportedReason) == "" && m.Family != "anthropic" {
		m.UnsupportedReason = "direct backend currently supports Claude-family production models only"
	}
}

func buildModelLookup(models []Model) map[string]int {
	lookup := map[string]int{}
	add := func(key string, idx int) {
		key = strings.TrimSpace(strings.ToLower(key))
		if key != "" {
			lookup[key] = idx
		}
	}
	for i, m := range models {
		add(m.ID, i)
		add(m.CascadeName, i)
		add(m.ModelUID, i)
	}
	for alias, target := range modelAliases {
		if idx, ok := lookup[strings.ToLower(target)]; ok {
			add(alias, idx)
		}
	}
	for alias, target := range nodeModelAliases {
		if idx, ok := lookup[strings.ToLower(target)]; ok {
			add(alias, idx)
		}
	}
	for alias, target := range publicModelAliases() {
		if idx, ok := lookup[strings.ToLower(target)]; ok {
			add(alias, idx)
		}
	}
	return lookup
}

func sameProviderFallback(originalID, candidateID string) bool {
	original := GetModelByID(originalID)
	candidate := GetModelByID(candidateID)
	if original == nil || candidate == nil {
		return false
	}
	originalProvider := fallbackProviderKey(*original)
	candidateProvider := fallbackProviderKey(*candidate)
	if originalProvider == "" || candidateProvider == "" {
		return true
	}
	return originalProvider == candidateProvider
}

func fallbackProviderKey(m Model) string {
	key := strings.ToLower(strings.TrimSpace(m.Family))
	if key == "" {
		key = strings.ToLower(strings.TrimSpace(m.OwnedBy))
	}
	switch key {
	case "claude":
		return "anthropic"
	case "windsurf":
		if strings.HasPrefix(strings.ToLower(m.ID), "claude-") {
			return "anthropic"
		}
	}
	return key
}

func publicModelAliases() map[string]string {
	raw := strings.TrimSpace(os.Getenv("WINDSURFAPI_PUBLIC_MODEL_ALIASES"))
	if raw == "" {
		return nil
	}
	out := map[string]string{}
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		sep := "="
		if strings.Contains(part, "=>") {
			sep = "=>"
		}
		pieces := strings.SplitN(part, sep, 2)
		if len(pieces) != 2 {
			continue
		}
		alias := strings.TrimSpace(pieces[0])
		target := strings.TrimSpace(strings.ToLower(pieces[1]))
		if alias == "" || target == "" {
			continue
		}
		out[alias] = target
	}
	return out
}
