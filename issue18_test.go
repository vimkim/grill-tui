package grilltui_test

import (
	"bytes"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestCommittedWorksheetMaintainsValidConsistentSQLiteBackup(t *testing.T) {
	workingDir := t.TempDir()
	terminal := startTerminal(t, workingDir, "--name", "durable", "40")
	terminal.send(t, "y")
	terminal.waitForSelection(t, 41)

	primaryPath := worksheetDatabasePath(workingDir, "durable")
	backupPath := worksheetBackupPath(workingDir, "durable")
	assertPublicAnswers(t, primaryPath, map[int]string{40: "yes"})
	assertPublicAnswers(t, backupPath, map[int]string{40: "yes"})
	assertFilePermissions(t, backupPath, 0o600)

	if err := terminal.cmd.Process.Kill(); err != nil {
		t.Fatalf("interrupt live Worksheet writer: %v", err)
	}
	select {
	case <-terminal.done:
	case <-time.After(5 * time.Second):
		t.Fatal("interrupted writer did not exit")
	}

	reopened := startTerminal(t, workingDir, "--name", "durable", "41")
	reopened.send(t, "q")
	reopened.waitForExit(t)
	assertPublicAnswers(t, primaryPath, map[int]string{40: "yes"})
	assertPublicAnswers(t, backupPath, map[int]string{40: "yes"})
}

func TestInterruptedBackupReplacementIsReconciledFromCommittedPrimary(t *testing.T) {
	workingDir := t.TempDir()
	installWorksheetThroughCLI(t, workingDir, 1, map[int]string{1: strings.Repeat("x", 8<<20)}, 2)
	primaryPath := worksheetDatabasePath(workingDir, "untitled")
	backupPath := worksheetBackupPath(workingDir, "untitled")
	terminal := startTerminal(t, workingDir, "2")
	terminal.waitForSelection(t, 2)
	waitForNoBackupReplacement(t, filepath.Dir(primaryPath))

	temporaryFound := make(chan string, 1)
	stopWatching := make(chan struct{})
	go func() {
		for {
			select {
			case <-stopWatching:
				return
			default:
			}
			entries, err := os.ReadDir(filepath.Dir(primaryPath))
			if err != nil {
				continue
			}
			for _, entry := range entries {
				if strings.HasPrefix(entry.Name(), ".worksheet.backup.sqlite.tmp-") {
					if err := terminal.cmd.Process.Signal(syscall.SIGSTOP); err == nil {
						temporaryFound <- filepath.Join(filepath.Dir(primaryPath), entry.Name())
					}
					return
				}
			}
		}
	}()
	terminal.send(t, "y")
	var interruptedPath string
	select {
	case interruptedPath = <-temporaryFound:
		close(stopWatching)
	case <-time.After(10 * time.Second):
		close(stopWatching)
		t.Fatal("did not observe in-progress SQLite backup replacement")
	}
	if _, err := os.Stat(interruptedPath); err != nil {
		t.Fatalf("backup replacement was not suspended at %s: %v", interruptedPath, err)
	}
	assertPublicAnswerLengths(t, primaryPath, map[int]int{1: 8 << 20, 2: 3})
	assertPublicAnswerLengths(t, backupPath, map[int]int{1: 8 << 20})
	if err := terminal.cmd.Process.Kill(); err != nil {
		t.Fatalf("kill writer during backup replacement: %v", err)
	}
	select {
	case <-terminal.done:
	case <-time.After(5 * time.Second):
		t.Fatal("interrupted writer did not exit")
	}

	reopened := startTerminal(t, workingDir, "3")
	reopened.waitFor(t, "Rebuilt stale Worksheet backup from validated primary")
	reopened.send(t, "q")
	reopened.waitForExit(t)
	assertPublicAnswerLengths(t, primaryPath, map[int]int{1: 8 << 20, 2: 3})
	assertPublicAnswerLengths(t, backupPath, map[int]int{1: 8 << 20, 2: 3})
}

func waitForNoBackupReplacement(t *testing.T, directory string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	quietSince := time.Time{}
	for time.Now().Before(deadline) {
		entries, err := os.ReadDir(directory)
		if err != nil {
			t.Fatalf("inspect Worksheet directory: %v", err)
		}
		active := false
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), ".worksheet.backup.sqlite.tmp-") {
				active = true
				break
			}
		}
		if active {
			quietSince = time.Time{}
		} else if quietSince.IsZero() {
			quietSince = time.Now()
		} else if time.Since(quietSince) >= 300*time.Millisecond {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	entries, _ := os.ReadDir(directory)
	t.Fatalf("Worksheet backup replacement did not become idle: %v", entryNames(entries))
}

