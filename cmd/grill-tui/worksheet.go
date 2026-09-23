package main

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

const (
	initialAnswerSlotCount = 10
	visibleAnswerSlotCount = 10
	worksheetSchemaVersion = 1
)

var (
	errInvalidStartingNumber  = errors.New("starting number must be a positive integer")
	errStartingNumberTooLarge = errors.New("starting number is too large for ten consecutive Answer Slots")
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
	SchemaVersion int
	FirstNumber   int
	LastNumber    int
	Answers       map[int]string
	Selected      int
	Viewport      int
	Undo          *worksheetUndo
}

type worksheetUndo struct {
	FirstNumber int            `json:"first_number"`
	LastNumber  int            `json:"last_number"`
	Answers     map[int]string `json:"answers,omitempty"`
	Selected    int            `json:"selected_number"`
	Viewport    int            `json:"viewport_number"`
}

func (storedWorksheet worksheet) answerList() string {
	numbers := make([]int, 0, len(storedWorksheet.Answers))
	for number := range storedWorksheet.Answers {
		numbers = append(numbers, number)
	}
	slices.Sort(numbers)
	answers := make([]string, 0, len(numbers))
	for _, number := range numbers {
		answer := storedWorksheet.answer(number)
		indent := strings.Repeat(" ", len(strconv.Itoa(number))+2)
		answer = strings.ReplaceAll(answer, "\n", "\n"+indent)
		answers = append(answers, fmt.Sprintf("%d. %s", number, answer))
	}
	return strings.Join(answers, "\n")
}

func (storedWorksheet worksheet) answer(number int) string {
	return storedWorksheet.Answers[number]
}

func (storedWorksheet worksheet) slotCount() int {
	return storedWorksheet.LastNumber - storedWorksheet.FirstNumber + 1
}

func (storedWorksheet *worksheet) revealSelection() {
	storedWorksheet.revealSelectionWithin(visibleAnswerSlotCount)
}

func (storedWorksheet *worksheet) revealSelectionWithin(visibleCount int) {
	maximumViewport := max(storedWorksheet.FirstNumber, storedWorksheet.LastNumber-visibleCount+1)
	if storedWorksheet.Viewport > maximumViewport {
		storedWorksheet.Viewport = maximumViewport
	}
	if storedWorksheet.Selected < storedWorksheet.Viewport {
		storedWorksheet.Viewport = storedWorksheet.Selected
	}
	if storedWorksheet.Selected-storedWorksheet.Viewport >= visibleCount {
		storedWorksheet.Viewport = storedWorksheet.Selected - visibleCount + 1
	}
}

func (storedWorksheet worksheet) withSelection(change int) (worksheet, bool) {
	if change < storedWorksheet.FirstNumber-storedWorksheet.Selected || change > storedWorksheet.LastNumber-storedWorksheet.Selected {
		return storedWorksheet, false
	}
	selected := storedWorksheet.Selected + change
	storedWorksheet.Selected = selected
	storedWorksheet.revealSelection()
	return storedWorksheet, true
}

func (storedWorksheet worksheet) withCommittedAnswer(answer string) worksheet {
	storedWorksheet.Undo = storedWorksheet.undoSnapshot()
	storedWorksheet.Answers = cloneAnswers(storedWorksheet.Answers)
	if answer == "" {
		delete(storedWorksheet.Answers, storedWorksheet.Selected)
	} else {
		storedWorksheet.Answers[storedWorksheet.Selected] = answer
	}
	if storedWorksheet.Selected == storedWorksheet.LastNumber {
		lastNumber := storedWorksheet.LastNumber
		maxInt := int(^uint(0) >> 1)
		if lastNumber == maxInt {
			return storedWorksheet
		}
		storedWorksheet.LastNumber++
	}
	storedWorksheet.Selected++
	storedWorksheet.revealSelection()
	return storedWorksheet
}

func (storedWorksheet worksheet) withClearedAnswer() worksheet {
	storedWorksheet.Undo = storedWorksheet.undoSnapshot()
	storedWorksheet.Answers = cloneAnswers(storedWorksheet.Answers)
	delete(storedWorksheet.Answers, storedWorksheet.Selected)
	return storedWorksheet
}

