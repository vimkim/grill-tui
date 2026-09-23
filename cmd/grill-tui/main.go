package main

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

type worksheetName string

const defaultWorksheetName worksheetName = "untitled"

var worksheetNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

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
	name, positional, err := parseWorksheetSelection(args)
	if err != nil {
		return err
	}
	selectWorksheetStorage(name)
	if len(positional) > 1 {
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
	} else if len(positional) == 1 {
		start, err := parseStartingNumber(positional[0])
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

func parseWorksheetSelection(args []string) (worksheetName, []string, error) {
	name := string(defaultWorksheetName)
	explicitName := false
	positional := make([]string, 0, 1)
	for index := 0; index < len(args); index++ {
		argument := args[index]
		switch {
		case argument == "--name":
			if explicitName {
				return "", nil, errors.New("provide --name only once")
			}
			if index+1 >= len(args) {
				return "", nil, errors.New("--name requires a Worksheet Name")
			}
			index++
			name = args[index]
			explicitName = true
		case strings.HasPrefix(argument, "--name="):
			if explicitName {
				return "", nil, errors.New("provide --name only once")
			}
			name = strings.TrimPrefix(argument, "--name=")
			explicitName = true
		case strings.HasPrefix(argument, "-"):
			return "", nil, fmt.Errorf("unknown option %q", argument)
		default:
			positional = append(positional, argument)
		}
	}
	if explicitName && name == string(defaultWorksheetName) {
		return "", nil, errors.New(`Worksheet Name "untitled" is reserved; omit --name to select it`)
	}
	if !worksheetNamePattern.MatchString(name) {
		return "", nil, fmt.Errorf("invalid Worksheet Name %q: use 1-64 lowercase letters, digits, underscores, or hyphens, beginning with a letter or digit", name)
	}
	return worksheetName(name), positional, nil
}
