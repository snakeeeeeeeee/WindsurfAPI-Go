package modelaccess

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/zhangyu/windsurfapi-go/internal/models"
	"github.com/zhangyu/windsurfapi-go/internal/store"
)

type Manager struct {
	db *sql.DB
}

type Access struct {
	ModelID           string `json:"model_id"`
	Visible           bool   `json:"visible"`
	Enabled           bool   `json:"enabled"`
	Deprecated        bool   `json:"deprecated"`
	UnsupportedReason string `json:"unsupported_reason,omitempty"`
	Notes             string `json:"notes,omitempty"`
}

type Config struct {
	Mode string   `json:"mode"`
	List []string `json:"list"`
}

type Patch struct {
	Visible           *bool  `json:"visible,omitempty"`
	Enabled           *bool  `json:"enabled,omitempty"`
	Deprecated        *bool  `json:"deprecated,omitempty"`
	UnsupportedReason string `json:"unsupported_reason,omitempty"`
	Notes             string `json:"notes,omitempty"`
}

func NewManager(store *store.SQLiteStore) *Manager {
	return &Manager{db: store.DB}
}

func (m *Manager) Config() Config {
	cfg := Config{Mode: "all", List: []string{}}
	row := m.db.QueryRow(`SELECT value FROM runtime_kv WHERE key = 'model_access_config'`)
	var raw string
	if err := row.Scan(&raw); err != nil {
		return cfg
	}
	var stored Config
	if err := json.Unmarshal([]byte(raw), &stored); err != nil {
		return cfg
	}
	stored.Mode = normalizeMode(stored.Mode)
	if stored.Mode == "" {
		stored.Mode = "all"
	}
	seen := map[string]bool{}
	for _, item := range stored.List {
		id := models.NormalizeModelID(item)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		cfg.List = append(cfg.List, id)
	}
	cfg.Mode = stored.Mode
	return cfg
}

func (m *Manager) SetConfig(cfg Config) error {
	cfg.Mode = normalizeMode(cfg.Mode)
	if cfg.Mode == "" {
		return fmt.Errorf("unsupported model access mode %q", cfg.Mode)
	}
	seen := map[string]bool{}
	list := make([]string, 0, len(cfg.List))
	for _, raw := range cfg.List {
		id := models.NormalizeModelID(raw)
		if id == "" {
			continue
		}
		if models.GetModelByID(id) == nil {
			return fmt.Errorf("unknown model: %s", raw)
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		list = append(list, id)
	}
	cfg.List = list
	value, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	_, err = m.db.Exec(
		`INSERT INTO runtime_kv (key, value, updated_at)
		 VALUES ('model_access_config', ?, CURRENT_TIMESTAMP)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = CURRENT_TIMESTAMP`,
		string(value),
	)
	return err
}

func normalizeMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", "all", "default":
		return "all"
	case "allowlist", "allow", "include":
		return "allowlist"
	case "blocklist", "block", "exclude":
		return "blocklist"
	default:
		return ""
	}
}

func (m *Manager) List() (map[string]Access, error) {
	rows, err := m.db.Query(`SELECT model_id, visible, enabled, deprecated, unsupported_reason, notes FROM model_access`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]Access{}
	for rows.Next() {
		var access Access
		var visible, enabled, deprecated bool
		if err := rows.Scan(&access.ModelID, &visible, &enabled, &deprecated, &access.UnsupportedReason, &access.Notes); err != nil {
			return nil, err
		}
		access.Visible = visible
		access.Enabled = enabled
		access.Deprecated = deprecated
		out[access.ModelID] = access
	}
	return out, rows.Err()
}

func (m *Manager) Get(modelID string) (Access, error) {
	modelID = models.NormalizeModelID(modelID)
	if modelID == "" {
		return Access{}, fmt.Errorf("model_id required")
	}
	row := m.db.QueryRow(`SELECT model_id, visible, enabled, deprecated, unsupported_reason, notes FROM model_access WHERE model_id = ?`, modelID)
	var access Access
	var visible, enabled, deprecated bool
	err := row.Scan(&access.ModelID, &visible, &enabled, &deprecated, &access.UnsupportedReason, &access.Notes)
	if err == nil {
		access.Visible = visible
		access.Enabled = enabled
		access.Deprecated = deprecated
		return access, nil
	}
	if err != sql.ErrNoRows {
		return Access{}, err
	}
	model := models.GetModelByID(modelID)
	defaultEnabled := true
	defaultDeprecated := false
	reason := ""
	if model != nil {
		defaultEnabled = model.DirectSupported
		defaultDeprecated = model.Deprecated
		reason = model.UnsupportedReason
		if defaultDeprecated && strings.TrimSpace(reason) == "" {
			reason = "deprecated model"
		}
	}
	return Access{ModelID: modelID, Visible: true, Enabled: defaultEnabled, Deprecated: defaultDeprecated, UnsupportedReason: reason}, nil
}

func (m *Manager) IsVisible(modelID string) bool {
	access, err := m.Get(modelID)
	return err == nil && access.Visible
}

func (m *Manager) IsEnabled(modelID string) (bool, string) {
	access, err := m.Get(modelID)
	if err != nil {
		return false, err.Error()
	}
	if !access.Enabled {
		if strings.TrimSpace(access.UnsupportedReason) != "" {
			return false, access.UnsupportedReason
		}
		return false, "model disabled"
	}
	if !access.Visible {
		return false, "model hidden by access policy"
	}
	return true, ""
}

func (m *Manager) Upsert(modelID string, patch Patch) (Access, error) {
	modelID = models.NormalizeModelID(modelID)
	if modelID == "" {
		return Access{}, fmt.Errorf("model_id required")
	}
	if models.GetModelByID(modelID) == nil {
		return Access{}, fmt.Errorf("unknown model: %s", modelID)
	}
	cur, err := m.Get(modelID)
	if err != nil {
		return Access{}, err
	}
	if patch.Visible != nil {
		cur.Visible = *patch.Visible
	}
	if patch.Enabled != nil {
		cur.Enabled = *patch.Enabled
	}
	if patch.Deprecated != nil {
		cur.Deprecated = *patch.Deprecated
	}
	cur.UnsupportedReason = strings.TrimSpace(patch.UnsupportedReason)
	cur.Notes = patch.Notes
	_, err = m.db.Exec(
		`INSERT INTO model_access (model_id, visible, enabled, deprecated, unsupported_reason, notes, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
		 ON CONFLICT(model_id) DO UPDATE SET
		   visible = excluded.visible,
		   enabled = excluded.enabled,
		   deprecated = excluded.deprecated,
		   unsupported_reason = excluded.unsupported_reason,
		   notes = excluded.notes,
		   updated_at = CURRENT_TIMESTAMP`,
		cur.ModelID, cur.Visible, cur.Enabled, cur.Deprecated, cur.UnsupportedReason, cur.Notes,
	)
	if err != nil {
		return Access{}, err
	}
	return cur, nil
}

func (m *Manager) Reset(modelID string) error {
	modelID = models.NormalizeModelID(modelID)
	if modelID == "" {
		return fmt.Errorf("model_id required")
	}
	_, err := m.db.Exec(`DELETE FROM model_access WHERE model_id = ?`, modelID)
	return err
}
