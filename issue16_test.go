package grilltui_test

import (
	"database/sql"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestListWithNoWorksheetsPrintsAnEmptyTableWithoutCreatingStorage(t *testing.T) {
	workingDir := t.TempDir()

	result := runCLI(t, workingDir, environmentOverrides{}, "list")

	if result.err != nil {
		t.Fatalf("grill-tui list failed: %v\nstderr: %s", result.err, result.stderr)
	}
	if result.stdout != "NAME  RANGE  ANSWERED  LAST  UPDATED\n" {
		t.Fatalf("grill-tui list stdout = %q, want an empty table", result.stdout)
	}
	if result.stderr != "" {
		t.Fatalf("grill-tui list stderr = %q, want empty", result.stderr)
	}
	if _, err := os.Stat(filepath.Join(workingDir, ".grill-data")); !os.IsNotExist(err) {
		t.Fatalf("list created Worksheet storage; stat error = %v", err)
	}
}

func TestListReportsEveryWorksheetFromPublicMetadataInRecencyOrder(t *testing.T) {
	workingDir := t.TempDir()
	createWorksheet := func(args []string, answer string) {
		t.Helper()
		terminal := startTerminal(t, workingDir, args...)
		if answer != "" {
			terminal.send(t, answer)
			terminal.waitForSelection(t, mustPositiveInteger(t, args[len(args)-1])+1)
		}
		terminal.send(t, "q")
		terminal.waitForExit(t)
	}
	createWorksheet([]string{"--name", "older", "8"}, "y")
	createWorksheet([]string{"20"}, "")
	createWorksheet([]string{"--name", "newest", "11"}, "r")

	wantByName := map[string]listedWorksheetMetadata{}
	for _, name := range []string{"older", "untitled", "newest"} {
		wantByName[name] = readListedWorksheetMetadata(t, worksheetDatabasePath(workingDir, name))
	}

	result := runCLI(t, workingDir, environmentOverrides{}, "list")
	if result.err != nil {
		t.Fatalf("grill-tui list failed: %v\nstderr: %s", result.err, result.stderr)
	}
	if result.stderr != "" {
		t.Fatalf("grill-tui list stderr = %q, want empty", result.stderr)
	}
	lines := strings.Split(strings.TrimSpace(result.stdout), "\n")
	if len(lines) != 4 {
		t.Fatalf("grill-tui list printed %d lines, want header plus three Worksheets:\n%s", len(lines), result.stdout)
	}
	if fields := strings.Fields(lines[0]); strings.Join(fields, "|") != "NAME|RANGE|ANSWERED|LAST|UPDATED" {
		t.Fatalf("list headers = %q", lines[0])
	}
	for index, wantName := range []string{"newest", "untitled", "older"} {
		fields := strings.Fields(lines[index+1])
		if len(fields) != 5 {
			t.Fatalf("list row = %q, want five columns", lines[index+1])
		}
		want := wantByName[wantName]
		wantLast := "—"
		if want.lastAnsweredNumber.Valid {
			wantLast = strconv.FormatInt(want.lastAnsweredNumber.Int64, 10)
		}
		if fields[0] != wantName || fields[1] != strconv.Itoa(want.firstNumber)+"-"+strconv.Itoa(want.lastNumber) ||
			fields[2] != strconv.Itoa(want.answeredCount) || fields[3] != wantLast || fields[4] != want.updatedAt {
			t.Fatalf("list row = %q, does not match public worksheet_info = %#v", lines[index+1], want)
		}
	}
}

type listedWorksheetMetadata struct {
	firstNumber        int
	lastNumber         int
	lastAnsweredNumber sql.NullInt64
	answeredCount      int
	updatedAt          string
}

func readListedWorksheetMetadata(t *testing.T, path string) listedWorksheetMetadata {
	t.Helper()
	database := openWorksheetDatabaseReadOnly(t, path)
	defer database.Close()
	var metadata listedWorksheetMetadata
	if err := database.QueryRow(`
SELECT first_number, last_number, last_answered_number, answered_count, updated_at
  FROM worksheet_info`).Scan(
		&metadata.firstNumber,
		&metadata.lastNumber,
		&metadata.lastAnsweredNumber,
		&metadata.answeredCount,
		&metadata.updatedAt,
	); err != nil {
		t.Fatalf("read public Worksheet metadata: %v", err)
	}
	return metadata
}

func mustPositiveInteger(t *testing.T, value string) int {
	t.Helper()
	number, err := strconv.Atoi(value)
	if err != nil || number < 1 {
		t.Fatalf("fixture value %q is not a positive integer", value)
	}
	return number
}

func TestListIgnoresArchivesAndUnrelatedEntriesAndBreaksUpdatedTiesByName(t *testing.T) {
	workingDir := t.TempDir()
	updated := "2026-09-23T08:15:30.123456789Z"
	installPublicWorksheetInfoFixture(t, workingDir, worksheetInfoFixture{
		name: "beta", firstNumber: 20, lastNumber: 29, lastAnsweredNumber: 21, answeredCount: 2, updatedAt: updated,
	})
	installPublicWorksheetInfoFixture(t, workingDir, worksheetInfoFixture{
		name: "alpha", firstNumber: 3, lastNumber: 12, updatedAt: updated,
	})

	archiveOnly := filepath.Join(workingDir, ".grill-data", "retired")
	if err := os.MkdirAll(archiveOnly, 0o700); err != nil {
		t.Fatalf("create archive-only fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(archiveOnly, "worksheet.sqlite.reset-20260923T081530Z"), []byte("archive"), 0o600); err != nil {
		t.Fatalf("write reset archive fixture: %v", err)
	}
	unrelated := filepath.Join(workingDir, ".grill-data", "Not-A-Worksheet")
	if err := os.MkdirAll(unrelated, 0o700); err != nil {
		t.Fatalf("create unrelated directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(unrelated, "worksheet.sqlite"), []byte("not sqlite"), 0o600); err != nil {
		t.Fatalf("write unrelated database-shaped file: %v", err)
	}

	result := runCLI(t, workingDir, environmentOverrides{}, "list")
	if result.err != nil || result.stderr != "" {
		t.Fatalf("grill-tui list = error %v, stderr %q", result.err, result.stderr)
	}
	want := "NAME   RANGE  ANSWERED  LAST  UPDATED\n" +
		"alpha  3-12   0         —     " + updated + "\n" +
		"beta   20-29  2         21    " + updated + "\n"
	if result.stdout != want {
		t.Fatalf("grill-tui list stdout =\n%s\nwant:\n%s", result.stdout, want)
	}
}

func TestListReportsEveryUnreadableActiveWorksheetWithoutPartialOutput(t *testing.T) {
	workingDir := t.TempDir()
	installPublicWorksheetInfoFixture(t, workingDir, worksheetInfoFixture{
		name: "healthy", firstNumber: 1, lastNumber: 10, lastAnsweredNumber: 4,
		answeredCount: 1, updatedAt: "2026-09-23T09:00:00Z",
	})
	for _, name := range []string{"broken-one", "broken-two"} {
		directory := filepath.Join(workingDir, ".grill-data", name)
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatalf("create unreadable Worksheet directory: %v", err)
		}
		if err := os.WriteFile(filepath.Join(directory, "worksheet.sqlite"), []byte("not a SQLite database: "+name), 0o600); err != nil {
			t.Fatalf("write unreadable Worksheet fixture: %v", err)
		}
	}

	result := runCLI(t, workingDir, environmentOverrides{}, "list")
	if result.err == nil {
		t.Fatal("grill-tui list unexpectedly succeeded with unreadable active Worksheets")
	}
	if result.stdout != "" {
		t.Fatalf("grill-tui list stdout = %q, want no partial table", result.stdout)
	}
	for _, want := range []string{"broken-one", "broken-two", "Worksheet", "read"} {
		if !strings.Contains(result.stderr, want) {
			t.Fatalf("grill-tui list stderr = %q, want actionable text containing %q", result.stderr, want)
		}
	}
	if strings.Contains(result.stderr, "healthy") {
		t.Fatalf("grill-tui list reported the healthy Worksheet as unreadable: %q", result.stderr)
	}
}

func TestListReadsAWorksheetWhileItsTUIWriterIsOpenWithoutMutation(t *testing.T) {
	workingDir := t.TempDir()
	owner := startTerminal(t, workingDir, "--name", "live", "40")
	owner.send(t, "y")
	owner.waitForSelection(t, 41)
	databasePath := worksheetDatabasePath(workingDir, "live")
	wantViews := publicViewSnapshot(t, databasePath)
	wantDatabase, err := os.ReadFile(databasePath)
	if err != nil {
		t.Fatalf("read live Worksheet before list: %v", err)
	}

	result := runCLI(t, workingDir, environmentOverrides{}, "list")
	if result.err != nil || result.stderr != "" {
		owner.send(t, "q")
		owner.waitForExit(t)
		t.Fatalf("grill-tui list during live TUI = error %v, stderr %q", result.err, result.stderr)
	}
	lines := strings.Split(strings.TrimSpace(result.stdout), "\n")
	var fields []string
	if len(lines) == 2 {
		fields = strings.Fields(lines[1])
	}
	if len(fields) != 5 ||
		fields[0] != "live" || fields[1] != "40-49" || fields[2] != "1" || fields[3] != "40" {
		owner.send(t, "q")
		owner.waitForExit(t)
		t.Fatalf("list did not report the live Worksheet metadata:\n%s", result.stdout)
	}
	if gotViews := publicViewSnapshot(t, databasePath); gotViews != wantViews {
		owner.send(t, "q")
		owner.waitForExit(t)
		t.Fatalf("list mutated public Worksheet metadata\nbefore:\n%s\nafter:\n%s", wantViews, gotViews)
	}
	gotDatabase, err := os.ReadFile(databasePath)
	if err != nil {
		owner.send(t, "q")
		owner.waitForExit(t)
		t.Fatalf("read live Worksheet after list: %v", err)
	}
	if string(gotDatabase) != string(wantDatabase) {
		owner.send(t, "q")
		owner.waitForExit(t)
		t.Fatal("list changed the authoritative Worksheet Database")
	}

	owner.send(t, "n")
	owner.waitForSelection(t, 42)
	owner.send(t, "q")
	owner.waitForExit(t)
	assertPublicAnswers(t, worksheetDatabasePath(workingDir, "live"), map[int]string{40: "yes", 41: "no"})
}

type worksheetInfoFixture struct {
	name               string
	firstNumber        int
	lastNumber         int
	lastAnsweredNumber int
	answeredCount      int
	updatedAt          string
}

func installPublicWorksheetInfoFixture(t *testing.T, workingDir string, fixture worksheetInfoFixture) {
	t.Helper()
	directory := filepath.Join(workingDir, ".grill-data", fixture.name)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatalf("create Worksheet fixture directory: %v", err)
	}
	database, err := sql.Open("sqlite", filepath.Join(directory, "worksheet.sqlite"))
	if err != nil {
		t.Fatalf("open Worksheet fixture: %v", err)
	}
	defer database.Close()
	if _, err := database.Exec(`
PRAGMA user_version = 1;
CREATE TABLE fixture_metadata (
    worksheet_name TEXT,
    first_number INTEGER,
    last_number INTEGER,
    last_answered_number INTEGER,
    answered_count INTEGER,
    updated_at TEXT
);`); err != nil {
		t.Fatalf("create public metadata fixture: %v", err)
	}
	var nullableLast any
	if fixture.lastAnsweredNumber > 0 {
		nullableLast = fixture.lastAnsweredNumber
	}
	if _, err := database.Exec(`
INSERT INTO fixture_metadata VALUES (?, ?, ?, ?, ?, ?)`,
		fixture.name, fixture.firstNumber, fixture.lastNumber, nullableLast, fixture.answeredCount, fixture.updatedAt); err != nil {
		t.Fatalf("insert public metadata fixture: %v", err)
	}
	if _, err := database.Exec(`
CREATE VIEW worksheet_info(
    worksheet_name, first_number, last_number, last_answered_number, answered_count, updated_at
) AS SELECT worksheet_name, first_number, last_number, last_answered_number, answered_count, updated_at
       FROM fixture_metadata`); err != nil {
		t.Fatalf("create public worksheet_info fixture: %v", err)
	}
}
