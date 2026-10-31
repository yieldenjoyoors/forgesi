package cob

import (
	"database/sql"
)

// D1Backend persists objects and refs in D1 (or any database/sql store —
// tests run it over pure-Go SQLite). Workers' isolates share nothing, so
// this — not memory — is the real backend; the wasm entry wires it to the
// D1 binding, tests use an in-memory SQLite running the same migrations.
type D1Backend struct {
	db *sql.DB
}

// NewD1Backend builds the SQL-backed Backend. Schema: migrations/0001_init.sql.
func NewD1Backend(db *sql.DB) *D1Backend {
	return &D1Backend{db: db}
}

// PutObject implements Backend (idempotent by content oid).
func (d *D1Backend) PutObject(content []byte) (string, error) {
	oid := objectID(content)
	_, err := d.db.Exec(
		`INSERT OR IGNORE INTO knot_objects (oid, content) VALUES (?, ?)`,
		oid, content)
	if err != nil {
		return "", err
	}
	return oid, nil
}

// GetObject implements Backend.
func (d *D1Backend) GetObject(oid string) ([]byte, error) {
	var content []byte
	err := d.db.QueryRow(`SELECT content FROM knot_objects WHERE oid = ?`, oid).Scan(&content)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	return content, err
}

// RefHead implements Backend.
func (d *D1Backend) RefHead(ref string) (string, bool, error) {
	var oid string
	err := d.db.QueryRow(`SELECT oid FROM knot_refs WHERE ref = ?`, ref).Scan(&oid)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return oid, true, nil
}

// SetRef implements Backend.
func (d *D1Backend) SetRef(ref string, oid string) error {
	_, err := d.db.Exec(
		`INSERT INTO knot_refs (ref, oid) VALUES (?, ?)
		 ON CONFLICT(ref) DO UPDATE SET oid = excluded.oid`,
		ref, oid)
	return err
}

// ListRefs implements Backend.ListRefs over the refs table. substr prefix
// match instead of LIKE: the repo-DID prefixes are long and D1 rejects them
// as "LIKE pattern too complex".
func (d *D1Backend) ListRefs(prefix, after string, limit int) ([]string, error) {
	rows, err := d.db.Query(
		`SELECT ref FROM knot_refs
		  WHERE substr(ref, 1, ?) = ? AND ref > ?
		  ORDER BY ref LIMIT ?`,
		len(prefix), prefix, after, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var ref string
		if err := rows.Scan(&ref); err != nil {
			return nil, err
		}
		out = append(out, ref)
	}
	return out, rows.Err()
}
