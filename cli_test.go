package grilltui_test

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
	"unicode/utf16"

	"github.com/creack/pty"
	"github.com/mattn/go-runewidth"
)

var grillTUIBinary string

const exactAnswerListFixture = "998. alpha\n1000. first 界\n      second line"

var completeHelpBindingDescriptions = []string{
	"Move: ↑/↓, j/k, Ctrl-N/Ctrl-P", "Skip: Space",
	"Presets: r/R recommended; y/Y yes; n/N no", "Choices: 1–5; a–e/A–E",
	"Explain: x", "Custom: i inline; o external editor",
	"Inline edit: Enter commit; Esc cancel",
	"Correct: Esc clear; u undo", "Mouse: left click select; wheel scroll",
	"Copy: s/Ctrl-S", "Reset: Ctrl-R twice within two seconds",
	"Help: ?; Quit: q, Ctrl-Q, Ctrl-C",
}

func TestMain(m *testing.M) {
	tempDir, err := os.MkdirTemp("", "grill-tui-tests-")
	if err != nil {
		panic(err)
	}

	grillTUIBinary = filepath.Join(tempDir, "grill-tui")
	build := exec.Command("go", "build", "-o", grillTUIBinary, "./cmd/grill-tui")
	if output, err := build.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "build grill-tui: %v\n%s", err, output)
		os.Exit(1)
	}

	exitCode := m.Run()
	if err := os.RemoveAll(tempDir); err != nil {
		fmt.Fprintf(os.Stderr, "remove test binaries: %v\n", err)
		if exitCode == 0 {
			exitCode = 1
		}
	}
	os.Exit(exitCode)
}

func TestPositiveArgumentCreatesAndRendersWorksheet(t *testing.T) {
	workingDir := t.TempDir()
	terminal := startTerminal(t, workingDir, "41")

	terminal.send(t, "q")
	terminal.waitForExit(t)
	screen := cleanTerminalOutput(terminal.output.String())
	answerSlots := answerSlotLinePattern.FindAllStringSubmatch(screen, -1)
	if len(answerSlots) != 10 {
		t.Fatalf("screen contains %d rendered Answer Slots, want 10:\n%s", len(answerSlots), screen)
	}
	for index, answerSlot := range answerSlots {
		if want := fmt.Sprintf("%d", 41+index); answerSlot[1] != want {
			t.Fatalf("rendered Answer Slot %d has number %s, want %s:\n%s", index, answerSlot[1], want, screen)
		}
	}
	for _, text := range []string{"Grill TUI", "Worksheet", "No.", "Answer", "> 41", "q", "quit"} {
		if !strings.Contains(screen, text) {
			t.Fatalf("screen does not contain %q:\n%s", text, screen)
		}
	}

	stateInfo, err := os.Stat(filepath.Join(workingDir, ".grill-tui", "worksheet.json"))
	if err != nil {
		t.Fatalf("stat created Worksheet: %v", err)
	}
	if !stateInfo.Mode().IsRegular() {
		t.Fatalf("Worksheet state is not a regular file: %s", stateInfo.Mode())
	}
}

func TestWorksheetStateUsesSchemaOneAndOwnerOnlyArtifacts(t *testing.T) {
	workingDir := t.TempDir()
	terminal := startTerminal(t, workingDir, "41")
	terminal.send(t, "q")
	terminal.waitForExit(t)

	stateDirectory := filepath.Join(workingDir, ".grill-tui")
	directoryInfo, err := os.Stat(stateDirectory)
	if err != nil {
		t.Fatalf("stat state directory: %v", err)
	}
	if permissions := directoryInfo.Mode().Perm(); permissions != 0o700 {
		t.Fatalf("state directory permissions = %o, want 700", permissions)
	}

	statePath := filepath.Join(stateDirectory, "worksheet.json")
	state, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("read Worksheet state: %v", err)
	}
	var document struct {
		SchemaVersion int `json:"schema_version"`
	}
	if err := json.Unmarshal(state, &document); err != nil {
		t.Fatalf("decode Worksheet state: %v", err)
	}
	if document.SchemaVersion != 1 {
		t.Fatalf("schema version = %d, want 1; state: %s", document.SchemaVersion, state)
	}
	assertFilePermissions(t, statePath, 0o600)

	ignorePath := filepath.Join(stateDirectory, ".gitignore")
	ignore, err := os.ReadFile(ignorePath)
	if err != nil {
		t.Fatalf("read state-directory .gitignore: %v", err)
	}
	if string(ignore) != "*\n" {
		t.Fatalf("state-directory .gitignore = %q, want %q", ignore, "*\\n")
	}
	assertFilePermissions(t, ignorePath, 0o600)
}

func assertFilePermissions(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if permissions := info.Mode().Perm(); permissions != want {
		t.Fatalf("%s permissions = %o, want %o", path, permissions, want)
	}
}

func TestCommittedMutationsAtomicallyReplacePrimaryAndMaintainBackup(t *testing.T) {
	workingDir := t.TempDir()
	terminal := startTerminal(t, workingDir, "41")
	stateDirectory := filepath.Join(workingDir, ".grill-tui")
	primaryPath := filepath.Join(stateDirectory, "worksheet.json")
	backupPath := filepath.Join(stateDirectory, "worksheet.backup.json")

	initial := readValidWorksheetDocument(t, primaryPath)
	initialBytes, err := os.ReadFile(primaryPath)
	if err != nil {
		t.Fatalf("read initial primary: %v", err)
	}
	initialBackupBytes, err := os.ReadFile(backupPath)
	if err != nil {
		t.Fatalf("read initial last-known-good backup: %v", err)
	}
	if !bytes.Equal(initialBackupBytes, initialBytes) {
		t.Fatalf("initial backup does not match primary\nprimary: %s\nbackup: %s", initialBytes, initialBackupBytes)
	}

	terminal.send(t, "r")
	terminal.waitForSelection(t, 42)
	afterFirstMutationBytes, err := os.ReadFile(primaryPath)
	if err != nil {
		t.Fatalf("read primary after first mutation: %v", err)
	}
	afterFirstMutation := decodeValidWorksheetDocument(t, primaryPath, afterFirstMutationBytes)
	if got := afterFirstMutation.Slots[0].Answer; got != "recommended" {
		t.Fatalf("first committed answer = %q, want recommended", got)
	}
	backupAfterFirst := readValidWorksheetDocument(t, backupPath)
	if backupAfterFirst.Selected != initial.Selected || backupAfterFirst.Slots[0].Answer != initial.Slots[0].Answer {
		t.Fatalf("backup does not contain the state preceding the first mutation: %#v", backupAfterFirst)
	}

	terminal.send(t, "y")
	terminal.waitForSelection(t, 43)
	primaryAfterSecond := readValidWorksheetDocument(t, primaryPath)
	if got := primaryAfterSecond.Slots[1].Answer; got != "yes" {
		t.Fatalf("second committed answer = %q, want yes", got)
	}
	backupAfterSecondBytes, err := os.ReadFile(backupPath)
	if err != nil {
		t.Fatalf("read backup after second mutation: %v", err)
	}
	if !bytes.Equal(backupAfterSecondBytes, afterFirstMutationBytes) {
		t.Fatalf("backup is not the exact previous primary\nwant: %s\ngot: %s", afterFirstMutationBytes, backupAfterSecondBytes)
	}
	assertFilePermissions(t, primaryPath, 0o600)
	assertFilePermissions(t, backupPath, 0o600)

	entries, err := os.ReadDir(stateDirectory)
	if err != nil {
		t.Fatalf("read state directory: %v", err)
	}
	for _, entry := range entries {
		if strings.Contains(entry.Name(), ".tmp-") {
			t.Fatalf("atomic replacement leaked temporary artifact %q", entry.Name())
		}
	}

	terminal.send(t, "q")
	terminal.waitForExit(t)
}

func TestCorruptPrimaryRecoversFromBackupAndPreservesEvidence(t *testing.T) {
	workingDir := t.TempDir()
	terminal := startTerminal(t, workingDir, "41")
	terminal.send(t, "r")
	terminal.waitForSelection(t, 42)
	terminal.send(t, "y")
	terminal.waitForSelection(t, 43)
	terminal.send(t, "q")
	terminal.waitForExit(t)

	stateDirectory := filepath.Join(workingDir, ".grill-tui")
	primaryPath := filepath.Join(stateDirectory, "worksheet.json")
	backupPath := filepath.Join(stateDirectory, "worksheet.backup.json")
	backupBeforeRecovery, err := os.ReadFile(backupPath)
	if err != nil {
		t.Fatalf("read recovery backup: %v", err)
	}
	corruptPrimary := []byte("{interrupted write")
	if err := os.WriteFile(primaryPath, corruptPrimary, 0o600); err != nil {
		t.Fatalf("install corrupt primary: %v", err)
	}

	recovered := startTerminal(t, workingDir)
	screen := recovered.waitFor(t, "Recovered Worksheet from backup")
	recovered.waitForSelection(t, 42)
	if !strings.Contains(screen, "  41 │ recommended") {
		t.Fatalf("recovered Worksheet did not load the last-known-good backup:\n%s", screen)
	}
	if answer := latestRenderedAnswer(t, screen, 42); answer != "" {
		t.Fatalf("recovered backup unexpectedly includes later answer %q:\n%s", answer, screen)
	}
	recovered.send(t, "q")
	recovered.waitForExit(t)

	primaryAfterRecovery, err := os.ReadFile(primaryPath)
	if err != nil {
		t.Fatalf("read recovered primary: %v", err)
	}
	if !bytes.Equal(primaryAfterRecovery, backupBeforeRecovery) {
		t.Fatalf("recovered primary does not exactly match backup\nbackup: %s\nprimary: %s", backupBeforeRecovery, primaryAfterRecovery)
	}
	backupAfterRecovery, err := os.ReadFile(backupPath)
	if err != nil {
		t.Fatalf("read backup after recovery: %v", err)
	}
	if !bytes.Equal(backupAfterRecovery, backupBeforeRecovery) {
		t.Fatalf("recovery mutated the valid backup\nbefore: %s\nafter: %s", backupBeforeRecovery, backupAfterRecovery)
	}

	artifacts, err := filepath.Glob(filepath.Join(stateDirectory, "worksheet.corrupt-*.json"))
	if err != nil {
		t.Fatalf("find preserved corrupt artifacts: %v", err)
	}
	if len(artifacts) != 1 {
		t.Fatalf("preserved corrupt artifact count = %d, want 1: %v", len(artifacts), artifacts)
	}
	preserved, err := os.ReadFile(artifacts[0])
	if err != nil {
		t.Fatalf("read preserved corrupt artifact: %v", err)
	}
	if !bytes.Equal(preserved, corruptPrimary) {
		t.Fatalf("preserved corrupt artifact = %q, want %q", preserved, corruptPrimary)
	}
	assertFilePermissions(t, artifacts[0], 0o600)
}

func TestInterruptedReplacementRecoversMissingPrimaryAndPreservesTemporaryEvidence(t *testing.T) {
	workingDir := createWorksheetWithBackup(t)
	stateDirectory := filepath.Join(workingDir, ".grill-tui")
	primaryPath := filepath.Join(stateDirectory, "worksheet.json")
	backupPath := filepath.Join(stateDirectory, "worksheet.backup.json")
	backup, err := os.ReadFile(backupPath)
	if err != nil {
		t.Fatalf("read last-known-good backup: %v", err)
	}
	if err := os.Remove(primaryPath); err != nil {
		t.Fatalf("simulate missing primary after interrupted replacement: %v", err)
	}
	temporaryPath := filepath.Join(stateDirectory, ".worksheet.json.tmp-interrupted")
	temporaryEvidence := []byte("partial replacement bytes")
	if err := os.WriteFile(temporaryPath, temporaryEvidence, 0o600); err != nil {
		t.Fatalf("install interrupted-write artifact: %v", err)
	}

	recovered := startTerminal(t, workingDir)
	screen := recovered.waitFor(t, "Recovered missing primary state from backup")
	if !strings.Contains(screen, "> 41 │ ") {
		t.Fatalf("missing primary was not restored from the last-known-good backup:\n%s", screen)
	}
	recovered.send(t, "q")
	recovered.waitForExit(t)

	assertFileContents(t, primaryPath, backup)
	assertFileContents(t, backupPath, backup)
	assertFileContents(t, temporaryPath, temporaryEvidence)
	assertFilePermissions(t, primaryPath, 0o600)
}

func TestKillingCLIAtAtomicWriteBoundariesLeavesValidRecoverableState(t *testing.T) {
	tests := []struct {
		name            string
		temporaryPrefix string
		input           string
	}{
		{
			name:            "backup replacement during selection persistence",
			temporaryPrefix: ".worksheet.backup.json.tmp-",
			input:           "j",
		},
		{
			name:            "primary replacement during committed answer mutation",
			temporaryPrefix: ".worksheet.json.tmp-",
			input:           "y",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			workingDir := t.TempDir()
			created := startTerminal(t, workingDir, "70")
			created.send(t, "q")
			created.waitForExit(t)

			stateDirectory := filepath.Join(workingDir, ".grill-tui")
			primaryPath := filepath.Join(stateDirectory, "worksheet.json")
			document := readValidWorksheetDocument(t, primaryPath)
			document.Slots[len(document.Slots)-1].Answer = strings.Repeat("x", 8<<20)
			largeState, err := json.MarshalIndent(document, "", "  ")
			if err != nil {
				t.Fatalf("encode large interrupted-write fixture: %v", err)
			}
			largeState = append(largeState, '\n')
			if err := os.WriteFile(primaryPath, largeState, 0o600); err != nil {
				t.Fatalf("install large interrupted-write fixture: %v", err)
			}

			terminal := startTerminal(t, workingDir)
			temporaryFound := make(chan string, 1)
			stopWatching := make(chan struct{})
			go func() {
				for {
					select {
					case <-stopWatching:
						return
					default:
					}
					entries, readErr := os.ReadDir(stateDirectory)
					if readErr != nil {
						continue
					}
					for _, entry := range entries {
						if strings.HasPrefix(entry.Name(), test.temporaryPrefix) {
							_ = terminal.cmd.Process.Signal(syscall.SIGSTOP)
							temporaryFound <- filepath.Join(stateDirectory, entry.Name())
							return
						}
					}
				}
			}()
			terminal.send(t, test.input)
			var interruptedPath string
			select {
			case interruptedPath = <-temporaryFound:
				close(stopWatching)
			case <-time.After(10 * time.Second):
				close(stopWatching)
				t.Fatalf("did not observe atomic-write artifact with prefix %q", test.temporaryPrefix)
			}
			if _, err := os.Stat(interruptedPath); err != nil {
				t.Fatalf("writer was not suspended at the observed boundary %s: %v", interruptedPath, err)
			}
			if err := terminal.cmd.Process.Kill(); err != nil {
				t.Fatalf("kill CLI at write boundary: %v", err)
			}
			select {
			case err := <-terminal.done:
				if err == nil {
					t.Fatal("CLI killed at a write boundary unexpectedly exited successfully")
				}
			case <-time.After(5 * time.Second):
				t.Fatal("CLI did not exit after being killed at a write boundary")
			}

			readValidWorksheetDocument(t, primaryPath)
			readValidWorksheetDocument(t, filepath.Join(stateDirectory, "worksheet.backup.json"))
			if _, err := os.Stat(interruptedPath); err != nil {
				t.Fatalf("interrupted-write evidence was not preserved: %v", err)
			}

			restarted := startTerminal(t, workingDir)
			restarted.waitFor(t, "Worksheet ready")
			time.Sleep(50 * time.Millisecond)
			restarted.send(t, "q")
			restarted.waitForExit(t)
		})
	}
}

