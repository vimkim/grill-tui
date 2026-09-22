package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

type worksheetLock struct {
	file *os.File
}

func acquireWorksheetLock() (*worksheetLock, error) {
	directory := filepath.Clean(stateDirectory)
	if err := ensurePrivateStateDirectory(directory); err != nil {
		return nil, fmt.Errorf("prepare Worksheet directory for lock: %w", err)
	}
	path := filepath.Join(directory, "worksheet.lock")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open Worksheet lock: %w", err)
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("protect Worksheet lock: %w", err)
	}
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = file.Close()
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return nil, errors.New("Worksheet is already open in another process; close it before starting a second writer")
		}
		return nil, fmt.Errorf("acquire Worksheet lock: %w", err)
	}
	if err := ensureStateDirectory(directory); err != nil {
		_ = unix.Flock(int(file.Fd()), unix.LOCK_UN)
		_ = file.Close()
		return nil, err
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
