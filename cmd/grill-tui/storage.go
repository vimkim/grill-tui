package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"golang.org/x/sys/unix"
	"modernc.org/sqlite"
)

const (
	worksheetDataDirectory = ".grill-data"
	worksheetDatabaseFile  = "worksheet.sqlite"
	worksheetBackupFile    = "worksheet.backup.sqlite"
)

type worksheetStore struct {
	name         worksheetName
	directory    string
	databasePath string
	backupPath   string
}

type worksheetInfo struct {
	name          worksheetName
	firstNumber   int
	lastNumber    int
	lastAnswered  sql.NullInt64
	answeredCount int
	updatedAt     time.Time
	updatedText   string
}

var activeWorksheetStore = newWorksheetStore(defaultWorksheetName)

func selectWorksheetStorage(name worksheetName) {
	activeWorksheetStore = newWorksheetStore(name)
}

func newWorksheetStore(name worksheetName) worksheetStore {
	directory := filepath.Join(worksheetDataDirectory, string(name))
	return worksheetStore{
		name:         name,
		directory:    directory,
		databasePath: filepath.Join(directory, worksheetDatabaseFile),
		backupPath:   filepath.Join(directory, worksheetBackupFile),
	}
}

func discoverWorksheets() ([]worksheetInfo, error) {
	entries, err := os.ReadDir(worksheetDataDirectory)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("discover Worksheets: %w", err)
	}
	worksheets := make([]worksheetInfo, 0, len(entries))
	var unreadable []error
	for _, entry := range entries {
		name := entry.Name()
		if !entry.IsDir() || !worksheetNamePattern.MatchString(name) {
			continue
		}
		store := newWorksheetStore(worksheetName(name))
		if _, err := os.Lstat(store.databasePath); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			unreadable = append(unreadable, fmt.Errorf("Worksheet %q: inspect live Worksheet Database: %w", name, err))
			continue
		}
		info, err := store.readInfo()
		if err != nil {
			unreadable = append(unreadable, fmt.Errorf("Worksheet %q: read live Worksheet Database: %w", name, err))
			continue
		}
		worksheets = append(worksheets, info)
	}
	if len(unreadable) != 0 {
		return nil, fmt.Errorf("cannot list unreadable Worksheets:\n%w", errors.Join(unreadable...))
	}
	return worksheets, nil
}

func (store worksheetStore) readInfo() (worksheetInfo, error) {
	database, err := store.openExistingReadOnlyDatabase()
	if err != nil {
		return worksheetInfo{}, err
	}
	defer database.Close()
	if err := requireSupportedSchema(database); err != nil {
		return worksheetInfo{}, err
	}
	var info worksheetInfo
	var storedName string
	if err := database.QueryRow(`
SELECT worksheet_name, first_number, last_number, last_answered_number, answered_count, updated_at
  FROM worksheet_info`).Scan(&storedName, &info.firstNumber, &info.lastNumber, &info.lastAnswered, &info.answeredCount, &info.updatedText); err != nil {
		return worksheetInfo{}, fmt.Errorf("query public worksheet_info: %w", err)
	}
	if storedName != string(store.name) {
		return worksheetInfo{}, fmt.Errorf("Worksheet Database records name %q, not directory name %q", storedName, store.name)
	}
	info.updatedAt, err = time.Parse(time.RFC3339Nano, info.updatedText)
	if err != nil {
		return worksheetInfo{}, fmt.Errorf("parse public worksheet_info updated_at %q: %w", info.updatedText, err)
	}
	info.name = store.name
	return info, nil
}

func loadWorksheet() (worksheet, string, error) {
	return activeWorksheetStore.load()
}

func saveWorksheet(storedWorksheet worksheet) error {
	return activeWorksheetStore.save(storedWorksheet)
}

func resetWorksheetState() error {
	return activeWorksheetStore.reset()
}

type worksheetLock struct {
	file *os.File
}

func acquireWorksheetLock() (*worksheetLock, error) {
	store := activeWorksheetStore
	if err := store.ensureDirectories(); err != nil {
		return nil, fmt.Errorf("prepare Worksheet directory for lock: %w", err)
	}
	path := filepath.Join(store.directory, "worksheet.lock")
	file, err := openOwnerOnlyFile(path, unix.O_CREAT|unix.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open Worksheet lock: %w", err)
	}
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = file.Close()
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return nil, fmt.Errorf("Worksheet %q is already open by a live TUI writer in another process; close it before starting a second writer", store.name)
		}
		return nil, fmt.Errorf("acquire Worksheet lock: %w", err)
	}
	return &worksheetLock{file: file}, nil
}

