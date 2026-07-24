package db

import (
	"strings"
	"testing"
)

func TestMigrationsAreOrderedAndPaired(t *testing.T) {
	migrations, err := Migrations()
	if err != nil {
		t.Fatalf("Migrations: %v", err)
	}
	if len(migrations) == 0 {
		t.Fatal("no migrations were embedded")
	}

	seen := make(map[int]bool)
	for i, m := range migrations {
		if i > 0 && m.Version <= migrations[i-1].Version {
			t.Errorf("migration %d is not in ascending version order", m.Version)
		}
		if seen[m.Version] {
			t.Errorf("duplicate migration version %d", m.Version)
		}
		seen[m.Version] = true

		if strings.TrimSpace(m.Up) == "" || strings.TrimSpace(m.Down) == "" {
			t.Errorf("migration %d (%s) is missing an up or down body", m.Version, m.Name)
		}
	}
}

// every table has to be creatable from an empty database and droppable again,
// so the statements are written to tolerate both.
func TestMigrationsAreSafeFromEmpty(t *testing.T) {
	migrations, err := Migrations()
	if err != nil {
		t.Fatalf("Migrations: %v", err)
	}

	for _, m := range migrations {
		for _, stmt := range strings.Split(m.Up, ";") {
			stmt = strings.ToLower(strings.TrimSpace(stripComments(stmt)))
			if strings.HasPrefix(stmt, "create table") && !strings.Contains(stmt, "if not exists") {
				t.Errorf("migration %d has a create table without IF NOT EXISTS", m.Version)
			}
			if strings.HasPrefix(stmt, "create index") && !strings.Contains(stmt, "if not exists") {
				t.Errorf("migration %d has a create index without IF NOT EXISTS", m.Version)
			}
		}
		for _, stmt := range strings.Split(m.Down, ";") {
			stmt = strings.ToLower(strings.TrimSpace(stripComments(stmt)))
			if strings.HasPrefix(stmt, "drop") && !strings.Contains(stmt, "if exists") {
				t.Errorf("migration %d has a drop without IF EXISTS", m.Version)
			}
		}
	}
}

func TestParseName(t *testing.T) {
	version, base, direction, err := parseName("0012_add_guest_sessions.up.sql")
	if err != nil {
		t.Fatalf("parseName: %v", err)
	}
	if version != 12 || base != "add_guest_sessions" || direction != "up" {
		t.Errorf("parseName = %d, %q, %q", version, base, direction)
	}

	for _, name := range []string{
		"initial.up.sql",        // no version prefix
		"0001_initial.sql",      // no direction
		"abc_initial.up.sql",    // non-numeric version
		"0001_initial.side.sql", // unknown direction
	} {
		if _, _, _, err := parseName(name); err == nil {
			t.Errorf("parseName(%q) should have failed", name)
		}
	}
}

func stripComments(stmt string) string {
	var out []string
	for _, line := range strings.Split(stmt, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "--") {
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}
