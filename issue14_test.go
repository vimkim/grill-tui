package grilltui_test

import (
	"bytes"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"testing"
)

func TestExistingWorksheetPromptsAndDefaultsAfterLastAnsweredNumber(t *testing.T) {
	workingDir := t.TempDir()
	firstRun := startTerminal(t, workingDir, "--name", "resume", "10")
	firstRun.send(t, "r")
	firstRun.waitForSelection(t, 11)
	firstRun.send(t, "y")
	firstRun.waitForSelection(t, 12)
	firstRun.send(t, "q")
	firstRun.waitForExit(t)

	resumed := startTerminal(t, workingDir, "--name", "resume")
	prompt := resumed.waitFor(t, "Last answered: 11. Start at [12]:")
	if strings.Contains(prompt, "No. │ Answer") {
		t.Fatalf("existing Worksheet rendered before choosing a resume position:\n%s", prompt)
	}
	resumed.send(t, "\r")
	screen := resumed.waitForSelection(t, 12)
	if !strings.Contains(screen, "  10 │ recommended") || !strings.Contains(screen, "  11 │ yes") {
		t.Fatalf("resumed Worksheet did not preserve its answers:\n%s", screen)
	}
	resumed.send(t, "q")
	resumed.waitForExit(t)

	assertPublicAnswers(t, worksheetDatabasePath(workingDir, "resume"), map[int]string{
		10: "recommended",
		11: "yes",
	})
}

func TestExistingWorksheetPositionalStartSelectsAndExtendsWithoutPrompt(t *testing.T) {
	workingDir := t.TempDir()
	firstRun := startTerminal(t, workingDir, "--name", "direct", "5")
	firstRun.send(t, "r")
	firstRun.waitForSelection(t, 6)
	firstRun.send(t, "n")
	firstRun.waitForSelection(t, 7)
	firstRun.send(t, "q")
	firstRun.waitForExit(t)

	resumed := startTerminal(t, workingDir, "--name", "direct", "12")
	screen := resumed.waitForSelection(t, 12)
	if strings.Contains(screen, "Last answered:") {
		t.Fatalf("positional resume showed the resume prompt:\n%s", screen)
	}
	if !strings.Contains(screen, "  5 │ recommended") || !strings.Contains(screen, "  6 │ no") {
		t.Fatalf("positional resume did not preserve answers:\n%s", screen)
	}
	resumed.send(t, "q")
	resumed.waitForExit(t)

	database := openWorksheetDatabaseReadOnly(t, worksheetDatabasePath(workingDir, "direct"))
	var firstNumber, lastNumber int
	var lastAnswered sql.NullInt64
	if err := database.QueryRow(`
SELECT first_number, last_number, last_answered_number
  FROM worksheet_info`).Scan(&firstNumber, &lastNumber, &lastAnswered); err != nil {
		t.Fatalf("query positioned Worksheet info: %v", err)
	}
	if firstNumber != 5 || lastNumber != 14 || !lastAnswered.Valid || lastAnswered.Int64 != 6 {
		t.Fatalf("positioned Worksheet info = range %d-%d, last answered %v", firstNumber, lastNumber, lastAnswered)
	}
	assertPublicAnswers(t, worksheetDatabasePath(workingDir, "direct"), map[int]string{
		5: "recommended",
		6: "no",
	})
}

