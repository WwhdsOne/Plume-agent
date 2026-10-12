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
