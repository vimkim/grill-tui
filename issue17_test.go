package grilltui_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSameNamedWorksheetRejectsASecondLiveTUIWriterWithoutMutation(t *testing.T) {
	workingDir := t.TempDir()
	owner := startTerminal(t, workingDir, "--name", "shared", "20")
	owner.send(t, "y")
	owner.waitForSelection(t, 21)

	databasePath := worksheetDatabasePath(workingDir, "shared")
	wantViews := publicViewSnapshot(t, databasePath)
	wantDatabase, err := os.ReadFile(databasePath)
	if err != nil {
		t.Fatalf("read shared Worksheet before competing writer: %v", err)
	}

	competitor := runCLI(t, workingDir, environmentOverrides{}, "--name", "shared", "99")
	if competitor.err == nil {
		t.Fatalf("second writer unexpectedly succeeded: stdout %q", competitor.stdout)
	}
	if competitor.stdout != "" {
		t.Fatalf("second writer stdout = %q, want empty", competitor.stdout)
	}
	for _, want := range []string{"shared", "live TUI writer"} {
		if !strings.Contains(competitor.stderr, want) {
			t.Fatalf("writer-conflict stderr = %q, want text containing %q", competitor.stderr, want)
		}
	}
	if gotViews := publicViewSnapshot(t, databasePath); gotViews != wantViews {
		t.Fatalf("rejected writer changed public Worksheet data\nbefore:\n%s\nafter:\n%s", wantViews, gotViews)
	}
	gotDatabase, err := os.ReadFile(databasePath)
	if err != nil {
		t.Fatalf("read shared Worksheet after competing writer: %v", err)
	}
	if string(gotDatabase) != string(wantDatabase) {
		t.Fatal("rejected writer changed the authoritative Worksheet Database")
	}
	if _, err := os.Stat(worksheetLockPath(workingDir, "shared")); err != nil {
		t.Fatalf("stat per-Worksheet lock artifact: %v", err)
	}

	owner.send(t, "n")
	owner.waitForSelection(t, 22)
	owner.send(t, "q")
	owner.waitForExit(t)
	assertPublicAnswers(t, databasePath, map[int]string{20: "yes", 21: "no"})

	reopened := startTerminal(t, workingDir, "--name", "shared", "22")
	reopened.send(t, "q")
	reopened.waitForExit(t)
}

func TestDifferentlyNamedWritersRemainActiveAndReadableIndependently(t *testing.T) {
	workingDir := t.TempDir()
	alpha := startTerminal(t, workingDir, "--name", "alpha", "10")
	beta := startTerminal(t, workingDir, "--name", "beta", "50")

	alpha.send(t, "r")
	alpha.waitForSelection(t, 11)
	beta.send(t, "n")
	beta.waitForSelection(t, 51)

	assertPublicAnswers(t, worksheetDatabasePath(workingDir, "alpha"), map[int]string{10: "recommended"})
	assertPublicAnswers(t, worksheetDatabasePath(workingDir, "beta"), map[int]string{50: "no"})
	for _, name := range []string{"alpha", "beta"} {
		lockPath := worksheetLockPath(workingDir, name)
		if _, err := os.Stat(lockPath); err != nil {
			t.Fatalf("stat %s per-Worksheet lock artifact: %v", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(workingDir, ".grill-data", "worksheet.lock")); !os.IsNotExist(err) {
		t.Fatalf("repository-wide lock artifact exists; stat error = %v", err)
	}

	query := runCLI(t, workingDir, environmentOverrides{}, "query", "1", "100", "--name", "alpha")
	if query.err != nil || query.stdout != "10. recommended" || query.stderr != "" {
		t.Fatalf("read-only query during live writers = stdout %q, stderr %q, error %v", query.stdout, query.stderr, query.err)
	}
	result := runCLI(t, workingDir, environmentOverrides{}, "result", "--name", "beta", "--answers")
	if result.err != nil || result.stdout != "50. no" || result.stderr != "" {
		t.Fatalf("read-only result during live writers = stdout %q, stderr %q, error %v", result.stdout, result.stderr, result.err)
	}

	alpha.send(t, "y")
	alpha.waitForSelection(t, 12)
	beta.send(t, "r")
	beta.waitForSelection(t, 52)
	alpha.send(t, "q")
	alpha.waitForExit(t)
	beta.send(t, "q")
	beta.waitForExit(t)

	assertPublicAnswers(t, worksheetDatabasePath(workingDir, "alpha"), map[int]string{10: "recommended", 11: "yes"})
	assertPublicAnswers(t, worksheetDatabasePath(workingDir, "beta"), map[int]string{50: "no", 51: "recommended"})
}

func TestTerminatedWriterDoesNotLeaveNamedWorksheetPermanentlyLocked(t *testing.T) {
	workingDir := t.TempDir()
	interrupted := startTerminal(t, workingDir, "--name", "interrupted", "70")
	interrupted.send(t, "y")
	interrupted.waitForSelection(t, 71)

	if err := interrupted.cmd.Process.Kill(); err != nil {
		t.Fatalf("terminate live TUI writer: %v", err)
	}
	select {
	case err := <-interrupted.done:
		if err == nil {
			t.Fatal("terminated TUI writer exited successfully, want signal failure")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for terminated TUI writer")
	}
	_ = interrupted.pty.Close()

	reopened := startTerminal(t, workingDir, "--name", "interrupted", "71")
	screen := reopened.waitForSelection(t, 71)
	if !strings.Contains(screen, "  70 │ yes") {
		t.Fatalf("reopened Worksheet lost the committed answer after process termination:\n%s", screen)
	}
	reopened.send(t, "n")
	reopened.waitForSelection(t, 72)
	reopened.send(t, "q")
	reopened.waitForExit(t)

	assertPublicAnswers(t, worksheetDatabasePath(workingDir, "interrupted"), map[int]string{70: "yes", 71: "no"})
}

func TestLockFilesystemFailureIsNotReportedAsALiveWriterConflict(t *testing.T) {
	workingDir := t.TempDir()
	lockPath := worksheetLockPath(workingDir, "broken-lock")
	if err := os.MkdirAll(lockPath, 0o700); err != nil {
		t.Fatalf("create invalid lock artifact: %v", err)
	}

	result := runCLI(t, workingDir, environmentOverrides{}, "--name", "broken-lock", "5")
	if result.err == nil {
		t.Fatalf("TUI unexpectedly opened invalid lock artifact: stdout %q", result.stdout)
	}
	if result.stdout != "" {
		t.Fatalf("lock filesystem failure stdout = %q, want empty", result.stdout)
	}
	for _, want := range []string{"open Worksheet lock", "not a regular file"} {
		if !strings.Contains(result.stderr, want) {
			t.Fatalf("lock filesystem failure stderr = %q, want text containing %q", result.stderr, want)
		}
	}
	if strings.Contains(result.stderr, "live TUI writer") {
		t.Fatalf("filesystem failure was misreported as a writer conflict: %q", result.stderr)
	}
	if _, err := os.Stat(worksheetDatabasePath(workingDir, "broken-lock")); !os.IsNotExist(err) {
		t.Fatalf("lock failure created a Worksheet Database; stat error = %v", err)
	}
}

func worksheetLockPath(workingDir, worksheetName string) string {
	return filepath.Join(workingDir, ".grill-data", worksheetName, "worksheet.lock")
}
