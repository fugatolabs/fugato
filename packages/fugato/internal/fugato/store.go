package fugato

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

type Runtime struct {
	Endpoints      []PortBinding `json:"endpoints,omitempty"`
	ID             string        `json:"id"`
	ProjectDir     string        `json:"project_dir"`
	Worktree       string        `json:"worktree"`
	Repository     string        `json:"repository"`
	ComposeProject string        `json:"compose_project"`
	Backend        string        `json:"backend"`
	Desired        string        `json:"desired"`
	Observed       string        `json:"observed"`
	PreviewURL     string        `json:"preview_url,omitempty"`
	LastError      string        `json:"last_error,omitempty"`
	UpdatedAt      string        `json:"updated_at"`
}

type Store struct{ db *sql.DB }

func OpenStore(ctx context.Context, stateDir string) (*Store, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(stateDir, 0700); err != nil {
		return nil, err
	}
	if err := os.Chmod(stateDir, 0700); err != nil {
		return nil, err
	}
	path := filepath.Join(stateDir, "registry.sqlite")
	if info, err := os.Lstat(path); err == nil && !info.Mode().IsRegular() {
		return nil, fmt.Errorf("registry path is not a regular file: %s", path)
	} else if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := file.Close(); err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0600); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	for _, statement := range []string{"PRAGMA journal_mode=WAL", "PRAGMA busy_timeout=5000"} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			db.Close()
			return nil, fmt.Errorf("initialize registry: %w", err)
		}
	}
	var version int
	if err := db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		db.Close()
		return nil, fmt.Errorf("read registry schema version: %w", err)
	}
	var obsolete int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM pragma_table_info('runtimes') WHERE name = 'endpoint'").Scan(&obsolete); err != nil {
		db.Close()
		return nil, fmt.Errorf("inspect registry schema: %w", err)
	}
	if obsolete > 0 {
		db.Close()
		return nil, errors.New("registry belongs to an unsupported prerelease; choose a fresh FUGATO_STATE_DIR and retain the old directory and data")
	}
	if version > 1 {
		db.Close()
		return nil, fmt.Errorf("registry schema version %d is newer than this Fugato build", version)
	}
	if version == 0 {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			db.Close()
			return nil, err
		}
		if _, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS runtimes (
			id TEXT PRIMARY KEY,
			project_dir TEXT NOT NULL UNIQUE,
			worktree TEXT NOT NULL,
			repository TEXT NOT NULL,
			compose_project TEXT NOT NULL UNIQUE,
			backend TEXT NOT NULL,
			desired TEXT NOT NULL,
			observed TEXT NOT NULL,
			last_error TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`); err != nil {
			_ = tx.Rollback()
			db.Close()
			return nil, fmt.Errorf("migrate registry: %w", err)
		}
		if _, err := tx.ExecContext(ctx, "PRAGMA user_version=1"); err != nil {
			_ = tx.Rollback()
			db.Close()
			return nil, fmt.Errorf("set registry version: %w", err)
		}
		if err := tx.Commit(); err != nil {
			db.Close()
			return nil, err
		}
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) Save(ctx context.Context, r *Runtime) error {
	updatedAt := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.db.ExecContext(ctx, `INSERT INTO runtimes
		(id, project_dir, worktree, repository, compose_project, backend, desired, observed, last_error, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET backend=excluded.backend, desired=excluded.desired, observed=excluded.observed,
		last_error=excluded.last_error, updated_at=excluded.updated_at`,
		r.ID, r.ProjectDir, r.Worktree, r.Repository, r.ComposeProject, r.Backend, r.Desired, r.Observed, r.LastError, updatedAt)
	if err == nil {
		r.UpdatedAt = updatedAt
	}
	return err
}

func (s *Store) Get(ctx context.Context, id string) (Runtime, bool, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id, project_dir, worktree, repository, compose_project, backend,
		desired, observed, last_error, updated_at FROM runtimes WHERE id = ?`, id)
	r, err := scanRuntime(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Runtime{}, false, nil
	}
	return r, err == nil, err
}

func (s *Store) List(ctx context.Context) ([]Runtime, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, project_dir, worktree, repository, compose_project, backend,
		desired, observed, last_error, updated_at FROM runtimes ORDER BY updated_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []Runtime
	for rows.Next() {
		r, err := scanRuntime(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, r)
	}
	return items, rows.Err()
}

type scanner interface{ Scan(...any) error }

func scanRuntime(row scanner) (Runtime, error) {
	var r Runtime
	err := row.Scan(&r.ID, &r.ProjectDir, &r.Worktree, &r.Repository, &r.ComposeProject,
		&r.Backend, &r.Desired, &r.Observed, &r.LastError, &r.UpdatedAt)
	return r, err
}
