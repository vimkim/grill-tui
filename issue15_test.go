package grilltui_test

import (
	"crypto/sha256"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestQueryPrintsBoundedAnswerListWithFlexibleNameFlag(t *testing.T) {
	workingDir := t.TempDir()
	terminal := startTerminal(t, workingDir, "--name", "notes", "998")
	terminal.send(t, "i")
	terminal.waitFor(t, "Inline Custom Answer 998:")
	terminal.send(t, "alpha\r")
	terminal.waitForSelection(t, 999)
	terminal.send(t, " ")
	terminal.waitForSelection(t, 1000)
	terminal.send(t, "i")
	terminal.waitFor(t, "Inline Custom Answer 1000:")
	terminal.send(t, "omega\r")
	terminal.waitForSelection(t, 1001)
	terminal.send(t, "q")
	terminal.waitForExit(t)

	for _, args := range [][]string{
		{"query", "998", "1000", "--name", "notes"},
		{"query", "--name", "notes", "998", "1000"},
	} {
		result := runCLI(t, workingDir, environmentOverrides{}, args...)
		if result.err != nil {
			t.Fatalf("grill-tui %s failed: %v\nstderr: %s", strings.Join(args, " "), result.err, result.stderr)
		}
		if result.stdout != "998. alpha\n1000. omega" {
			t.Fatalf("grill-tui %s stdout = %q, want bounded Answer List", strings.Join(args, " "), result.stdout)
		}
		if result.stderr != "" {
			t.Fatalf("grill-tui %s stderr = %q, want empty", strings.Join(args, " "), result.stderr)
		}
	}
}

func TestReadOnlyCommandsLeaveWorksheetViewsAndDatabaseUnchanged(t *testing.T) {
	workingDir := t.TempDir()
	installCopyAnswerListFixture(t, workingDir)
	databasePath := worksheetDatabasePath(workingDir, "untitled")
	wantViews := publicViewSnapshot(t, databasePath)
	wantDatabase, err := os.ReadFile(databasePath)
	if err != nil {
		t.Fatalf("read Worksheet Database before inspection: %v", err)
	}

	maxInt := strconv.Itoa(int(^uint(0) >> 1))
	for _, args := range [][]string{
		{"query", "999", "999"},
		{"query", "1", "997"},
		{"query", "1001", maxInt},
	} {
		result := runCLI(t, workingDir, environmentOverrides{}, args...)
		if result.err != nil {
			t.Fatalf("grill-tui %s failed: %v\nstderr: %s", strings.Join(args, " "), result.err, result.stderr)
		}
		if result.stdout != "" || result.stderr != "" {
			t.Fatalf("grill-tui %s output = stdout %q, stderr %q; want both empty", strings.Join(args, " "), result.stdout, result.stderr)
		}
	}
	for _, args := range [][]string{{"result"}, {"result", "--answers"}} {
		result := runCLI(t, workingDir, environmentOverrides{}, args...)
		if result.err != nil {
			t.Fatalf("grill-tui %s failed: %v\nstderr: %s", strings.Join(args, " "), result.err, result.stderr)
		}
	}

	if gotViews := publicViewSnapshot(t, databasePath); gotViews != wantViews {
		t.Fatalf("public SQLite views changed after read-only commands\nbefore: %s\nafter:  %s", wantViews, gotViews)
	}
	gotDatabase, err := os.ReadFile(databasePath)
	if err != nil {
		t.Fatalf("read Worksheet Database after inspection: %v", err)
	}
	if string(gotDatabase) != string(wantDatabase) {
		t.Fatal("Worksheet Database bytes changed after read-only commands")
	}
}

func TestQueryRejectsInvalidAndReversedBoundsWithoutCreatingState(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "missing bound", args: []string{"query", "1"}, want: "Usage:\n  grill-tui query FROM TO"},
		{name: "non-numeric FROM", args: []string{"query", "one", "2"}, want: `FROM bound "one" must be a positive integer`},
		{name: "zero TO", args: []string{"query", "1", "0"}, want: `TO bound "0" must be a positive integer`},
		{name: "reversed", args: []string{"query", "9", "3"}, want: "FROM bound 9 must not exceed TO bound 3"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			workingDir := t.TempDir()
			result := runCLI(t, workingDir, environmentOverrides{}, test.args...)
			if result.err == nil {
				t.Fatalf("grill-tui %s unexpectedly succeeded", strings.Join(test.args, " "))
			}
			if result.stdout != "" {
				t.Fatalf("stdout = %q, want empty", result.stdout)
			}
			if !strings.Contains(result.stderr, test.want) {
				t.Fatalf("stderr = %q, want actionable text %q", result.stderr, test.want)
			}
			if _, err := os.Stat(filepath.Join(workingDir, ".grill-data")); !os.IsNotExist(err) {
				t.Fatalf("invalid query created Worksheet state; stat error = %v", err)
			}
		})
	}
}

