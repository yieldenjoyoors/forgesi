-- The knot's event log: every record write and push appends one row; the
-- firehose replays it in seq order with cursor resume.
CREATE TABLE IF NOT EXISTS knot_events (
  seq        INTEGER PRIMARY KEY AUTOINCREMENT,
  time       TEXT NOT NULL,
  repo       TEXT NOT NULL,
  kind       TEXT NOT NULL,          -- 'commit' | 'sync' | 'identity' | 'account'
  collection TEXT NOT NULL,
  rkey       TEXT NOT NULL,
  rev        TEXT NOT NULL,          -- TID, monotonic per repo
  prev_rev   TEXT NOT NULL DEFAULT '',
  cid        TEXT NOT NULL DEFAULT '',
  change     TEXT NOT NULL DEFAULT '', -- git oid (x-tngl-change)
  record     BLOB                    -- dag-cbor encoded record body (commits only)
);
