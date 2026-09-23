package grilltui_test

import (
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func TestPublicSQLiteViewsExposeWorksheetSemantics(t *testing.T) {
	workingDir := t.TempDir()
	terminal := startTerminal(t, workingDir, "--name", "views", "7")
	terminal.send(t, "r")
	terminal.waitForSelection(t, 8)
	terminal.send(t, " ")
	terminal.waitForSelection(t, 9)
	terminal.send(t, "y")
	terminal.waitForSelection(t, 10)
	terminal.send(t, "q")
	terminal.waitForExit(t)

	database := openWorksheetDatabaseReadOnly(t, worksheetDatabasePath(workingDir, "views"))
	assertViewColumns(t, database, "answer_slots", []string{"question_number", "answer", "is_answered"})
	assertViewColumns(t, database, "answered_questions", []string{"question_number", "answer"})
	assertViewColumns(t, database, "worksheet_info", []string{
		"worksheet_name", "first_number", "last_number", "last_answered_number", "answered_count", "updated_at",
	})

	rows, err := database.Query("SELECT question_number, answer, is_answered FROM answer_slots ORDER BY question_number")
	if err != nil {
		t.Fatalf("query public answer_slots view: %v", err)
	}
	defer rows.Close()
	var slots []struct {
		number     int
		answer     string
		isAnswered int
	}
	for rows.Next() {
		var slot struct {
			number     int
			answer     string
			isAnswered int
		}
		if err := rows.Scan(&slot.number, &slot.answer, &slot.isAnswered); err != nil {
			t.Fatalf("scan public answer_slots view: %v", err)
		}
		slots = append(slots, slot)
	}
	if len(slots) != 10 || slots[0].number != 7 || slots[9].number != 16 {
		t.Fatalf("answer_slots range = %#v, want ten slots from 7 through 16", slots)
	}
	if slots[0].answer != "recommended" || slots[0].isAnswered != 1 {
		t.Fatalf("answered slot 7 = %#v", slots[0])
	}
	if slots[1].answer != "" || slots[1].isAnswered != 0 {
		t.Fatalf("empty slot 8 = %#v", slots[1])
	}
	assertPublicAnswers(t, worksheetDatabasePath(workingDir, "views"), map[int]string{7: "recommended", 9: "yes"})

	var (
		name               string
		firstNumber        int
		lastNumber         int
		lastAnsweredNumber sql.NullInt64
		answeredCount      int
		updatedAt          string
	)
	err = database.QueryRow(`
SELECT worksheet_name, first_number, last_number, last_answered_number, answered_count, updated_at
  FROM worksheet_info`).Scan(&name, &firstNumber, &lastNumber, &lastAnsweredNumber, &answeredCount, &updatedAt)
	if err != nil {
		t.Fatalf("query public worksheet_info view: %v", err)
	}
	if name != "views" || firstNumber != 7 || lastNumber != 16 || !lastAnsweredNumber.Valid || lastAnsweredNumber.Int64 != 9 || answeredCount != 2 || updatedAt == "" {
		t.Fatalf("worksheet_info = name=%q range=%d-%d last=%v count=%d updated=%q", name, firstNumber, lastNumber, lastAnsweredNumber, answeredCount, updatedAt)
	}
}

func TestEmptyWorksheetReportsNoAnswersOrLastAnsweredNumber(t *testing.T) {
	workingDir := t.TempDir()
	terminal := startTerminal(t, workingDir, "--name", "empty", "30")
	terminal.send(t, "q")
	terminal.waitForExit(t)

	database := openWorksheetDatabaseReadOnly(t, worksheetDatabasePath(workingDir, "empty"))
	var (
		lastAnsweredNumber sql.NullInt64
		answeredCount      int
		firstNumber        int
		lastNumber         int
	)
	if err := database.QueryRow(`
SELECT first_number, last_number, last_answered_number, answered_count
  FROM worksheet_info`).Scan(&firstNumber, &lastNumber, &lastAnsweredNumber, &answeredCount); err != nil {
		t.Fatalf("query empty public worksheet_info view: %v", err)
	}
	if firstNumber != 30 || lastNumber != 39 || lastAnsweredNumber.Valid || answeredCount != 0 {
		t.Fatalf("empty worksheet_info = range=%d-%d last=%v count=%d", firstNumber, lastNumber, lastAnsweredNumber, answeredCount)
	}
	assertPublicAnswers(t, worksheetDatabasePath(workingDir, "empty"), map[int]string{})
}

func TestNewerSQLiteSchemaIsRefusedWithoutReplacingData(t *testing.T) {
	workingDir := t.TempDir()
	terminal := startTerminal(t, workingDir, "--name", "future", "4")
	terminal.send(t, "n")
	terminal.waitForSelection(t, 5)
	terminal.send(t, "q")
	terminal.waitForExit(t)

	databasePath := worksheetDatabasePath(workingDir, "future")
	// user_version is SQLite's schema-version field. Writing it only constructs
	// a future-version fixture; the acceptance assertion remains at the CLI seam.
	database, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatalf("open fixture database: %v", err)
	}
	if _, err := database.Exec("PRAGMA user_version = 2"); err != nil {
		t.Fatalf("set future schema version: %v", err)
	}
	if err := database.Close(); err != nil {
		t.Fatalf("close fixture database: %v", err)
	}

	output := runCLIExpectFailure(t, workingDir, "--name", "future")
	if !strings.Contains(output, "unsupported schema version 2") || !strings.Contains(output, "no migration was attempted") {
		t.Fatalf("future-schema refusal is not clear: %q", output)
	}
	database = openWorksheetDatabaseReadOnly(t, databasePath)
	var version int
	if err := database.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatalf("read schema version after refusal: %v", err)
	}
	if version != 2 {
		t.Fatalf("schema version after refusal = %d, want 2", version)
	}
	assertPublicAnswers(t, databasePath, map[int]string{4: "no"})
}