func (storedWorksheet worksheet) undoSnapshot() *worksheetUndo {
	return &worksheetUndo{
		FirstNumber: storedWorksheet.FirstNumber,
		LastNumber:  storedWorksheet.LastNumber,
		Answers:     cloneAnswers(storedWorksheet.Answers),
		Selected:    storedWorksheet.Selected,
		Viewport:    storedWorksheet.Viewport,
	}
}

func (storedWorksheet worksheet) withUndo() (worksheet, int) {
	undo := storedWorksheet.Undo
	number := undo.Selected
	return worksheet{
		SchemaVersion: storedWorksheet.SchemaVersion,
		FirstNumber:   undo.FirstNumber,
		LastNumber:    undo.LastNumber,
		Answers:       cloneAnswers(undo.Answers),
		Selected:      undo.Selected,
		Viewport:      undo.Viewport,
	}, number
}

func newWorksheet(start int) worksheet {
	return worksheet{
		SchemaVersion: worksheetSchemaVersion,
		FirstNumber:   start,
		LastNumber:    start + initialAnswerSlotCount - 1,
		Answers:       make(map[int]string),
		Selected:      start,
		Viewport:      start,
	}
}

func (storedWorksheet worksheet) lastAnsweredNumber() (int, bool) {
	last := 0
	for number, answer := range storedWorksheet.Answers {
		if answer != "" && number > last {
			last = number
		}
	}
	return last, last != 0
}

func (storedWorksheet worksheet) defaultResumeNumber() int {
	if lastAnswered, ok := storedWorksheet.lastAnsweredNumber(); ok {
		if lastAnswered < int(^uint(0)>>1) {
			return lastAnswered + 1
		}
		return lastAnswered
	}
	return storedWorksheet.FirstNumber
}

func (storedWorksheet worksheet) expandAndSelect(number int) worksheet {
	if number < storedWorksheet.FirstNumber {
		storedWorksheet.FirstNumber = number
	} else if number > storedWorksheet.LastNumber {
		storedWorksheet.LastNumber = number
	}
	storedWorksheet.Selected = number
	storedWorksheet.Viewport = storedWorksheet.Selected
	storedWorksheet.revealSelection()
	return storedWorksheet
}

func parsePositiveNumber(input string) (int, error) {
	number, err := strconv.Atoi(input)
	if err != nil || number < 1 {
		return 0, errInvalidStartingNumber
	}
	return number, nil
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
	if err := validateWorksheetPosition(storedWorksheet.FirstNumber, storedWorksheet.LastNumber, storedWorksheet.Answers, storedWorksheet.Selected, storedWorksheet.Viewport); err != nil {
		return err
	}
	if storedWorksheet.Undo != nil {
		if err := validateWorksheetPosition(storedWorksheet.Undo.FirstNumber, storedWorksheet.Undo.LastNumber, storedWorksheet.Undo.Answers, storedWorksheet.Undo.Selected, storedWorksheet.Undo.Viewport); err != nil {
			return fmt.Errorf("invalid undo state: %w", err)
		}
	}
	return nil
}

func validateWorksheetPosition(first, last int, answers map[int]string, selected, viewport int) error {
	if first < 1 || last < first {
		return fmt.Errorf("Worksheet has invalid Answer Slot range %d-%d", first, last)
	}
	if selected < first || selected > last {
		return fmt.Errorf("selected Answer Slot %d is out of range", selected)
	}
	if viewport < first || viewport > last {
		return fmt.Errorf("viewport Answer Slot %d is out of range", viewport)
	}
	for number, answer := range answers {
		if number < first || number > last {
			return fmt.Errorf("answered Answer Slot %d is out of range", number)
		}
		if answer == "" {
			return fmt.Errorf("Answer Slot %d stores an empty answer", number)
		}
	}
	return nil
}

func cloneAnswers(answers map[int]string) map[int]string {
	cloned := make(map[int]string, len(answers))
	for number, answer := range answers {
		cloned[number] = answer
	}
	return cloned
}
