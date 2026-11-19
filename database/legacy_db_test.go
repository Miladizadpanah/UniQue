package database

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/mhsanaei/3x-ui/v2/config"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// makeTestDB writes a real SQLite file with the three tables the adoption probe
// looks at and the given row counts. The schema is deliberately minimal: inspectDB
// must work on a foreign (older) database it has never migrated.
func makeTestDB(t *testing.T, path string, users, inbounds, accounts int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	gdb, err := gorm.Open(sqlite.Open(path+"?_busy_timeout=2000"), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	sqlDB, err := gdb.DB()
	if err != nil {
		t.Fatalf("sql db: %v", err)
	}
	defer sqlDB.Close()
	for _, stmt := range []string{
		"CREATE TABLE users (id integer primary key, username text)",
		"CREATE TABLE inbounds (id integer primary key, remark text)",
		"CREATE TABLE client_traffics (id integer primary key, email text)",
	} {
		if _, err := sqlDB.Exec(stmt); err != nil {
			t.Fatalf("exec %q: %v", stmt, err)
		}
	}
	for i := 0; i < users; i++ {
		if _, err := sqlDB.Exec("INSERT INTO users (username) VALUES (?)", "u"); err != nil {
			t.Fatalf("insert user: %v", err)
		}
	}
	for i := 0; i < inbounds; i++ {
		if _, err := sqlDB.Exec("INSERT INTO inbounds (remark) VALUES (?)", "i"); err != nil {
			t.Fatalf("insert inbound: %v", err)
		}
	}
	for i := 0; i < accounts; i++ {
		if _, err := sqlDB.Exec("INSERT INTO client_traffics (email) VALUES (?)", "a"); err != nil {
			t.Fatalf("insert account: %v", err)
		}
	}
	if err := sqlDB.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

func TestInspectDBReadsCounts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vpn-ui.db")
	makeTestDB(t, path, 2, 3, 5)

	stats, ok := inspectDB(path)
	if !ok {
		t.Fatal("inspectDB said a valid panel database is unreadable")
	}
	if stats.Users != 2 || stats.Inbounds != 3 || stats.Accounts != 5 {
		t.Fatalf("counts = %+v, want users=2 inbounds=3 accounts=5", stats)
	}
	if !stats.carriesData() {
		t.Fatal("a database with inbounds must count as carrying data")
	}
}

func TestInspectDBRejectsNonDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vpn-ui.db")
	if err := os.WriteFile(path, []byte("this is not sqlite"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := inspectDB(path); ok {
		t.Fatal("inspectDB accepted a text file as a database")
	}
	if _, ok := inspectDB(filepath.Join(t.TempDir(), "missing.db")); ok {
		t.Fatal("inspectDB accepted a missing file")
	}
}

// The upgrade trap: the binary was installed elsewhere, so the new path has no
// database at all while the old one still holds the operator's inbounds.
func TestMigrateLegacyAdoptsPopulatedDBWhenCurrentMissing(t *testing.T) {
	dir := t.TempDir()
	current := filepath.Join(dir, "new", "vpn-ui.db")
	legacy := filepath.Join(dir, "old", "vpn-ui.db")
	makeTestDB(t, legacy, 1, 7, 9)

	migrateLegacyDBFrom(current, []string{legacy})

	stats, ok := inspectDB(current)
	if !ok || stats.Inbounds != 7 || stats.Accounts != 9 {
		t.Fatalf("current DB after adoption = %+v (ok=%v), want the legacy 7 inbounds / 9 accounts", stats, ok)
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Fatalf("legacy DB still present at %s; adoption should move it", legacy)
	}
}

// The regression this whole path exists for: a fresh binary created an empty
// database next to itself and the panel looked wiped. The empty file must be kept
// (as a recoverable copy) and the populated one adopted.
func TestMigrateLegacyReplacesEmptyCurrentAndKeepsItAside(t *testing.T) {
	dir := t.TempDir()
	current := filepath.Join(dir, "new", "vpn-ui.db")
	legacy := filepath.Join(dir, "old", "vpn-ui.db")
	makeTestDB(t, current, 1, 0, 0) // freshly seeded: one admin, no data
	makeTestDB(t, legacy, 1, 4, 4)

	migrateLegacyDBFrom(current, []string{legacy})

	stats, ok := inspectDB(current)
	if !ok || stats.Inbounds != 4 {
		t.Fatalf("current DB after adoption = %+v (ok=%v), want the legacy 4 inbounds", stats, ok)
	}
	aside, err := filepath.Glob(current + ".empty-*")
	if err != nil {
		t.Fatal(err)
	}
	if len(aside) != 1 {
		t.Fatalf("expected one .empty-* copy of the discarded database, got %v", aside)
	}
	if _, ok := inspectDB(aside[0]); !ok {
		t.Fatalf("%s is not a readable database; the empty file must stay recoverable", aside[0])
	}
}

func TestMigrateLegacyNeverTouchesPopulatedCurrent(t *testing.T) {
	dir := t.TempDir()
	current := filepath.Join(dir, "vpn-ui.db")
	legacy := filepath.Join(dir, "old.db")
	makeTestDB(t, current, 1, 2, 2)
	makeTestDB(t, legacy, 1, 50, 50) // richer, but not this panel's

	migrateLegacyDBFrom(current, []string{legacy})

	stats, ok := inspectDB(current)
	if !ok || stats.Inbounds != 2 {
		t.Fatalf("current DB = %+v (ok=%v), want its own 2 inbounds untouched", stats, ok)
	}
	if _, err := os.Stat(legacy); err != nil {
		t.Fatalf("legacy DB at %s was consumed even though the current one had data", legacy)
	}
}

func TestMigrateLegacyPrefersTheRicherCandidate(t *testing.T) {
	dir := t.TempDir()
	current := filepath.Join(dir, "vpn-ui.db")
	poor := filepath.Join(dir, "poor.db")
	rich := filepath.Join(dir, "rich.db")
	makeTestDB(t, poor, 1, 1, 1)
	makeTestDB(t, rich, 1, 20, 30)

	migrateLegacyDBFrom(current, []string{poor, rich})

	stats, ok := inspectDB(current)
	if !ok || stats.Inbounds != 20 {
		t.Fatalf("adopted %+v (ok=%v), want the richer candidate (20 inbounds)", stats, ok)
	}
}

func TestMigrateLegacySkipsEmptyCandidates(t *testing.T) {
	dir := t.TempDir()
	current := filepath.Join(dir, "vpn-ui.db")
	empty := filepath.Join(dir, "old.db")
	makeTestDB(t, empty, 1, 0, 0)

	migrateLegacyDBFrom(current, []string{empty})

	if _, err := os.Stat(current); !os.IsNotExist(err) {
		t.Fatalf("a fresh install with only empty legacy files must not adopt anything (current=%s)", current)
	}
}

func TestMigrateLegacyLeavesUnreadableCurrentAlone(t *testing.T) {
	dir := t.TempDir()
	current := filepath.Join(dir, "vpn-ui.db")
	legacy := filepath.Join(dir, "old.db")
	if err := os.WriteFile(current, []byte("corrupt"), 0o644); err != nil {
		t.Fatal(err)
	}
	makeTestDB(t, legacy, 1, 9, 9)

	migrateLegacyDBFrom(current, []string{legacy})

	raw, err := os.ReadFile(current)
	if err != nil || string(raw) != "corrupt" {
		t.Fatalf("an unreadable current DB was replaced (err=%v, body=%q)", err, string(raw))
	}
}

func TestLegacyDBPathsExcludeCurrentAndHonourExplicitFolder(t *testing.T) {
	t.Setenv("VPNUI_DB_FOLDER", "")
	paths := config.LegacyDBPaths()
	current := config.GetDBPath()
	for _, p := range paths {
		if p == current {
			t.Fatalf("LegacyDBPaths included the current database %s", current)
		}
	}
	if !containsPath(paths, filepath.Join("/etc/vpn-ui", "vpn-ui.db")) {
		t.Fatalf("LegacyDBPaths must search the historical install prefixes, got %v", paths)
	}
	t.Setenv("VPNUI_DB_FOLDER", t.TempDir())
	if paths := config.LegacyDBPaths(); len(paths) != 0 {
		t.Fatalf("VPNUI_DB_FOLDER must suppress discovery, got %v", paths)
	}
}

func containsPath(paths []string, needle string) bool {
	for _, p := range paths {
		if p == needle {
			return true
		}
	}
	return false
}