func TestSQLiteStorageIsPrivateAndLegacyJSONIsIgnored(t *testing.T) {
	workingDir := t.TempDir()
	legacyDirectory := filepath.Join(workingDir, ".grill-tui")
	if err := os.MkdirAll(legacyDirectory, 0o700); err != nil {
		t.Fatalf("create legacy fixture directory: %v", err)
	}
	legacyPath := filepath.Join(legacyDirectory, "worksheet.json")
	legacyContents := []byte(`{"schema_version":1,"slots":[{"number":99,"answer":"legacy"}],"selected":0,"viewport":0}`)
	if err := os.WriteFile(legacyPath, legacyContents, 0o600); err != nil {
		t.Fatalf("write legacy fixture: %v", err)
	}

	terminal := startTerminal(t, workingDir, "5")
	screen := terminal.waitForSelection(t, 5)
	if strings.Contains(screen, "99") || strings.Contains(screen, "legacy") {
		t.Fatalf("legacy JSON affected the selected Worksheet:\n%s", screen)
	}
	terminal.send(t, "r")
	terminal.waitForSelection(t, 6)
	terminal.send(t, "q")
	terminal.waitForExit(t)

	root := filepath.Join(workingDir, ".grill-data")
	worksheetDirectory := filepath.Join(root, "untitled")
	databasePath := worksheetDatabasePath(workingDir, "untitled")
	assertOwnerOnlyMode(t, root, 0o700)
	assertOwnerOnlyMode(t, worksheetDirectory, 0o700)
	assertOwnerOnlyMode(t, filepath.Join(root, ".gitignore"), 0o600)
	assertOwnerOnlyMode(t, filepath.Join(worksheetDirectory, "worksheet.lock"), 0o600)
	assertOwnerOnlyMode(t, databasePath, 0o600)
	assertFileContents(t, filepath.Join(root, ".gitignore"), []byte("*\n"))
	assertFileContents(t, legacyPath, legacyContents)
	for _, legacyProjection := range []string{"worksheet.json", "worksheet.backup.json"} {
		assertPathDoesNotExist(t, filepath.Join(worksheetDirectory, legacyProjection))
	}
}