func (lock *worksheetLock) release() {
	if lock == nil || lock.file == nil {
		return
	}
	_ = unix.Flock(int(lock.file.Fd()), unix.LOCK_UN)
	_ = lock.file.Close()
}

func (store worksheetStore) ensureDirectories() error {
	root := filepath.Clean(worksheetDataDirectory)
	if err := ensureOwnerOnlyDirectory(root); err != nil {
		return err
	}
	ignore, err := openOwnerOnlyFile(filepath.Join(root, ".gitignore"), unix.O_CREAT|unix.O_TRUNC|unix.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("protect Worksheet from source control: %w", err)
	}
	if _, err := ignore.WriteString("*\n"); err != nil {
		_ = ignore.Close()
		return fmt.Errorf("write Worksheet gitignore: %w", err)
	}
	if err := ignore.Close(); err != nil {
		return fmt.Errorf("close Worksheet gitignore: %w", err)
	}
	return ensureOwnerOnlyDirectory(store.directory)
}

func ensureOwnerOnlyDirectory(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.Mkdir(path, 0o700); err != nil {
			return fmt.Errorf("create Worksheet directory %s: %w", path, err)
		}
	} else if err != nil {
		return fmt.Errorf("inspect Worksheet directory %s: %w", path, err)
	} else if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refuse symbolic link for Worksheet directory %s", path)
	} else if !info.IsDir() {
		return fmt.Errorf("Worksheet directory path %s is not a directory", path)
	}
	fileDescriptor, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("securely open Worksheet directory %s: %w", path, err)
	}
	defer unix.Close(fileDescriptor)
	if err := unix.Fchmod(fileDescriptor, 0o700); err != nil {
		return fmt.Errorf("protect Worksheet directory %s: %w", path, err)
	}
	return nil
}

