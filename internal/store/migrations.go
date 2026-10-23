package store

// schemaStatements 是 v1 全量建表语句，只在 user_version < 1 的事务内执行。
// 表名与列名是存储契约的一部分；消息与用量以带版本语义的 JSON 载荷存储，
// 不把运行时结构体直接当永久格式。
var schemaStatements = []string{
	`CREATE TABLE IF NOT EXISTS sessions (
		id TEXT PRIMARY KEY,
		workspace TEXT NOT NULL,
		title TEXT NOT NULL,
		model_ref TEXT NOT NULL,
		created_at INTEGER NOT NULL,
		last_run_at INTEGER NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS runs (
		id TEXT PRIMARY KEY,
		session_id TEXT NOT NULL REFERENCES sessions(id),
		input TEXT NOT NULL,
		status TEXT NOT NULL,
		error TEXT NOT NULL DEFAULT '',
		started_at INTEGER NOT NULL,
		finished_at INTEGER,
		reply TEXT NOT NULL DEFAULT '',
		reasoning TEXT NOT NULL DEFAULT '',
		tool_events TEXT NOT NULL DEFAULT '[]'
	)`,
	`CREATE INDEX IF NOT EXISTS idx_runs_session ON runs(session_id)`,
	`CREATE TABLE IF NOT EXISTS model_calls (
		id TEXT PRIMARY KEY,
		run_id TEXT NOT NULL REFERENCES runs(id),
		session_id TEXT NOT NULL,
		sequence INTEGER NOT NULL,
		usage TEXT,
		saved_at INTEGER NOT NULL
	)`,
	`CREATE INDEX IF NOT EXISTS idx_calls_session ON model_calls(session_id)`,
	`CREATE TABLE IF NOT EXISTS turns (
		session_id TEXT NOT NULL REFERENCES sessions(id),
		seq INTEGER NOT NULL,
		run_id TEXT NOT NULL,
		messages TEXT NOT NULL,
		created_at INTEGER NOT NULL,
		PRIMARY KEY (session_id, seq)
	)`,
}

// managementSchemaStatements 在同一迁移事务内扩展 v2。旧历史明确归属
// local；绑定故意没有 session 外键，允许默认空会话先绑定、首次 run 才落盘。
var managementSchemaStatements = []string{
	`ALTER TABLE sessions ADD COLUMN scope_key TEXT NOT NULL DEFAULT 'local'`,
	`ALTER TABLE sessions ADD COLUMN title_manual INTEGER NOT NULL DEFAULT 0`,
	`CREATE INDEX idx_sessions_scope_workspace ON sessions(scope_key, workspace, last_run_at)`,
	`CREATE TABLE bindings (
		scope_key TEXT NOT NULL,
		workspace TEXT NOT NULL,
		session_id TEXT NOT NULL,
		PRIMARY KEY (scope_key, workspace)
	)`,
	`CREATE INDEX idx_bindings_session ON bindings(session_id)`,
	`CREATE TABLE command_receipts (
		scope_key TEXT NOT NULL,
		workspace TEXT NOT NULL,
		request_id TEXT NOT NULL,
		fingerprint TEXT NOT NULL,
		command TEXT NOT NULL,
		payload TEXT NOT NULL,
		created_at INTEGER NOT NULL,
		PRIMARY KEY (scope_key, workspace, request_id)
	)`,
}