func TestWorksheetStorageRejectsSymlinkRedirection(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symbolic-link storage protections are Unix-specific")
	}
	tests := []struct {
		name    string
		prepare func(t *testing.T, workingDir, outside string)
		args    []string
	}{
		{
			name: "data root",
			prepare: func(t *testing.T, workingDir, outside string) {
				if err := os.Symlink(outside, filepath.Join(workingDir, ".grill-data")); err != nil {
					t.Fatalf("create data-root symlink: %v", err)
				}
			},
			args: []string{"1"},
		},
		{
			name: "Worksheet directory",
			prepare: func(t *testing.T, workingDir, outside string) {
				root := filepath.Join(workingDir, ".grill-data")
				if err := os.Mkdir(root, 0o700); err != nil {
					t.Fatalf("create data root: %v", err)
				}
				if err := os.Symlink(outside, filepath.Join(root, "redirected")); err != nil {
					t.Fatalf("create Worksheet-directory symlink: %v", err)
				}
			},
			args: []string{"--name", "redirected", "1"},
		},
		{
			name: "Worksheet Database",
			prepare: func(t *testing.T, workingDir, outside string) {
				directory := filepath.Join(workingDir, ".grill-data", "redirected")
				if err := os.MkdirAll(directory, 0o700); err != nil {
					t.Fatalf("create Worksheet directory: %v", err)
				}
				target := filepath.Join(outside, "target.sqlite")
				if err := os.WriteFile(target, nil, 0o600); err != nil {
					t.Fatalf("create outside database target: %v", err)
				}
				if err := os.Symlink(target, filepath.Join(directory, "worksheet.sqlite")); err != nil {
					t.Fatalf("create Worksheet Database symlink: %v", err)
				}
			},
			args: []string{"--name", "redirected", "1"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			workingDir := t.TempDir()
			outside := t.TempDir()
			test.prepare(t, workingDir, outside)
			output := runCLIExpectFailure(t, workingDir, test.args...)
			if !strings.Contains(strings.ToLower(output), "symbolic link") {
				t.Fatalf("symlink refusal is not clear: %q", output)
			}
			if matches, err := filepath.Glob(filepath.Join(outside, "worksheet*")); err != nil || len(matches) != 0 {
				t.Fatalf("symlinked destination received Worksheet artifacts: matches=%v error=%v", matches, err)
			}
		})
	}
}

func TestNamedWorksheetsUseIndependentSQLiteDatabases(t *testing.T) {
	workingDir := t.TempDir()

	untitled := startTerminal(t, workingDir, "12")
	untitled.send(t, "r")
	untitled.waitForSelection(t, 13)
	untitled.send(t, "q")
	untitled.waitForExit(t)

	named := startTerminal(t, workingDir, "--name", "abc", "20")
	named.send(t, "y")
	named.waitForSelection(t, 21)
	named.send(t, "q")
	named.waitForExit(t)

	assertPublicAnswers(t, worksheetDatabasePath(workingDir, "untitled"), map[int]string{12: "recommended"})
	assertPublicAnswers(t, worksheetDatabasePath(workingDir, "abc"), map[int]string{20: "yes"})

	reopened := startTerminal(t, workingDir, "--name", "abc")
	screen := reopened.waitForSelection(t, 21)
	if !strings.Contains(screen, "20") || !strings.Contains(screen, "yes") {
		t.Fatalf("named Worksheet did not reopen from SQLite:\n%s", screen)
	}
	reopened.send(t, "q")
	reopened.waitForExit(t)
}