func entryNames(entries []os.DirEntry) []string {
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}

func assertPublicAnswerLengths(t *testing.T, path string, want map[int]int) {
	t.Helper()
	database := openWorksheetDatabaseReadOnly(t, path)
	defer database.Close()
	rows, err := database.Query("SELECT question_number, length(answer) FROM answered_questions ORDER BY question_number")
	if err != nil {
		t.Fatalf("query public answer lengths: %v", err)
	}
	defer rows.Close()
	got := make(map[int]int)
	for rows.Next() {
		var number, length int
		if err := rows.Scan(&number, &length); err != nil {
			t.Fatalf("scan public answer length: %v", err)
		}
		got[number] = length
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read public answer lengths: %v", err)
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("public answer lengths = %v, want %v", got, want)
	}
}

func TestCorruptPrimaryIsPreservedAndRecoveredFromValidBackup(t *testing.T) {
	workingDir := t.TempDir()
	created := startTerminal(t, workingDir, "--name", "recoverable", "40")
	created.send(t, "y")
	created.waitForSelection(t, 41)
	created.send(t, "q")
	created.waitForExit(t)

	primaryPath := worksheetDatabasePath(workingDir, "recoverable")
	backupPath := worksheetBackupPath(workingDir, "recoverable")
	corruptEvidence := []byte("not a SQLite database")
	if err := os.WriteFile(primaryPath, corruptEvidence, 0o600); err != nil {
		t.Fatalf("install corrupt primary: %v", err)
	}

	recovered := startTerminal(t, workingDir, "--name", "recoverable")
	screen := recovered.waitFor(t, "Recovered Worksheet from validated backup")
	if !strings.Contains(screen, "Resume Worksheet") {
		t.Fatalf("recovery did not retain the existing Worksheet startup flow:\n%s", screen)
	}
	recovered.send(t, "q")
	recovered.waitForExit(t)
	assertPublicAnswers(t, primaryPath, map[int]string{40: "yes"})
	assertPublicAnswers(t, backupPath, map[int]string{40: "yes"})

	artifacts, err := filepath.Glob(filepath.Join(workingDir, ".grill-data", "recoverable", "worksheet.corrupt-*.sqlite"))
	if err != nil {
		t.Fatalf("find corrupt-primary evidence: %v", err)
	}
	if len(artifacts) != 1 {
		t.Fatalf("corrupt-primary artifacts = %v, want exactly one", artifacts)
	}
	preserved, err := os.ReadFile(artifacts[0])
	if err != nil {
		t.Fatalf("read preserved corrupt primary: %v", err)
	}
	if !bytes.Equal(preserved, corruptEvidence) {
		t.Fatalf("preserved corrupt primary = %q, want %q", preserved, corruptEvidence)
	}
	assertFilePermissions(t, artifacts[0], 0o600)
}