func TestReadOnlyCommandsWorkWhileTUIHoldsWriterLock(t *testing.T) {
	workingDir := t.TempDir()
	owner := startTerminal(t, workingDir, "--name", "live", "40")
	owner.send(t, "y")
	owner.waitForSelection(t, 41)

	query := runCLI(t, workingDir, environmentOverrides{}, "query", "40", "49", "--name", "live")
	if query.err != nil || query.stdout != "40. yes" || query.stderr != "" {
		t.Fatalf("query during live TUI = stdout %q, stderr %q, error %v", query.stdout, query.stderr, query.err)
	}
	result := runCLI(t, workingDir, environmentOverrides{}, "result", "--name", "live")
	if result.err != nil || result.stderr != "" {
		t.Fatalf("result during live TUI = stdout %q, stderr %q, error %v", result.stdout, result.stderr, result.err)
	}
	if result.stdout != worksheetDatabasePath(workingDir, "live")+"\n" {
		t.Fatalf("result during live TUI stdout = %q", result.stdout)
	}

	owner.send(t, "n")
	owner.waitForSelection(t, 42)
	owner.send(t, "q")
	owner.waitForExit(t)
	assertPublicAnswers(t, worksheetDatabasePath(workingDir, "live"), map[int]string{40: "yes", 41: "no"})
}

func TestReadOnlyCommandsRejectEverySymlinkRedirectionWithoutMutation(t *testing.T) {
	outside := t.TempDir()
	terminal := startTerminal(t, outside, "--name", "redirected", "5")
	terminal.send(t, "r")
	terminal.waitForSelection(t, 6)
	terminal.send(t, "q")
	terminal.waitForExit(t)
	targetRoot := filepath.Join(outside, ".grill-data")
	targetWorksheet := filepath.Join(targetRoot, "redirected")
	targetDatabase := filepath.Join(targetWorksheet, "worksheet.sqlite")

	tests := []struct {
		name    string
		prepare func(t *testing.T, workingDir string)
	}{
		{
			name: "data root",
			prepare: func(t *testing.T, workingDir string) {
				t.Helper()
				if err := os.Symlink(targetRoot, filepath.Join(workingDir, ".grill-data")); err != nil {
					t.Fatalf("create data-root symlink: %v", err)
				}
			},
		},
		{
			name: "Worksheet directory",
			prepare: func(t *testing.T, workingDir string) {
				t.Helper()
				root := filepath.Join(workingDir, ".grill-data")
				if err := os.Mkdir(root, 0o700); err != nil {
					t.Fatalf("create local data root: %v", err)
				}
				if err := os.Symlink(targetWorksheet, filepath.Join(root, "redirected")); err != nil {
					t.Fatalf("create Worksheet-directory symlink: %v", err)
				}
			},
		},
		{
			name: "Worksheet Database",
			prepare: func(t *testing.T, workingDir string) {
				t.Helper()
				directory := filepath.Join(workingDir, ".grill-data", "redirected")
				if err := os.MkdirAll(directory, 0o700); err != nil {
					t.Fatalf("create local Worksheet directory: %v", err)
				}
				if err := os.Symlink(targetDatabase, filepath.Join(directory, "worksheet.sqlite")); err != nil {
					t.Fatalf("create Worksheet Database symlink: %v", err)
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			workingDir := t.TempDir()
			test.prepare(t, workingDir)
			wantTarget := filesystemSnapshot(t, targetRoot)
			wantLocal := filesystemSnapshot(t, workingDir)

			for _, args := range [][]string{
				{"query", "5", "14", "--name", "redirected"},
				{"result", "--name", "redirected"},
			} {
				result := runCLI(t, workingDir, environmentOverrides{}, args...)
				if result.err == nil {
					t.Errorf("grill-tui %s followed symlink redirection; stdout: %q", strings.Join(args, " "), result.stdout)
				}
				if result.stdout != "" {
					t.Errorf("grill-tui %s stdout = %q, want empty", strings.Join(args, " "), result.stdout)
				}
				if !strings.Contains(strings.ToLower(result.stderr), "symbolic link") {
					t.Errorf("grill-tui %s stderr does not explain symlink refusal: %q", strings.Join(args, " "), result.stderr)
				}
			}

			if gotTarget := filesystemSnapshot(t, targetRoot); gotTarget != wantTarget {
				t.Fatalf("external target changed after refused read-only commands\nbefore:\n%s\nafter:\n%s", wantTarget, gotTarget)
			}
			if gotLocal := filesystemSnapshot(t, workingDir); gotLocal != wantLocal {
				t.Fatalf("local redirection tree changed after refused read-only commands\nbefore:\n%s\nafter:\n%s", wantLocal, gotLocal)
			}
		})
	}
}

func filesystemSnapshot(t *testing.T, root string) string {
	t.Helper()
	var snapshot strings.Builder
	if err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		fmt.Fprintf(&snapshot, "%s %s %d %d", relative, info.Mode(), info.Size(), info.ModTime().UnixNano())
		if info.Mode().IsRegular() {
			contents, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			fmt.Fprintf(&snapshot, " %x", sha256.Sum256(contents))
		} else if info.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			fmt.Fprintf(&snapshot, " %s", target)
		}
		snapshot.WriteByte('\n')
		return nil
	}); err != nil {
		t.Fatalf("snapshot filesystem tree %s: %v", root, err)
	}
	return snapshot.String()
}