func TestDistantPositionalStartKeepsTheWorksheetDatabaseSparse(t *testing.T) {
	workingDir := t.TempDir()
	firstRun := startTerminal(t, workingDir, "--name", "sparse", "40")
	firstRun.send(t, "y")
	firstRun.waitForSelection(t, 41)
	firstRun.send(t, "q")
	firstRun.waitForExit(t)

	const distant = 100_000
	resumed := startTerminal(t, workingDir, "--name", "sparse", "100000")
	resumed.waitForSelection(t, distant)
	resumed.send(t, "q")
	resumed.waitForExit(t)

	database := openWorksheetDatabaseReadOnly(t, worksheetDatabasePath(workingDir, "sparse"))
	var firstNumber, lastNumber int
	if err := database.QueryRow(`SELECT first_number, last_number FROM worksheet_info`).Scan(&firstNumber, &lastNumber); err != nil {
		t.Fatalf("query distant Worksheet range: %v", err)
	}
	if firstNumber != 40 || lastNumber != distant {
		t.Fatalf("distant Worksheet range = %d-%d, want 40-%d", firstNumber, lastNumber, distant)
	}

	rows, err := database.Query(`
SELECT question_number, answer, is_answered
  FROM answer_slots
 WHERE question_number IN (40, 100000)
 ORDER BY question_number`)
	if err != nil {
		t.Fatalf("query distant public Answer Slots: %v", err)
	}
	defer rows.Close()
	type publicSlot struct {
		number     int
		answer     string
		isAnswered int
	}
	var slots []publicSlot
	for rows.Next() {
		var slot publicSlot
		if err := rows.Scan(&slot.number, &slot.answer, &slot.isAnswered); err != nil {
			t.Fatalf("scan distant public Answer Slot: %v", err)
		}
		slots = append(slots, slot)
	}
	if len(slots) != 2 || slots[0] != (publicSlot{number: 40, answer: "yes", isAnswered: 1}) || slots[1] != (publicSlot{number: distant}) {
		t.Fatalf("distant public Answer Slots = %#v", slots)
	}

	var pageCount, pageSize int64
	if err := database.QueryRow("PRAGMA page_count").Scan(&pageCount); err != nil {
		t.Fatalf("query Worksheet Database page count: %v", err)
	}
	if err := database.QueryRow("PRAGMA page_size").Scan(&pageSize); err != nil {
		t.Fatalf("query Worksheet Database page size: %v", err)
	}
	if databaseBytes := pageCount * pageSize; databaseBytes > 256*1024 {
		t.Fatalf("sparse Worksheet Database uses %d bytes for one answer and a distant range", databaseBytes)
	}
}

func TestEmptyExistingWorksheetDefaultsToItsFirstAnswerSlot(t *testing.T) {
	workingDir := t.TempDir()
	firstRun := startTerminal(t, workingDir, "--name", "empty-resume", "30")
	firstRun.send(t, "q")
	firstRun.waitForExit(t)

	resumed := startTerminal(t, workingDir, "--name", "empty-resume")
	resumed.waitFor(t, "Last answered: none. Start at [30]:")
	resumed.send(t, "\r")
	resumed.waitForSelection(t, 30)
	resumed.send(t, "q")
	resumed.waitForExit(t)

	database := openWorksheetDatabaseReadOnly(t, worksheetDatabasePath(workingDir, "empty-resume"))
	var lastAnswered sql.NullInt64
	if err := database.QueryRow(`SELECT last_answered_number FROM worksheet_info`).Scan(&lastAnswered); err != nil {
		t.Fatalf("query empty existing Worksheet: %v", err)
	}
	if lastAnswered.Valid {
		t.Fatalf("empty existing Worksheet reports Last Answered Number %d", lastAnswered.Int64)
	}
}

func TestOpeningExistingWorksheetAtResumePromptDoesNotChangeItsDatabase(t *testing.T) {
	workingDir := t.TempDir()
	firstRun := startTerminal(t, workingDir, "--name", "unchanged", "8")
	firstRun.send(t, "r")
	firstRun.waitForSelection(t, 9)
	firstRun.send(t, "q")
	firstRun.waitForExit(t)

	path := worksheetDatabasePath(workingDir, "unchanged")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read Worksheet before opening resume prompt: %v", err)
	}
	opened := startTerminal(t, workingDir, "--name", "unchanged")
	opened.waitFor(t, "Last answered: 8. Start at [9]:")
	opened.send(t, "q")
	opened.waitForExit(t)
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read Worksheet after closing resume prompt: %v", err)
	}
	if !bytes.Equal(after, before) {
		t.Fatal("opening and closing the resume prompt changed the Worksheet Database")
	}
}

