package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
)

const initialAnswerSlotCount = 10

var (
	errInvalidStartingNumber  = errors.New("starting number must be a positive integer")
	errStartingNumberTooLarge = errors.New("starting number is too large for ten consecutive Answer Slots")
)

type answerSlot struct {
	Number int    `json:"number"`
	Answer string `json:"answer"`
}

type worksheet struct {
	Slots []answerSlot `json:"slots"`
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