func TestWorksheetNameValidationRejectsInvalidAndReservedNamesBeforeStorage(t *testing.T) {
	invalidNames := map[string]string{
		"uppercase":          "Abc",
		"whitespace":         "two words",
		"dot":                "a.b",
		"forward separator":  "a/b",
		"backward separator": `a\b`,
		"traversal":          "..",
		"leading separator":  "_abc",
		"leading hyphen":     "-abc",
		"non-ASCII":          "café",
		"overlong":           strings.Repeat("a", 65),
		"empty":              "",
	}
	for testName, worksheetName := range invalidNames {
		t.Run(testName, func(t *testing.T) {
			workingDir := t.TempDir()
			output := runCLIExpectFailure(t, workingDir, "--name", worksheetName, "1")
			if !strings.Contains(output, "invalid Worksheet Name") {
				t.Fatalf("invalid-name error is not actionable: %q", output)
			}
			assertPathDoesNotExist(t, filepath.Join(workingDir, ".grill-data"))
		})
	}

	workingDir := t.TempDir()
	output := runCLIExpectFailure(t, workingDir, "--name", "untitled", "1")
	if !strings.Contains(output, "reserved") || !strings.Contains(output, "omit --name") {
		t.Fatalf("reserved-name error is not actionable: %q", output)
	}
	assertPathDoesNotExist(t, filepath.Join(workingDir, ".grill-data"))
}

func TestWorksheetNameAcceptsBoundaryAndShellFriendlyValues(t *testing.T) {
	for _, worksheetName := range []string{"a", "0", "abc-123_def", strings.Repeat("a", 64)} {
		t.Run(worksheetName, func(t *testing.T) {
			workingDir := t.TempDir()
			terminal := startTerminal(t, workingDir, "--name", worksheetName, "3")
			terminal.waitForSelection(t, 3)
			terminal.send(t, "q")
			terminal.waitForExit(t)
			if _, err := os.Stat(worksheetDatabasePath(workingDir, worksheetName)); err != nil {
				t.Fatalf("valid Worksheet Name did not create its database: %v", err)
			}
		})
	}
}

func worksheetDatabasePath(workingDir, worksheetName string) string {
	return filepath.Join(workingDir, ".grill-data", worksheetName, "worksheet.sqlite")
}

func openWorksheetDatabaseReadOnly(t *testing.T, path string) *sql.DB {
	t.Helper()
	databaseURL := (&url.URL{Scheme: "file", Path: path}).String() + "?mode=ro"
	database, err := sql.Open("sqlite", databaseURL)
	if err != nil {
		t.Fatalf("open Worksheet Database %s: %v", path, err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := database.Ping(); err != nil {
		t.Fatalf("ping Worksheet Database %s: %v", path, err)
	}
	return database
}

func assertPublicAnswers(t *testing.T, path string, want map[int]string) {
	t.Helper()
	database := openWorksheetDatabaseReadOnly(t, path)
	rows, err := database.Query("SELECT question_number, answer FROM answered_questions ORDER BY question_number")
	if err != nil {
		t.Fatalf("query public answered_questions view: %v", err)
	}
	defer rows.Close()
	got := make(map[int]string)
	for rows.Next() {
		var number int
		var answer string
		if err := rows.Scan(&number, &answer); err != nil {
			t.Fatalf("scan public answered_questions view: %v", err)
		}
		got[number] = answer
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read public answered_questions view: %v", err)
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("answered_questions = %#v, want %#v", got, want)
	}
}

func assertViewColumns(t *testing.T, database *sql.DB, view string, want []string) {
	t.Helper()
	rows, err := database.Query("SELECT name FROM pragma_table_info(?) ORDER BY cid", view)
	if err != nil {
		t.Fatalf("inspect %s columns: %v", view, err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan %s column: %v", view, err)
		}
		got = append(got, name)
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("%s columns = %v, want %v", view, got, want)
	}
}

func assertPathDoesNotExist(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("%s exists after rejected command (error %v)", path, err)
	}
}

func assertOwnerOnlyMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission bits are not supported on Windows")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("%s permissions = %04o, want %04o", path, got, want)
	}
}
