package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	initialAnswerSlotCount = 10
	visibleAnswerSlotCount = 10
	worksheetSchemaVersion = 1
	primaryStateFile       = "worksheet.json"
	backupStateFile        = "worksheet.backup.json"
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

func loadWorksheet() (worksheet, string, error) {
	primaryPath := filepath.Join(stateDirectory, primaryStateFile)
	backupPath := filepath.Join(stateDirectory, backupStateFile)
	primary, err := os.ReadFile(primaryPath)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return worksheet{}, "", fmt.Errorf("read Worksheet: %w", err)
		}
		backup, backupErr := os.ReadFile(backupPath)
		if errors.Is(backupErr, os.ErrNotExist) {
			return worksheet{}, "", fmt.Errorf("read Worksheet: %w", err)
		}
		if backupErr != nil {
			return worksheet{}, "", fmt.Errorf("primary state is missing and backup recovery is impossible: %w", backupErr)
		}
		if err := protectStateArtifact(backupPath); err != nil {
			return worksheet{}, "", err
		}
		storedWorksheet, decodeErr := decodeWorksheet(backup)
		if decodeErr != nil {
			return worksheet{}, "", fmt.Errorf("primary state is missing and safe recovery is impossible; %s was preserved; repair or restore it manually (backup is invalid: %w)", backupPath, decodeErr)
		}
		if err := atomicWrite(primaryPath, backup); err != nil {
			return worksheet{}, "", fmt.Errorf("restore missing primary state from backup: %w", err)
		}
		return storedWorksheet, "Recovered missing primary state from backup", nil
	}
	if err := protectStateArtifact(primaryPath); err != nil {
		return worksheet{}, "", err
	}
	if err := protectStateArtifact(backupPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return worksheet{}, "", err
	}

	storedWorksheet, decodeErr := decodeWorksheet(primary)
	if decodeErr == nil {
		if err := validateExistingBackup(backupPath); err != nil {
			return worksheet{}, "", err
		}
		return storedWorksheet, "", nil
	}
	var versionError unsupportedSchemaVersionError
	if errors.As(decodeErr, &versionError) {
		return worksheet{}, "", fmt.Errorf("refuse to open Worksheet: %w; no migration was attempted", decodeErr)
	}

	backup, backupErr := os.ReadFile(backupPath)
	if backupErr != nil {
		return worksheet{}, "", fmt.Errorf("primary state is corrupt and safe recovery is impossible; repair or restore %s manually (backup unavailable: %v): %w", primaryPath, backupErr, decodeErr)
	}
	recoveredWorksheet, backupDecodeErr := decodeWorksheet(backup)
	if backupDecodeErr != nil {
		return worksheet{}, "", fmt.Errorf("primary state is corrupt and safe recovery is impossible; both %s and %s were preserved; repair or restore one artifact manually (backup invalid: %v): %w", primaryPath, backupPath, backupDecodeErr, decodeErr)
	}
	artifactPath, err := preserveCorruptState(primary)
	if err != nil {
		return worksheet{}, "", fmt.Errorf("preserve corrupt primary before recovery: %w", err)
	}
	if err := atomicWrite(primaryPath, backup); err != nil {
		return worksheet{}, "", fmt.Errorf("restore primary from backup after preserving corrupt state as %s: %w", artifactPath, err)
	}
	return recoveredWorksheet, fmt.Sprintf("Recovered Worksheet from backup; preserved corrupt primary as %s", filepath.Base(artifactPath)), nil
}