func TestInvalidPrimaryAndBackupRefuseRecoveryWithoutChangingEvidence(t *testing.T) {
	workingDir := createWorksheetWithBackup(t)
	stateDirectory := filepath.Join(workingDir, ".grill-tui")
	primaryPath := filepath.Join(stateDirectory, "worksheet.json")
	backupPath := filepath.Join(stateDirectory, "worksheet.backup.json")
	primaryEvidence := []byte("corrupt primary evidence")
	backupEvidence := []byte("corrupt backup evidence")
	if err := os.WriteFile(primaryPath, primaryEvidence, 0o600); err != nil {
		t.Fatalf("install corrupt primary: %v", err)
	}
	if err := os.WriteFile(backupPath, backupEvidence, 0o600); err != nil {
		t.Fatalf("install corrupt backup: %v", err)
	}
	if err := os.Chmod(primaryPath, 0o777); err != nil {
		t.Fatalf("make corrupt primary overly permissive: %v", err)
	}
	if err := os.Chmod(backupPath, 0o777); err != nil {
		t.Fatalf("make corrupt backup overly permissive: %v", err)
	}

	output := runCLIExpectFailure(t, workingDir)
	for _, want := range []string{"safe recovery is impossible", "repair or restore", "both", "preserved"} {
		if !strings.Contains(output, want) {
			t.Fatalf("recovery refusal does not contain %q:\n%s", want, output)
		}
	}
	assertFileContents(t, primaryPath, primaryEvidence)
	assertFileContents(t, backupPath, backupEvidence)
	assertFilePermissions(t, primaryPath, 0o600)
	assertFilePermissions(t, backupPath, 0o600)
	artifacts, err := filepath.Glob(filepath.Join(stateDirectory, "worksheet.corrupt-*.json"))
	if err != nil {
		t.Fatalf("find corrupt artifacts: %v", err)
	}
	if len(artifacts) != 0 {
		t.Fatalf("unsafe recovery created unexpected diagnostic copies: %v", artifacts)
	}
}

func TestUnsupportedSchemaIsRefusedWithoutFallbackOrMutation(t *testing.T) {
	workingDir := createWorksheetWithBackup(t)
	stateDirectory := filepath.Join(workingDir, ".grill-tui")
	primaryPath := filepath.Join(stateDirectory, "worksheet.json")
	backupPath := filepath.Join(stateDirectory, "worksheet.backup.json")
	primary, err := os.ReadFile(primaryPath)
	if err != nil {
		t.Fatalf("read primary: %v", err)
	}
	unsupported := bytes.Replace(primary, []byte(`"schema_version": 1`), []byte(`"schema_version": 99`), 1)
	if bytes.Equal(unsupported, primary) {
		t.Fatal("test setup did not replace the schema version")
	}
	if err := os.WriteFile(primaryPath, unsupported, 0o600); err != nil {
		t.Fatalf("install unsupported primary: %v", err)
	}
	backup, err := os.ReadFile(backupPath)
	if err != nil {
		t.Fatalf("read backup: %v", err)
	}
	if err := os.Chmod(primaryPath, 0o777); err != nil {
		t.Fatalf("make unsupported primary overly permissive: %v", err)
	}
	if err := os.Chmod(backupPath, 0o777); err != nil {
		t.Fatalf("make backup overly permissive: %v", err)
	}

	output := runCLIExpectFailure(t, workingDir)
	for _, want := range []string{"unsupported schema version 99", "supports version 1", "no migration was attempted"} {
		if !strings.Contains(output, want) {
			t.Fatalf("unsupported-schema refusal does not contain %q:\n%s", want, output)
		}
	}
	assertFileContents(t, primaryPath, unsupported)
	assertFileContents(t, backupPath, backup)
	assertFilePermissions(t, primaryPath, 0o600)
	assertFilePermissions(t, backupPath, 0o600)
	artifacts, err := filepath.Glob(filepath.Join(stateDirectory, "worksheet.corrupt-*.json"))
	if err != nil {
		t.Fatalf("find corrupt artifacts: %v", err)
	}
	if len(artifacts) != 0 {
		t.Fatalf("unsupported schema was incorrectly treated as corruption: %v", artifacts)
	}
}

func TestUnsupportedBackupSchemaIsRefusedWithoutMutation(t *testing.T) {
	workingDir := createWorksheetWithBackup(t)
	stateDirectory := filepath.Join(workingDir, ".grill-tui")
	primaryPath := filepath.Join(stateDirectory, "worksheet.json")
	backupPath := filepath.Join(stateDirectory, "worksheet.backup.json")
	primary, err := os.ReadFile(primaryPath)
	if err != nil {
		t.Fatalf("read primary: %v", err)
	}
	backup, err := os.ReadFile(backupPath)
	if err != nil {
		t.Fatalf("read backup: %v", err)
	}
	unsupported := bytes.Replace(backup, []byte(`"schema_version": 1`), []byte(`"schema_version": 99`), 1)
	if bytes.Equal(unsupported, backup) {
		t.Fatal("test setup did not replace the backup schema version")
	}
	if err := os.WriteFile(backupPath, unsupported, 0o600); err != nil {
		t.Fatalf("install unsupported backup: %v", err)
	}

	output := runCLIExpectFailure(t, workingDir)
	for _, want := range []string{"backup", "unsupported schema version 99", "supports version 1", "no migration was attempted"} {
		if !strings.Contains(output, want) {
			t.Fatalf("unsupported-backup refusal does not contain %q:\n%s", want, output)
		}
	}
	assertFileContents(t, primaryPath, primary)
	assertFileContents(t, backupPath, unsupported)
}

func TestValidExistingStateArtifactsAreTightenedToOwnerOnly(t *testing.T) {
	workingDir := createWorksheetWithBackup(t)
	stateDirectory := filepath.Join(workingDir, ".grill-tui")
	paths := []string{
		stateDirectory,
		filepath.Join(stateDirectory, ".gitignore"),
		filepath.Join(stateDirectory, "worksheet.lock"),
		filepath.Join(stateDirectory, "worksheet.json"),
		filepath.Join(stateDirectory, "worksheet.backup.json"),
	}
	for _, path := range paths {
		if err := os.Chmod(path, 0o777); err != nil {
			t.Fatalf("make %s overly permissive: %v", path, err)
		}
	}

	terminal := startTerminal(t, workingDir)
	terminal.send(t, "q")
	terminal.waitForExit(t)
	assertFilePermissions(t, stateDirectory, 0o700)
	for _, path := range paths[1:] {
		assertFilePermissions(t, path, 0o600)
	}
}

func TestMissingPrimaryWithInvalidBackupReportsRepairAction(t *testing.T) {
	workingDir := createWorksheetWithBackup(t)
	stateDirectory := filepath.Join(workingDir, ".grill-tui")
	primaryPath := filepath.Join(stateDirectory, "worksheet.json")
	backupPath := filepath.Join(stateDirectory, "worksheet.backup.json")
	if err := os.Remove(primaryPath); err != nil {
		t.Fatalf("remove primary: %v", err)
	}
	invalidBackup := []byte("invalid backup evidence")
	if err := os.WriteFile(backupPath, invalidBackup, 0o600); err != nil {
		t.Fatalf("install invalid backup: %v", err)
	}

	output := runCLIExpectFailure(t, workingDir)
	for _, want := range []string{"safe recovery is impossible", "repair or restore", "worksheet.backup.json", "preserved"} {
		if !strings.Contains(output, want) {
			t.Fatalf("missing-primary diagnostic does not contain %q:\n%s", want, output)
		}
	}
	assertFileContents(t, backupPath, invalidBackup)
}

func TestWorksheetAllowsOnlyOneWriterProcess(t *testing.T) {
	workingDir := t.TempDir()
	owner := startTerminal(t, workingDir, "70")
	primaryPath := filepath.Join(workingDir, ".grill-tui", "worksheet.json")
	beforeCompetitor, err := os.ReadFile(primaryPath)
	if err != nil {
		t.Fatalf("read primary before competing process: %v", err)
	}

	output := runCLIExpectFailure(t, workingDir, "99")
	for _, want := range []string{"already open", "another process"} {
		if !strings.Contains(output, want) {
			t.Fatalf("competing-process diagnostic does not contain %q:\n%s", want, output)
		}
	}
	assertFileContents(t, primaryPath, beforeCompetitor)
	assertFilePermissions(t, filepath.Join(workingDir, ".grill-tui", "worksheet.lock"), 0o600)

	owner.send(t, "r")
	screen := owner.waitForSelection(t, 71)
	if !strings.Contains(screen, "  70 │ recommended") {
		t.Fatalf("lock owner could not continue mutating its Worksheet:\n%s", screen)
	}
	owner.send(t, "q")
	owner.waitForExit(t)

	resumed := startTerminal(t, workingDir)
	screen = resumed.waitForSelection(t, 71)
	if !strings.Contains(screen, "  70 │ recommended") {
		t.Fatalf("Worksheet was damaged by competing process:\n%s", screen)
	}
	resumed.send(t, "q")
	resumed.waitForExit(t)
}

func TestResetRequiresConfirmationAndReturnsToStartingPrompt(t *testing.T) {
	workingDir := t.TempDir()
	terminal := startTerminal(t, workingDir, "70")
	terminal.send(t, "r")
	terminal.waitForSelection(t, 71)
	stateDirectory := filepath.Join(workingDir, ".grill-tui")
	primaryPath := filepath.Join(stateDirectory, "worksheet.json")
	backupPath := filepath.Join(stateDirectory, "worksheet.backup.json")
	if _, err := os.Stat(backupPath); err != nil {
		t.Fatalf("reset test requires a backup: %v", err)
	}

	terminal.send(t, "\x12")
	screen := terminal.waitFor(t, "Reset armed")
	if !strings.Contains(screen, "press Ctrl-R again within 2 seconds") {
		t.Fatalf("reset confirmation guidance is incomplete:\n%s", screen)
	}
	if _, err := os.Stat(primaryPath); err != nil {
		t.Fatalf("first reset key changed primary state: %v", err)
	}
	if _, err := os.Stat(backupPath); err != nil {
		t.Fatalf("first reset key changed backup state: %v", err)
	}

	terminal.send(t, "\x12")
	screen = terminal.waitFor(t, "Worksheet reset")
	if !strings.Contains(screen, "Starting number:") {
		t.Fatalf("confirmed reset did not return to starting-number prompt:\n%s", screen)
	}
	for _, path := range []string{primaryPath, backupPath} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("confirmed reset left %s behind; stat error = %v", path, err)
		}
	}
	terminal.send(t, "q")
	terminal.waitForExit(t)

	restarted := startTerminal(t, workingDir)
	screen = restarted.waitFor(t, "Starting number:")
	if strings.Contains(screen, "70 Answer") || strings.Contains(screen, "recommended") {
		t.Fatalf("post-reset restart resumed discarded Worksheet data:\n%s", screen)
	}
	restarted.send(t, "90\r")
	restarted.waitForSelection(t, 90)
	restarted.send(t, "q")
	restarted.waitForExit(t)
}

func TestAnyOtherKeyDisarmsResetAndStillPerformsItsAction(t *testing.T) {
	workingDir := t.TempDir()
	terminal := startTerminal(t, workingDir, "70")
	terminal.send(t, "j")
	terminal.waitForSelection(t, 71)

	terminal.send(t, "\x12")
	terminal.waitFor(t, "Reset armed")
	terminal.send(t, "j")
	terminal.waitForSelection(t, 72)

	terminal.send(t, "\x12")
	time.Sleep(100 * time.Millisecond)
	if _, err := os.Stat(filepath.Join(workingDir, ".grill-tui", "worksheet.json")); err != nil {
		t.Fatalf("single Ctrl-R after cancellation reset the Worksheet: %v", err)
	}
	terminal.send(t, "j")
	terminal.waitForSelection(t, 73)
	terminal.send(t, "q")
	terminal.waitForExit(t)
}

func TestResetConfirmationExpiresAfterTwoSeconds(t *testing.T) {
	workingDir := t.TempDir()
	terminal := startTerminal(t, workingDir, "70")
	terminal.send(t, "j")
	terminal.waitForSelection(t, 71)

	terminal.send(t, "\x12")
	terminal.waitFor(t, "Reset armed")
	terminal.waitFor(t, "Reset disarmed after 2 seconds")

	terminal.send(t, "\x12")
	time.Sleep(100 * time.Millisecond)
	if _, err := os.Stat(filepath.Join(workingDir, ".grill-tui", "worksheet.json")); err != nil {
		t.Fatalf("Ctrl-R after expiration reset the Worksheet: %v", err)
	}
	terminal.send(t, "j")
	terminal.waitForSelection(t, 72)
	terminal.send(t, "q")
	terminal.waitForExit(t)
}

func TestResetCannotConfirmAfterDeadlineWhenProcessWasSuspended(t *testing.T) {
	workingDir := t.TempDir()
	terminal := startTerminal(t, workingDir, "70")
	terminal.send(t, "\x12")
	terminal.waitFor(t, "Reset armed")
	if err := terminal.cmd.Process.Signal(os.Signal(syscall.SIGSTOP)); err != nil {
		t.Fatalf("suspend grill-tui: %v", err)
	}
	time.Sleep(2100 * time.Millisecond)
	terminal.send(t, "\x12")
	if err := terminal.cmd.Process.Signal(os.Signal(syscall.SIGCONT)); err != nil {
		t.Fatalf("resume grill-tui: %v", err)
	}
	time.Sleep(100 * time.Millisecond)
	if _, err := os.Stat(filepath.Join(workingDir, ".grill-tui", "worksheet.json")); err != nil {
		t.Fatalf("Ctrl-R after the deadline reset the Worksheet: %v", err)
	}
	terminal.send(t, "j")
	terminal.waitForSelection(t, 71)
	terminal.send(t, "q")
	terminal.waitForExit(t)
}

func createWorksheetWithBackup(t *testing.T) string {
	t.Helper()
	workingDir := t.TempDir()
	terminal := startTerminal(t, workingDir, "41")
	terminal.send(t, "r")
	terminal.waitForSelection(t, 42)
	terminal.send(t, "q")
	terminal.waitForExit(t)
	return workingDir
}

func runCLIExpectFailure(t *testing.T, workingDir string, args ...string) string {
	t.Helper()
	command := exec.Command(grillTUIBinary, args...)
	command.Dir = workingDir
	command.Env = overriddenEnvironment(t, environmentOverrides{})
	output, err := command.CombinedOutput()
	if err == nil {
		t.Fatalf("grill-tui unexpectedly succeeded; output:\n%s", output)
	}
	return cleanTerminalOutput(string(output))
}