func openOwnerOnlyFile(path string, flags int, permissions uint32) (*os.File, error) {
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("refuse symbolic link for Worksheet artifact %s", path)
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("Worksheet artifact %s is not a regular file", path)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	fileDescriptor, err := unix.Open(path, flags|unix.O_NOFOLLOW|unix.O_CLOEXEC, permissions)
	if err != nil {
		return nil, err
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fileDescriptor, &stat); err != nil {
		_ = unix.Close(fileDescriptor)
		return nil, err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG {
		_ = unix.Close(fileDescriptor)
		return nil, fmt.Errorf("Worksheet artifact %s is not a regular file", path)
	}
	if err := unix.Fchmod(fileDescriptor, permissions); err != nil {
		_ = unix.Close(fileDescriptor)
		return nil, err
	}
	return os.NewFile(uintptr(fileDescriptor), path), nil
}

const worksheetSchema = `
CREATE TABLE worksheet_state (
    singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
    worksheet_name TEXT NOT NULL,
    first_number INTEGER NOT NULL,
    last_number INTEGER NOT NULL,
    selected_index INTEGER NOT NULL,
    viewport_index INTEGER NOT NULL,
    undo_state TEXT,
    updated_at TEXT NOT NULL
);
CREATE TABLE worksheet_answers (
    question_number INTEGER PRIMARY KEY,
    answer TEXT NOT NULL
);
CREATE VIEW answer_slots(question_number, answer, is_answered) AS
WITH RECURSIVE slot_numbers(question_number) AS (
    SELECT first_number FROM worksheet_state WHERE singleton = 1
    UNION ALL
    SELECT slot_numbers.question_number + 1
      FROM slot_numbers, worksheet_state
     WHERE slot_numbers.question_number < worksheet_state.last_number
)
SELECT slot_numbers.question_number,
       COALESCE(worksheet_answers.answer, ''),
       CASE WHEN COALESCE(worksheet_answers.answer, '') = '' THEN 0 ELSE 1 END
  FROM slot_numbers
  LEFT JOIN worksheet_answers USING (question_number);
CREATE VIEW answered_questions(question_number, answer) AS
SELECT question_number, answer
  FROM worksheet_answers
 WHERE answer <> '';
CREATE VIEW worksheet_info(
    worksheet_name,
    first_number,
    last_number,
    last_answered_number,
    answered_count,
    updated_at
) AS
SELECT worksheet_name,
       first_number,
       last_number,
       (SELECT MAX(question_number) FROM worksheet_answers WHERE answer <> ''),
       (SELECT COUNT(*) FROM worksheet_answers WHERE answer <> ''),
       updated_at
  FROM worksheet_state
 WHERE singleton = 1;
PRAGMA user_version = 1;
`

func (store worksheetStore) load() (worksheet, string, error) {
	primaryExists, err := protectExistingRegularFile(store.databasePath)
	if err != nil {
		return worksheet{}, "", fmt.Errorf("protect Worksheet Database: %w", err)
	}
	backupExists, err := protectExistingRegularFile(store.backupPath)
	if err != nil {
		return worksheet{}, "", fmt.Errorf("protect Worksheet backup: %w", err)
	}
	if !primaryExists {
		if !backupExists {
			return worksheet{}, "", fmt.Errorf("read Worksheet: %w", os.ErrNotExist)
		}
		storedWorksheet, err := store.loadDatabasePath(store.backupPath)
		if err != nil {
			return worksheet{}, "", store.unrecoverableError("primary is missing", err)
		}
		if err := store.preserveOrphanedPrimarySidecars(); err != nil {
			return worksheet{}, "", fmt.Errorf("preserve interrupted Worksheet artifacts before recovery: %w", err)
		}
		if err := store.restorePrimaryFromBackup(); err != nil {
			return worksheet{}, "", err
		}
		return storedWorksheet, "Recovered missing Worksheet Database from validated backup", nil
	}

	storedWorksheet, err := store.loadDatabasePath(store.databasePath)
	if err == nil {
		if !backupExists {
			if err := store.createValidatedCopy(store.databasePath, store.backupPath, store.directory); err != nil {
				return worksheet{}, "", fmt.Errorf("rebuild missing Worksheet backup: %w", err)
			}
			return storedWorksheet, "Rebuilt missing Worksheet backup from validated primary", nil
		}
		backupWorksheet, backupErr := store.loadDatabasePath(store.backupPath)
		if backupErr != nil {
			var backupVersionError unsupportedSchemaVersionError
			if errors.As(backupErr, &backupVersionError) {
				return worksheet{}, "", fmt.Errorf("refuse to replace Worksheet backup: %w; no migration was attempted", backupErr)
			}
			if err := store.createValidatedCopy(store.databasePath, store.backupPath, store.directory); err != nil {
				return worksheet{}, "", fmt.Errorf("rebuild invalid Worksheet backup: %w", err)
			}
			return storedWorksheet, "Rebuilt invalid Worksheet backup from validated primary", nil
		}
		if !reflect.DeepEqual(storedWorksheet, backupWorksheet) {
			if err := store.createValidatedCopy(store.databasePath, store.backupPath, store.directory); err != nil {
				return worksheet{}, "", fmt.Errorf("rebuild stale Worksheet backup: %w", err)
			}
			return storedWorksheet, "Rebuilt stale Worksheet backup from validated primary", nil
		}
		return storedWorksheet, "", nil
	}
	var versionError unsupportedSchemaVersionError
	if errors.As(err, &versionError) {
		return worksheet{}, "", fmt.Errorf("refuse to open Worksheet: %w; no migration was attempted", err)
	}
	if !backupExists {
		return worksheet{}, "", store.unrecoverableError("primary is corrupt and no backup exists", err)
	}
	storedWorksheet, backupErr := store.loadDatabasePath(store.backupPath)
	if backupErr != nil {
		return worksheet{}, "", store.unrecoverableError("primary and backup are both unusable", errors.Join(err, backupErr))
	}
	if _, err := store.preserveCorruptPrimary(); err != nil {
		return worksheet{}, "", fmt.Errorf("preserve corrupt Worksheet Database before recovery: %w", err)
	}
	if err := store.restorePrimaryFromBackup(); err != nil {
		return worksheet{}, "", err
	}
	return storedWorksheet, "Recovered Worksheet from validated backup; corrupt primary preserved", nil
}

func (store worksheetStore) loadReadOnly() (worksheet, error) {
	database, err := store.openExistingReadOnlyDatabase()
	if err != nil {
		return worksheet{}, err
	}
	return store.loadFromDatabase(database)
}

func (store worksheetStore) unrecoverableError(reason string, cause error) error {
	return fmt.Errorf("safe Worksheet recovery is impossible: %s: %w; primary and backup artifacts were preserved; repair or restore them manually in %s", reason, cause, store.directory)
}

func (store worksheetStore) loadFromDatabase(database *sql.DB) (worksheet, error) {
	defer database.Close()
	if err := requireSupportedSchema(database); err != nil {
		return worksheet{}, fmt.Errorf("refuse to open Worksheet: %w; no migration was attempted", err)
	}

	var storedName string
	var firstNumber, lastNumber, selected, viewport int
	var encodedUndo sql.NullString
	err := database.QueryRow(`
SELECT worksheet_name, first_number, last_number, selected_index, viewport_index, undo_state
  FROM worksheet_state
 WHERE singleton = 1`).Scan(&storedName, &firstNumber, &lastNumber, &selected, &viewport, &encodedUndo)
	if err != nil {
		return worksheet{}, fmt.Errorf("read Worksheet metadata: %w", err)
	}
	if storedName != string(store.name) {
		return worksheet{}, fmt.Errorf("Worksheet Database records name %q, not selected name %q", storedName, store.name)
	}
	if firstNumber < 1 || lastNumber < firstNumber {
		return worksheet{}, fmt.Errorf("Worksheet Database has invalid range %d-%d", firstNumber, lastNumber)
	}
	storedWorksheet := worksheet{
		SchemaVersion: worksheetSchemaVersion,
		FirstNumber:   firstNumber,
		LastNumber:    lastNumber,
		Answers:       make(map[int]string),
		Selected:      firstNumber + selected,
		Viewport:      firstNumber + viewport,
	}
	rows, err := database.Query("SELECT question_number, answer FROM worksheet_answers ORDER BY question_number")
	if err != nil {
		return worksheet{}, fmt.Errorf("read Worksheet answers: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var number int
		var answer string
		if err := rows.Scan(&number, &answer); err != nil {
			return worksheet{}, fmt.Errorf("read Worksheet answer: %w", err)
		}
		if number < firstNumber || number > lastNumber {
			return worksheet{}, fmt.Errorf("Worksheet answer %d is outside range %d-%d", number, firstNumber, lastNumber)
		}
		if answer != "" {
			storedWorksheet.Answers[number] = answer
		}
	}
	if err := rows.Err(); err != nil {
		return worksheet{}, fmt.Errorf("read Worksheet answers: %w", err)
	}
	if encodedUndo.Valid {
		undo, err := decodeWorksheetUndo([]byte(encodedUndo.String))
		if err != nil {
			return worksheet{}, fmt.Errorf("read Worksheet undo state: %w", err)
		}
		storedWorksheet.Undo = undo
	}
	if err := validateWorksheet(storedWorksheet); err != nil {
		return worksheet{}, fmt.Errorf("validate Worksheet Database: %w", err)
	}
	return storedWorksheet, nil
}

func (store worksheetStore) openExistingReadOnlyDatabase() (*sql.DB, error) {
	if err := inspectReadOnlyDirectory(worksheetDataDirectory, "Worksheet data root"); err != nil {
		return nil, err
	}
	if err := inspectReadOnlyDirectory(store.directory, "Worksheet directory"); err != nil {
		return nil, err
	}
	info, err := os.Lstat(store.databasePath)
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read Worksheet: %w", os.ErrNotExist)
	}
	if err != nil {
		return nil, fmt.Errorf("inspect Worksheet Database: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("refuse symbolic link for Worksheet Database %s", store.databasePath)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("Worksheet Database %s is not a regular file", store.databasePath)
	}
	absolutePath, err := filepath.Abs(store.databasePath)
	if err != nil {
		return nil, fmt.Errorf("resolve Worksheet Database path: %w", err)
	}
	databaseURL := (&url.URL{Scheme: "file", Path: absolutePath}).String() + "?mode=ro"
	return openSQLiteDatabase(databaseURL, false)
}

func inspectReadOnlyDirectory(path, description string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read Worksheet: %w", os.ErrNotExist)
	}
	if err != nil {
		return fmt.Errorf("inspect %s: %w", description, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refuse symbolic link for %s %s", description, path)
	}
	if !info.IsDir() {
		return fmt.Errorf("%s %s is not a directory", description, path)
	}
	return nil
}

func (store worksheetStore) save(storedWorksheet worksheet) error {
	if err := validateWorksheet(storedWorksheet); err != nil {
		return fmt.Errorf("refuse to save invalid Worksheet: %w", err)
	}
	if err := store.ensureDirectories(); err != nil {
		return err
	}
	database, created, err := store.openWritableDatabase()
	if err != nil {
		return err
	}
	defer database.Close()
	if created {
		if _, err := database.Exec(worksheetSchema); err != nil {
			return fmt.Errorf("initialize Worksheet Database: %w", err)
		}
	} else if err := requireSupportedSchema(database); err != nil {
		return fmt.Errorf("refuse to open Worksheet: %w; no migration was attempted", err)
	}

	transaction, err := database.Begin()
	if err != nil {
		return fmt.Errorf("begin Worksheet save: %w", err)
	}
	defer transaction.Rollback()
	var undo any
	if storedWorksheet.Undo != nil {
		encoded, err := json.Marshal(storedWorksheet.Undo)
		if err != nil {
			return fmt.Errorf("encode Worksheet undo state: %w", err)
		}
		undo = string(encoded)
	}
	firstNumber := storedWorksheet.FirstNumber
	lastNumber := storedWorksheet.LastNumber
	_, err = transaction.Exec(`
INSERT INTO worksheet_state (
    singleton, worksheet_name, first_number, last_number,
    selected_index, viewport_index, undo_state, updated_at
) VALUES (1, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(singleton) DO UPDATE SET
    worksheet_name = excluded.worksheet_name,
    first_number = excluded.first_number,
    last_number = excluded.last_number,
    selected_index = excluded.selected_index,
    viewport_index = excluded.viewport_index,
    undo_state = excluded.undo_state,
    updated_at = excluded.updated_at`,
		string(store.name), firstNumber, lastNumber, storedWorksheet.Selected-firstNumber,
		storedWorksheet.Viewport-firstNumber, undo, time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("save Worksheet metadata: %w", err)
	}
	if _, err := transaction.Exec("DELETE FROM worksheet_answers"); err != nil {
		return fmt.Errorf("replace Worksheet answers: %w", err)
	}
	statement, err := transaction.Prepare("INSERT INTO worksheet_answers(question_number, answer) VALUES (?, ?)")
	if err != nil {
		return fmt.Errorf("prepare Worksheet answers: %w", err)
	}
	defer statement.Close()
	for number, answer := range storedWorksheet.Answers {
		if _, err := statement.Exec(number, answer); err != nil {
			return fmt.Errorf("save Answer Slot %d: %w", number, err)
		}
	}
	if err := statement.Close(); err != nil {
		return fmt.Errorf("finish Worksheet answers: %w", err)
	}
	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("commit Worksheet save: %w", err)
	}
	if err := store.replaceBackupFrom(database); err != nil {
		return fmt.Errorf("maintain validated Worksheet backup: %w", err)
	}
	return store.protectDatabaseArtifacts()
}

type sqliteBackuper interface {
	NewBackup(string) (*sqlite.Backup, error)
}

func (store worksheetStore) replaceBackupFrom(database *sql.DB) (finalErr error) {
	return store.installValidatedSnapshot(database, store.backupPath, store.directory, ".worksheet.backup.sqlite.tmp-*")
}

func (store worksheetStore) installValidatedSnapshot(database *sql.DB, destinationPath, directory, pattern string) (finalErr error) {
	temporaryPath, err := store.createValidatedSnapshot(database, directory, pattern)
	if err != nil {
		return err
	}
	defer func() {
		if finalErr != nil {
			_ = removeDatabaseArtifacts(temporaryPath)
		}
	}()
	if err := removeDatabaseSidecars(destinationPath); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, destinationPath); err != nil {
		return err
	}
	if err := syncDirectory(directory); err != nil {
		return err
	}
	return nil
}

