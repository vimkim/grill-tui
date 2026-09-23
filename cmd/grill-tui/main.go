package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"text/tabwriter"

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
	if handled, err := runCLICommand(args); handled {
		return err
	}
	name, positional, err := parseWorksheetSelection(args, []string{"--help", "--name"}, launchUsage)
	if err != nil {
		return addCLIUsage(err, topLevelUsage)
	}
	selectWorksheetStorage(name)
	if len(positional) > 1 {
		return cliFailure("provide at most one positive starting number", "", launchUsage)
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
		initialModel.worksheet = existing
		initialModel.status = recoveryStatus
		if len(positional) == 0 {
			initialModel.mode = resumeNumberMode
		} else {
			start, err := parsePositiveNumber(positional[0])
			if err != nil {
				return err
			}
			positioned := existing.expandAndSelect(start)
			if positioned.FirstNumber != existing.FirstNumber || positioned.LastNumber != existing.LastNumber ||
				positioned.Selected != existing.Selected || positioned.Viewport != existing.Viewport {
				if err := saveWorksheet(positioned); err != nil {
					return err
				}
			}
			initialModel.mode = normalMode
			initialModel.worksheet = positioned
		}
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

func runListCommand(args []string) error {
	if len(args) != 0 {
		if strings.HasPrefix(args[0], "-") {
			return unknownCLIValue("option", args[0], cliCommands["list"].options, listUsage)
		}
		return cliFailure("list does not accept arguments", "", listUsage)
	}
	worksheets, err := discoverWorksheets()
	if err != nil {
		return err
	}
	sort.Slice(worksheets, func(left, right int) bool {
		if worksheets[left].updatedAt.Equal(worksheets[right].updatedAt) {
			return worksheets[left].name < worksheets[right].name
		}
		return worksheets[left].updatedAt.After(worksheets[right].updatedAt)
	})
	output := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(output, "NAME\tRANGE\tANSWERED\tLAST\tUPDATED"); err != nil {
		return err
	}
	for _, worksheet := range worksheets {
		lastAnswered := "—"
		if worksheet.lastAnswered.Valid {
			lastAnswered = fmt.Sprint(worksheet.lastAnswered.Int64)
		}
		if _, err := fmt.Fprintf(output, "%s\t%d-%d\t%d\t%s\t%s\n",
			worksheet.name, worksheet.firstNumber, worksheet.lastNumber,
			worksheet.answeredCount, lastAnswered, worksheet.updatedText); err != nil {
			return err
		}
	}
	return output.Flush()
}

func runResultCommand(args []string) error {
	answers := false
	selectionArgs := make([]string, 0, len(args))
	for _, argument := range args {
		if argument == "--answers" {
			if answers {
				return cliFailure("provide --answers only once", "", resultUsage)
			}
			answers = true
			continue
		}
		selectionArgs = append(selectionArgs, argument)
	}
	name, positional, err := parseWorksheetSelection(selectionArgs, cliCommands["result"].options, resultUsage)
	if err != nil {
		return addCLIUsage(err, resultUsage)
	}
	if len(positional) != 0 {
		return cliFailure("result does not accept positional arguments", "", resultUsage)
	}
	selectWorksheetStorage(name)
	storedWorksheet, err := activeWorksheetStore.loadReadOnly()
	if err != nil {
		return err
	}
	if answers {
		_, err = fmt.Print(storedWorksheet.answerList())
		return err
	}
	absolutePath, err := filepath.Abs(activeWorksheetStore.databasePath)
	if err != nil {
		return fmt.Errorf("resolve Worksheet Database path: %w", err)
	}
	_, err = fmt.Fprintln(os.Stdout, absolutePath)
	return err
}

func runQueryCommand(args []string) error {
	name, bounds, err := parseWorksheetSelection(args, cliCommands["query"].options, queryUsage)
	if err != nil {
		return addCLIUsage(err, queryUsage)
	}
	if len(bounds) != 2 {
		return cliFailure("usage: "+queryInvocation, "", queryUsage)
	}
	from, err := parsePositiveNumber(bounds[0])
	if err != nil {
		return cliFailure(fmt.Sprintf("query FROM bound %q must be a positive integer", bounds[0]), "", queryUsage)
	}
	to, err := parsePositiveNumber(bounds[1])
	if err != nil {
		return cliFailure(fmt.Sprintf("query TO bound %q must be a positive integer", bounds[1]), "", queryUsage)
	}
	if from > to {
		return cliFailure(fmt.Sprintf("query FROM bound %d must not exceed TO bound %d", from, to), "", queryUsage)
	}
	selectWorksheetStorage(name)
	storedWorksheet, err := activeWorksheetStore.loadReadOnly()
	if err != nil {
		return err
	}
	_, err = fmt.Print(storedWorksheet.answerListBetween(from, to))
	return err
}

func parseWorksheetSelection(args, optionCandidates []string, usage string) (worksheetName, []string, error) {
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
			return "", nil, unknownCLIValue("option", argument, optionCandidates, usage)
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
