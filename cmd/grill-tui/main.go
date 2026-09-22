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
	if len(args) > 1 {
		return fmt.Errorf("provide at most one positive starting number")
	}

	initialModel := worksheetModel{prompting: true}
	existing, err := loadWorksheet()
	if err == nil {
		initialModel = worksheetModel{worksheet: existing}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	} else if len(args) == 1 {
		start, err := parseStartingNumber(args[0])
		if err != nil {
			return err
		}
		initialModel = worksheetModel{worksheet: newWorksheet(start)}
		if err := saveWorksheet(initialModel.worksheet); err != nil {
			return err
		}
	}

	program := tea.NewProgram(initialModel, tea.WithAltScreen())
	_, err = program.Run()
	return err
}
