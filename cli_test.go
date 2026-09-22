package grilltui_test

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf16"

	"github.com/creack/pty"
	"github.com/mattn/go-runewidth"
)

var grillTUIBinary string

const exactAnswerListFixture = "998. alpha\n1000. first 界\n      second line"

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
	fixture := fmt.Sprintf(`{"slots":[{"number":1,"answer":"%s"}],"selected":0,"viewport":0}`, largeAnswer)
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

func startTerminal(t *testing.T, workingDir string, args ...string) *testTerminal {
	return startTerminalWithEnvironment(t, workingDir, environmentOverrides{}, args...)
}

type environmentOverrides struct {
	values  map[string]string
	removed []string
}

func startTerminalWithEnvironment(t *testing.T, workingDir string, overrides environmentOverrides, args ...string) *testTerminal {
	t.Helper()
	cmd := exec.Command(grillTUIBinary, args...)
	cmd.Dir = workingDir
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
	if _, overridden := overrides.values["TERM"]; !overridden {
		environment["TERM"] = "xterm-256color"
	}
	environment["NO_COLOR"] = "1"
	cmd.Env = make([]string, 0, len(environment))
	for name, value := range environment {
		cmd.Env = append(cmd.Env, name+"="+value)
	}

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

func (terminal *testTerminal) send(t *testing.T, input string) {
	t.Helper()
	if _, err := terminal.pty.WriteString(input); err != nil {
		t.Fatalf("send %q: %v", input, err)
	}
}

func (terminal *testTerminal) waitFor(t *testing.T, text string) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		output := cleanTerminalOutput(terminal.output.String())
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
	t.Fatalf("timed out waiting for %q; output:\n%s", text, cleanTerminalOutput(terminal.output.String()))
	return ""
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
var answerSlotLinePattern = regexp.MustCompile(`(?m)^[ >] ([0-9]+) │`)
var selectedAnswerSlotPattern = regexp.MustCompile(`(?m)^> ([0-9]+) │`)

func cleanTerminalOutput(output string) string {
	return ansiSequence.ReplaceAllString(strings.ReplaceAll(output, "\r", ""), "")
}
