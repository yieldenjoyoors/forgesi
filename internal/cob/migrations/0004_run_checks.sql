-- CI checks reported by the agent (or a spindle) with its run result.
-- Separate table: migrations re-run unconditionally, so ALTER (not
-- idempotent in SQLite) is out; this joins on run id instead.
CREATE TABLE IF NOT EXISTS knot_run_checks (
  run_id TEXT PRIMARY KEY,
  checks TEXT NOT NULL
);
