package main

import (
	"errors"
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"
)

const stateDirectory = ".grill-tui"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "grill-tui:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) > 0 && args[0] == "config" {
		return runConfigCommand(args[1:])
	}
	if len(args) > 1 {
		return fmt.Errorf("provide at most one positive starting number")
	}
	effectiveKeymap, err := loadKeymap()
	if err != nil {
		return err
	}
	worksheetLock, err := acquireWorksheetLock()
	if err != nil {
		return err
	}
	defer worksheetLock.release()

	useColor := os.Getenv("NO_COLOR") == ""
	initialModel := worksheetModel{
		mode:     startingNumberMode,
		size:     defaultTerminalSize,
		useColor: useColor,
		keymap:   effectiveKeymap,
	}
	existing, recoveryStatus, err := loadWorksheet()
	if err == nil {
		initialModel.mode = normalMode
		initialModel.worksheet = existing
		initialModel.status = recoveryStatus
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	} else if len(args) == 1 {
		start, err := parseStartingNumber(args[0])
		if err != nil {
			return err
		}
		initialModel.mode = normalMode
		initialModel.worksheet = newWorksheet(start)
		if err := saveWorksheet(initialModel.worksheet); err != nil {
			return err
		}
	}

	program := tea.NewProgram(initialModel, tea.WithAltScreen(), tea.WithMouseCellMotion())
	_, err = program.Run()
	return err
}
