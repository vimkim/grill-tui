package grilltui_test

import (
	"database/sql"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestJumpLastAnsweredUsesHighestNonEmptySlotOrFirstSlot(t *testing.T) {
	workingDir := t.TempDir()
	terminal := startTerminal(t, workingDir, "20")
	terminal.send(t, "r")
	terminal.waitForSelection(t, 21)
	terminal.send(t, " ")
	terminal.waitForSelection(t, 22)
	terminal.send(t, "y")
	terminal.waitForSelection(t, 23)
	for selected := 24; selected <= 29; selected++ {
		terminal.send(t, "j")
		terminal.waitForSelection(t, selected)
	}
	want := observeWorksheet(t, workingDir, "untitled")
	terminal.send(t, "G")
	screen := terminal.waitForSelection(t, 22)
	if !strings.Contains(screen, "Status: Selection moved") {
		t.Fatalf("G did not visibly report navigation:\n%s", screen)
	}
	if got := observeWorksheet(t, workingDir, "untitled"); !reflect.DeepEqual(got, want) {
		t.Fatalf("G altered public Answer Slot state: got %#v, want %#v", got, want)
	}
	terminal.send(t, "q")
	terminal.waitForExit(t)

	empty := startTerminal(t, t.TempDir(), "40")
	empty.send(t, "j")
	empty.waitForSelection(t, 41)
	empty.send(t, "G")
	empty.waitForSelection(t, 40)
	empty.send(t, "q")
	empty.waitForExit(t)
}

func TestNavigationAliasesMoveSelectionWithoutAlteringAnswers(t *testing.T) {
	workingDir := t.TempDir()
	terminal := startTerminal(t, workingDir, "20")
	terminal.send(t, "r")
	terminal.waitForSelection(t, 21)
	want := observeWorksheet(t, workingDir, "untitled")
	for _, keys := range []struct {
		down string
		up   string
	}{
		{down: "\x1b[B", up: "\x1b[A"},
		{down: "j", up: "k"},
		{down: "\x0e", up: "\x10"},
	} {
		terminal.send(t, keys.down)
		screen := terminal.waitForSelection(t, 22)
		if !strings.Contains(screen, "Status: Selection moved") {
			t.Fatalf("down navigation did not report visible status:\n%s", screen)
		}
		terminal.send(t, keys.up)
		terminal.waitForSelection(t, 21)
	}
	if got := observeWorksheet(t, workingDir, "untitled"); !reflect.DeepEqual(got, want) {
		t.Fatalf("navigation altered public Answer Slot state: got %#v, want %#v", got, want)
	}
	terminal.send(t, "q")
	terminal.waitForExit(t)
}

func TestRebaseMovesEverySlotAndAnswerAndUndoRestoresTheWholeState(t *testing.T) {
	workingDir := t.TempDir()
	terminal := startTerminal(t, workingDir, "10")
	terminal.send(t, "r")
	terminal.waitForSelection(t, 11)
	terminal.send(t, " ")
	terminal.waitForSelection(t, 12)
	terminal.send(t, "y")
	terminal.waitForSelection(t, 13)

	terminal.send(t, "\x1b[D")
	terminal.waitForSelection(t, 12)
	assertObservedWorksheet(t, observeWorksheet(t, workingDir, "untitled"), 9, 18, 11, "9=recommended,11=yes")

	terminal.send(t, "u")
	terminal.waitForSelection(t, 13)
	assertObservedWorksheet(t, observeWorksheet(t, workingDir, "untitled"), 10, 19, 12, "10=recommended,12=yes")

	terminal.send(t, "\x1b[C")
	terminal.waitForSelection(t, 14)
	terminal.send(t, "q")
	terminal.waitForExit(t)
	assertObservedWorksheet(t, observeWorksheet(t, workingDir, "untitled"), 11, 20, 13, "11=recommended,13=yes")

	restarted := startTerminal(t, workingDir, "14")
	restarted.waitForSelection(t, 14)
	restarted.send(t, "q")
	restarted.waitForExit(t)
}

func TestRebaseRejectsUnderflowAndOverflowWithoutConsumingUndo(t *testing.T) {
	underflowDir := t.TempDir()
	underflow := startTerminal(t, underflowDir, "1")
	underflow.send(t, "r")
	underflow.waitForSelection(t, 2)
	underflow.send(t, "gg")
	underflow.waitForSelection(t, 1)
	want := observeWorksheet(t, underflowDir, "untitled")
	underflow.send(t, "\x1b[D")
	underflow.waitFor(t, "cannot rebase below 1")
	if got := observeWorksheet(t, underflowDir, "untitled"); !reflect.DeepEqual(got, want) {
		t.Fatalf("rejected underflow mutated public Worksheet state:\n got: %#v\nwant: %#v", got, want)
	}
	underflow.send(t, "u")
	underflow.waitFor(t, "Undid Answer Slot 1")
	assertObservedWorksheet(t, observeWorksheet(t, underflowDir, "untitled"), 1, 10, 0, "")
	underflow.send(t, "q")
	underflow.waitForExit(t)

	maximum := int(^uint(0) >> 1)
	overflowDir := t.TempDir()
	overflow := startTerminal(t, overflowDir, strconv.Itoa(maximum-9))
	want = observeWorksheet(t, overflowDir, "untitled")
	overflow.send(t, "\x1b[C")
	overflow.waitFor(t, "cannot rebase beyond")
	if got := observeWorksheet(t, overflowDir, "untitled"); !reflect.DeepEqual(got, want) {
		t.Fatalf("rejected overflow mutated public Worksheet state:\n got: %#v\nwant: %#v", got, want)
	}
	overflow.send(t, "q")
	overflow.waitForExit(t)
}

func TestInsertAboveAndBelowShiftAnswersAndUndoRestoresTheRange(t *testing.T) {
	workingDir := t.TempDir()
	terminal := startTerminal(t, workingDir, "20")
	for _, key := range []string{"r", "y", "n"} {
		terminal.send(t, key)
	}
	terminal.waitForSelection(t, 23)
	terminal.send(t, "k")
	terminal.waitForSelection(t, 22)

	terminal.send(t, "O")
	terminal.waitFor(t, "Inserted blank Answer Slot above 22")
	assertObservedWorksheet(t, observeWorksheet(t, workingDir, "untitled"), 20, 30, 23, "20=recommended,21=yes,23=no")
	terminal.send(t, "u")
	terminal.waitFor(t, "Undid Answer Slot 22")
	assertObservedWorksheet(t, observeWorksheet(t, workingDir, "untitled"), 20, 29, 22, "20=recommended,21=yes,22=no")

	mark := len(terminal.output.String())
	terminal.send(t, "o")
	terminal.waitForAfter(t, mark, "Inserted blank Answer Slot below 22")
	assertObservedWorksheet(t, observeWorksheet(t, workingDir, "untitled"), 20, 30, 22, "20=recommended,21=yes,22=no")
	mark = len(terminal.output.String())
	terminal.send(t, "u")
	terminal.waitForAfter(t, mark, "Undid Answer Slot 22")
	assertObservedWorksheet(t, observeWorksheet(t, workingDir, "untitled"), 20, 29, 22, "20=recommended,21=yes,22=no")
	terminal.send(t, "q")
	terminal.waitForExit(t)
}

func TestDeleteShiftsLaterAnswersAndUndoRestoresTheDeletedSlot(t *testing.T) {
	workingDir := t.TempDir()
	terminal := startTerminal(t, workingDir, "20")
	for _, key := range []string{"r", "y", "n"} {
		terminal.send(t, key)
	}
	terminal.waitForSelection(t, 23)
	terminal.send(t, "k")
	terminal.waitForSelection(t, 22)

	terminal.send(t, "D")
	terminal.waitFor(t, "Deleted Answer Slot 22")
	assertObservedWorksheet(t, observeWorksheet(t, workingDir, "untitled"), 20, 28, 21, "20=recommended,21=yes")
	terminal.send(t, "u")
	terminal.waitFor(t, "Undid Answer Slot 22")
	assertObservedWorksheet(t, observeWorksheet(t, workingDir, "untitled"), 20, 29, 22, "20=recommended,21=yes,22=no")
	terminal.send(t, "q")
	terminal.waitForExit(t)
}

func TestDeleteRefusesTheOnlySlotAndInsertRefusesIntegerOverflow(t *testing.T) {
	oneDir := t.TempDir()
	one := startTerminal(t, oneDir, "5")
	for last := 13; last >= 5; last-- {
		one.send(t, "D")
		one.waitFor(t, fmt.Sprintf("Deleted Answer Slot 5; range is now 5-%d", last))
	}
	want := observeWorksheet(t, oneDir, "untitled")
	one.send(t, "D")
	one.waitFor(t, "cannot delete the only Answer Slot")
	if got := observeWorksheet(t, oneDir, "untitled"); !reflect.DeepEqual(got, want) {
		t.Fatalf("refused deletion mutated public Worksheet state: got %#v, want %#v", got, want)
	}
	one.send(t, "q")
	one.waitForExit(t)

	maximum := int(^uint(0) >> 1)
	overflowDir := t.TempDir()
	overflow := startTerminal(t, overflowDir, strconv.Itoa(maximum-9))
	want = observeWorksheet(t, overflowDir, "untitled")
	overflow.send(t, "O")
	overflow.waitFor(t, "cannot insert beyond")
	if got := observeWorksheet(t, overflowDir, "untitled"); !reflect.DeepEqual(got, want) {
		t.Fatalf("refused insertion mutated public Worksheet state: got %#v, want %#v", got, want)
	}
	overflow.send(t, "q")
	overflow.waitForExit(t)
}

func TestBackspaceAndDeleteClearAnswersWithoutDeletingOrMovingSlots(t *testing.T) {
	workingDir := t.TempDir()
	terminal := startTerminal(t, workingDir, "30")
	terminal.send(t, "r")
	terminal.waitForSelection(t, 31)
	terminal.send(t, "k")
	terminal.waitForSelection(t, 30)

	terminal.send(t, "\x7f")
	terminal.waitFor(t, "Answer Slot 30 cleared")
	assertObservedWorksheet(t, observeWorksheet(t, workingDir, "untitled"), 30, 39, 0, "")
	terminal.send(t, "u")
	terminal.waitFor(t, "Undid Answer Slot 30")
	assertObservedWorksheet(t, observeWorksheet(t, workingDir, "untitled"), 30, 39, 30, "30=recommended")

	mark := len(terminal.output.String())
	terminal.send(t, "\x1b[3~")
	terminal.waitForAfter(t, mark, "Answer Slot 30 cleared")
	terminal.send(t, "q")
	terminal.waitForExit(t)
	assertObservedWorksheet(t, observeWorksheet(t, workingDir, "untitled"), 30, 39, 0, "")
}

func TestSpaceClearsAndAdvancesAndAppendsAtTheFinalSlot(t *testing.T) {
	workingDir := t.TempDir()
	terminal := startTerminal(t, workingDir, "8")
	terminal.send(t, "r")
	terminal.waitForSelection(t, 9)
	terminal.send(t, "k")
	terminal.waitForSelection(t, 8)
	terminal.send(t, " ")
	terminal.waitForSelection(t, 9)
	assertObservedWorksheet(t, observeWorksheet(t, workingDir, "untitled"), 8, 17, 0, "")

	for number := 10; number <= 17; number++ {
		terminal.send(t, "j")
		terminal.waitForSelection(t, number)
	}
	terminal.send(t, " ")
	terminal.waitForSelection(t, 18)
	assertObservedWorksheet(t, observeWorksheet(t, workingDir, "untitled"), 8, 18, 0, "")
	terminal.send(t, "u")
	terminal.waitFor(t, "Undid Answer Slot 17")
	assertObservedWorksheet(t, observeWorksheet(t, workingDir, "untitled"), 8, 17, 0, "")
	terminal.send(t, "q")
	terminal.waitForExit(t)
}

func TestNoOpSpaceAndEscapePreserveThePreviousUndoSnapshot(t *testing.T) {
	terminal := startTerminal(t, t.TempDir(), "30")
	terminal.send(t, "r")
	terminal.waitForSelection(t, 31)
	terminal.send(t, " ")
	terminal.waitForSelection(t, 32)
	terminal.send(t, "\x1b")
	time.Sleep(100 * time.Millisecond)
	terminal.send(t, "u")
	terminal.waitFor(t, "Undid Answer Slot 30")
	screen := terminal.waitForSelection(t, 30)
	if answer := latestRenderedAnswer(t, screen, 30); answer != "" {
		t.Fatalf("no-op Space or Esc replaced undo; Answer Slot 30 = %q:\n%s", answer, screen)
	}
	terminal.send(t, "q")
	terminal.waitForExit(t)
}

func TestFinalPresetMapCommitsOnlyTheSpecifiedAnswers(t *testing.T) {
	presets := []struct {
		key    string
		answer string
	}{
		{"r", "recommended"}, {"R", "recommended"}, {"y", "yes"}, {"n", "no"}, {"N", "no"},
		{"1", "1"}, {"2", "2"}, {"3", "3"}, {"4", "4"}, {"5", "5"},
		{"a", "a"}, {"b", "b"}, {"c", "c"}, {"d", "d"}, {"e", "e"}, {"A", "A"}, {"B", "B"},
		{"x", "explain further"},
	}
	for _, preset := range presets {
		t.Run(preset.key, func(t *testing.T) {
			terminal := startTerminal(t, t.TempDir(), "40")
			terminal.send(t, preset.key)
			screen := terminal.waitForSelection(t, 41)
			if answer := latestRenderedAnswer(t, screen, 40); answer != preset.answer {
				t.Fatalf("preset %q committed %q, want %q:\n%s", preset.key, answer, preset.answer, screen)
			}
			terminal.send(t, "q")
			terminal.waitForExit(t)
		})
	}
}

func TestReservedUppercaseKeysDoNotCommitPresetAnswers(t *testing.T) {
	for _, key := range []string{"C", "E", "Y"} {
		t.Run(key, func(t *testing.T) {
			workingDir := t.TempDir()
			terminal := startTerminalWithEnvironment(t, workingDir, environmentOverrides{removed: []string{"VISUAL", "EDITOR"}}, "40")
			terminal.send(t, key)
			if key == "E" {
				terminal.waitFor(t, "Could not open external editor")
			}
			terminal.send(t, "q")
			terminal.waitForExit(t)
			assertObservedWorksheet(t, observeWorksheet(t, workingDir, "untitled"), 40, 49, 0, "")
		})
	}
}

type observedSlot struct {
	number     int
	answer     string
	isAnswered int
}

type observedWorksheet struct {
	first        int
	last         int
	lastAnswered sql.NullInt64
	slots        []observedSlot
}

func observeWorksheet(t *testing.T, workingDir, name string) observedWorksheet {
	t.Helper()
	database := openWorksheetDatabaseReadOnly(t, worksheetDatabasePath(workingDir, name))
	defer database.Close()

	var observed observedWorksheet
	if err := database.QueryRow(`SELECT first_number, last_number, last_answered_number FROM worksheet_info`).Scan(&observed.first, &observed.last, &observed.lastAnswered); err != nil {
		t.Fatalf("query public Worksheet metadata: %v", err)
	}
	rows, err := database.Query(`SELECT question_number, answer, is_answered FROM answer_slots ORDER BY question_number`)
	if err != nil {
		t.Fatalf("query public Answer Slots: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var slot observedSlot
		if err := rows.Scan(&slot.number, &slot.answer, &slot.isAnswered); err != nil {
			t.Fatalf("scan public Answer Slot: %v", err)
		}
		observed.slots = append(observed.slots, slot)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read public Answer Slots: %v", err)
	}
	return observed
}

func formatObservedSlots(slots []observedSlot) string {
	var rendered []string
	for _, slot := range slots {
		if slot.isAnswered != 0 {
			rendered = append(rendered, fmt.Sprintf("%d=%s", slot.number, slot.answer))
		}
	}
	return strings.Join(rendered, ",")
}

func assertObservedWorksheet(t *testing.T, got observedWorksheet, first, last int, lastAnswered int64, answered string) {
	t.Helper()
	if got.first != first || got.last != last {
		t.Fatalf("public Worksheet range = %d-%d, want %d-%d", got.first, got.last, first, last)
	}
	if lastAnswered == 0 {
		if got.lastAnswered.Valid {
			t.Fatalf("Last Answered Number = %d, want NULL", got.lastAnswered.Int64)
		}
	} else if !got.lastAnswered.Valid || got.lastAnswered.Int64 != lastAnswered {
		t.Fatalf("Last Answered Number = %v, want %d", got.lastAnswered, lastAnswered)
	}
	if gotAnswers := formatObservedSlots(got.slots); gotAnswers != answered {
		t.Fatalf("public answered slots = %q, want %q", gotAnswers, answered)
	}
}
