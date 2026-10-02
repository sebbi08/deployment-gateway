package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/sebbi08/deployment-gateway/internal/tmpl"
)

type StoredTemplate struct {
	ID int64
	tmpl.Template
	CreatedAt time.Time
	UpdatedAt time.Time
}

const tplCols = `id, name, description, host_pattern, networks_json, labels_json, created_at, updated_at`

func scanTemplate(sc scanner) (StoredTemplate, error) {
	var t StoredTemplate
	var nets, labels string
	var created, updated int64
	if err := sc.Scan(&t.ID, &t.Name, &t.Description, &t.HostPattern, &nets, &labels, &created, &updated); err != nil {
		return StoredTemplate{}, notFound(err)
	}
	t.CreatedAt, t.UpdatedAt = time.Unix(created, 0), time.Unix(updated, 0)
	if err := json.Unmarshal([]byte(nets), &t.Networks); err != nil {
		return StoredTemplate{}, err
	}
	if err := json.Unmarshal([]byte(labels), &t.Labels); err != nil {
		return StoredTemplate{}, err
	}
	return t, nil
}

func encodeTemplate(t tmpl.Template) (nets, labels string) {
	if t.Networks == nil {
		t.Networks = []string{}
	}
	if t.Labels == nil {
		t.Labels = map[string]string{}
	}
	n, _ := json.Marshal(t.Networks)
	l, _ := json.Marshal(t.Labels)
	return string(n), string(l)
}

func (s *Store) CreateTemplate(ctx context.Context, t tmpl.Template) (int64, error) {
	nets, labels := encodeTemplate(t)
	now := s.now().Unix()
	res, err := s.db.ExecContext(ctx, `INSERT INTO templates (name, description, host_pattern, networks_json, labels_json, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, t.Name, t.Description, t.HostPattern, nets, labels, now, now)
	if isUnique(err) {
		return 0, ErrExists
	}
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) UpdateTemplate(ctx context.Context, id int64, t tmpl.Template) error {
	nets, labels := encodeTemplate(t)
	res, err := s.db.ExecContext(ctx, `UPDATE templates SET name=?, description=?, host_pattern=?, networks_json=?, labels_json=?, updated_at=?
		WHERE id=?`, t.Name, t.Description, t.HostPattern, nets, labels, s.now().Unix(), id)
	if isUnique(err) {
		return ErrExists
	}
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) DeleteTemplate(ctx context.Context, id int64) error {
	return s.withTx(ctx, func(tx *sql.Tx) error {
		var grants int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM workspace_templates WHERE template_id=?`, id).Scan(&grants); err != nil {
			return err
		}
		if grants > 0 {
			return ErrInUse
		}
		res, err := tx.ExecContext(ctx, `DELETE FROM templates WHERE id=?`, id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		return nil
	})
}

func (s *Store) GetTemplate(ctx context.Context, id int64) (StoredTemplate, error) {
	return scanTemplate(s.db.QueryRowContext(ctx, `SELECT `+tplCols+` FROM templates WHERE id=?`, id))
}

func (s *Store) ListTemplates(ctx context.Context) ([]StoredTemplate, error) {
	return s.queryTemplates(ctx, `SELECT `+tplCols+` FROM templates ORDER BY name`)
}

func (s *Store) GrantedTemplates(ctx context.Context, wsID int64) ([]StoredTemplate, error) {
	return s.queryTemplates(ctx, `SELECT t.id, t.name, t.description, t.host_pattern, t.networks_json, t.labels_json, t.created_at, t.updated_at
		FROM templates t JOIN workspace_templates wt ON wt.template_id = t.id WHERE wt.workspace_id=? ORDER BY t.name`, wsID)
}

func (s *Store) queryTemplates(ctx context.Context, q string, args ...any) ([]StoredTemplate, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []StoredTemplate{}
	for rows.Next() {
		t, err := scanTemplate(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) SetGrants(ctx context.Context, wsID int64, templateIDs []int64) error {
	return s.withTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM workspace_templates WHERE workspace_id=?`, wsID); err != nil {
			return err
		}
		for _, id := range templateIDs {
			if _, err := tx.ExecContext(ctx, `INSERT INTO workspace_templates (workspace_id, template_id) VALUES (?, ?)`, wsID, id); err != nil {
				return err
			}
		}
		return nil
	})
}