func TestConfirmedResetArchivesAndRecreatesTheSameNamedWorksheet(t *testing.T) {
	workingDir := t.TempDir()
	terminal := startTerminal(t, workingDir, "--name", "resettable", "70")
	terminal.send(t, "r")
	terminal.waitForSelection(t, 71)

	primaryPath := worksheetDatabasePath(workingDir, "resettable")
	backupPath := worksheetBackupPath(workingDir, "resettable")
	terminal.send(t, "\x12")
	terminal.waitFor(t, "Reset armed")
	assertPublicAnswers(t, primaryPath, map[int]string{70: "recommended"})
	assertPublicAnswers(t, backupPath, map[int]string{70: "recommended"})

	terminal.send(t, "\x12")
	screen := terminal.waitFor(t, "Worksheet reset")
	if !strings.Contains(screen, "Starting number:") {
		t.Fatalf("confirmed reset did not return to the initial prompt:\n%s", screen)
	}
	archives := worksheetArchives(t, workingDir, "resettable")
	if len(archives) != 1 {
		t.Fatalf("archives after first reset = %v, want one", archives)
	}
	assertPublicAnswers(t, archives[0], map[int]string{70: "recommended"})
	if _, err := os.Stat(primaryPath); !os.IsNotExist(err) {
		t.Fatalf("reset primary still exists; stat error = %v", err)
	}
	if _, err := os.Stat(backupPath); !os.IsNotExist(err) {
		t.Fatalf("reset backup still exists; stat error = %v", err)
	}

	terminal.send(t, "90\r")
	terminal.waitForSelection(t, 90)
	terminal.send(t, "n")
	terminal.waitForSelection(t, 91)
	assertPublicAnswers(t, primaryPath, map[int]string{90: "no"})
	assertPublicAnswers(t, backupPath, map[int]string{90: "no"})

	mark := len(terminal.output.String())
	terminal.send(t, "\x12")
	terminal.waitForAfter(t, mark, "Reset armed")
	mark = len(terminal.output.String())
	terminal.send(t, "\x12")
	terminal.waitForAfter(t, mark, "Worksheet reset")
	archives = worksheetArchives(t, workingDir, "resettable")
	if len(archives) != 2 {
		t.Fatalf("archives after second reset = %v, want two retained archives", archives)
	}
	assertPublicAnswers(t, archives[0], map[int]string{70: "recommended"})
	assertPublicAnswers(t, archives[1], map[int]string{90: "no"})
	if filepath.Base(archives[0]) >= filepath.Base(archives[1]) {
		t.Fatalf("archive names are not chronologically sortable: %v", archives)
	}
	terminal.send(t, "q")
	terminal.waitForExit(t)
}

func TestMissingPrimaryRecoversOnlyFromAValidBackup(t *testing.T) {
	workingDir := t.TempDir()
	createAnsweredWorksheet(t, workingDir, "missing", 20, "y")
	primaryPath := worksheetDatabasePath(workingDir, "missing")
	backupPath := worksheetBackupPath(workingDir, "missing")
	if err := os.Remove(primaryPath); err != nil {
		t.Fatalf("remove primary to simulate interrupted replacement: %v", err)
	}
	orphanedWAL := []byte("interrupted WAL evidence")
	if err := os.WriteFile(primaryPath+"-wal", orphanedWAL, 0o600); err != nil {
		t.Fatalf("install interrupted WAL evidence: %v", err)
	}

	recovered := startTerminal(t, workingDir, "--name", "missing")
	recovered.waitFor(t, "Recovered missing Worksheet Database from validated backup")
	recovered.send(t, "q")
	recovered.waitForExit(t)
	assertPublicAnswers(t, primaryPath, map[int]string{20: "yes"})
	assertPublicAnswers(t, backupPath, map[int]string{20: "yes"})
	interruptedArtifacts, err := filepath.Glob(filepath.Join(filepath.Dir(primaryPath), "worksheet.interrupted-*.sqlite-wal"))
	if err != nil || len(interruptedArtifacts) != 1 {
		t.Fatalf("interrupted-write artifacts = %v, error = %v; want one", interruptedArtifacts, err)
	}
	assertFileContents(t, interruptedArtifacts[0], orphanedWAL)
}

