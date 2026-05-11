package store

import (
	"database/sql"
	"fmt"
	"strings"

	_ "github.com/mattn/go-sqlite3"
)

// SQLiteStore 封装 SQLite 连接和操作
type SQLiteStore struct {
	DB *sql.DB
}

// NewSQLiteStore 创建并初始化 SQLite 存储
func NewSQLiteStore(dbPath string) (*SQLiteStore, error) {
	db, err := sql.Open("sqlite3", dbPath+"?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}

	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}

	store := &SQLiteStore{DB: db}
	if err := store.migrate(); err != nil {
		return nil, fmt.Errorf("migrate sqlite: %w", err)
	}

	return store, nil
}

func (s *SQLiteStore) migrate() error {
	queries := []string{
		`CREATE TABLE IF NOT EXISTS accounts (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			email TEXT NOT NULL UNIQUE,
			firebase_token TEXT NOT NULL,
			user_id TEXT NOT NULL,
			proxy_url TEXT DEFAULT '',
			tier TEXT DEFAULT 'unknown',
			plan_name TEXT DEFAULT '',
			rate_limited_until DATETIME DEFAULT NULL,
			quota_daily_percent REAL DEFAULT NULL,
			quota_weekly_percent REAL DEFAULT NULL,
			quota_daily_reset_at DATETIME DEFAULT NULL,
			quota_weekly_reset_at DATETIME DEFAULT NULL,
			prompt_limit REAL DEFAULT NULL,
			prompt_used REAL DEFAULT NULL,
			prompt_remaining REAL DEFAULT NULL,
			flex_limit REAL DEFAULT NULL,
			flex_used REAL DEFAULT NULL,
			flex_remaining REAL DEFAULT NULL,
			overage_balance REAL DEFAULT NULL,
			plan_start TEXT DEFAULT '',
			plan_end TEXT DEFAULT '',
			health_checked_at DATETIME DEFAULT NULL,
			last_used_at DATETIME DEFAULT NULL,
			enabled INTEGER DEFAULT 1,
			banned INTEGER DEFAULT 0,
			notes TEXT DEFAULT '',
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TABLE IF NOT EXISTS account_models (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			account_id INTEGER NOT NULL,
			model_name TEXT NOT NULL,
			enabled INTEGER DEFAULT 1,
			cooldown_until DATETIME DEFAULT NULL,
			FOREIGN KEY (account_id) REFERENCES accounts(id),
			UNIQUE(account_id, model_name)
		)`,
		`CREATE TABLE IF NOT EXISTS model_access (
			model_id TEXT PRIMARY KEY,
			visible INTEGER DEFAULT 1,
			enabled INTEGER DEFAULT 1,
			deprecated INTEGER DEFAULT 0,
			unsupported_reason TEXT DEFAULT '',
			notes TEXT DEFAULT '',
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TABLE IF NOT EXISTS runtime_kv (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TABLE IF NOT EXISTS request_events (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			time DATETIME NOT NULL,
			req_id TEXT NOT NULL,
			route TEXT DEFAULT '',
			model TEXT DEFAULT '',
			caller_key_hash TEXT DEFAULT '',
			account_id INTEGER DEFAULT 0,
			attempt INTEGER DEFAULT 0,
			status TEXT DEFAULT '',
			http_status INTEGER DEFAULT 0,
			error_class TEXT DEFAULT '',
			error TEXT DEFAULT '',
			retry INTEGER DEFAULT 0,
			stream INTEGER DEFAULT 0,
			latency_ms INTEGER DEFAULT 0,
			send_ms INTEGER DEFAULT 0,
			first_text_ms INTEGER DEFAULT 0,
			usage_input INTEGER DEFAULT 0,
			usage_output INTEGER DEFAULT 0,
			usage_cache_read INTEGER DEFAULT 0,
			tool_call_count INTEGER DEFAULT 0,
			reuse_hit INTEGER DEFAULT 0,
			reuse_miss_reason TEXT DEFAULT ''
		)`,
		`CREATE TABLE IF NOT EXISTS proxy_pool (
			id TEXT PRIMARY KEY,
			url TEXT NOT NULL,
			enabled INTEGER DEFAULT 1,
			successes INTEGER DEFAULT 0,
			failures INTEGER DEFAULT 0,
			last_error TEXT DEFAULT '',
			last_test_status TEXT DEFAULT '',
			last_test_latency_ms INTEGER DEFAULT 0,
			last_test_at DATETIME DEFAULT NULL,
			cooldown_until DATETIME DEFAULT NULL,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TABLE IF NOT EXISTS account_proxy_bindings (
			account_id INTEGER PRIMARY KEY,
			provider TEXT NOT NULL DEFAULT 'novproxy',
			protocol TEXT NOT NULL DEFAULT 'http',
			host TEXT NOT NULL,
			port INTEGER NOT NULL,
			username TEXT DEFAULT '',
			password TEXT DEFAULT '',
			session_id TEXT DEFAULT '',
			egress_ip TEXT DEFAULT '',
			country TEXT DEFAULT '',
			region TEXT DEFAULT '',
			city TEXT DEFAULT '',
			isp_org TEXT DEFAULT '',
			status TEXT NOT NULL DEFAULT 'active',
			expires_at DATETIME DEFAULT NULL,
			last_verified_at DATETIME DEFAULT NULL,
			verify_error TEXT DEFAULT '',
			fail_count INTEGER NOT NULL DEFAULT 0,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			FOREIGN KEY (account_id) REFERENCES accounts(id) ON DELETE CASCADE
		)`,
		`CREATE INDEX IF NOT EXISTS idx_request_events_time ON request_events(time DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_request_events_route ON request_events(route, time DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_request_events_model ON request_events(model, time DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_proxy_pool_enabled ON proxy_pool(enabled, updated_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_account_proxy_bindings_status ON account_proxy_bindings(status)`,
		`CREATE INDEX IF NOT EXISTS idx_account_proxy_bindings_expires ON account_proxy_bindings(expires_at)`,
		`ALTER TABLE accounts ADD COLUMN tier TEXT DEFAULT 'unknown'`,
		`ALTER TABLE accounts ADD COLUMN plan_name TEXT DEFAULT ''`,
		`ALTER TABLE accounts ADD COLUMN rate_limited_until DATETIME DEFAULT NULL`,
		`ALTER TABLE accounts ADD COLUMN quota_daily_percent REAL DEFAULT NULL`,
		`ALTER TABLE accounts ADD COLUMN quota_weekly_percent REAL DEFAULT NULL`,
		`ALTER TABLE accounts ADD COLUMN quota_daily_reset_at DATETIME DEFAULT NULL`,
		`ALTER TABLE accounts ADD COLUMN quota_weekly_reset_at DATETIME DEFAULT NULL`,
		`ALTER TABLE accounts ADD COLUMN prompt_limit REAL DEFAULT NULL`,
		`ALTER TABLE accounts ADD COLUMN prompt_used REAL DEFAULT NULL`,
		`ALTER TABLE accounts ADD COLUMN prompt_remaining REAL DEFAULT NULL`,
		`ALTER TABLE accounts ADD COLUMN flex_limit REAL DEFAULT NULL`,
		`ALTER TABLE accounts ADD COLUMN flex_used REAL DEFAULT NULL`,
		`ALTER TABLE accounts ADD COLUMN flex_remaining REAL DEFAULT NULL`,
		`ALTER TABLE accounts ADD COLUMN overage_balance REAL DEFAULT NULL`,
		`ALTER TABLE accounts ADD COLUMN plan_start TEXT DEFAULT ''`,
		`ALTER TABLE accounts ADD COLUMN plan_end TEXT DEFAULT ''`,
		`ALTER TABLE accounts ADD COLUMN health_checked_at DATETIME DEFAULT NULL`,
		`ALTER TABLE accounts ADD COLUMN last_used_at DATETIME DEFAULT NULL`,
		`ALTER TABLE request_events ADD COLUMN reuse_hit INTEGER DEFAULT 0`,
		`ALTER TABLE request_events ADD COLUMN reuse_miss_reason TEXT DEFAULT ''`,
	}

	for _, q := range queries {
		if _, err := s.DB.Exec(q); err != nil {
			if isDuplicateColumnErr(err) {
				continue
			}
			return fmt.Errorf("exec migration: %w", err)
		}
	}
	return nil
}

func isDuplicateColumnErr(err error) bool {
	return err != nil && strings.Contains(err.Error(), "duplicate column name:")
}

// Close 关闭数据库连接
func (s *SQLiteStore) Close() error {
	return s.DB.Close()
}
