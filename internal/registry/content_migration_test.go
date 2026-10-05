package registry

import (
	"io/fs"
	"strings"
	"testing"
)

func TestContentMigrationIsDDLOnlyAndPreservesLegacyIdentities(t *testing.T) {
	body, err := fs.ReadFile(migrationsFS, "migrations/00011_content_bundles.sql")
	if err != nil {
		t.Fatal(err)
	}
	var sql strings.Builder
	for _, line := range strings.Split(string(body), "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "--") {
			sql.WriteString(line)
			sql.WriteByte('\n')
		}
	}
	text := strings.ToUpper(sql.String())
	for _, forbidden := range []string{"INSERT INTO ", "UPDATE PAPER_", "UPDATE PARSE_", "DELETE FROM ", "TRUNCATE ", "DROP TABLE IF EXISTS PAPER_SOURCES", "DROP TABLE IF EXISTS PARSE_REVISIONS"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("content migration changes legacy data: %s", forbidden)
		}
	}
	for _, required := range []string{
		"CREATE TABLE IF NOT EXISTS PAPER_SOURCE_IMPORTS",
		"CREATE TABLE IF NOT EXISTS PARSE_BUNDLES",
		"MANIFEST_SHA256 CHAR(64) NOT NULL",
		"FOREIGN KEY (PAPER_ID, SOURCE_ID)",
		"FOREIGN KEY (PAPER_ID, SOURCE_ID, REVISION_ID)",
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("missing content schema safety: %s", required)
		}
	}
}