func TestUnusablePrimaryAndBackupFailWithoutReplacingEvidence(t *testing.T) {
	tests := []struct {
		name           string
		removePrimary  bool
		primaryContent []byte
	}{
		{name: "both corrupt", primaryContent: []byte("corrupt primary")},
		{name: "missing primary", removePrimary: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			workingDir := t.TempDir()
			createAnsweredWorksheet(t, workingDir, "broken", 20, "y")
			primaryPath := worksheetDatabasePath(workingDir, "broken")
			backupPath := worksheetBackupPath(workingDir, "broken")
			backupEvidence := []byte("corrupt backup")
			if test.removePrimary {
				if err := os.Remove(primaryPath); err != nil {
					t.Fatalf("remove primary: %v", err)
				}
			} else if err := os.WriteFile(primaryPath, test.primaryContent, 0o600); err != nil {
				t.Fatalf("install corrupt primary: %v", err)
			}
			if err := os.WriteFile(backupPath, backupEvidence, 0o600); err != nil {
				t.Fatalf("install corrupt backup: %v", err)
			}

			result := runCLI(t, workingDir, environmentOverrides{}, "--name", "broken")
			if result.err == nil || result.stdout != "" {
				t.Fatalf("unsafe recovery result = stdout %q, stderr %q, error %v", result.stdout, result.stderr, result.err)
			}
			for _, want := range []string{"safe Worksheet recovery is impossible", "preserved", "repair or restore"} {
				if !strings.Contains(result.stderr, want) {
					t.Fatalf("unsafe recovery stderr = %q, want %q", result.stderr, want)
				}
			}
			if test.removePrimary {
				if _, err := os.Stat(primaryPath); !os.IsNotExist(err) {
					t.Fatalf("unsafe recovery created a primary; stat error = %v", err)
				}
			} else {
				assertFileContents(t, primaryPath, test.primaryContent)
			}
			assertFileContents(t, backupPath, backupEvidence)
			if artifacts, err := filepath.Glob(filepath.Join(filepath.Dir(primaryPath), "worksheet.corrupt-*.sqlite")); err != nil || len(artifacts) != 0 {
				t.Fatalf("unsafe recovery moved evidence: artifacts=%v error=%v", artifacts, err)
			}
		})
	}
}

func TestUnsupportedNewerPrimaryNeverRollsBackToBackup(t *testing.T) {
	workingDir := t.TempDir()
	createAnsweredWorksheet(t, workingDir, "future", 30, "n")
	primaryPath := worksheetDatabasePath(workingDir, "future")
	backupPath := worksheetBackupPath(workingDir, "future")
	database, err := sql.Open("sqlite", primaryPath)
	if err != nil {
		t.Fatalf("open future-schema fixture: %v", err)
	}
	if _, err := database.Exec("PRAGMA user_version = 2"); err != nil {
		t.Fatalf("set future schema version: %v", err)
	}
	if err := database.Close(); err != nil {
		t.Fatalf("close future-schema fixture: %v", err)
	}

	result := runCLI(t, workingDir, environmentOverrides{}, "--name", "future")
	if result.err == nil || result.stdout != "" {
		t.Fatalf("future-schema result = stdout %q, stderr %q, error %v", result.stdout, result.stderr, result.err)
	}
	for _, want := range []string{"unsupported schema version 2", "no migration was attempted"} {
		if !strings.Contains(result.stderr, want) {
			t.Fatalf("future-schema stderr = %q, want %q", result.stderr, want)
		}
	}
	database = openWorksheetDatabaseReadOnly(t, primaryPath)
	var version int
	if err := database.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatalf("read future schema version: %v", err)
	}
	if version != 2 {
		t.Fatalf("primary schema version = %d, want 2", version)
	}
	database.Close()
	assertPublicAnswers(t, backupPath, map[int]string{30: "no"})
	if artifacts, err := filepath.Glob(filepath.Join(filepath.Dir(primaryPath), "worksheet.corrupt-*.sqlite")); err != nil || len(artifacts) != 0 {
		t.Fatalf("future schema was treated as corruption: artifacts=%v error=%v", artifacts, err)
	}
}

func TestValidPrimaryRebuildsAnInvalidBackupConsistently(t *testing.T) {
	workingDir := t.TempDir()
	createAnsweredWorksheet(t, workingDir, "repair-backup", 30, "n")
	primaryPath := worksheetDatabasePath(workingDir, "repair-backup")
	backupPath := worksheetBackupPath(workingDir, "repair-backup")
	if err := os.WriteFile(backupPath, []byte("invalid backup"), 0o600); err != nil {
		t.Fatalf("install invalid backup: %v", err)
	}

	reopened := startTerminal(t, workingDir, "--name", "repair-backup")
	reopened.waitFor(t, "Rebuilt invalid Worksheet backup from validated primary")
	reopened.send(t, "q")
	reopened.waitForExit(t)
	assertPublicAnswers(t, primaryPath, map[int]string{30: "no"})
	assertPublicAnswers(t, backupPath, map[int]string{30: "no"})
}