func TestGappedWorksheetCanResumeAtAndEditAnEarlierAnswer(t *testing.T) {
	workingDir := t.TempDir()
	firstRun := startTerminal(t, workingDir, "--name", "gapped", "10")
	firstRun.send(t, "r")
	firstRun.waitForSelection(t, 11)
	firstRun.send(t, " ")
	firstRun.waitForSelection(t, 12)
	firstRun.send(t, "y")
	firstRun.waitForSelection(t, 13)
	firstRun.send(t, "q")
	firstRun.waitForExit(t)

	resumed := startTerminal(t, workingDir, "--name", "gapped")
	resumed.waitFor(t, "Last answered: 12. Start at [13]:")
	resumed.send(t, "10\r")
	screen := resumed.waitForSelection(t, 10)
	if !strings.Contains(screen, "  12 │ yes") {
		t.Fatalf("earlier resume did not preserve the later answer:\n%s", screen)
	}
	resumed.send(t, "n")
	resumed.waitForSelection(t, 11)
	resumed.send(t, "q")
	resumed.waitForExit(t)

	assertPublicAnswers(t, worksheetDatabasePath(workingDir, "gapped"), map[int]string{
		10: "no",
		12: "yes",
	})
	database := openWorksheetDatabaseReadOnly(t, worksheetDatabasePath(workingDir, "gapped"))
	var lastAnswered int
	if err := database.QueryRow(`SELECT last_answered_number FROM worksheet_info`).Scan(&lastAnswered); err != nil {
		t.Fatalf("query gapped Last Answered Number: %v", err)
	}
	if lastAnswered != 12 {
		t.Fatalf("gapped Last Answered Number = %d, want 12", lastAnswered)
	}
}

func TestPromptedResumeExpandsBothEndsWithoutLosingAnswers(t *testing.T) {
	workingDir := t.TempDir()
	firstRun := startTerminal(t, workingDir, "--name", "expand", "50")
	firstRun.send(t, "r")
	firstRun.waitForSelection(t, 51)
	firstRun.send(t, "q")
	firstRun.waitForExit(t)

	earlier := startTerminal(t, workingDir, "--name", "expand")
	earlier.waitFor(t, "Last answered: 50. Start at [51]:")
	earlier.send(t, "3\r")
	earlier.waitForSelection(t, 3)
	earlier.send(t, "q")
	earlier.waitForExit(t)

	later := startTerminal(t, workingDir, "--name", "expand")
	later.waitFor(t, "Last answered: 50. Start at [51]:")
	later.send(t, "100\r")
	later.waitForSelection(t, 100)
	later.send(t, "q")
	later.waitForExit(t)

	database := openWorksheetDatabaseReadOnly(t, worksheetDatabasePath(workingDir, "expand"))
	var firstNumber, lastNumber int
	if err := database.QueryRow(`SELECT first_number, last_number FROM worksheet_info`).Scan(&firstNumber, &lastNumber); err != nil {
		t.Fatalf("query expanded Worksheet range: %v", err)
	}
	if firstNumber != 3 || lastNumber != 100 {
		t.Fatalf("expanded Worksheet range = %d-%d, want 3-100", firstNumber, lastNumber)
	}
	assertPublicAnswers(t, worksheetDatabasePath(workingDir, "expand"), map[int]string{50: "recommended"})
}

func TestLargestAnswerSlotCanBeSelectedRenderedAndEdited(t *testing.T) {
	workingDir := t.TempDir()
	maximum := int(^uint(0) >> 1)
	firstRun := startTerminal(t, workingDir, "--name", "maximum", fmt.Sprint(maximum-9))
	firstRun.send(t, "q")
	firstRun.waitForExit(t)

	resumed := startTerminal(t, workingDir, "--name", "maximum", fmt.Sprint(maximum))
	resumed.waitForSelection(t, maximum)
	mark := len(resumed.output.String())
	resumed.send(t, "y")
	resumed.waitForAfter(t, mark, "Preset Answer committed")
	resumed.send(t, "q")
	resumed.waitForExit(t)

	assertPublicAnswers(t, worksheetDatabasePath(workingDir, "maximum"), map[int]string{maximum: "yes"})
	database := openWorksheetDatabaseReadOnly(t, worksheetDatabasePath(workingDir, "maximum"))
	var lastAnswered int
	if err := database.QueryRow(`SELECT last_answered_number FROM worksheet_info`).Scan(&lastAnswered); err != nil {
		t.Fatalf("query maximum Last Answered Number: %v", err)
	}
	if lastAnswered != maximum {
		t.Fatalf("maximum Last Answered Number = %d, want %d", lastAnswered, maximum)
	}
}
