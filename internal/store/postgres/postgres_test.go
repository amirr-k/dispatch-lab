package postgres_test

import (
	"context"
	"os"
	"testing"

	"dispatchlab/db"
	"dispatchlab/internal/store"
	"dispatchlab/internal/store/postgres"
	"dispatchlab/internal/store/storetest"
)

// databaseURL is the test database. Without it the Postgres tests skip, so a
// plain `go test ./...` works on a machine with no database; CI sets it.
func databaseURL(t *testing.T) string {
	t.Helper()
	url := os.Getenv("DISPATCHLAB_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set DISPATCHLAB_TEST_DATABASE_URL to run the Postgres store tests")
	}
	return url
}

func open(t *testing.T) *postgres.Store {
	t.Helper()
	ctx := context.Background()

	s, err := postgres.Open(ctx, databaseURL(t))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	// each test starts from an empty database; the cascade takes the event and
	// snapshot rows with it.
	if _, err := s.Pool().Exec(ctx, `truncate simulations, comparisons cascade`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	return s
}

func TestPostgresStoreConformance(t *testing.T) {
	storetest.Run(t, func(t *testing.T) store.Store {
		return open(t)
	})
}

// migrating an already-migrated database has to be a no-op, since the server
// runs migrations on every start.
func TestMigrateIsIdempotent(t *testing.T) {
	ctx := context.Background()
	s := open(t)

	for i := 0; i < 3; i++ {
		if err := s.Migrate(ctx); err != nil {
			t.Fatalf("Migrate run %d: %v", i, err)
		}
	}

	migrations, err := db.Migrations()
	if err != nil {
		t.Fatalf("Migrations: %v", err)
	}

	var count int
	if err := s.Pool().QueryRow(ctx, `select count(*) from schema_migrations`).Scan(&count); err != nil {
		t.Fatalf("count schema_migrations: %v", err)
	}
	if count != len(migrations) {
		t.Errorf("schema_migrations has %d rows after three migrate runs, want %d", count, len(migrations))
	}
}

// the down files have to actually work, and a migrate afterwards has to bring
// the schema back from empty.
func TestRollbackAndReapply(t *testing.T) {
	ctx := context.Background()
	s := open(t)

	migrations, err := db.Migrations()
	if err != nil {
		t.Fatalf("Migrations: %v", err)
	}
	// Rollback reverses one migration at a time, newest first.
	for range migrations {
		if err := s.Rollback(ctx); err != nil {
			t.Fatalf("Rollback: %v", err)
		}
	}

	var exists bool
	if err := s.Pool().QueryRow(ctx, `select to_regclass('simulations') is not null`).Scan(&exists); err != nil {
		t.Fatalf("check table: %v", err)
	}
	if exists {
		t.Error("simulations table still exists after rollback")
	}

	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate after rollback: %v", err)
	}
	if err := s.Pool().QueryRow(ctx, `select to_regclass('simulations') is not null`).Scan(&exists); err != nil {
		t.Fatalf("check table: %v", err)
	}
	if !exists {
		t.Error("simulations table missing after re-migrating")
	}
}

func TestOpenRejectsBadURL(t *testing.T) {
	if _, err := postgres.Open(context.Background(), "not-a-url"); err == nil {
		t.Fatal("expected an error for a malformed database url")
	}
}