func assertFileContents(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s changed\nwant: %q\ngot: %q", path, want, got)
	}
}

type worksheetDocument struct {
	SchemaVersion int `json:"schema_version"`
	Slots         []struct {
		Number int    `json:"number"`
		Answer string `json:"answer"`
	} `json:"slots"`
	Selected int `json:"selected"`
	Viewport int `json:"viewport"`
}

func readValidWorksheetDocument(t *testing.T, path string) worksheetDocument {
	t.Helper()
	encoded, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return decodeValidWorksheetDocument(t, path, encoded)
}

func decodeValidWorksheetDocument(t *testing.T, path string, encoded []byte) worksheetDocument {
	t.Helper()
	var document worksheetDocument
	if err := json.Unmarshal(encoded, &document); err != nil {
		t.Fatalf("decode %s: %v; contents: %q", path, err, encoded)
	}
	if document.SchemaVersion != 1 {
		t.Fatalf("%s schema version = %d, want 1", path, document.SchemaVersion)
	}
	if len(document.Slots) == 0 || document.Selected < 0 || document.Selected >= len(document.Slots) {
		t.Fatalf("%s contains invalid Worksheet state: %#v", path, document)
	}
	return document
}

func TestPromptRejectsNonPositiveInputThenCreatesWorksheet(t *testing.T) {
	workingDir := t.TempDir()
	terminal := startTerminal(t, workingDir)

	prompt := terminal.waitFor(t, "Starting number:")
	if !strings.Contains(prompt, "positive") {
		t.Fatalf("prompt does not ask for a positive starting number:\n%s", prompt)
	}

	terminal.send(t, "0\r")
	feedback := terminal.waitFor(t, "must be a positive integer")
	if !strings.Contains(feedback, "Try again") {
		t.Fatalf("invalid-input feedback is not actionable:\n%s", feedback)
	}

	terminal.send(t, "7\r")
	worksheet := terminal.waitFor(t, "10 Answer Slots")
	if !strings.Contains(worksheet, "> 7") || !strings.Contains(worksheet, "16") {
		t.Fatalf("created Worksheet does not show slots 7 through 16:\n%s", worksheet)
	}
	terminal.send(t, "q")
	terminal.waitForExit(t)
}

func TestPromptRejectsStartThatCannotFitTenConsecutiveSlots(t *testing.T) {
	maxInt := int(^uint(0) >> 1)
	tests := []struct {
		name  string
		input string
	}{
		{name: "largest int", input: fmt.Sprintf("%d", maxInt)},
		{name: "larger than int", input: "9999999999999999999999999999999999999999"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			terminal := startTerminal(t, t.TempDir())
			terminal.send(t, test.input+"\r")
			feedback := terminal.waitFor(t, "too large")
			if !strings.Contains(feedback, "ten consecutive Answer Slots") || !strings.Contains(feedback, "Try again") {
				t.Fatalf("overflow feedback is not actionable:\n%s", feedback)
			}
			terminal.send(t, "q")
			terminal.waitForExit(t)
		})
	}
}

func TestExistingWorksheetResumesAndWinsOverSuppliedStart(t *testing.T) {
	workingDir := t.TempDir()
	firstRun := startTerminal(t, workingDir, "22")
	firstRun.send(t, "q")
	firstRun.waitForExit(t)

	statePath := filepath.Join(workingDir, ".grill-tui", "worksheet.json")
	beforeResume, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("read Worksheet before resuming: %v", err)
	}

	resumed := startTerminal(t, workingDir, "99")
	screen := resumed.waitFor(t, "10 Answer Slots")
	if !strings.Contains(screen, "> 22") || !strings.Contains(screen, "31") {
		t.Fatalf("existing Worksheet was not resumed:\n%s", screen)
	}
	if strings.Contains(screen, "99") {
		t.Fatalf("supplied start replaced the existing Worksheet:\n%s", screen)
	}
	resumed.send(t, "q")
	resumed.waitForExit(t)

	afterResume, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("read Worksheet after resuming: %v", err)
	}
	if !bytes.Equal(afterResume, beforeResume) {
		t.Fatalf("resuming with a supplied start changed the Worksheet\nbefore: %s\nafter: %s", beforeResume, afterResume)
	}
}

func TestDefaultQuitBindingsExit(t *testing.T) {
	tests := []struct {
		name string
		key  string
	}{
		{name: "q", key: "q"},
		{name: "Ctrl-Q", key: "\x11"},
		{name: "Ctrl-C", key: "\x03"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			terminal := startTerminal(t, t.TempDir(), "5")
			terminal.send(t, test.key)
			terminal.waitForExit(t)
		})
	}
}

func TestConfiguredBindingsDriveActionsAndEffectiveHelp(t *testing.T) {
	configHome := t.TempDir()
	writeConfigFixture(t, configHome, "[keymap]\nmove_down = [\"z\", \"v\"]\n")
	terminal := startTerminalWithEnvironment(t, t.TempDir(), environmentOverrides{values: map[string]string{
		"XDG_CONFIG_HOME": configHome,
	}}, "5")

	terminal.send(t, "j")
	time.Sleep(50 * time.Millisecond)
	beforeRemappedKey := cleanTerminalOutput(terminal.output.String())
	selections := selectedAnswerSlotPattern.FindAllStringSubmatch(beforeRemappedKey, -1)
	if selections[len(selections)-1][1] != "5" {
		t.Fatalf("removed j binding moved the selection:\n%s", beforeRemappedKey)
	}
	terminal.send(t, "z")
	terminal.waitForSelection(t, 6)
	terminal.send(t, "v")
	worksheet := terminal.waitForSelection(t, 7)
	if !strings.Contains(worksheet, "z/v/↑/k/Ctrl-P move") {
		t.Fatalf("compact help does not reflect the effective movement bindings:\n%s", worksheet)
	}

	mark := len(terminal.output.String())
	terminal.send(t, "?")
	help := terminal.waitForAfter(t, mark, "Complete Help")
	if !strings.Contains(help, "Move down: z/v; up: ↑/k/Ctrl-P") {
		t.Fatalf("complete help does not reflect the effective movement bindings:\n%s", help)
	}
	if strings.Contains(help, "↓/j/Ctrl-N") {
		t.Fatalf("complete help still claims removed movement bindings are active:\n%s", help)
	}
	terminal.send(t, "?")
	terminal.waitForSelection(t, 7)
	terminal.send(t, "q")
	terminal.waitForExit(t)
}

func TestConfigDefaultsPrintsTheCompleteAuthoritativeKeymap(t *testing.T) {
	result := runCLI(t, t.TempDir(), environmentOverrides{values: map[string]string{
		"XDG_CONFIG_HOME": t.TempDir(),
	}}, "config", "defaults")
	if result.err != nil {
		t.Fatalf("config defaults failed: %v\nstderr: %s", result.err, result.stderr)
	}
	if result.stderr != "" {
		t.Fatalf("config defaults wrote stderr: %q", result.stderr)
	}
	for _, line := range []string{
		"[keymap]",
		`move_down = ["down", "j", "ctrl+n"]`,
		`move_up = ["up", "k", "ctrl+p"]`,
		`skip = ["space"]`,
		`recommended = ["r", "R"]`,
		`yes = ["y", "Y"]`,
		`no = ["n", "N"]`,
		`choice_1 = ["1"]`, `choice_2 = ["2"]`, `choice_3 = ["3"]`, `choice_4 = ["4"]`, `choice_5 = ["5"]`,
		`choice_a = ["a"]`, `choice_b = ["b"]`, `choice_c = ["c"]`, `choice_d = ["d"]`, `choice_e = ["e"]`,
		`choice_upper_a = ["A"]`, `choice_upper_b = ["B"]`, `choice_upper_c = ["C"]`, `choice_upper_d = ["D"]`, `choice_upper_e = ["E"]`,
		`explain = ["x"]`, `inline = ["i"]`, `external = ["o"]`, `clear = ["esc"]`, `undo = ["u"]`,
		`copy = ["s", "ctrl+s"]`, `reset = ["ctrl+r"]`, `help = ["?"]`, `quit = ["q", "ctrl+q", "ctrl+c"]`,
	} {
		if !strings.Contains(result.stdout, line+"\n") && result.stdout != line+"\n" {
			t.Fatalf("config defaults is missing %q:\n%s", line, result.stdout)
		}
	}
}

func TestConfigurationErrorsAreStrictAndActionable(t *testing.T) {
	tests := []struct {
		name       string
		contents   string
		wantDetail string
	}{
		{name: "unknown top-level option", contents: "mystery = true\n", wantDetail: "unknown option(s): mystery"},
		{name: "unknown action", contents: "[keymap]\nmystery = [\"z\"]\n", wantDetail: "unknown option keymap.mystery"},
		{name: "invalid key name", contents: "[keymap]\nmove_down = [\"ctrl+banana\"]\n", wantDetail: `keymap.move_down: invalid key name "ctrl+banana"`},
		{name: "conflicting bindings", contents: "[keymap]\nmove_down = [\"k\"]\n", wantDetail: `binding "k" conflicts between move_down and move_up`},
		{name: "terminal alias conflict", contents: "[keymap]\nmove_down = [\"ctrl+i\"]\nmove_up = [\"tab\"]\n", wantDetail: `binding "tab" conflicts between move_down and move_up`},
		{name: "duplicate definition", contents: "[keymap]\nmove_down = [\"z\"]\nmove_down = [\"v\"]\n", wantDetail: "duplicate option keymap.move_down"},
		{name: "invalid value type", contents: "[keymap]\nmove_down = \"z\"\n", wantDetail: "incompatible types"},
		{name: "unreachable action", contents: "[keymap]\nquit = []\n", wantDetail: "keymap.quit must contain at least one key binding"},
		{name: "Ctrl-S-only copy", contents: "[keymap]\ncopy = [\"ctrl+s\"]\n", wantDetail: "must include a binding other than ctrl+s"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			workingDir := t.TempDir()
			configHome := t.TempDir()
			writeConfigFixture(t, configHome, test.contents)
			result := runCLI(t, workingDir, environmentOverrides{values: map[string]string{
				"XDG_CONFIG_HOME": configHome,
			}}, "5")
			if result.err == nil || !strings.Contains(result.stderr, test.wantDetail) {
				t.Fatalf("invalid configuration error = %v, stderr = %q; want detail %q", result.err, result.stderr, test.wantDetail)
			}
			if _, err := os.Stat(filepath.Join(workingDir, ".grill-tui")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("invalid configuration partially created Worksheet state: %v", err)
			}
		})
	}
}

func TestOptionalActionCanBeUnbound(t *testing.T) {
	configHome := t.TempDir()
	writeConfigFixture(t, configHome, "[keymap]\nexternal = []\n")
	terminal := startTerminalWithEnvironment(t, t.TempDir(), environmentOverrides{values: map[string]string{
		"XDG_CONFIG_HOME": configHome,
	}}, "5")

	terminal.send(t, "o")
	time.Sleep(50 * time.Millisecond)
	mark := len(terminal.output.String())
	terminal.send(t, "?")
	help := terminal.waitForAfter(t, mark, "Complete Help")
	if !strings.Contains(help, "Custom: i inline; (unbound) external editor") {
		t.Fatalf("complete help does not identify the unbound optional action:\n%s", help)
	}
	mark = len(terminal.output.String())
	terminal.send(t, "?")
	terminal.waitForAfter(t, mark, "Selected Answer 5 (full):")
	terminal.send(t, "q")
	terminal.waitForExit(t)
}

func TestUnreadableConfigurationProducesAnActionableDiagnostic(t *testing.T) {
	configHome := t.TempDir()
	configPath := filepath.Join(configHome, "grill-tui", "config.toml")
	if err := os.MkdirAll(configPath, 0o700); err != nil {
		t.Fatalf("create unreadable configuration fixture: %v", err)
	}
	result := runCLI(t, t.TempDir(), environmentOverrides{values: map[string]string{
		"XDG_CONFIG_HOME": configHome,
	}}, "5")
	if result.err == nil || !strings.Contains(result.stderr, "read configuration") || !strings.Contains(result.stderr, configPath) {
		t.Fatalf("unreadable configuration error = %v, stderr = %q", result.err, result.stderr)
	}
}

func TestConfigInstallIsSafeAndForcePreservesATimestampedBackup(t *testing.T) {
	configHome := t.TempDir()
	overrides := environmentOverrides{values: map[string]string{"XDG_CONFIG_HOME": configHome}}
	defaults := runCLI(t, t.TempDir(), overrides, "config", "defaults")
	if defaults.err != nil {
		t.Fatalf("read authoritative defaults: %v", defaults.err)
	}
	installed := runCLI(t, t.TempDir(), overrides, "config", "install")
	if installed.err != nil {
		t.Fatalf("config install failed: %v\nstderr: %s", installed.err, installed.stderr)
	}
	configPath := filepath.Join(configHome, "grill-tui", "config.toml")
	assertFileContentsAndMode(t, configPath, defaults.stdout, 0o600)

	refused := runCLI(t, t.TempDir(), overrides, "config", "install")
	if refused.err == nil || !strings.Contains(refused.stderr, "already exists") || !strings.Contains(refused.stderr, "--force") {
		t.Fatalf("second config install error = %v, stderr = %q", refused.err, refused.stderr)
	}
	assertFileContentsAndMode(t, configPath, defaults.stdout, 0o600)

	const previous = "[keymap]\nquit = [\"q\"]\n"
	if err := os.WriteFile(configPath, []byte(previous), 0o600); err != nil {
		t.Fatalf("replace configuration fixture: %v", err)
	}
	forced := runCLI(t, t.TempDir(), overrides, "config", "install", "--force")
	if forced.err != nil {
		t.Fatalf("config install --force failed: %v\nstderr: %s", forced.err, forced.stderr)
	}
	assertFileContentsAndMode(t, configPath, defaults.stdout, 0o600)
	backups, err := filepath.Glob(configPath + ".backup-*")
	if err != nil || len(backups) != 1 {
		t.Fatalf("timestamped configuration backups = %v, err = %v; want one", backups, err)
	}
	assertFileContentsAndMode(t, backups[0], previous, 0o600)
}

func TestConfiguredQuitCanRemoveCtrlCButCannotRemoveEveryBinding(t *testing.T) {
	configHome := t.TempDir()
	writeConfigFixture(t, configHome, "[keymap]\nquit = [\"q\"]\n")
	terminal := startTerminalWithEnvironment(t, t.TempDir(), environmentOverrides{values: map[string]string{
		"XDG_CONFIG_HOME": configHome,
	}}, "5")

	terminal.send(t, "\x03")
	time.Sleep(50 * time.Millisecond)
	select {
	case err := <-terminal.done:
		t.Fatalf("Ctrl-C exited after it was removed from quit bindings: %v", err)
	default:
	}
	terminal.send(t, "q")
	terminal.waitForExit(t)
}