func (store worksheetStore) createValidatedSnapshot(database *sql.DB, directory, pattern string) (path string, finalErr error) {
	temporary, err := os.CreateTemp(directory, pattern)
	if err != nil {
		return "", err
	}
	temporaryPath := temporary.Name()
	defer func() {
		if finalErr != nil {
			_ = removeDatabaseArtifacts(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return "", err
	}
	if err := temporary.Close(); err != nil {
		return "", err
	}
	if err := createSQLiteBackup(database, temporaryPath); err != nil {
		return "", err
	}
	if err := store.validateDatabasePath(temporaryPath); err != nil {
		return "", fmt.Errorf("validate SQLite snapshot: %w", err)
	}
	if err := removeDatabaseSidecars(temporaryPath); err != nil {
		return "", err
	}
	if err := syncRegularFile(temporaryPath); err != nil {
		return "", err
	}
	return temporaryPath, nil
}

func (store worksheetStore) validateDatabasePath(path string) error {
	_, err := store.loadDatabasePath(path)
	return err
}

func (store worksheetStore) loadDatabasePath(path string) (worksheet, error) {
	absolutePath, err := filepath.Abs(path)
	if err != nil {
		return worksheet{}, err
	}
	databaseURL := (&url.URL{Scheme: "file", Path: absolutePath}).String() + "?mode=ro"
	database, err := openSQLiteDatabase(databaseURL, false)
	if err != nil {
		return worksheet{}, err
	}
	var integrity string
	if err := database.QueryRow("PRAGMA integrity_check").Scan(&integrity); err != nil {
		_ = database.Close()
		return worksheet{}, fmt.Errorf("check SQLite integrity: %w", err)
	}
	if integrity != "ok" {
		_ = database.Close()
		return worksheet{}, fmt.Errorf("SQLite integrity check failed: %s", integrity)
	}
	return store.loadFromDatabase(database)
}

func createSQLiteBackup(database *sql.DB, destination string) error {
	connection, err := database.Conn(context.Background())
	if err != nil {
		return err
	}
	defer connection.Close()
	return connection.Raw(func(driverConnection any) error {
		backuper, ok := driverConnection.(sqliteBackuper)
		if !ok {
			return errors.New("SQLite driver does not support consistent backup")
		}
		backup, err := backuper.NewBackup(destination)
		if err != nil {
			return err
		}
		for more := true; more; {
			more, err = backup.Step(-1)
			if err != nil {
				_ = backup.Finish()
				return err
			}
		}
		return backup.Finish()
	})
}

func (store worksheetStore) restorePrimaryFromBackup() error {
	if err := store.createValidatedCopy(store.backupPath, store.databasePath, store.directory); err != nil {
		return fmt.Errorf("restore Worksheet from backup: %w", err)
	}
	return nil
}

func (store worksheetStore) openDatabasePathReadOnly(path string) (*sql.DB, error) {
	absolutePath, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	databaseURL := (&url.URL{Scheme: "file", Path: absolutePath}).String() + "?mode=ro"
	return openSQLiteDatabase(databaseURL, false)
}

func (store worksheetStore) preserveCorruptPrimary() (string, error) {
	artifactPath, err := linkTimestampedArtifact(store.databasePath, store.directory, "worksheet.corrupt-", ".sqlite")
	if err != nil {
		return "", err
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		source := store.databasePath + suffix
		if _, err := os.Lstat(source); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return "", err
		}
		if err := os.Link(source, artifactPath+suffix); err != nil {
			return "", err
		}
		if err := os.Remove(source); err != nil {
			return "", err
		}
	}
	if err := os.Remove(store.databasePath); err != nil {
		return "", err
	}
	if err := syncDirectory(store.directory); err != nil {
		return "", err
	}
	return artifactPath, nil
}

func (store worksheetStore) preserveOrphanedPrimarySidecars() error {
	var existing []string
	for _, suffix := range []string{"-wal", "-shm"} {
		path := store.databasePath + suffix
		if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return err
		}
		existing = append(existing, suffix)
	}
	if len(existing) == 0 {
		return nil
	}
	firstSuffix := existing[0]
	firstArtifact, err := linkTimestampedArtifact(store.databasePath+firstSuffix, store.directory, "worksheet.interrupted-", ".sqlite"+firstSuffix)
	if err != nil {
		return err
	}
	artifactPath := strings.TrimSuffix(firstArtifact, firstSuffix)
	if err := os.Remove(store.databasePath + firstSuffix); err != nil {
		return err
	}
	for _, suffix := range existing[1:] {
		if err := os.Link(store.databasePath+suffix, artifactPath+suffix); err != nil {
			return err
		}
		if err := os.Remove(store.databasePath + suffix); err != nil {
			return err
		}
	}
	return syncDirectory(store.directory)
}

