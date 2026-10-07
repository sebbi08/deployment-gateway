package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"
)

type AuditEntry struct {
	ID         int64
	Time       time.Time
	ActorType  string // agent | admin | system
	Actor      string
	Workspace  string
	TokenID    int64 // 0 = none
	Action     string
	Params     string // JSON, already redacted
	Result     string // ok | error code
	Error      string
	Severity   string // info | warn | critical
	DurationMS int64
	ClientIP   string
}

type AuditFilter struct {
	Workspace string
	Action    string
	Result    string
	Severity  string
	BeforeID  int64
	Limit     int
}

func (s *Store) InsertAudit(ctx context.Context, e AuditEntry) error {
	_, err := s.InsertAuditID(ctx, e)
	return err
}

// InsertAuditID inserts an audit row and returns its ID.
func (s *Store) InsertAuditID(ctx context.Context, e AuditEntry) (int64, error) {
	var tokenID any
	if e.TokenID != 0 {
		tokenID = e.TokenID
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO audit (ts, actor_type, actor, workspace, token_id, action, params_json,
		result, error, severity, duration_ms, client_ip) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.Time.Unix(), e.ActorType, e.Actor, e.Workspace, tokenID, e.Action, e.Params, e.Result, e.Error,
		e.Severity, e.DurationMS, e.ClientIP)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// FinishAudit completes a row written at call start.
func (s *Store) FinishAudit(ctx context.Context, id int64, e AuditEntry) error {
	_, err := s.db.ExecContext(ctx, `UPDATE audit SET result=?, error=?, severity=?, duration_ms=? WHERE id=?`,
		e.Result, e.Error, e.Severity, e.DurationMS, id)
	return err
}

func (s *Store) ListAudit(ctx context.Context, f AuditFilter) ([]AuditEntry, error) {
	var where []string
	var args []any
	for _, c := range []struct{ col, val string }{
		{"workspace", f.Workspace}, {"action", f.Action}, {"result", f.Result}, {"severity", f.Severity},
	} {
		if c.val != "" {
			where = append(where, c.col+" = ?")
			args = append(args, c.val)
		}
	}
	if f.BeforeID > 0 {
		where = append(where, "id < ?")
		args = append(args, f.BeforeID)
	}
	q := `SELECT id, ts, actor_type, actor, workspace, token_id, action, params_json, result, error, severity, duration_ms, client_ip FROM audit`
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	limit := f.Limit
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	q += " ORDER BY id DESC LIMIT " + strconv.Itoa(limit)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AuditEntry{}
	for rows.Next() {
		var e AuditEntry
		var ts int64
		var tokenID sql.NullInt64
		if err := rows.Scan(&e.ID, &ts, &e.ActorType, &e.Actor, &e.Workspace, &tokenID, &e.Action, &e.Params,
			&e.Result, &e.Error, &e.Severity, &e.DurationMS, &e.ClientIP); err != nil {
			return nil, err
		}
		e.Time, e.TokenID = time.Unix(ts, 0), tokenID.Int64
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *Store) PruneAudit(ctx context.Context, before time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM audit WHERE ts < ?`, before.Unix())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (s *Store) getSetting(ctx context.Context, key string) (string, bool, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key=?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return v, err == nil, err
}

func (s *Store) setSetting(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO settings (key, value) VALUES (?, ?)
		ON CONFLICT (key) DO UPDATE SET value=excluded.value`, key, value)
	return err
}

func (s *Store) DefaultQuota(ctx context.Context) (Quota, error) {
	v, ok, err := s.getSetting(ctx, "default_quota")
	if err != nil || !ok {
		return BuiltinDefaultQuota(), err
	}
	var q Quota
	if err := json.Unmarshal([]byte(v), &q); err != nil {
		return BuiltinDefaultQuota(), err
	}
	return q, nil
}

func (s *Store) SetDefaultQuota(ctx context.Context, q Quota) error {
	if err := q.Validate(); err != nil {
		return err
	}
	b, _ := json.Marshal(q)
	return s.setSetting(ctx, "default_quota", string(b))
}

func (s *Store) AuditRetentionDays(ctx context.Context) (int, error) {
	v, ok, err := s.getSetting(ctx, "audit_retention_days")
	if err != nil || !ok {
		return 90, err
	}
	return strconv.Atoi(v)
}

func (s *Store) SetAuditRetentionDays(ctx context.Context, days int) error {
	if days < 1 || days > 3650 {
		return errors.New("audit retention must be 1-3650 days")
	}
	return s.setSetting(ctx, "audit_retention_days", strconv.Itoa(days))
}