func TestRemappedPresetBindingsKeepTheirActionSemantics(t *testing.T) {
	configHome := t.TempDir()
	writeConfigFixture(t, configHome, `[keymap]
recommended = ["z"]
choice_upper_a = ["v"]
explain = ["g"]
`)
	terminal := startTerminalWithEnvironment(t, t.TempDir(), environmentOverrides{values: map[string]string{
		"XDG_CONFIG_HOME": configHome,
	}}, "10")

	for _, step := range []struct {
		key    string
		number int
		answer string
	}{
		{key: "z", number: 10, answer: "recommended"},
		{key: "v", number: 11, answer: "A"},
		{key: "g", number: 12, answer: "explain further"},
	} {
		terminal.send(t, step.key)
		screen := terminal.waitForSelection(t, step.number+1)
		if answer := latestRenderedAnswer(t, screen, step.number); answer != step.answer {
			t.Fatalf("remapped %q committed %q, want %q:\n%s", step.key, answer, step.answer, screen)
		}
	}
	terminal.send(t, "q")
	terminal.waitForExit(t)
}

func TestRemappedResetBindingControlsConfirmationAndHelp(t *testing.T) {
	configHome := t.TempDir()
	writeConfigFixture(t, configHome, "[keymap]\nreset = [\"z\"]\n")
	workingDir := t.TempDir()
	terminal := startTerminalWithEnvironment(t, workingDir, environmentOverrides{values: map[string]string{
		"XDG_CONFIG_HOME": configHome,
	}}, "70")

	terminal.send(t, "\x12")
	time.Sleep(50 * time.Millisecond)
	if _, err := os.Stat(filepath.Join(workingDir, ".grill-tui", "worksheet.json")); err != nil {
		t.Fatalf("removed Ctrl-R reset binding changed the Worksheet: %v", err)
	}
	terminal.send(t, "z")
	armed := terminal.waitFor(t, "Reset armed")
	if !strings.Contains(armed, "press z again within 2 seconds") || strings.Contains(armed, "press Ctrl-R again") {
		t.Fatalf("reset status does not reflect the effective binding:\n%s", armed)
	}
	mark := len(terminal.output.String())
	terminal.send(t, "?")
	help := terminal.waitForAfter(t, mark, "Complete Help")
	if !strings.Contains(help, "Reset: z twice within two seconds") || strings.Contains(help, "Reset: Ctrl-R") {
		t.Fatalf("complete help does not reflect the effective reset binding:\n%s", help)
	}
	terminal.send(t, "?")
	terminal.waitFor(t, "Reset disarmed; Worksheet unchanged")
	terminal.send(t, "q")
	terminal.waitForExit(t)
}

func TestInlineCustomAnswerAcceptsQuitBindingsAsText(t *testing.T) {
	tests := []struct {
		name       string
		config     string
		answer     string
		normalQuit string
	}{
		{name: "default printable quit", answer: "question", normalQuit: "q"},
		{name: "remapped printable quit", config: "[keymap]\nquit = [\"z\"]\n", answer: "pizza", normalQuit: "z"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			configHome := t.TempDir()
			if test.config != "" {
				writeConfigFixture(t, configHome, test.config)
			}
			terminal := startTerminalWithEnvironment(t, t.TempDir(), environmentOverrides{values: map[string]string{
				"XDG_CONFIG_HOME": configHome,
			}}, "30")
			terminal.send(t, "i")
			terminal.waitFor(t, "Inline Custom Answer 30:")
			terminal.send(t, test.answer+"\r")
			screen := terminal.waitForSelection(t, 31)
			if answer := latestRenderedAnswer(t, screen, 30); answer != test.answer {
				t.Fatalf("inline answer = %q, want %q:\n%s", answer, test.answer, screen)
			}
			terminal.send(t, test.normalQuit)
			terminal.waitForExit(t)
		})
	}
}

func TestTerminalModesAreEnabledAndRestored(t *testing.T) {
	terminal := startTerminal(t, t.TempDir(), "5")

	started := terminal.output.String()
	for _, sequence := range []string{"\x1b[?1049h", "\x1b[?1002h", "\x1b[?1006h"} {
		if !strings.Contains(started, sequence) {
			t.Fatalf("startup output does not enable terminal mode %q: %q", sequence, started)
		}
	}

	terminal.send(t, "q")
	terminal.waitForExit(t)
	finished := terminal.output.String()
	for _, sequence := range []string{"\x1b[?1049l", "\x1b[?1002l", "\x1b[?1006l"} {
		if !strings.Contains(finished, sequence) {
			t.Fatalf("exit output does not restore terminal mode %q: %q", sequence, finished)
		}
	}
}

func TestResizeShowsSmallTerminalStateAndRestoresWorksheet(t *testing.T) {
	workingDir := t.TempDir()
	terminal := startTerminal(t, workingDir, "5")
	for number := 6; number <= 14; number++ {
		terminal.send(t, "j")
		terminal.waitForSelection(t, number)
	}

	mark := len(terminal.output.String())
	terminal.resize(t, 40, 10)
	small := terminal.waitForAfter(t, mark, "Terminal too small")
	for _, text := range []string{"40×10", "Selected Answer Slot 14", "Worksheet data is safe", "resize"} {
		if !strings.Contains(small, text) {
			t.Fatalf("small-terminal state does not contain %q:\n%s", text, small)
		}
	}
	smallLines := strings.Split(strings.TrimSpace(small), "\n")
	if len(smallLines) > 10 {
		t.Fatalf("small-terminal state uses %d rows, want at most 10:\n%s", len(smallLines), small)
	}
	for _, line := range smallLines {
		if width := runewidth.StringWidth(line); width > 40 {
			t.Fatalf("small-terminal state rendered a %d-cell line %q:\n%s", width, line, small)
		}
	}

	mark = len(terminal.output.String())
	terminal.resize(t, 80, 24)
	restored := terminal.waitForAfter(t, mark, "Selected Answer 14 (full):")
	if !strings.Contains(restored, "> 14 │") {
		t.Fatalf("resized Worksheet did not keep the selected Answer Slot visible:\n%s", restored)
	}

	terminal.send(t, "q")
	terminal.waitForExit(t)
	resumed := startTerminal(t, workingDir)
	resumed.waitForSelection(t, 14)
	resumed.send(t, "q")
	resumed.waitForExit(t)
}

func TestSmallTerminalStateDoesNotAcceptHiddenPromptInput(t *testing.T) {
	terminal := startTerminal(t, t.TempDir())
	mark := len(terminal.output.String())
	terminal.resize(t, 40, 10)
	small := terminal.waitForAfter(t, mark, "Terminal too small")
	if !strings.Contains(small, "No Worksheet has been created yet") {
		t.Fatalf("small-terminal prompt state does not explain that no data exists:\n%s", small)
	}

	terminal.send(t, "7\r")
	time.Sleep(50 * time.Millisecond)
	mark = len(terminal.output.String())
	terminal.resize(t, 80, 24)
	prompt := terminal.waitForAfter(t, mark, "Starting number:")
	if strings.Contains(prompt, "10 Answer Slots") || !strings.Contains(prompt, "Starting number: \n") {
		t.Fatalf("hidden input changed the starting-number prompt:\n%s", prompt)
	}

	terminal.send(t, "q")
	terminal.waitForExit(t)
}

func TestResizeReflowsViewportAroundSelectedAnswerSlot(t *testing.T) {
	terminal := startTerminal(t, t.TempDir(), "5")
	for number := 6; number <= 14; number++ {
		terminal.send(t, "j")
		terminal.waitForSelection(t, number)
	}

	mark := len(terminal.output.String())
	terminal.resize(t, 80, 16)
	compact := terminal.waitForAfter(t, mark, "Selected Answer 14 (full):")
	assertVisibleAnswerSlotRange(t, compact, 11, 14)

	mark = len(terminal.output.String())
	terminal.resize(t, 80, 24)
	expanded := terminal.waitForAfter(t, mark, "Selected Answer 14 (full):")
	assertVisibleAnswerSlotRange(t, expanded, 5, 14)

	terminal.send(t, "q")
	terminal.waitForExit(t)
}

func TestCompactHeightKeepsSelectionVisibleAcrossNavigationCommitAndUndo(t *testing.T) {
	terminal := startTerminal(t, t.TempDir(), "5")
	terminal.resize(t, 80, 16)
	terminal.waitFor(t, "Selected Answer 5 (full):")

	for selected := 6; selected <= 8; selected++ {
		terminal.send(t, "j")
		terminal.waitForSelection(t, selected)
	}
	mark := len(terminal.output.String())
	terminal.send(t, "j")
	terminal.waitForAfter(t, mark, "Selected Answer 9 (full):")
	mark = len(terminal.output.String())
	terminal.send(t, "?")
	terminal.waitForAfter(t, mark, "Move: ↑/↓, j/k, Ctrl-N/Ctrl-P")
	mark = len(terminal.output.String())
	terminal.send(t, "?")
	navigated := terminal.waitForAfter(t, mark, "Selected Answer 9 (full):")
	assertVisibleAnswerSlotRange(t, navigated, 6, 9)

	terminal.send(t, "i")
	terminal.waitFor(t, "Inline Custom Answer 9:")
	mark = len(terminal.output.String())
	terminal.send(t, "compact answer\r")
	terminal.waitForAfter(t, mark, "Selected Answer 10 (full):")
	mark = len(terminal.output.String())
	terminal.send(t, "?")
	terminal.waitForAfter(t, mark, "Move: ↑/↓, j/k, Ctrl-N/Ctrl-P")
	mark = len(terminal.output.String())
	terminal.send(t, "?")
	committed := terminal.waitForAfter(t, mark, "Selected Answer 10 (full):")
	assertVisibleAnswerSlotRange(t, committed, 7, 10)

	mark = len(terminal.output.String())
	terminal.send(t, "u")
	terminal.waitForAfter(t, mark, "Undid Answer Slot 9")
	mark = len(terminal.output.String())
	terminal.send(t, "?")
	terminal.waitForAfter(t, mark, "Move: ↑/↓, j/k, Ctrl-N/Ctrl-P")
	mark = len(terminal.output.String())
	terminal.send(t, "?")
	undone := terminal.waitForAfter(t, mark, "Undid Answer Slot 9")
	assertVisibleAnswerSlotRange(t, undone, 6, 9)

	terminal.send(t, "q")
	terminal.waitForExit(t)
}

func TestInlineCancelAfterCompactResizeKeepsSelectionVisible(t *testing.T) {
	terminal := startTerminal(t, t.TempDir(), "5")
	terminal.resize(t, 80, 17)
	terminal.waitFor(t, "Selected Answer 5 (full):")
	for selected := 6; selected <= 9; selected++ {
		terminal.send(t, "j")
		terminal.waitForSelection(t, selected)
	}

	terminal.send(t, "i")
	terminal.waitFor(t, "Inline Custom Answer 9:")
	mark := len(terminal.output.String())
	terminal.resize(t, 80, 16)
	terminal.waitForAfter(t, mark, "Inline Custom Answer 9:")
	terminal.send(t, "\x1b")
	terminal.waitFor(t, "Inline Custom Answer cancelled")

	mark = len(terminal.output.String())
	terminal.send(t, "?")
	terminal.waitForAfter(t, mark, "Move: ↑/↓, j/k, Ctrl-N/Ctrl-P")
	mark = len(terminal.output.String())
	terminal.send(t, "?")
	worksheet := terminal.waitForAfter(t, mark, "Inline Custom Answer cancelled")
	assertVisibleAnswerSlotRange(t, worksheet, 6, 9)

	terminal.send(t, "q")
	terminal.waitForExit(t)
}

func TestCompactHeightBudgetsRowsForMultilinePreviewAndWrappedHelp(t *testing.T) {
	editor := writeEditorFixture(t, filepath.Join(t.TempDir(), "multiline-editor"), `#!/bin/sh
printf 'first line\nsecond line\nthird line' > "$1"
`)
	terminal := startTerminalWithEnvironment(t, t.TempDir(), environmentOverrides{
		values:  map[string]string{"VISUAL": editor},
		removed: []string{"EDITOR"},
	}, "5")
	terminal.send(t, "o")
	terminal.waitForSelection(t, 6)
	terminal.send(t, "k")
	terminal.waitForSelection(t, 5)

	mark := len(terminal.output.String())
	terminal.resize(t, 50, 16)
	compact := terminal.waitForAfter(t, mark, "Selected Answer 5 (full):")
	assertVisibleAnswerSlotRange(t, compact, 5, 5)
	for _, line := range []string{"first line", "second line", "third line"} {
		if !strings.Contains(compact, line) {
			t.Fatalf("compact layout lost preview line %q:\n%s", line, compact)
		}
	}

	terminal.send(t, "q")
	terminal.waitForExit(t)
}

func TestSupportedWidthReflowsLongContentWithoutLosingPreview(t *testing.T) {
	workingDir := t.TempDir()
	longAnswer := "wide " + strings.Repeat("界", 30)
	installWorksheetFixture(t, workingDir, filepath.Join("testdata", "long-answer-worksheet.json"))
	terminal := startTerminal(t, workingDir)

	mark := len(terminal.output.String())
	terminal.resize(t, 50, 24)
	narrow := terminal.waitForAfter(t, mark, "Selected Answer 20 (full):")
	for _, line := range strings.Split(narrow, "\n") {
		if width := runewidth.StringWidth(line); width > 50 {
			t.Fatalf("50-column layout rendered a %d-cell line %q:\n%s", width, line, narrow)
		}
	}
	previewAndBelow := strings.SplitN(narrow, "Selected Answer 20 (full):\n", 2)
	if len(previewAndBelow) != 2 || !strings.Contains(strings.ReplaceAll(previewAndBelow[1], "\n", ""), longAnswer) {
		t.Fatalf("width reflow lost part of the full selected-answer preview:\n%s", narrow)
	}

	terminal.send(t, "q")
	terminal.waitForExit(t)
}

func TestMouseClickSelectsAnswerSlotAndPersistsSelection(t *testing.T) {
	workingDir := t.TempDir()
	terminal := startTerminal(t, workingDir, "5")

	terminal.send(t, sgrMousePress(10, 9, 0))
	terminal.waitForSelection(t, 8)
	terminal.send(t, "q")
	terminal.waitForExit(t)

	resumed := startTerminal(t, workingDir)
	resumed.waitForSelection(t, 8)
	resumed.send(t, "q")
	resumed.waitForExit(t)
}

func TestMouseClickSelectsVisibleAnswerSlotAtCompactHeight(t *testing.T) {
	terminal := startTerminal(t, t.TempDir(), "5")
	terminal.resize(t, 80, 16)
	terminal.waitFor(t, "Selected Answer 5 (full):")
	for selected := 6; selected <= 8; selected++ {
		terminal.send(t, "j")
		terminal.waitForSelection(t, selected)
	}

	terminal.send(t, sgrMousePress(10, 6, 0))
	terminal.waitForSelection(t, 5)

	terminal.send(t, "q")
	terminal.waitForExit(t)
}

