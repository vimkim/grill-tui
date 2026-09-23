package main

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

const (
	initialAnswerSlotCount = 10
	visibleAnswerSlotCount = 10
	worksheetSchemaVersion = 1
)

var (
	errInvalidStartingNumber    = errors.New("starting number must be a positive integer")
	errStartingNumberTooLarge   = errors.New("starting number is too large for ten consecutive Answer Slots")
	errAnswerSlotNumberTooLarge = errors.New("next Answer Slot number is too large")
)

type unsupportedSchemaVersionError struct {
	found int
}

func (versionError unsupportedSchemaVersionError) Error() string {
	return fmt.Sprintf("unsupported schema version %d; this release supports version %d", versionError.found, worksheetSchemaVersion)
}

type answerSlot struct {
	Number int    `json:"number"`
	Answer string `json:"answer"`
}

type worksheet struct {
	SchemaVersion int            `json:"schema_version"`
	Slots         []answerSlot   `json:"slots"`
	Selected      int            `json:"selected"`
	Viewport      int            `json:"viewport"`
	Undo          *worksheetUndo `json:"undo,omitempty"`
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
	storedWorksheet.revealSelectionWithin(visibleAnswerSlotCount)
}

func (storedWorksheet *worksheet) revealSelectionWithin(visibleCount int) {
	maximumViewport := max(0, len(storedWorksheet.Slots)-visibleCount)
	if storedWorksheet.Viewport > maximumViewport {
		storedWorksheet.Viewport = maximumViewport
	}
	if storedWorksheet.Selected < storedWorksheet.Viewport {
		storedWorksheet.Viewport = storedWorksheet.Selected
	}
	if storedWorksheet.Selected >= storedWorksheet.Viewport+visibleCount {
		storedWorksheet.Viewport = storedWorksheet.Selected - visibleCount + 1
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
		SchemaVersion: storedWorksheet.SchemaVersion,
		Slots:         append([]answerSlot(nil), undo.Slots...),
		Selected:      undo.Selected,
		Viewport:      undo.Viewport,
	}, number
}

func newWorksheet(start int) worksheet {
	slots := make([]answerSlot, initialAnswerSlotCount)
	for index := range slots {
		slots[index].Number = start + index
	}
	return worksheet{SchemaVersion: worksheetSchemaVersion, Slots: slots}
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

func validateWorksheet(storedWorksheet worksheet) error {
	if storedWorksheet.SchemaVersion != worksheetSchemaVersion {
		return unsupportedSchemaVersionError{found: storedWorksheet.SchemaVersion}
	}
	if err := validateWorksheetPosition(storedWorksheet.Slots, storedWorksheet.Selected, storedWorksheet.Viewport); err != nil {
		return err
	}
	if storedWorksheet.Undo != nil {
		if err := validateWorksheetPosition(storedWorksheet.Undo.Slots, storedWorksheet.Undo.Selected, storedWorksheet.Undo.Viewport); err != nil {
			return fmt.Errorf("invalid undo state: %w", err)
		}
	}
	return nil
}

func validateWorksheetPosition(slots []answerSlot, selected, viewport int) error {
	if len(slots) == 0 {
		return errors.New("Worksheet has no Answer Slots")
	}
	for index, slot := range slots {
		if slot.Number < 1 {
			return fmt.Errorf("Answer Slot %d has a non-positive number", index)
		}
		if index > 0 && slot.Number != slots[index-1].Number+1 {
			return fmt.Errorf("Answer Slot numbers are not consecutive at index %d", index)
		}
	}
	if selected < 0 || selected >= len(slots) {
		return fmt.Errorf("selected Answer Slot index %d is out of range", selected)
	}
	if viewport < 0 || viewport >= len(slots) {
		return fmt.Errorf("viewport index %d is out of range", viewport)
	}
	return nil
}