func saveWorksheet(storedWorksheet worksheet) error {
	directory := filepath.Clean(stateDirectory)
	if err := ensureStateDirectory(directory); err != nil {
		return err
	}
	if err := validateWorksheet(storedWorksheet); err != nil {
		return fmt.Errorf("refuse to save invalid Worksheet: %w", err)
	}
	encoded, err := json.MarshalIndent(storedWorksheet, "", "  ")
	if err != nil {
		return fmt.Errorf("encode Worksheet: %w", err)
	}
	encoded = append(encoded, '\n')
	primaryPath := filepath.Join(directory, primaryStateFile)
	current, err := os.ReadFile(primaryPath)
	if err == nil {
		if _, decodeErr := decodeWorksheet(current); decodeErr != nil {
			return fmt.Errorf("existing primary state is invalid; refusing to replace it: %w", decodeErr)
		}
		if err := validateExistingBackup(filepath.Join(directory, backupStateFile)); err != nil {
			return err
		}
		if err := atomicWrite(filepath.Join(directory, backupStateFile), current); err != nil {
			return fmt.Errorf("save last-known-good backup: %w", err)
		}
	} else if errors.Is(err, os.ErrNotExist) {
		if err := atomicWrite(filepath.Join(directory, backupStateFile), encoded); err != nil {
			return fmt.Errorf("save initial last-known-good backup: %w", err)
		}
	} else {
		return fmt.Errorf("read existing Worksheet before save: %w", err)
	}
	if err := atomicWrite(primaryPath, encoded); err != nil {
		return fmt.Errorf("save Worksheet: %w", err)
	}
	return nil
}

func validateExistingBackup(path string) error {
	encoded, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read Worksheet backup: %w", err)
	}
	if _, err := decodeWorksheet(encoded); err != nil {
		var versionError unsupportedSchemaVersionError
		if errors.As(err, &versionError) {
			return fmt.Errorf("refuse to open Worksheet backup: %w; no migration was attempted", err)
		}
		return fmt.Errorf("Worksheet backup is invalid and was preserved; repair or remove %s before continuing: %w", path, err)
	}
	return nil
}

func resetWorksheetState() error {
	directory := filepath.Clean(stateDirectory)
	for _, name := range []string{backupStateFile, primaryStateFile} {
		path := filepath.Join(directory, name)
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove %s: %w", name, err)
		}
	}
	directoryHandle, err := os.Open(directory)
	if err != nil {
		return fmt.Errorf("open Worksheet directory after reset: %w", err)
	}
	defer directoryHandle.Close()
	if err := directoryHandle.Sync(); err != nil {
		return fmt.Errorf("sync Worksheet reset: %w", err)
	}
	return nil
}

func ensureStateDirectory(directory string) error {
	if err := ensurePrivateStateDirectory(directory); err != nil {
		return err
	}
	ignorePath := filepath.Join(directory, ".gitignore")
	if err := os.WriteFile(ignorePath, []byte("*\n"), 0o600); err != nil {
		return fmt.Errorf("protect Worksheet from source control: %w", err)
	}
	if err := os.Chmod(ignorePath, 0o600); err != nil {
		return fmt.Errorf("protect Worksheet gitignore: %w", err)
	}
	return nil
}

func ensurePrivateStateDirectory(directory string) error {
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create Worksheet directory: %w", err)
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		return fmt.Errorf("protect Worksheet directory: %w", err)
	}
	return nil
}

func atomicWrite(path string, contents []byte) error {
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, "."+filepath.Base(path)+".tmp-")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := writeDurableFile(temporary, contents); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	return syncDirectory(directory)
}

func preserveCorruptState(contents []byte) (string, error) {
	name := fmt.Sprintf("worksheet.corrupt-%s.json", time.Now().UTC().Format("20060102T150405.000000000Z"))
	path := filepath.Join(stateDirectory, name)
	artifact, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", err
	}
	if err := writeDurableFile(artifact, contents); err != nil {
		return "", err
	}
	if err := syncDirectory(stateDirectory); err != nil {
		return "", err
	}
	return path, nil
}

func writeDurableFile(file *os.File, contents []byte) error {
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return err
	}
	if _, err := file.Write(contents); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func protectStateArtifact(path string) error {
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("protect Worksheet artifact %s: %w", path, err)
	}
	return nil
}

func decodeWorksheet(encoded []byte) (worksheet, error) {
	var storedWorksheet worksheet
	if err := json.Unmarshal(encoded, &storedWorksheet); err != nil {
		return worksheet{}, err
	}
	if err := validateWorksheet(storedWorksheet); err != nil {
		return worksheet{}, err
	}
	return storedWorksheet, nil
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