func TestCompactMouseSelectionRevealsSlotWithMultilinePreview(t *testing.T) {
	editor := writeEditorFixture(t, filepath.Join(t.TempDir(), "mouse-preview-editor"), `#!/bin/sh
printf 'first line\nsecond line\nthird line' > "$1"
`)
	terminal := startTerminalWithEnvironment(t, t.TempDir(), environmentOverrides{
		values:  map[string]string{"VISUAL": editor},
		removed: []string{"EDITOR"},
	}, "5")
	for selected := 6; selected <= 8; selected++ {
		terminal.send(t, "j")
		terminal.waitForSelection(t, selected)
	}
	terminal.send(t, "o")
	terminal.waitForSelection(t, 9)
	for selected := 8; selected >= 5; selected-- {
		terminal.send(t, "k")
		terminal.waitForSelection(t, selected)
	}

	terminal.resize(t, 80, 16)
	terminal.waitFor(t, "Selected Answer 5 (full):")
	terminal.send(t, sgrMousePress(10, 9, 0))
	terminal.waitForSelection(t, 8)

	mark := len(terminal.output.String())
	terminal.send(t, "?")
	terminal.waitForAfter(t, mark, "Move: ↑/↓, j/k, Ctrl-N/Ctrl-P")
	mark = len(terminal.output.String())
	terminal.send(t, "?")
	worksheet := terminal.waitForAfter(t, mark, "Selected Answer 8 (full):")
	assertVisibleAnswerSlotRange(t, worksheet, 7, 8)

	terminal.send(t, "q")
	terminal.waitForExit(t)
}

func TestMouseWheelScrollsWorksheetWithoutChangingSelection(t *testing.T) {
	terminal := startTerminal(t, t.TempDir(), "5")
	for selected := 6; selected <= 15; selected++ {
		terminal.send(t, "r")
		terminal.waitForSelection(t, selected)
	}

	mark := len(terminal.output.String())
	terminal.send(t, sgrMousePress(10, 10, 64))
	scrolledUp := terminal.waitForAfter(t, mark, "Worksheet scrolled")
	assertVisibleAnswerSlotRange(t, scrolledUp, 5, 14)

	mark = len(terminal.output.String())
	terminal.send(t, sgrMousePress(10, 10, 65))
	scrolledDown := terminal.waitForAfter(t, mark, "> 15 │")
	assertVisibleAnswerSlotRange(t, scrolledDown, 6, 15)

	terminal.send(t, "k")
	terminal.waitForSelection(t, 14)
	terminal.send(t, "q")
	terminal.waitForExit(t)
}

func TestHelpOverlayListsEveryCurrentBindingAndLeavesWorksheetUnchanged(t *testing.T) {
	workingDir := t.TempDir()
	terminal := startTerminal(t, workingDir, "5")
	terminal.send(t, "j")
	terminal.waitForSelection(t, 6)

	mark := len(terminal.output.String())
	terminal.send(t, "?")
	help := terminal.waitForAfter(t, mark, "Complete Help")
	for _, text := range completeHelpBindingDescriptions {
		if !strings.Contains(help, text) {
			t.Fatalf("complete help does not contain %q:\n%s", text, help)
		}
	}
	if strings.Contains(help, "layout variant") || strings.Contains(help, "[/]") {
		t.Fatalf("complete help exposes prototype-only layout bindings:\n%s", help)
	}

	terminal.send(t, "r")
	time.Sleep(50 * time.Millisecond)
	mark = len(terminal.output.String())
	terminal.send(t, "?")
	worksheet := terminal.waitForAfter(t, mark, "Selected Answer 6 (full):")
	if !regexp.MustCompile(`(?m)^> 6 │ $`).MatchString(worksheet) {
		t.Fatalf("closing help did not return to the unchanged Worksheet:\n%s", worksheet)
	}
	if !strings.Contains(worksheet, "? help") || !strings.Contains(worksheet, "Status:") {
		t.Fatalf("normal view is missing compact help or status:\n%s", worksheet)
	}

	terminal.send(t, "q")
	terminal.waitForExit(t)
}

func TestCompleteHelpFitsMinimumSupportedTerminal(t *testing.T) {
	terminal := startTerminal(t, t.TempDir(), "5")
	terminal.resize(t, 50, 15)
	terminal.waitFor(t, "Selected Answer 5 (full):")

	mark := len(terminal.output.String())
	terminal.send(t, "?")
	help := terminal.waitForAfter(t, mark, "Help: ?; Quit: q, Ctrl-Q, Ctrl-C")
	for _, line := range strings.Split(help, "\n") {
		if width := runewidth.StringWidth(line); width > 50 {
			t.Fatalf("minimum-size help rendered a %d-cell line %q:\n%s", width, line, help)
		}
	}
	for _, text := range completeHelpBindingDescriptions {
		if !strings.Contains(help, text) {
			t.Fatalf("minimum-size help does not contain %q:\n%s", text, help)
		}
	}

	terminal.send(t, "q")
	terminal.waitForExit(t)
}

func TestNoColorDisablesPresentationColorWithoutRemovingCues(t *testing.T) {
	colored := startTerminalWithEnv(t, t.TempDir(), []string{"NO_COLOR="}, "5")
	if !sgrColorPattern.MatchString(colored.output.String()) {
		t.Fatalf("normal presentation did not use color for emphasis: %q", colored.output.String())
	}
	colored.send(t, "q")
	colored.waitForExit(t)

	colorFree := startTerminal(t, t.TempDir(), "5")
	raw := colorFree.output.String()
	if sgrColorPattern.MatchString(raw) {
		t.Fatalf("NO_COLOR presentation contains color styling: %q", raw)
	}
	screen := cleanTerminalOutput(raw)
	for _, cue := range []string{"> 5 │", "Selected Answer 5", "Status: Worksheet ready", "? help"} {
		if !strings.Contains(screen, cue) {
			t.Fatalf("NO_COLOR presentation lost %q cue:\n%s", cue, screen)
		}
	}
	colorFree.send(t, "q")
	colorFree.waitForExit(t)
}

func sgrMousePress(x, y, button int) string {
	return fmt.Sprintf("\x1b[<%d;%d;%dM", button, x, y)
}

func TestJKMovesSelectionAndStopsAtWorksheetBoundaries(t *testing.T) {
	workingDir := t.TempDir()
	terminal := startTerminal(t, workingDir, "5")

	terminal.send(t, "j")
	terminal.waitForSelection(t, 6)
	terminal.send(t, "k")
	terminal.waitForSelection(t, 5)

	terminal.send(t, "k")
	time.Sleep(50 * time.Millisecond)
	terminal.send(t, "j")
	terminal.waitForSelection(t, 6)
	for number := 7; number <= 14; number++ {
		terminal.send(t, "j")
		terminal.waitForSelection(t, number)
	}
	terminal.send(t, "j")
	time.Sleep(50 * time.Millisecond)
	terminal.send(t, "k")
	terminal.waitForSelection(t, 13)

	terminal.send(t, "q")
	terminal.waitForExit(t)
}

func TestDefaultNavigationFamiliesMoveSelection(t *testing.T) {
	tests := []struct {
		name     string
		next     string
		previous string
	}{
		{name: "arrow keys", next: "\x1b[B", previous: "\x1b[A"},
		{name: "control keys", next: "\x0e", previous: "\x10"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			terminal := startTerminal(t, t.TempDir(), "8")
			terminal.send(t, test.next)
			terminal.waitForSelection(t, 9)
			terminal.send(t, test.previous)
			terminal.waitForSelection(t, 8)
			terminal.send(t, "q")
			terminal.waitForExit(t)
		})
	}
}

func TestSpaceAdvancesWithoutEditingOrGrowingWorksheet(t *testing.T) {
	workingDir := t.TempDir()
	terminal := startTerminal(t, workingDir, "8")

	terminal.send(t, "r")
	terminal.waitForSelection(t, 9)
	terminal.send(t, "k")
	terminal.waitForSelection(t, 8)
	terminal.send(t, " ")
	terminal.waitForSelection(t, 9)
	for number := 10; number <= 17; number++ {
		terminal.send(t, "j")
		terminal.waitForSelection(t, number)
	}
	terminal.send(t, " ")
	time.Sleep(50 * time.Millisecond)
	terminal.send(t, "q")
	terminal.waitForExit(t)

	resumed := startTerminal(t, workingDir)
	resumed.waitForSelection(t, 17)
	resumed.send(t, "q")
	resumed.waitForExit(t)
	screen := cleanTerminalOutput(resumed.output.String())
	if slots := answerSlotLinePattern.FindAllStringSubmatch(screen, -1); len(slots) != 10 {
		t.Fatalf("Space at the final Answer Slot rendered %d slots, want 10:\n%s", len(slots), screen)
	}
	if !regexp.MustCompile(`(?m)^> 17 │ $`).MatchString(screen) {
		t.Fatalf("Space changed the selected Answer Slot instead of leaving it empty:\n%s", screen)
	}
	if !strings.Contains(screen, "  8 │ recommended") {
		t.Fatalf("Space changed the existing answer instead of preserving it:\n%s", screen)
	}
}

func TestPresetAnswerFamiliesCommitExactValuesAndAutoAdvance(t *testing.T) {
	tests := []struct {
		name   string
		key    string
		answer string
	}{
		{name: "lowercase recommended", key: "r", answer: "recommended"},
		{name: "uppercase recommended", key: "R", answer: "recommended"},
		{name: "lowercase yes", key: "y", answer: "yes"},
		{name: "uppercase yes", key: "Y", answer: "yes"},
		{name: "lowercase no", key: "n", answer: "no"},
		{name: "uppercase no", key: "N", answer: "no"},
		{name: "digit 1", key: "1", answer: "1"},
		{name: "digit 2", key: "2", answer: "2"},
		{name: "digit 3", key: "3", answer: "3"},
		{name: "digit 4", key: "4", answer: "4"},
		{name: "digit 5", key: "5", answer: "5"},
		{name: "lowercase a", key: "a", answer: "a"},
		{name: "lowercase b", key: "b", answer: "b"},
		{name: "lowercase c", key: "c", answer: "c"},
		{name: "lowercase d", key: "d", answer: "d"},
		{name: "lowercase e", key: "e", answer: "e"},
		{name: "uppercase A", key: "A", answer: "A"},
		{name: "uppercase B", key: "B", answer: "B"},
		{name: "uppercase C", key: "C", answer: "C"},
		{name: "uppercase D", key: "D", answer: "D"},
		{name: "uppercase E", key: "E", answer: "E"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			terminal := startTerminal(t, t.TempDir(), "30")
			terminal.send(t, test.key)
			screen := terminal.waitForSelection(t, 31)
			want := fmt.Sprintf("  30 │ %s", test.answer)
			if !strings.Contains(screen, want) {
				t.Fatalf("Preset Answer %q was not rendered exactly; want line containing %q:\n%s", test.key, want, screen)
			}
			terminal.send(t, "q")
			terminal.waitForExit(t)
		})
	}
}

func TestXCommitsExplainFurtherAndAutoAdvances(t *testing.T) {
	terminal := startTerminal(t, t.TempDir(), "30")
	terminal.send(t, "x")
	screen := terminal.waitForSelection(t, 31)
	if !strings.Contains(screen, "  30 │ explain further") {
		t.Fatalf("x did not commit the exact elaboration request:\n%s", screen)
	}
	terminal.send(t, "q")
	terminal.waitForExit(t)
}

func TestInlineCustomAnswerIsSeededAndEscapeCancelsWithoutChangingAnswer(t *testing.T) {
	terminal := startTerminal(t, t.TempDir(), "30")
	terminal.send(t, "r")
	terminal.waitForSelection(t, 31)
	terminal.send(t, "k")
	terminal.waitForSelection(t, 30)

	terminal.send(t, "i")
	editing := terminal.waitFor(t, "Inline Custom Answer 30: recommended")
	if !strings.Contains(editing, "Enter commit • Esc cancel") {
		t.Fatalf("inline editing help is missing:\n%s", editing)
	}
	terminal.send(t, " replacement")
	terminal.waitFor(t, "recommended replacement")
	terminal.send(t, "\x1b")
	screen := terminal.waitFor(t, "Inline Custom Answer cancelled")
	if !strings.Contains(screen, "> 30 │ recommended") {
		t.Fatalf("cancelling inline editing changed the Answer Slot:\n%s", screen)
	}
	if !strings.Contains(screen, "Selected Answer 30 (full):\nrecommended") {
		t.Fatalf("cancelling inline editing changed the selected-answer preview:\n%s", screen)
	}

	terminal.send(t, "q")
	terminal.waitForExit(t)
}

func TestNormalModeEscapeClearsSelectedAnswerAndPersists(t *testing.T) {
	workingDir := t.TempDir()
	terminal := startTerminal(t, workingDir, "30")
	terminal.send(t, "r")
	terminal.waitForSelection(t, 31)
	terminal.send(t, "k")
	terminal.waitForSelection(t, 30)

	terminal.send(t, "\x1b")
	screen := terminal.waitFor(t, "Answer Slot 30 cleared")
	if answer := latestRenderedAnswer(t, screen, 30); answer != "" {
		t.Fatalf("cleared Answer Slot contains %q, want empty:\n%s", answer, screen)
	}
	terminal.send(t, "q")
	terminal.waitForExit(t)

	resumed := startTerminal(t, workingDir)
	screen = resumed.waitForSelection(t, 30)
	if answer := latestRenderedAnswer(t, screen, 30); answer != "" {
		t.Fatalf("cleared Answer Slot contains %q after restart, want empty:\n%s", answer, screen)
	}
	resumed.send(t, "q")
	resumed.waitForExit(t)
}

func TestUndoWithNoCommittedMutationLeavesWorksheetUnchanged(t *testing.T) {
	terminal := startTerminal(t, t.TempDir(), "30")
	terminal.send(t, "j")
	terminal.waitForSelection(t, 31)

	terminal.send(t, "u")
	screen := terminal.waitFor(t, "Nothing to undo")
	if answer := latestRenderedAnswer(t, screen, 31); answer != "" {
		t.Fatalf("empty undo changed Answer Slot 31 to %q:\n%s", answer, screen)
	}
	if selections := selectedAnswerSlotPattern.FindAllStringSubmatch(screen, -1); selections[len(selections)-1][1] != "31" {
		t.Fatalf("empty undo moved selection away from Answer Slot 31:\n%s", screen)
	}
	terminal.send(t, "q")
	terminal.waitForExit(t)
}

func TestUndoPresetCommitAfterNavigationAndRestartRestoresAffectedSlot(t *testing.T) {
	workingDir := t.TempDir()
	terminal := startTerminal(t, workingDir, "30")
	terminal.send(t, "r")
	terminal.waitForSelection(t, 31)
	terminal.send(t, "j")
	terminal.waitForSelection(t, 32)
	terminal.send(t, "q")
	terminal.waitForExit(t)

	resumed := startTerminal(t, workingDir)
	resumed.waitForSelection(t, 32)
	assertUndoRestoresAnswer(t, resumed, 30, "")

	resumed.send(t, "u")
	resumed.waitFor(t, "Nothing to undo")
	resumed.send(t, "q")
	resumed.waitForExit(t)
}

