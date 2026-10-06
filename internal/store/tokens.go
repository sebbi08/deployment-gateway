package store

import (
	"context"
	"database/sql"
	"time"
)

type Token struct {
	ID          int64
	WorkspaceID int64
	Label       string
	Prefix      string
	CreatedAt   time.Time
	LastUsedAt  *time.Time
	RevokedAt   *time.Time
}

const tokenCols = `id, workspace_id, label, prefix, created_at, last_used_at, revoked_at`

func scanToken(sc scanner) (Token, error) {
	var t Token
	var created int64
	var used, revoked sql.NullInt64
	err := sc.Scan(&t.ID, &t.WorkspaceID, &t.Label, &t.Prefix, &created, &used, &revoked)
	t.CreatedAt = time.Unix(created, 0)
	t.LastUsedAt, t.RevokedAt = timePtr(used), timePtr(revoked)
	return t, notFound(err)
}

func (s *Store) CreateToken(ctx context.Context, wsID int64, label, hash, prefix string) (Token, error) {
	now := s.now().Unix()
	res, err := s.db.ExecContext(ctx, `INSERT INTO tokens (workspace_id, label, hash, prefix, created_at) VALUES (?, ?, ?, ?, ?)`,
		wsID, label, hash, prefix, now)
	if isUnique(err) {
		return Token{}, ErrExists
	}
	if err != nil {
		return Token{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Token{}, err
	}
	return s.GetToken(ctx, id)
}

func (s *Store) GetToken(ctx context.Context, id int64) (Token, error) {
	return scanToken(s.db.QueryRowContext(ctx, `SELECT `+tokenCols+` FROM tokens WHERE id=?`, id))
}

// TokenByHash returns a non-revoked token and its workspace.
func (s *Store) TokenByHash(ctx context.Context, hash string) (Token, Workspace, error) {
	t, err := scanToken(s.db.QueryRowContext(ctx, `SELECT `+tokenCols+` FROM tokens WHERE hash=? AND revoked_at IS NULL`, hash))
	if err != nil {
		return Token{}, Workspace{}, err
	}
	w, err := s.GetWorkspace(ctx, t.WorkspaceID)
	if err != nil {
		return Token{}, Workspace{}, err
	}
	return t, w, nil
}

func (s *Store) TouchToken(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE tokens SET last_used_at=? WHERE id=?`, s.now().Unix(), id)
	return err
}

func (s *Store) RevokeToken(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `UPDATE tokens SET revoked_at=? WHERE id=? AND revoked_at IS NULL`, s.now().Unix(), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// RevokeWorkspaceTokens revokes every active token of a workspace in one
// statement. Already revoked tokens keep their original revocation time.
func (s *Store) RevokeWorkspaceTokens(ctx context.Context, wsID int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE tokens SET revoked_at=? WHERE workspace_id=? AND revoked_at IS NULL`, s.now().Unix(), wsID)
	return err
}

func (s *Store) ListTokens(ctx context.Context, wsID int64) ([]Token, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+tokenCols+` FROM tokens WHERE workspace_id=? ORDER BY id`, wsID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Token{}
	for rows.Next() {
		t, err := scanToken(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// TokenActive reports whether token id still exists with the given hash, is
// not revoked and belongs to workspace wsID.
func (s *Store) TokenActive(ctx context.Context, id int64, hash string, wsID int64) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM tokens WHERE id=? AND hash=? AND workspace_id=? AND revoked_at IS NULL`, id, hash, wsID).Scan(&n)
	return n == 1, err
}
