package registry

import (
	"io/fs"
	"strconv"
	"strings"
	"testing"
)

func TestMigrationVersionsAreUnique(t *testing.T) {
	entries, err := fs.ReadDir(migrationsFS, "migrations")
	if err != nil {
		t.Fatalf("read embedded migrations: %v", err)
	}

	seen := make(map[int]string)
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".sql") {
			continue
		}
		prefix, _, ok := strings.Cut(name, "_")
		if !ok {
			t.Fatalf("migration %q has no numeric version prefix", name)
		}
		version, err := strconv.Atoi(prefix)
		if err != nil || version <= 0 {
			t.Fatalf("migration %q has invalid version prefix %q", name, prefix)
		}
		if previous, exists := seen[version]; exists {
			t.Fatalf("migration version %d is duplicated by %q and %q", version, previous, name)
		}
		seen[version] = name
	}
}