func TestUndoInlineCommitRestoresAffectedSlot(t *testing.T) {
	terminal := startTerminal(t, t.TempDir(), "30")
	terminal.send(t, "i")
	terminal.waitFor(t, "Inline Custom Answer 30:")
	terminal.send(t, "custom answer\r")
	terminal.waitForSelection(t, 31)

	assertUndoRestoresAnswer(t, terminal, 30, "")
	terminal.send(t, "q")
	terminal.waitForExit(t)
}

func TestUndoExternalEditorCommitRestoresAffectedSlot(t *testing.T) {
	editor := writeEditorFixture(t, filepath.Join(t.TempDir(), "editor"), `#!/bin/sh
printf 'external answer' > "$1"
`)
	terminal := startTerminalWithEnvironment(t, t.TempDir(), environmentOverrides{
		values:  map[string]string{"VISUAL": editor},
		removed: []string{"EDITOR"},
	}, "30")
	terminal.send(t, "o")
	terminal.waitForSelection(t, 31)

	assertUndoRestoresAnswer(t, terminal, 30, "")
	terminal.send(t, "q")
	terminal.waitForExit(t)
}

func TestUndoClearRestoresClearedAnswer(t *testing.T) {
	terminal := startTerminal(t, t.TempDir(), "30")
	terminal.send(t, "r")
	terminal.waitForSelection(t, 31)
	terminal.send(t, "k")
	terminal.waitForSelection(t, 30)
	terminal.send(t, "\x1b")
	terminal.waitFor(t, "Answer Slot 30 cleared")

	assertUndoRestoresAnswer(t, terminal, 30, "recommended")
	terminal.send(t, "q")
	terminal.waitForExit(t)
}

func TestClearingEmptySlotDoesNotReplaceAvailableUndo(t *testing.T) {
	terminal := startTerminal(t, t.TempDir(), "30")
	terminal.send(t, "r")
	terminal.waitForSelection(t, 31)

	terminal.send(t, "\x1b")
	terminal.waitFor(t, "Answer Slot 31 is already empty")
	assertUndoRestoresAnswer(t, terminal, 30, "")
	terminal.send(t, "q")
	terminal.waitForExit(t)
}

func TestUndoReplacementRestoresPreviousAnswer(t *testing.T) {
	terminal := startTerminal(t, t.TempDir(), "30")
	terminal.send(t, "r")
	terminal.waitForSelection(t, 31)
	terminal.send(t, "k")
	terminal.waitForSelection(t, 30)
	terminal.send(t, "y")
	terminal.waitForSelection(t, 31)

	assertUndoRestoresAnswer(t, terminal, 30, "recommended")
	terminal.send(t, "q")
	terminal.waitForExit(t)
}

func TestUndoFinalSlotCommitRemovesAppendedSlot(t *testing.T) {
	workingDir := t.TempDir()
	terminal := startTerminal(t, workingDir, "30")
	for selected := 31; selected <= 39; selected++ {
		terminal.send(t, "j")
		terminal.waitForSelection(t, selected)
	}
	terminal.send(t, "r")
	terminal.waitForSelection(t, 40)
	terminal.waitFor(t, "11 Answer Slots")

	terminal.send(t, "u")
	terminal.waitFor(t, "Undid Answer Slot 39")
	terminal.send(t, "q")
	terminal.waitForExit(t)

	resumed := startTerminal(t, workingDir)
	screen := resumed.waitForSelection(t, 39)
	if !strings.Contains(screen, "10 Answer Slots") {
		t.Fatalf("undo did not remove the appended Answer Slot:\n%s", screen)
	}
	if answer := latestRenderedAnswer(t, screen, 39); answer != "" {
		t.Fatalf("undo restored Answer Slot 39 to %q, want empty:\n%s", answer, screen)
	}
	resumed.send(t, "u")
	resumed.waitFor(t, "Nothing to undo")
	resumed.send(t, "q")
	resumed.waitForExit(t)
}

func TestInlineCustomAnswerCommitAutoAdvancesGrowsAndSurvivesResume(t *testing.T) {
	workingDir := t.TempDir()
	terminal := startTerminal(t, workingDir, "90")
	for selected := 91; selected <= 99; selected++ {
		terminal.send(t, "j")
		terminal.waitForSelection(t, selected)
	}

	terminal.send(t, "i")
	terminal.waitFor(t, "Inline Custom Answer 99:")
	terminal.send(t, "short custom answer\r")
	screen := terminal.waitForSelection(t, 100)
	if !strings.Contains(screen, "11 Answer Slots") || !strings.Contains(screen, "  99 │ short custom answer") {
		t.Fatalf("inline commit did not grow the Worksheet with the exact answer:\n%s", screen)
	}
	terminal.send(t, "q")
	terminal.waitForExit(t)

	resumed := startTerminal(t, workingDir)
	resumed.waitForSelection(t, 100)
	resumed.send(t, "k")
	screen = resumed.waitFor(t, "Selected Answer 99 (full):\nshort custom answer")
	if !strings.Contains(screen, "> 99 │ short custom answer") {
		t.Fatalf("resumed Worksheet lost the inline Custom Answer:\n%s", screen)
	}
	resumed.send(t, "q")
	resumed.waitForExit(t)
}

func TestInlineCustomAnswerKeepsBracketedPasteOnOneLine(t *testing.T) {
	terminal := startTerminal(t, t.TempDir(), "70")
	terminal.send(t, "i")
	terminal.waitFor(t, "Inline Custom Answer 70:")
	terminal.send(t, "\x1b[200~first\nsecond\x1b[201~")
	screen := terminal.waitFor(t, "Inline Custom Answer 70: firstsecond")
	if strings.Contains(screen, "Inline Custom Answer 70: first\nsecond") {
		t.Fatalf("inline editing accepted a multiline paste:\n%s", screen)
	}
	terminal.send(t, "\r")
	screen = terminal.waitForSelection(t, 71)
	if !strings.Contains(screen, "  70 │ firstsecond") {
		t.Fatalf("inline editing did not commit the paste as one line:\n%s", screen)
	}
	terminal.send(t, "q")
	terminal.waitForExit(t)
}

func TestVisualEditorWithSpacesReceivesSeedAndCommitsMultilineCustomAnswer(t *testing.T) {
	workingDir := t.TempDir()
	fixtureDir := t.TempDir()
	seedCapture := filepath.Join(fixtureDir, "seed capture.txt")
	visual := writeEditorFixture(t, filepath.Join(fixtureDir, "visual editor with spaces"), `#!/bin/sh
cp "$1" "$SEED_CAPTURE"
printf 'first line\nsecond 界 line' > "$1"
`)
	editor := writeEditorFixture(t, filepath.Join(fixtureDir, "fallback-editor"), `#!/bin/sh
printf 'wrong editor' > "$1"
`)

	terminal := startTerminalWithEnvironment(t, workingDir, environmentOverrides{values: map[string]string{
		"VISUAL":       visual,
		"EDITOR":       editor,
		"SEED_CAPTURE": seedCapture,
	}}, "30")
	terminal.send(t, "i")
	terminal.waitFor(t, "Inline Custom Answer 30:")
	terminal.send(t, "seeded answer\r")
	terminal.waitForSelection(t, 31)
	terminal.send(t, "k")
	terminal.waitForSelection(t, 30)

	terminal.send(t, "o")
	screen := terminal.waitForSelection(t, 31)
	if !strings.Contains(screen, "  30 │ first line second 界 line") {
		t.Fatalf("external-editor answer was not compact in the grid:\n%s", screen)
	}
	seed, err := os.ReadFile(seedCapture)
	if err != nil {
		t.Fatalf("read editor seed capture: %v", err)
	}
	if string(seed) != "seeded answer" {
		t.Fatalf("editor draft seed = %q, want %q", seed, "seeded answer")
	}
	terminal.send(t, "k")
	screen = terminal.waitFor(t, "Selected Answer 30 (full):\nfirst line\nsecond 界 line")
	if strings.Contains(screen, "wrong editor") {
		t.Fatalf("$EDITOR ran even though $VISUAL was configured:\n%s", screen)
	}
	terminal.send(t, "i")
	screen = terminal.waitFor(t, "Inline Custom Answer 30: first line second 界 line")
	if strings.Contains(screen, "Inline Custom Answer 30: first line\nsecond 界 line") {
		t.Fatalf("a multiline seed made inline editing span multiple lines:\n%s", screen)
	}
	terminal.send(t, "\x1b")
	terminal.waitFor(t, "Inline Custom Answer cancelled")
	terminal.send(t, "q")
	terminal.waitForExit(t)

	resumed := startTerminal(t, workingDir)
	screen = resumed.waitFor(t, "Selected Answer 30 (full):\nfirst line\nsecond 界 line")
	if !strings.Contains(screen, "> 30 │ first line second 界 line") {
		t.Fatalf("multiline Custom Answer did not survive resume:\n%s", screen)
	}
	resumed.send(t, "q")
	resumed.waitForExit(t)
}

func TestEditorFallbackCommitsAtFinalSlotAndGrowsWorksheet(t *testing.T) {
	workingDir := t.TempDir()
	editor := writeEditorFixture(t, filepath.Join(t.TempDir(), "editor"), `#!/bin/sh
printf 'grown\nanswer' > "$1"
`)
	terminal := startTerminalWithEnvironment(t, workingDir, environmentOverrides{
		values:  map[string]string{"EDITOR": editor},
		removed: []string{"VISUAL"},
	}, "40")
	for selected := 41; selected <= 49; selected++ {
		terminal.send(t, "j")
		terminal.waitForSelection(t, selected)
	}

	terminal.send(t, "o")
	screen := terminal.waitForSelection(t, 50)
	if !strings.Contains(screen, "11 Answer Slots") || !strings.Contains(screen, "  49 │ grown answer") {
		t.Fatalf("$EDITOR fallback did not commit and grow the Worksheet:\n%s", screen)
	}
	terminal.send(t, "q")
	terminal.waitForExit(t)
}

func TestExternalEditorErrorsAreActionableAndLeaveAnswerUnchanged(t *testing.T) {
	fixtureDir := t.TempDir()
	nonzero := writeEditorFixture(t, filepath.Join(fixtureDir, "nonzero-editor"), "#!/bin/sh\nexit 23\n")
	removesDraft := writeEditorFixture(t, filepath.Join(fixtureDir, "remove-draft-editor"), "#!/bin/sh\nrm -- \"$1\"\n")
	tests := []struct {
		name   string
		env    environmentOverrides
		status string
	}{
		{
			name:   "missing configuration",
			env:    environmentOverrides{removed: []string{"VISUAL", "EDITOR"}},
			status: "set $VISUAL or $EDITOR to an executable path",
		},
		{
			name: "launch failure",
			env: environmentOverrides{
				values:  map[string]string{"VISUAL": filepath.Join(fixtureDir, "missing-editor")},
				removed: []string{"EDITOR"},
			},
			status: "External editor failed; Answer Slot unchanged",
		},
		{
			name: "non-zero exit",
			env: environmentOverrides{
				values:  map[string]string{"VISUAL": nonzero},
				removed: []string{"EDITOR"},
			},
			status: "exit status 23",
		},
		{
			name: "read failure",
			env: environmentOverrides{
				values:  map[string]string{"VISUAL": removesDraft},
				removed: []string{"EDITOR"},
			},
			status: "read saved draft",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			terminal := startTerminalWithEnvironment(t, t.TempDir(), test.env, "60")
			terminal.send(t, "r")
			terminal.waitForSelection(t, 61)
			terminal.send(t, "k")
			terminal.waitForSelection(t, 60)

			terminal.send(t, "o")
			screen := terminal.waitFor(t, test.status)
			if !strings.Contains(screen, "> 60 │ recommended") || !strings.Contains(screen, "Selected Answer 60 (full):\nrecommended") {
				t.Fatalf("editor error changed the selected Answer Slot:\n%s", screen)
			}
			terminal.send(t, "q")
			terminal.waitForExit(t)
		})
	}
}

func writeEditorFixture(t *testing.T, path, source string) string {
	t.Helper()
	if err := os.WriteFile(path, []byte(source), 0o700); err != nil {
		t.Fatalf("write editor fixture: %v", err)
	}
	return path
}

func writeConfigFixture(t *testing.T, configHome, contents string) string {
	t.Helper()
	configDirectory := filepath.Join(configHome, "grill-tui")
	if err := os.MkdirAll(configDirectory, 0o700); err != nil {
		t.Fatalf("create configuration fixture directory: %v", err)
	}
	configPath := filepath.Join(configDirectory, "config.toml")
	if err := os.WriteFile(configPath, []byte(contents), 0o600); err != nil {
		t.Fatalf("write configuration fixture: %v", err)
	}
	return configPath
}

func TestFinalPresetCommitGrowsAndScrollsWorksheetAndResumeRestoresPosition(t *testing.T) {
	workingDir := t.TempDir()
	terminal := startTerminal(t, workingDir, "1")

	for selected := 2; selected <= 11; selected++ {
		terminal.send(t, "r")
		terminal.waitForSelection(t, selected)
	}

	terminal.waitFor(t, "11 Answer Slots")
	terminal.send(t, "q")
	terminal.waitForExit(t)

	resumedAtEnd := startTerminal(t, workingDir)
	resumedAtEnd.waitForSelection(t, 11)
	screen := resumedAtEnd.waitFor(t, "Selected Answer 11 (full):")
	assertVisibleAnswerSlotRange(t, screen, 2, 11)
	for number := 2; number <= 10; number++ {
		if !strings.Contains(screen, fmt.Sprintf("  %d │ recommended", number)) {
			t.Fatalf("resumed Worksheet lost Preset Answer %d:\n%s", number, screen)
		}
	}
	if !regexp.MustCompile(`(?m)^> 11 │ $`).MatchString(screen) {
		t.Fatalf("grown Answer Slot is not selected and empty after resume:\n%s", screen)
	}

	for selected := 10; selected >= 1; selected-- {
		resumedAtEnd.send(t, "k")
		resumedAtEnd.waitForSelection(t, selected)
	}
	resumedAtEnd.send(t, "q")
	resumedAtEnd.waitForExit(t)

	resumedAtStart := startTerminal(t, workingDir)
	resumedAtStart.waitForSelection(t, 1)
	screen = resumedAtStart.waitFor(t, "Selected Answer 1 (full):")
	assertVisibleAnswerSlotRange(t, screen, 1, 10)
	for number := 1; number <= 10; number++ {
		if !strings.Contains(screen, fmt.Sprintf("%d │ recommended", number)) {
			t.Fatalf("resumed Worksheet lost Preset Answer %d:\n%s", number, screen)
		}
	}
	for selected := 2; selected <= 11; selected++ {
		resumedAtStart.send(t, "j")
		resumedAtStart.waitForSelection(t, selected)
	}
	resumedAtStart.send(t, "q")
	resumedAtStart.waitForExit(t)

	resumedAfterNavigation := startTerminal(t, workingDir)
	resumedAfterNavigation.waitForSelection(t, 11)
	screen = resumedAfterNavigation.waitFor(t, "Selected Answer 11 (full):")
	if !strings.Contains(screen, "11 Answer Slots") {
		t.Fatalf("navigation grew the Worksheet instead of preserving 11 slots:\n%s", screen)
	}
	assertVisibleAnswerSlotRange(t, screen, 2, 11)
	resumedAfterNavigation.send(t, "q")
	resumedAfterNavigation.waitForExit(t)
}

