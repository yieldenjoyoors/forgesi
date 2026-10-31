-- Fleet runs: one row per agent run the orchestrator starts.
CREATE TABLE IF NOT EXISTS knot_runs (
  id         TEXT PRIMARY KEY,
  repo       TEXT NOT NULL,
  branch     TEXT NOT NULL,
  todo_line  INTEGER NOT NULL,
  todo_title TEXT NOT NULL,
  status     TEXT NOT NULL DEFAULT 'running', -- running|done|failed
  summary    TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  ended_at   TEXT NOT NULL DEFAULT ''
);
