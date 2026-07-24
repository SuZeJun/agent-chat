package persistence

import (
	"testing"
	"testing/fstest"
)

func TestLoadMigrations(t *testing.T) {
	items, err := loadMigrations()
	if err != nil {
		t.Fatalf("loadMigrations returned error: %v", err)
	}
	if len(items) == 0 {
		t.Fatal("expected at least one migration")
	}
	if items[0].version != 1 || items[0].name != "000001_init.sql" {
		t.Fatalf("unexpected first migration: %#v", items[0])
	}
	if items[0].sql == "" {
		t.Fatal("expected migration SQL")
	}
	if len(items[0].checksum) != 64 {
		t.Fatalf("unexpected migration checksum: %q", items[0].checksum)
	}
	last := items[len(items)-1]
	if last.version != 3 || last.name != "000003_chat.sql" {
		t.Fatalf("unexpected last migration: %#v", last)
	}
}

func TestParseMigrationRejectsInvalidName(t *testing.T) {
	if _, err := parseMigration("init.sql"); err == nil {
		t.Fatal("expected an error")
	}
}

func TestLoadMigrationsRejectsEmptySQL(t *testing.T) {
	source := fstest.MapFS{
		"000001_empty.sql": {
			Data: []byte(" \n\t"),
		},
	}
	if _, err := loadMigrationsFromFS(source); err == nil {
		t.Fatal("expected an error")
	}
}