func assertVisibleAnswerSlotRange(t *testing.T, screen string, first, last int) {
	t.Helper()
	slots := answerSlotLinePattern.FindAllStringSubmatch(screen, -1)
	if len(slots) != last-first+1 || slots[0][1] != fmt.Sprintf("%d", first) || slots[len(slots)-1][1] != fmt.Sprintf("%d", last) {
		t.Fatalf("visible Answer Slots do not span %d through %d:\n%s", first, last, screen)
	}
}

func latestRenderedAnswer(t *testing.T, screen string, number int) string {
	t.Helper()
	pattern := regexp.MustCompile(fmt.Sprintf(`(?m)^[ >] %d │(.*)$`, number))
	matches := pattern.FindAllStringSubmatch(screen, -1)
	if len(matches) == 0 {
		t.Fatalf("Answer Slot %d is missing from rendered output:\n%s", number, screen)
	}
	return strings.TrimPrefix(matches[len(matches)-1][1], " ")
}

func assertUndoRestoresAnswer(t *testing.T, terminal *testTerminal, number int, wantAnswer string) {
	t.Helper()
	terminal.send(t, "u")
	screen := terminal.waitFor(t, fmt.Sprintf("Undid Answer Slot %d", number))
	if answer := latestRenderedAnswer(t, screen, number); answer != wantAnswer {
		t.Fatalf("undo restored Answer Slot %d to %q, want %q:\n%s", number, answer, wantAnswer, screen)
	}
	selections := selectedAnswerSlotPattern.FindAllStringSubmatch(screen, -1)
	if selections[len(selections)-1][1] != fmt.Sprintf("%d", number) {
		t.Fatalf("undo did not return selection to Answer Slot %d:\n%s", number, screen)
	}
}

func TestLongAnswerIsTruncatedInGridAndShownInFullPreview(t *testing.T) {
	workingDir := t.TempDir()
	longAnswer := "wide " + strings.Repeat("界", 30)
	installWorksheetFixture(t, workingDir, filepath.Join("testdata", "long-answer-worksheet.json"))

	terminal := startTerminal(t, workingDir)
	screen := terminal.waitFor(t, longAnswer)
	terminal.send(t, "q")
	terminal.waitForExit(t)

	gridAnswerPattern := regexp.MustCompile(`(?m)^> 20 │ (.*)$`)
	match := gridAnswerPattern.FindStringSubmatch(screen)
	if len(match) != 2 {
		t.Fatalf("selected Answer Slot is missing from grid:\n%s", screen)
	}
	if match[1] == longAnswer || !strings.HasSuffix(match[1], "…") {
		t.Fatalf("grid answer = %q, want a truncated value ending in an ellipsis", match[1])
	}
	if width := runewidth.StringWidth(match[1]); width > 40 {
		t.Fatalf("grid answer display width = %d, want at most 40: %q", width, match[1])
	}
	preview := "Selected Answer 20 (full):\n" + longAnswer
	if !strings.Contains(screen, preview) {
		t.Fatalf("full selected answer preview is missing; want %q:\n%s", preview, screen)
	}
}

func TestSCopiesExactAnswerListToWaylandClipboard(t *testing.T) {
	workingDir := t.TempDir()
	installCopyAnswerListFixture(t, workingDir)
	fixtureDir := t.TempDir()
	capturePath := filepath.Join(fixtureDir, "clipboard")
	writeEditorFixture(t, filepath.Join(fixtureDir, "wl-copy"), "#!/bin/sh\n/bin/cat > \"$CAPTURE\"\n")

	terminal := startTerminalWithEnvironment(t, workingDir, environmentOverrides{values: map[string]string{
		"PATH":            fixtureDir,
		"WAYLAND_DISPLAY": "wayland-test",
		"CAPTURE":         capturePath,
	}})
	copyAndExit(t, terminal, "s", "Answer List copied")

	copied, err := os.ReadFile(capturePath)
	if err != nil {
		t.Fatalf("read Wayland clipboard capture: %v", err)
	}
	want := exactAnswerListFixture
	if string(copied) != want {
		t.Fatalf("copied Answer List = %q, want %q", copied, want)
	}
}

func TestCopyWithNoAnswersLeavesClipboardUnchanged(t *testing.T) {
	fixtureDir := t.TempDir()
	capturePath := filepath.Join(fixtureDir, "clipboard")
	if err := os.WriteFile(capturePath, []byte("keep this"), 0o600); err != nil {
		t.Fatalf("seed clipboard capture: %v", err)
	}
	writeEditorFixture(t, filepath.Join(fixtureDir, "wl-copy"), "#!/bin/sh\n/bin/cat > \"$CAPTURE\"\n")

	terminal := startTerminalWithEnvironment(t, t.TempDir(), environmentOverrides{values: map[string]string{
		"PATH":            fixtureDir,
		"WAYLAND_DISPLAY": "wayland-test",
		"CAPTURE":         capturePath,
	}}, "20")
	copyAndExit(t, terminal, "s", "No answers to copy; clipboard unchanged")

	copied, err := os.ReadFile(capturePath)
	if err != nil {
		t.Fatalf("read clipboard capture: %v", err)
	}
	if string(copied) != "keep this" {
		t.Fatalf("clipboard changed to %q, want existing content preserved", copied)
	}
}

func TestCtrlSCopiesAnswerListWhenDelivered(t *testing.T) {
	fixtureDir := t.TempDir()
	capturePath := filepath.Join(fixtureDir, "clipboard")
	writeEditorFixture(t, filepath.Join(fixtureDir, "wl-copy"), "#!/bin/sh\n/bin/cat > \"$CAPTURE\"\n")
	terminal := startTerminalWithEnvironment(t, t.TempDir(), environmentOverrides{values: map[string]string{
		"PATH":            fixtureDir,
		"WAYLAND_DISPLAY": "wayland-test",
		"CAPTURE":         capturePath,
	}}, "20")
	terminal.send(t, "r")
	terminal.waitForSelection(t, 21)

	copyAndExit(t, terminal, "\x13", "Answer List copied")

	copied, err := os.ReadFile(capturePath)
	if err != nil {
		t.Fatalf("read clipboard capture: %v", err)
	}
	if string(copied) != "20. recommended" {
		t.Fatalf("copied Answer List = %q, want %q", copied, "20. recommended")
	}
}

func TestWSLClipboardTakesPrecedenceAndReceivesUTF16LEWithCRLF(t *testing.T) {
	workingDir := t.TempDir()
	installCopyAnswerListFixture(t, workingDir)
	fixtureDir := t.TempDir()
	clipCapture := filepath.Join(fixtureDir, "clip-capture")
	waylandCapture := filepath.Join(fixtureDir, "wayland-capture")
	writeEditorFixture(t, filepath.Join(fixtureDir, "clip.exe"), "#!/bin/sh\n/bin/cat > \"$CLIP_CAPTURE\"\n")
	writeEditorFixture(t, filepath.Join(fixtureDir, "wl-copy"), "#!/bin/sh\n/bin/cat > \"$WAYLAND_CAPTURE\"\n")

	terminal := startTerminalWithEnvironment(t, workingDir, environmentOverrides{values: map[string]string{
		"PATH":            fixtureDir,
		"WSL_DISTRO_NAME": "Test Linux",
		"WAYLAND_DISPLAY": "wayland-test",
		"CLIP_CAPTURE":    clipCapture,
		"WAYLAND_CAPTURE": waylandCapture,
	}})
	copyAndExit(t, terminal, "s", "Answer List copied using clip.exe")

	copied, err := os.ReadFile(clipCapture)
	if err != nil {
		t.Fatalf("read WSL clipboard capture: %v", err)
	}
	wantText := "998. alpha\r\n1000. first 界\r\n      second line"
	codeUnits := utf16.Encode([]rune(wantText))
	want := make([]byte, len(codeUnits)*2)
	for index, codeUnit := range codeUnits {
		binary.LittleEndian.PutUint16(want[index*2:], codeUnit)
	}
	if !bytes.Equal(copied, want) {
		t.Fatalf("clip.exe stdin bytes = %x, want UTF-16LE bytes %x", copied, want)
	}
	if _, err := os.Stat(waylandCapture); !os.IsNotExist(err) {
		t.Fatalf("wl-copy ran despite WSL precedence; stat error = %v", err)
	}
}

func TestClipboardFailureFallsBackToNextEnvironmentBackend(t *testing.T) {
	workingDir := t.TempDir()
	installCopyAnswerListFixture(t, workingDir)
	fixtureDir := t.TempDir()
	waylandCapture := filepath.Join(fixtureDir, "wayland-capture")
	writeEditorFixture(t, filepath.Join(fixtureDir, "clip.exe"), "#!/bin/sh\nexit 23\n")
	writeEditorFixture(t, filepath.Join(fixtureDir, "wl-copy"), "#!/bin/sh\n/bin/cat > \"$WAYLAND_CAPTURE\"\n")

	terminal := startTerminalWithEnvironment(t, workingDir, environmentOverrides{values: map[string]string{
		"PATH":            fixtureDir,
		"WSL_DISTRO_NAME": "Test Linux",
		"WAYLAND_DISPLAY": "wayland-test",
		"WAYLAND_CAPTURE": waylandCapture,
	}})
	copyAndExit(t, terminal, "s", "Answer List copied using wl-copy")

	copied, err := os.ReadFile(waylandCapture)
	if err != nil {
		t.Fatalf("read Wayland fallback capture: %v", err)
	}
	if string(copied) != exactAnswerListFixture {
		t.Fatalf("Wayland fallback received %q", copied)
	}
}

func TestClipboardLaunchFailureFallsBackToNextEnvironmentBackend(t *testing.T) {
	workingDir := t.TempDir()
	installCopyAnswerListFixture(t, workingDir)
	fixtureDir := t.TempDir()
	xclipCapture := filepath.Join(fixtureDir, "xclip-capture")
	writeEditorFixture(t, filepath.Join(fixtureDir, "wl-copy"), "not an executable format\n")
	writeEditorFixture(t, filepath.Join(fixtureDir, "xclip"), "#!/bin/sh\n/bin/cat > \"$XCLIP_CAPTURE\"\n")

	terminal := startTerminalWithEnvironment(t, workingDir, environmentOverrides{
		values: map[string]string{
			"PATH":            fixtureDir,
			"WAYLAND_DISPLAY": "wayland-test",
			"DISPLAY":         ":99",
			"XCLIP_CAPTURE":   xclipCapture,
		},
		removed: []string{"WSL_DISTRO_NAME", "WSL_INTEROP"},
	})
	copyAndExit(t, terminal, "s", "Answer List copied using xclip")

	copied, err := os.ReadFile(xclipCapture)
	if err != nil {
		t.Fatalf("read X11 fallback capture: %v", err)
	}
	if string(copied) != exactAnswerListFixture {
		t.Fatalf("xclip received %q", copied)
	}
}

func TestClipboardWriteFailureFallsBackToNextEnvironmentBackend(t *testing.T) {
	workingDir := t.TempDir()
	largeAnswer := strings.Repeat("a", 1<<20)
	directory := filepath.Join(workingDir, ".grill-tui")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatalf("create persisted Worksheet directory: %v", err)
	}
	fixture := fmt.Sprintf(`{"schema_version":1,"slots":[{"number":1,"answer":"%s"},{"number":2,"answer":""}],"selected":1,"viewport":0}`, largeAnswer)
	if err := os.WriteFile(filepath.Join(directory, "worksheet.json"), []byte(fixture), 0o600); err != nil {
		t.Fatalf("write persisted Worksheet: %v", err)
	}

	fixtureDir := t.TempDir()
	xclipCapture := filepath.Join(fixtureDir, "xclip-capture")
	writeEditorFixture(t, filepath.Join(fixtureDir, "wl-copy"), "#!/bin/sh\nexec 0<&-\n/bin/sleep 0.2\n")
	writeEditorFixture(t, filepath.Join(fixtureDir, "xclip"), "#!/bin/sh\n/bin/cat > \"$XCLIP_CAPTURE\"\n")

	terminal := startTerminalWithEnvironment(t, workingDir, environmentOverrides{
		values: map[string]string{
			"PATH":            fixtureDir,
			"WAYLAND_DISPLAY": "wayland-test",
			"DISPLAY":         ":99",
			"XCLIP_CAPTURE":   xclipCapture,
		},
		removed: []string{"WSL_DISTRO_NAME", "WSL_INTEROP"},
	})
	copyAndExit(t, terminal, "s", "Answer List copied using xclip")

	copied, err := os.ReadFile(xclipCapture)
	if err != nil {
		t.Fatalf("read X11 fallback capture: %v", err)
	}
	want := "1. " + largeAnswer
	if string(copied) != want {
		t.Fatalf("xclip received %d bytes, want %d", len(copied), len(want))
	}
}

func TestX11ClipboardPrefersXclipWithClipboardSelection(t *testing.T) {
	workingDir := t.TempDir()
	installCopyAnswerListFixture(t, workingDir)
	fixtureDir := t.TempDir()
	xclipCapture := filepath.Join(fixtureDir, "xclip-capture")
	argsCapture := filepath.Join(fixtureDir, "args-capture")
	xselCapture := filepath.Join(fixtureDir, "xsel-capture")
	writeEditorFixture(t, filepath.Join(fixtureDir, "xclip"), "#!/bin/sh\nprintf '%s' \"$*\" > \"$ARGS_CAPTURE\"\n/bin/cat > \"$XCLIP_CAPTURE\"\n")
	writeEditorFixture(t, filepath.Join(fixtureDir, "xsel"), "#!/bin/sh\n/bin/cat > \"$XSEL_CAPTURE\"\n")

	terminal := startTerminalWithEnvironment(t, workingDir, environmentOverrides{
		values: map[string]string{
			"PATH":          fixtureDir,
			"DISPLAY":       ":99",
			"XCLIP_CAPTURE": xclipCapture,
			"ARGS_CAPTURE":  argsCapture,
			"XSEL_CAPTURE":  xselCapture,
		},
		removed: []string{"WSL_DISTRO_NAME", "WSL_INTEROP", "WAYLAND_DISPLAY"},
	})
	copyAndExit(t, terminal, "s", "Answer List copied using xclip")

	copied, err := os.ReadFile(xclipCapture)
	if err != nil {
		t.Fatalf("read xclip capture: %v", err)
	}
	if string(copied) != exactAnswerListFixture {
		t.Fatalf("xclip received %q", copied)
	}
	args, err := os.ReadFile(argsCapture)
	if err != nil {
		t.Fatalf("read xclip args: %v", err)
	}
	if string(args) != "-selection clipboard" {
		t.Fatalf("xclip args = %q, want %q", args, "-selection clipboard")
	}
	if _, err := os.Stat(xselCapture); !os.IsNotExist(err) {
		t.Fatalf("xsel ran despite xclip precedence; stat error = %v", err)
	}
}