func TestRecoveryWaitsForSelectedWorksheetWriterOwnership(t *testing.T) {
	workingDir := t.TempDir()
	createAnsweredWorksheet(t, workingDir, "owned", 60, "y")
	owner := startTerminal(t, workingDir, "--name", "owned")
	owner.waitFor(t, "Resume Worksheet")
	primaryPath := worksheetDatabasePath(workingDir, "owned")
	corruptEvidence := []byte("corrupt while ownership is held")
	if err := os.WriteFile(primaryPath, corruptEvidence, 0o600); err != nil {
		t.Fatalf("install corrupt primary: %v", err)
	}

	competitor := runCLI(t, workingDir, environmentOverrides{}, "--name", "owned")
	if competitor.err == nil || !strings.Contains(competitor.stderr, "live TUI writer") {
		t.Fatalf("competing recovery = stdout %q, stderr %q, error %v", competitor.stdout, competitor.stderr, competitor.err)
	}
	assertFileContents(t, primaryPath, corruptEvidence)
	if artifacts, err := filepath.Glob(filepath.Join(filepath.Dir(primaryPath), "worksheet.corrupt-*.sqlite")); err != nil || len(artifacts) != 0 {
		t.Fatalf("competitor recovered without ownership: artifacts=%v error=%v", artifacts, err)
	}

	owner.send(t, "q")
	owner.waitForExit(t)
	recovered := startTerminal(t, workingDir, "--name", "owned")
	recovered.waitFor(t, "Recovered Worksheet from validated backup")
	recovered.send(t, "q")
	recovered.waitForExit(t)
	assertPublicAnswers(t, primaryPath, map[int]string{60: "yes"})
}

func TestArchivesAreNeitherDiscoveredNorRestoredAutomatically(t *testing.T) {
	workingDir := t.TempDir()
	terminal := startTerminal(t, workingDir, "--name", "archived", "50")
	terminal.send(t, "y")
	terminal.waitForSelection(t, 51)
	terminal.send(t, "\x12")
	terminal.waitFor(t, "Reset armed")
	mark := len(terminal.output.String())
	terminal.send(t, "\x12")
	terminal.waitForAfter(t, mark, "Worksheet reset")
	terminal.send(t, "q")
	terminal.waitForExit(t)
	if archives := worksheetArchives(t, workingDir, "archived"); len(archives) != 1 {
		t.Fatalf("archives = %v, want one", archives)
	}

	listed := runCLI(t, workingDir, environmentOverrides{}, "list")
	if listed.err != nil || listed.stderr != "" {
		t.Fatalf("list with archive-only Worksheet = stdout %q, stderr %q, error %v", listed.stdout, listed.stderr, listed.err)
	}
	if strings.Contains(listed.stdout, "archived") {
		t.Fatalf("archive-only Worksheet appeared in discovery:\n%s", listed.stdout)
	}

	reopened := startTerminal(t, workingDir, "--name", "archived")
	screen := reopened.waitFor(t, "Create a Worksheet")
	if strings.Contains(screen, "Resume Worksheet") || strings.Contains(screen, "50. yes") {
		t.Fatalf("archive was restored automatically:\n%s", screen)
	}
	reopened.send(t, "q")
	reopened.waitForExit(t)
}

func createAnsweredWorksheet(t *testing.T, workingDir, name string, start int, answerKey string) {
	t.Helper()
	terminal := startTerminal(t, workingDir, "--name", name, fmt.Sprint(start))
	terminal.send(t, answerKey)
	terminal.waitForSelection(t, start+1)
	terminal.send(t, "q")
	terminal.waitForExit(t)
}

func worksheetArchives(t *testing.T, workingDir, worksheetName string) []string {
	t.Helper()
	archives, err := filepath.Glob(filepath.Join(workingDir, ".grill-data", worksheetName, "archive", "*.sqlite"))
	if err != nil {
		t.Fatalf("find Worksheet archives: %v", err)
	}
	return archives
}

func worksheetBackupPath(workingDir, worksheetName string) string {
	return filepath.Join(workingDir, ".grill-data", worksheetName, "worksheet.backup.sqlite")
}

func assertPathExists(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
}
