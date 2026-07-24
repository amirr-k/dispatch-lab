// Package db holds the SQL schema. The migration files are embedded so the
// server binary can bring an empty database up to date on its own, with no
// separate migration tool to install in the deployment image.
package db

import (
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

// Migration is one versioned schema change and its reverse.
type Migration struct {
	Version int
	Name    string
	Up      string
	Down    string
}

// Migrations returns every migration in ascending version order. It fails
// loudly on a malformed or unpaired file rather than silently skipping it,
// since a missing migration would surface much later as a confusing query
// error.
func Migrations() ([]Migration, error) {
	entries, err := fs.ReadDir(migrationFiles, "migrations")
	if err != nil {
		return nil, err
	}

	byVersion := make(map[int]*Migration)
	for _, entry := range entries {
		name := entry.Name()
		version, base, direction, err := parseName(name)
		if err != nil {
			return nil, err
		}

		body, err := fs.ReadFile(migrationFiles, "migrations/"+name)
		if err != nil {
			return nil, err
		}

		m, ok := byVersion[version]
		if !ok {
			m = &Migration{Version: version, Name: base}
			byVersion[version] = m
		}
		if direction == "up" {
			m.Up = string(body)
		} else {
			m.Down = string(body)
		}
	}

	out := make([]Migration, 0, len(byVersion))
	for _, m := range byVersion {
		if strings.TrimSpace(m.Up) == "" {
			return nil, fmt.Errorf("migration %d (%s) has no up file", m.Version, m.Name)
		}
		if strings.TrimSpace(m.Down) == "" {
			return nil, fmt.Errorf("migration %d (%s) has no down file", m.Version, m.Name)
		}
		out = append(out, *m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Version < out[j].Version })
	return out, nil
}

// parseName splits "0001_initial_schema.up.sql" into 1, "initial_schema", "up".
func parseName(name string) (version int, base, direction string, err error) {
	parts := strings.Split(strings.TrimSuffix(name, ".sql"), ".")
	if len(parts) != 2 {
		return 0, "", "", fmt.Errorf("migration %q must be named <version>_<name>.<up|down>.sql", name)
	}
	direction = parts[1]
	if direction != "up" && direction != "down" {
		return 0, "", "", fmt.Errorf("migration %q has direction %q, want up or down", name, direction)
	}

	prefix, base, found := strings.Cut(parts[0], "_")
	if !found {
		return 0, "", "", fmt.Errorf("migration %q must be named <version>_<name>.<up|down>.sql", name)
	}
	version, err = strconv.Atoi(prefix)
	if err != nil {
		return 0, "", "", fmt.Errorf("migration %q has a non-numeric version: %w", name, err)
	}
	return version, base, direction, nil
}