func linkTimestampedArtifact(sourcePath, directory, prefix, extension string) (string, error) {
	timestamp := time.Now().UTC().Format("20060102T150405.000000000Z")
	for collision := 0; ; collision++ {
		name := fmt.Sprintf("%s%s-%06d%s", prefix, timestamp, collision, extension)
		path := filepath.Join(directory, name)
		if err := os.Link(sourcePath, path); errors.Is(err, os.ErrExist) {
			continue
		} else if err != nil {
			return "", err
		}
		return path, nil
	}
}

func syncRegularFile(path string) error {
	file, err := openOwnerOnlyFile(path, unix.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	return file.Sync()
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func decodeWorksheetUndo(encoded []byte) (*worksheetUndo, error) {
	var undo worksheetUndo
	if err := json.Unmarshal(encoded, &undo); err != nil {
		return nil, err
	}
	if undo.FirstNumber > 0 {
		if undo.Answers == nil {
			undo.Answers = make(map[int]string)
		}
		return &undo, nil
	}

	var legacy struct {
		Slots    []answerSlot `json:"slots"`
		Selected int          `json:"selected"`
		Viewport int          `json:"viewport"`
	}
	if err := json.Unmarshal(encoded, &legacy); err != nil {
		return nil, err
	}
	if len(legacy.Slots) == 0 {
		return nil, errors.New("undo state has no Answer Slots")
	}
	undo = worksheetUndo{
		FirstNumber: legacy.Slots[0].Number,
		LastNumber:  legacy.Slots[len(legacy.Slots)-1].Number,
		Answers:     make(map[int]string),
		Selected:    legacy.Slots[0].Number + legacy.Selected,
		Viewport:    legacy.Slots[0].Number + legacy.Viewport,
	}
	for _, slot := range legacy.Slots {
		if slot.Answer != "" {
			undo.Answers[slot.Number] = slot.Answer
		}
	}
	return &undo, nil
}

func (store worksheetStore) openExistingDatabase() (*sql.DB, error) {
	exists, err := protectExistingRegularFile(store.databasePath)
	if err != nil {
		return nil, fmt.Errorf("protect Worksheet Database: %w", err)
	}
	if !exists {
		return nil, fmt.Errorf("read Worksheet: %w", os.ErrNotExist)
	}
	if err := store.protectDatabaseArtifacts(); err != nil {
		return nil, err
	}
	return openSQLiteDatabase(store.databasePath, false)
}

func (store worksheetStore) openWritableDatabase() (*sql.DB, bool, error) {
	exists, err := protectExistingRegularFile(store.databasePath)
	if err != nil {
		return nil, false, fmt.Errorf("protect Worksheet Database: %w", err)
	}
	created := !exists
	if created {
		file, err := openOwnerOnlyFile(store.databasePath, unix.O_CREAT|unix.O_EXCL|unix.O_RDWR, 0o600)
		if err != nil {
			return nil, false, fmt.Errorf("create Worksheet Database: %w", err)
		}
		if err := file.Close(); err != nil {
			return nil, false, fmt.Errorf("create Worksheet Database: %w", err)
		}
	}
	if err := store.protectDatabaseArtifacts(); err != nil {
		return nil, false, err
	}
	database, err := openSQLiteDatabase(store.databasePath, true)
	return database, created, err
}

func openSQLiteDatabase(path string, writable bool) (*sql.DB, error) {
	database, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open Worksheet Database: %w", err)
	}
	database.SetMaxOpenConns(1)
	if writable {
		_, err = database.Exec("PRAGMA journal_mode = WAL; PRAGMA synchronous = FULL; PRAGMA busy_timeout = 5000")
	} else {
		err = database.Ping()
	}
	if err != nil {
		_ = database.Close()
		return nil, fmt.Errorf("configure Worksheet Database: %w", err)
	}
	return database, nil
}

func protectExistingRegularFile(path string) (bool, error) {
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	file, err := openOwnerOnlyFile(path, unix.O_RDWR, 0o600)
	if err != nil {
		return false, err
	}
	return true, file.Close()
}

func requireSupportedSchema(database *sql.DB) error {
	var version int
	if err := database.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	if version != worksheetSchemaVersion {
		return unsupportedSchemaVersionError{found: version}
	}
	return nil
}

func (store worksheetStore) protectDatabaseArtifacts() error {
	artifacts, err := filepath.Glob(store.databasePath + "*")
	if err != nil {
		return fmt.Errorf("find Worksheet Database artifacts: %w", err)
	}
	for _, artifact := range artifacts {
		if _, err := protectExistingRegularFile(artifact); err != nil {
			return fmt.Errorf("protect Worksheet Database artifact %s: %w", artifact, err)
		}
	}
	return nil
}

func (store worksheetStore) reset() error {
	if _, err := store.loadDatabasePath(store.databasePath); err != nil {
		return fmt.Errorf("refuse to archive invalid Worksheet Database: %w", err)
	}
	archiveDirectory := filepath.Join(store.directory, "archive")
	if err := ensureOwnerOnlyDirectory(archiveDirectory); err != nil {
		return fmt.Errorf("prepare Worksheet archive directory: %w", err)
	}
	if _, err := store.createTimestampedValidatedCopy(store.databasePath, archiveDirectory, "", ".sqlite"); err != nil {
		return fmt.Errorf("archive Worksheet Database: %w", err)
	}
	if err := removeDatabaseArtifacts(store.backupPath); err != nil {
		return fmt.Errorf("remove reset Worksheet backup: %w", err)
	}
	if err := removeDatabaseArtifacts(store.databasePath); err != nil {
		return fmt.Errorf("remove reset Worksheet Database: %w", err)
	}
	return syncDirectory(store.directory)
}

func (store worksheetStore) createValidatedCopy(sourcePath, destinationPath, destinationDirectory string) (finalErr error) {
	database, err := store.openDatabasePathReadOnly(sourcePath)
	if err != nil {
		return err
	}
	defer database.Close()
	return store.installValidatedSnapshot(database, destinationPath, destinationDirectory, ".worksheet-copy-*.sqlite")
}

func (store worksheetStore) createTimestampedValidatedCopy(sourcePath, destinationDirectory, prefix, extension string) (string, error) {
	database, err := store.openDatabasePathReadOnly(sourcePath)
	if err != nil {
		return "", err
	}
	defer database.Close()
	temporaryPath, err := store.createValidatedSnapshot(database, destinationDirectory, ".worksheet-archive-*.sqlite")
	if err != nil {
		return "", err
	}
	defer removeDatabaseArtifacts(temporaryPath)
	timestamp := time.Now().UTC().Format("20060102T150405.000000000Z")
	for collision := 0; ; collision++ {
		name := fmt.Sprintf("%s%s-%06d%s", prefix, timestamp, collision, extension)
		destinationPath := filepath.Join(destinationDirectory, name)
		if err := os.Link(temporaryPath, destinationPath); errors.Is(err, os.ErrExist) {
			continue
		} else if err != nil {
			return "", err
		}
		if err := syncDirectory(destinationDirectory); err != nil {
			return "", err
		}
		return destinationPath, nil
	}
}

func removeDatabaseSidecars(databasePath string) error {
	for _, suffix := range []string{"-wal", "-shm"} {
		if err := os.Remove(databasePath + suffix); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove SQLite sidecar %s: %w", databasePath+suffix, err)
		}
	}
	return nil
}

func removeDatabaseArtifacts(databasePath string) error {
	artifacts, err := filepath.Glob(databasePath + "*")
	if err != nil {
		return err
	}
	for _, artifact := range artifacts {
		info, err := os.Lstat(artifact)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("refuse symbolic link for Worksheet artifact %s", artifact)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("Worksheet artifact %s is not a regular file", artifact)
		}
	}
	for _, artifact := range artifacts {
		if artifact == databasePath {
			continue
		}
		if err := os.Remove(artifact); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove Worksheet artifact %s: %w", artifact, err)
		}
	}
	if err := os.Remove(databasePath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove Worksheet artifact %s: %w", databasePath, err)
	}
	return nil
}