func TestX11ClipboardFallsBackFromXclipToXsel(t *testing.T) {
	workingDir := t.TempDir()
	installCopyAnswerListFixture(t, workingDir)
	fixtureDir := t.TempDir()
	xselCapture := filepath.Join(fixtureDir, "xsel-capture")
	argsCapture := filepath.Join(fixtureDir, "args-capture")
	writeEditorFixture(t, filepath.Join(fixtureDir, "xclip"), "#!/bin/sh\nexit 17\n")
	writeEditorFixture(t, filepath.Join(fixtureDir, "xsel"), "#!/bin/sh\nprintf '%s' \"$*\" > \"$ARGS_CAPTURE\"\n/bin/cat > \"$XSEL_CAPTURE\"\n")

	terminal := startTerminalWithEnvironment(t, workingDir, environmentOverrides{
		values: map[string]string{
			"PATH":         fixtureDir,
			"DISPLAY":      ":99",
			"XSEL_CAPTURE": xselCapture,
			"ARGS_CAPTURE": argsCapture,
		},
		removed: []string{"WSL_DISTRO_NAME", "WSL_INTEROP", "WAYLAND_DISPLAY"},
	})
	copyAndExit(t, terminal, "s", "Answer List copied using xsel")

	copied, err := os.ReadFile(xselCapture)
	if err != nil {
		t.Fatalf("read xsel capture: %v", err)
	}
	if string(copied) != exactAnswerListFixture {
		t.Fatalf("xsel received %q", copied)
	}
	args, err := os.ReadFile(argsCapture)
	if err != nil {
		t.Fatalf("read xsel args: %v", err)
	}
	if string(args) != "--clipboard --input" {
		t.Fatalf("xsel args = %q, want %q", args, "--clipboard --input")
	}
}

func TestClipboardFallsBackToOSC52(t *testing.T) {
	workingDir := t.TempDir()
	installCopyAnswerListFixture(t, workingDir)
	terminal := startTerminalWithEnvironment(t, workingDir, environmentOverrides{
		values: map[string]string{"PATH": t.TempDir()},
		removed: []string{
			"WSL_DISTRO_NAME", "WSL_INTEROP", "WAYLAND_DISPLAY", "DISPLAY",
		},
	})

	copyAndExit(t, terminal, "s", "Answer List copied using OSC 52")

	wantSequence := "\x1b]52;c;" + base64.StdEncoding.EncodeToString([]byte(exactAnswerListFixture)) + "\a"
	if output := terminal.output.String(); !strings.Contains(output, wantSequence) {
		t.Fatalf("terminal output does not contain exact OSC 52 sequence %q; output: %q", wantSequence, output)
	}
}

func TestWaylandClipboardTakesPrecedenceOverX11(t *testing.T) {
	workingDir := t.TempDir()
	installCopyAnswerListFixture(t, workingDir)
	fixtureDir := t.TempDir()
	waylandCapture := filepath.Join(fixtureDir, "wayland-capture")
	xclipCapture := filepath.Join(fixtureDir, "xclip-capture")
	writeEditorFixture(t, filepath.Join(fixtureDir, "wl-copy"), "#!/bin/sh\n/bin/cat > \"$WAYLAND_CAPTURE\"\n")
	writeEditorFixture(t, filepath.Join(fixtureDir, "xclip"), "#!/bin/sh\n/bin/cat > \"$XCLIP_CAPTURE\"\n")

	terminal := startTerminalWithEnvironment(t, workingDir, environmentOverrides{
		values: map[string]string{
			"PATH":            fixtureDir,
			"WAYLAND_DISPLAY": "wayland-test",
			"DISPLAY":         ":99",
			"WAYLAND_CAPTURE": waylandCapture,
			"XCLIP_CAPTURE":   xclipCapture,
		},
		removed: []string{"WSL_DISTRO_NAME", "WSL_INTEROP"},
	})
	copyAndExit(t, terminal, "s", "Answer List copied using wl-copy")

	if _, err := os.Stat(waylandCapture); err != nil {
		t.Fatalf("Wayland clipboard did not run: %v", err)
	}
	if _, err := os.Stat(xclipCapture); !os.IsNotExist(err) {
		t.Fatalf("xclip ran despite Wayland precedence; stat error = %v", err)
	}
}

func TestCopyFailureIsActionableAndLeavesWorksheetUnchanged(t *testing.T) {
	workingDir := t.TempDir()
	installCopyAnswerListFixture(t, workingDir)
	statePath := filepath.Join(workingDir, ".grill-tui", "worksheet.json")
	before, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("read Worksheet before failed copy: %v", err)
	}
	fixtureDir := t.TempDir()
	for _, executable := range []string{"wl-copy", "xclip", "xsel"} {
		writeEditorFixture(t, filepath.Join(fixtureDir, executable), "#!/bin/sh\nexit 19\n")
	}

	terminal := startTerminalWithEnvironment(t, workingDir, environmentOverrides{
		values: map[string]string{
			"PATH":            fixtureDir,
			"TERM":            "dumb",
			"WAYLAND_DISPLAY": "wayland-test",
			"DISPLAY":         ":99",
		},
		removed: []string{"WSL_DISTRO_NAME", "WSL_INTEROP"},
	})
	terminal.send(t, "s")
	screen := terminal.waitFor(t, "Could not copy Answer List: all backends failed")
	if !strings.Contains(screen, "check clipboard tools or OSC 52 support") {
		t.Fatalf("failed-copy status is not actionable:\n%s", screen)
	}
	terminal.send(t, "q")
	terminal.waitForExit(t)

	after, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("read Worksheet after failed copy: %v", err)
	}
	if !bytes.Equal(after, before) {
		t.Fatalf("failed copy changed Worksheet state\nbefore: %s\nafter: %s", before, after)
	}
}

func installCopyAnswerListFixture(t *testing.T, workingDir string) {
	t.Helper()
	installWorksheetFixture(t, workingDir, filepath.Join("testdata", "copy-answer-list-worksheet.json"))
}

func copyAndExit(t *testing.T, terminal *testTerminal, key, status string) string {
	t.Helper()
	terminal.send(t, key)
	screen := terminal.waitFor(t, status)
	terminal.send(t, "q")
	terminal.waitForExit(t)
	return screen
}

func installWorksheetFixture(t *testing.T, workingDir, fixturePath string) {
	t.Helper()
	fixture, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatalf("read Worksheet fixture: %v", err)
	}
	directory := filepath.Join(workingDir, ".grill-tui")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatalf("create persisted Worksheet directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(directory, "worksheet.json"), fixture, 0o600); err != nil {
		t.Fatalf("write persisted Worksheet: %v", err)
	}
}

type testTerminal struct {
	cmd    *exec.Cmd
	pty    *os.File
	output synchronizedBuffer
	done   chan error
}

type synchronizedBuffer struct {
	mutex  sync.Mutex
	buffer bytes.Buffer
}

func (buffer *synchronizedBuffer) Write(data []byte) (int, error) {
	buffer.mutex.Lock()
	defer buffer.mutex.Unlock()
	return buffer.buffer.Write(data)
}

func (buffer *synchronizedBuffer) String() string {
	buffer.mutex.Lock()
	defer buffer.mutex.Unlock()
	return buffer.buffer.String()
}

func (buffer *synchronizedBuffer) StringFrom(offset int) string {
	buffer.mutex.Lock()
	defer buffer.mutex.Unlock()
	contents := buffer.buffer.String()
	if offset > len(contents) {
		return ""
	}
	return contents[offset:]
}

func startTerminal(t *testing.T, workingDir string, args ...string) *testTerminal {
	return startTerminalWithEnvironment(t, workingDir, environmentOverrides{}, args...)
}

type environmentOverrides struct {
	values  map[string]string
	removed []string
}

type commandResult struct {
	stdout string
	stderr string
	err    error
}

func runCLI(t *testing.T, workingDir string, overrides environmentOverrides, args ...string) commandResult {
	t.Helper()
	command := exec.Command(grillTUIBinary, args...)
	command.Dir = workingDir
	command.Env = overriddenEnvironment(t, overrides)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	return commandResult{stdout: stdout.String(), stderr: stderr.String(), err: err}
}

func assertFileContentsAndMode(t *testing.T, path, wantContents string, wantMode os.FileMode) {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if string(contents) != wantContents {
		t.Fatalf("%s contents = %q, want %q", path, contents, wantContents)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if info.Mode().Perm() != wantMode {
		t.Fatalf("%s mode = %04o, want %04o", path, info.Mode().Perm(), wantMode)
	}
}

func startTerminalWithEnvironment(t *testing.T, workingDir string, overrides environmentOverrides, args ...string) *testTerminal {
	t.Helper()
	cmd := exec.Command(grillTUIBinary, args...)
	cmd.Dir = workingDir
	cmd.Env = overriddenEnvironment(t, overrides)

	ptyFile, err := pty.Start(cmd)
	if err != nil {
		t.Fatalf("start grill-tui in PTY: %v", err)
	}
	if err := pty.Setsize(ptyFile, &pty.Winsize{Rows: 30, Cols: 100}); err != nil {
		_ = ptyFile.Close()
		t.Fatalf("size PTY: %v", err)
	}

	terminal := &testTerminal{cmd: cmd, pty: ptyFile, done: make(chan error, 1)}
	t.Cleanup(func() {
		_ = terminal.pty.Close()
		if terminal.cmd.Process != nil {
			_ = terminal.cmd.Process.Kill()
		}
	})
	go func() {
		_, _ = io.Copy(&terminal.output, ptyFile)
	}()
	go func() { terminal.done <- cmd.Wait() }()
	deadline := time.Now().Add(5 * time.Second)
	repliedToBackgroundQuery := false
	repliedToForegroundQuery := false
	cursorReplies := 0
	for time.Now().Before(deadline) {
		output := terminal.output.String()
		if !repliedToBackgroundQuery && strings.Contains(output, "\x1b]11;?") {
			_, _ = ptyFile.WriteString("\x1b]11;rgb:0000/0000/0000\x1b\\")
			repliedToBackgroundQuery = true
		}
		if !repliedToForegroundQuery && strings.Contains(output, "\x1b]10;?") {
			_, _ = ptyFile.WriteString("\x1b]10;rgb:ffff/ffff/ffff\x1b\\")
			repliedToForegroundQuery = true
		}
		for cursorReplies < strings.Count(output, "\x1b[6n") {
			_, _ = ptyFile.WriteString("\x1b[1;1R")
			cursorReplies++
		}
		if strings.Contains(cleanTerminalOutput(output), "Grill TUI") {
			return terminal
		}
		select {
		case err := <-terminal.done:
			t.Fatalf("grill-tui exited before its initial screen: %v; output: %q", err, output)
		default:
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("grill-tui did not render its initial screen; output: %q", terminal.output.String())
	return terminal
}

func overriddenEnvironment(t *testing.T, overrides environmentOverrides) []string {
	t.Helper()
	environment := make(map[string]string)
	for _, entry := range os.Environ() {
		name, value, found := strings.Cut(entry, "=")
		if found {
			environment[name] = value
		}
	}
	for _, name := range overrides.removed {
		delete(environment, name)
	}
	for name, value := range overrides.values {
		environment[name] = value
	}
	if _, overridden := overrides.values["XDG_CONFIG_HOME"]; !overridden {
		environment["XDG_CONFIG_HOME"] = t.TempDir()
	}
	if _, overridden := overrides.values["TERM"]; !overridden {
		environment["TERM"] = "xterm-256color"
	}
	if _, overridden := overrides.values["NO_COLOR"]; !overridden {
		environment["NO_COLOR"] = "1"
	}
	configured := make([]string, 0, len(environment))
	for name, value := range environment {
		configured = append(configured, name+"="+value)
	}
	return configured
}

func startTerminalWithEnv(t *testing.T, workingDir string, environment []string, args ...string) *testTerminal {
	t.Helper()
	values := make(map[string]string, len(environment))
	for _, entry := range environment {
		name, value, found := strings.Cut(entry, "=")
		if !found {
			t.Fatalf("environment override %q is not NAME=value", entry)
		}
		values[name] = value
	}
	return startTerminalWithEnvironment(t, workingDir, environmentOverrides{values: values}, args...)
}

func (terminal *testTerminal) send(t *testing.T, input string) {
	t.Helper()
	if _, err := terminal.pty.WriteString(input); err != nil {
		t.Fatalf("send %q: %v", input, err)
	}
}

func (terminal *testTerminal) resize(t *testing.T, width, height uint16) {
	t.Helper()
	if err := pty.Setsize(terminal.pty, &pty.Winsize{Rows: height, Cols: width}); err != nil {
		t.Fatalf("resize PTY to %d×%d: %v", width, height, err)
	}
}

func (terminal *testTerminal) waitForAfter(t *testing.T, offset int, text string) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		output := cleanTerminalOutput(terminal.output.StringFrom(offset))
		if strings.Contains(output, text) {
			return output
		}
		select {
		case err := <-terminal.done:
			t.Fatalf("grill-tui exited before rendering %q: %v; output:\n%s", text, err, output)
		default:
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %q; output:\n%s", text, cleanTerminalOutput(terminal.output.StringFrom(offset)))
	return ""
}

func (terminal *testTerminal) waitFor(t *testing.T, text string) string {
	t.Helper()
	return terminal.waitForAfter(t, 0, text)
}

func (terminal *testTerminal) waitForSelection(t *testing.T, number int) string {
	t.Helper()
	want := fmt.Sprintf("%d", number)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		output := cleanTerminalOutput(terminal.output.String())
		selections := selectedAnswerSlotPattern.FindAllStringSubmatch(output, -1)
		if len(selections) > 0 && selections[len(selections)-1][1] == want {
			return output
		}
		select {
		case err := <-terminal.done:
			t.Fatalf("grill-tui exited before selecting Answer Slot %s: %v; output:\n%s", want, err, output)
		default:
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for selected Answer Slot %s; output:\n%s", want, cleanTerminalOutput(terminal.output.String()))
	return ""
}

func (terminal *testTerminal) waitForExit(t *testing.T) {
	t.Helper()
	select {
	case err := <-terminal.done:
		if err != nil {
			t.Fatalf("grill-tui exited with error: %v; output:\n%s", err, cleanTerminalOutput(terminal.output.String()))
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for grill-tui to exit; output:\n%s", cleanTerminalOutput(terminal.output.String()))
	}
}

var ansiSequence = regexp.MustCompile(`\x1b(?:\[[0-?]*[ -/]*[@-~]|\][^\x07]*(?:\x07|\x1b\\)|[()][0-2A-Z])`)
var sgrColorPattern = regexp.MustCompile(`\x1b\[(?:3[0-7]|38;)[0-9;]*m`)
var answerSlotLinePattern = regexp.MustCompile(`(?m)^[ >] ([0-9]+) │`)
var selectedAnswerSlotPattern = regexp.MustCompile(`(?m)^> ([0-9]+) │`)

func cleanTerminalOutput(output string) string {
	return ansiSequence.ReplaceAllString(strings.ReplaceAll(output, "\r", ""), "")
}
