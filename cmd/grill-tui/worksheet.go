package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	initialAnswerSlotCount = 10
	visibleAnswerSlotCount = 10
)

var (
	errInvalidStartingNumber    = errors.New("starting number must be a positive integer")
	errStartingNumberTooLarge   = errors.New("starting number is too large for ten consecutive Answer Slots")
	errAnswerSlotNumberTooLarge = errors.New("next Answer Slot number is too large")
)

type answerSlot struct {
	Number int    `json:"number"`
	Answer string `json:"answer"`
}

type worksheet struct {
	Slots    []answerSlot   `json:"slots"`
	Selected int            `json:"selected"`
	Viewport int            `json:"viewport"`
	Undo     *worksheetUndo `json:"undo,omitempty"`
}

type worksheetUndo struct {
	Slots    []answerSlot `json:"slots"`
	Selected int          `json:"selected"`
	Viewport int          `json:"viewport"`
}

func (storedWorksheet worksheet) answerList() string {
	var answers []string
	for _, slot := range storedWorksheet.Slots {
		if slot.Answer == "" {
			continue
		}
		indent := strings.Repeat(" ", len(strconv.Itoa(slot.Number))+2)
		answer := strings.ReplaceAll(slot.Answer, "\n", "\n"+indent)
		answers = append(answers, fmt.Sprintf("%d. %s", slot.Number, answer))
	}
	return strings.Join(answers, "\n")
}

func (storedWorksheet *worksheet) revealSelection() {
	if storedWorksheet.Selected < storedWorksheet.Viewport {
		storedWorksheet.Viewport = storedWorksheet.Selected
	}
	if storedWorksheet.Selected >= storedWorksheet.Viewport+visibleAnswerSlotCount {
		storedWorksheet.Viewport = storedWorksheet.Selected - visibleAnswerSlotCount + 1
	}
}

func (storedWorksheet worksheet) withSelection(change int) (worksheet, bool) {
	selected := storedWorksheet.Selected + change
	if selected < 0 || selected >= len(storedWorksheet.Slots) {
		return storedWorksheet, false
	}
	storedWorksheet.Selected = selected
	storedWorksheet.revealSelection()
	return storedWorksheet, true
}

func (storedWorksheet worksheet) withCommittedAnswer(answer string) (worksheet, error) {
	storedWorksheet.Undo = storedWorksheet.undoSnapshot()
	storedWorksheet.Slots = append([]answerSlot(nil), storedWorksheet.Slots...)
	storedWorksheet.Slots[storedWorksheet.Selected].Answer = answer
	if storedWorksheet.Selected == len(storedWorksheet.Slots)-1 {
		lastNumber := storedWorksheet.Slots[storedWorksheet.Selected].Number
		maxInt := int(^uint(0) >> 1)
		if lastNumber == maxInt {
			return worksheet{}, errAnswerSlotNumberTooLarge
		}
		storedWorksheet.Slots = append(storedWorksheet.Slots, answerSlot{Number: lastNumber + 1})
	}
	storedWorksheet.Selected++
	storedWorksheet.revealSelection()
	return storedWorksheet, nil
}

func (storedWorksheet worksheet) withClearedAnswer() worksheet {
	storedWorksheet.Undo = storedWorksheet.undoSnapshot()
	storedWorksheet.Slots = append([]answerSlot(nil), storedWorksheet.Slots...)
	storedWorksheet.Slots[storedWorksheet.Selected].Answer = ""
	return storedWorksheet
}

func (storedWorksheet worksheet) undoSnapshot() *worksheetUndo {
	return &worksheetUndo{
		Slots:    append([]answerSlot(nil), storedWorksheet.Slots...),
		Selected: storedWorksheet.Selected,
		Viewport: storedWorksheet.Viewport,
	}
}

func (storedWorksheet worksheet) withUndo() (worksheet, int) {
	undo := storedWorksheet.Undo
	number := undo.Slots[undo.Selected].Number
	return worksheet{
		Slots:    append([]answerSlot(nil), undo.Slots...),
		Selected: undo.Selected,
		Viewport: undo.Viewport,
	}, number
}

func newWorksheet(start int) worksheet {
	slots := make([]answerSlot, initialAnswerSlotCount)
	for index := range slots {
		slots[index].Number = start + index
	}
	return worksheet{Slots: slots}
}

func parseStartingNumber(input string) (int, error) {
	start, err := strconv.Atoi(input)
	if err != nil {
		var numberError *strconv.NumError
		if errors.As(err, &numberError) && errors.Is(numberError.Err, strconv.ErrRange) && len(input) > 0 && input[0] != '-' {
			return 0, errStartingNumberTooLarge
		}
		return 0, errInvalidStartingNumber
	}
	if start < 1 {
		return 0, errInvalidStartingNumber
	}
	maxInt := int(^uint(0) >> 1)
	if start > maxInt-(initialAnswerSlotCount-1) {
		return 0, errStartingNumberTooLarge
	}
	return start, nil
}

func loadWorksheet() (worksheet, error) {
	path := filepath.Join(stateDirectory, "worksheet.json")
	encoded, err := os.ReadFile(path)
	if err != nil {
		return worksheet{}, fmt.Errorf("read Worksheet: %w", err)
	}
	var storedWorksheet worksheet
	if err := json.Unmarshal(encoded, &storedWorksheet); err != nil {
		return worksheet{}, fmt.Errorf("decode Worksheet: %w", err)
	}
	return storedWorksheet, nil
}

func saveWorksheet(storedWorksheet worksheet) error {
	directory := filepath.Clean(stateDirectory)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create Worksheet directory: %w", err)
	}
	encoded, err := json.MarshalIndent(storedWorksheet, "", "  ")
	if err != nil {
		return fmt.Errorf("encode Worksheet: %w", err)
	}
	encoded = append(encoded, '\n')
	if err := os.WriteFile(filepath.Join(directory, "worksheet.json"), encoded, 0o600); err != nil {
		return fmt.Errorf("save Worksheet: %w", err)
	}
	return nil
}