func TestResultAnswersForEmptyWorksheetWritesNothing(t *testing.T) {
	workingDir := t.TempDir()
	terminal := startTerminal(t, workingDir, "25")
	terminal.send(t, "q")
	terminal.waitForExit(t)

	result := runCLI(t, workingDir, environmentOverrides{}, "result", "--answers")
	if result.err != nil || result.stdout != "" || result.stderr != "" {
		t.Fatalf("empty result --answers = stdout %q, stderr %q, error %v", result.stdout, result.stderr, result.err)
	}
}

func publicViewSnapshot(t *testing.T, databasePath string) string {
	t.Helper()
	database := openWorksheetDatabaseReadOnly(t, databasePath)
	queries := []string{
		`SELECT question_number, quote(answer), is_answered FROM answer_slots ORDER BY question_number`,
		`SELECT question_number, quote(answer) FROM answered_questions ORDER BY question_number`,
		`SELECT quote(worksheet_name), first_number, last_number, COALESCE(last_answered_number, -1), answered_count, quote(updated_at) FROM worksheet_info`,
	}
	var snapshot strings.Builder
	for _, query := range queries {
		rows, err := database.Query(query)
		if err != nil {
			t.Fatalf("query public view snapshot: %v", err)
		}
		columns, err := rows.Columns()
		if err != nil {
			rows.Close()
			t.Fatalf("read public view columns: %v", err)
		}
		for rows.Next() {
			values := make([]any, len(columns))
			destinations := make([]any, len(columns))
			for index := range values {
				destinations[index] = &values[index]
			}
			if err := rows.Scan(destinations...); err != nil {
				rows.Close()
				t.Fatalf("scan public view snapshot: %v", err)
			}
			fmt.Fprintf(&snapshot, "%v\n", values)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			t.Fatalf("read public view snapshot: %v", err)
		}
		if err := rows.Close(); err != nil {
			t.Fatalf("close public view snapshot: %v", err)
		}
		snapshot.WriteString("--\n")
	}
	return snapshot.String()
}

func TestResultPrintsAbsoluteAuthoritativeDatabasePath(t *testing.T) {
	workingDir := t.TempDir()
	terminal := startTerminal(t, workingDir, "--name", "report", "7")
	terminal.send(t, "q")
	terminal.waitForExit(t)

	result := runCLI(t, workingDir, environmentOverrides{}, "result", "--name", "report")
	if result.err != nil {
		t.Fatalf("grill-tui result failed: %v\nstderr: %s", result.err, result.stderr)
	}
	want := filepath.Join(workingDir, ".grill-data", "report", "worksheet.sqlite") + "\n"
	if result.stdout != want {
		t.Fatalf("grill-tui result stdout = %q, want %q", result.stdout, want)
	}
	if result.stderr != "" {
		t.Fatalf("grill-tui result stderr = %q, want empty", result.stderr)
	}
}

func TestResultAnswersAndQueryShareMultilineAnswerListRendering(t *testing.T) {
	workingDir := t.TempDir()
	installCopyAnswerListFixture(t, workingDir)

	result := runCLI(t, workingDir, environmentOverrides{}, "result", "--answers")
	if result.err != nil {
		t.Fatalf("grill-tui result --answers failed: %v\nstderr: %s", result.err, result.stderr)
	}
	if result.stdout != exactAnswerListFixture {
		t.Fatalf("result --answers stdout = %q, want %q", result.stdout, exactAnswerListFixture)
	}
	if result.stderr != "" {
		t.Fatalf("result --answers stderr = %q, want empty", result.stderr)
	}

	query := runCLI(t, workingDir, environmentOverrides{}, "query", "998", "1000")
	if query.err != nil {
		t.Fatalf("grill-tui query failed: %v\nstderr: %s", query.err, query.stderr)
	}
	if query.stdout != result.stdout {
		t.Fatalf("query stdout = %q, want same Answer List as result --answers %q", query.stdout, result.stdout)
	}
	if query.stderr != "" {
		t.Fatalf("query stderr = %q, want empty", query.stderr)
	}
}
