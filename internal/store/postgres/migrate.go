package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"dispatchlab/db"
)

// Migrate applies every migration the database has not seen yet, in version
// order. Each migration and its version row are committed in a single
// transaction, so a failure halfway leaves the schema exactly where it was.
func (s *Store) Migrate(ctx context.Context) error {
	if _, err := s.pool.Exec(ctx, `
		create table if not exists schema_migrations (
			version    integer primary key,
			name       text        not null,
			applied_at timestamptz not null default now()
		)`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	applied, err := s.appliedVersions(ctx)
	if err != nil {
		return err
	}

	migrations, err := db.Migrations()
	if err != nil {
		return err
	}
	for _, m := range migrations {
		if applied[m.Version] {
			continue
		}
		if err := s.applyMigration(ctx, m.Version, m.Name, m.Up); err != nil {
			return fmt.Errorf("apply migration %d (%s): %w", m.Version, m.Name, err)
		}
	}
	return nil
}

// Rollback reverses the most recently applied migration. It exists so the
// down files are exercised rather than assumed to work.
func (s *Store) Rollback(ctx context.Context) error {
	applied, err := s.appliedVersions(ctx)
	if err != nil {
		return err
	}

	migrations, err := db.Migrations()
	if err != nil {
		return err
	}
	for i := len(migrations) - 1; i >= 0; i-- {
		m := migrations[i]
		if !applied[m.Version] {
			continue
		}
		return s.inTx(ctx, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, m.Down); err != nil {
				return err
			}
			_, err := tx.Exec(ctx, `delete from schema_migrations where version = $1`, m.Version)
			return err
		})
	}
	return nil
}

func (s *Store) appliedVersions(ctx context.Context) (map[int]bool, error) {
	rows, err := s.pool.Query(ctx, `select version from schema_migrations`)
	if err != nil {
		return nil, fmt.Errorf("read schema_migrations: %w", err)
	}
	defer rows.Close()

	applied := make(map[int]bool)
	for rows.Next() {
		var version int
		if err := rows.Scan(&version); err != nil {
			return nil, err
		}
		applied[version] = true
	}
	return applied, rows.Err()
}

func (s *Store) applyMigration(ctx context.Context, version int, name, body string) error {
	return s.inTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, body); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `insert into schema_migrations (version, name) values ($1, $2)`, version, name)
		return err
	})
}

func (s *Store) inTx(ctx context.Context, fn func(pgx.Tx) error) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
