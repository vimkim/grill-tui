package main

import (
	"errors"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/mattn/go-runewidth"
)

const gridAnswerWidth = 40

type worksheetModel struct {
	worksheet  worksheet
	prompting  bool
	startInput string
	status     string
}

func (worksheetModel) Init() tea.Cmd {
	return nil
}

func (model worksheetModel) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	if key, ok := message.(tea.KeyMsg); ok {
		switch key.String() {
		case "q", "ctrl+q", "ctrl+c":
			return model, tea.Quit
		}
		if model.prompting {
			switch key.String() {
			case "enter":
				start, err := parseStartingNumber(model.startInput)
				if err != nil {
					model.startInput = ""
					if errors.Is(err, errStartingNumberTooLarge) {
						model.status = "Starting number is too large for ten consecutive Answer Slots. Try again."
					} else {
						model.status = "Starting number must be a positive integer. Try again."
					}
					return model, nil
				}
				created := newWorksheet(start)
				if err := saveWorksheet(created); err != nil {
					model.status = fmt.Sprintf("Could not create Worksheet: %v", err)
					return model, nil
				}
				model.worksheet = created
				model.prompting = false
				model.startInput = ""
				model.status = "Worksheet created"
				return model, nil
			case "backspace":
				if len(model.startInput) > 0 {
					model.startInput = model.startInput[:len(model.startInput)-1]
				}
				return model, nil
			}
			if len(key.Runes) > 0 {
				for _, typed := range key.Runes {
					if typed < '0' || typed > '9' {
						model.status = "Starting number must contain digits only. Try again."
						return model, nil
					}
				}
				model.startInput += string(key.Runes)
				model.status = ""
				return model, nil
			}
			model.status = "Starting number must contain digits only. Try again."
			return model, nil
		}
		switch key.String() {
		case "j", "down", "ctrl+n", " ":
			return model.moveSelection(1), nil
		case "k", "up", "ctrl+p":
			return model.moveSelection(-1), nil
		}
		if answer, ok := presetAnswerForKey(key.String()); ok {
			return model.commitAnswer(string(answer), "Preset Answer committed"), nil
		}
		if key.String() == "x" {
			return model.commitAnswer("explain further", "Answer committed"), nil
		}
	}
	return model, nil
}

type presetAnswer string

func presetAnswerForKey(key string) (presetAnswer, bool) {
	switch key {
	case "r", "R":
		return presetAnswer("recommended"), true
	case "y", "Y":
		return presetAnswer("yes"), true
	case "n", "N":
		return presetAnswer("no"), true
	case "1", "2", "3", "4", "5", "a", "b", "c", "d", "e", "A", "B", "C", "D", "E":
		return presetAnswer(key), true
	default:
		return "", false
	}
}

func (model worksheetModel) commitAnswer(answer, successStatus string) worksheetModel {
	candidate, err := model.worksheet.withCommittedAnswer(answer)
	if err != nil {
		model.status = fmt.Sprintf("Could not grow Worksheet: %v", err)
		return model
	}
	return model.persistWorksheet(candidate, successStatus)
}

func (model worksheetModel) moveSelection(change int) worksheetModel {
	candidate, moved := model.worksheet.withSelection(change)
	if !moved {
		return model
	}
	return model.persistWorksheet(candidate, "Selection moved")
}

func (model worksheetModel) persistWorksheet(candidate worksheet, successStatus string) worksheetModel {
	if err := saveWorksheet(candidate); err != nil {
		model.status = fmt.Sprintf("Could not save Worksheet: %v", err)
		return model
	}
	model.worksheet = candidate
	model.status = successStatus
	return model
}

func (model worksheetModel) View() string {
	if model.prompting {
		return model.promptView()
	}
	return model.worksheetView()
}

func (model worksheetModel) promptView() string {
	var view strings.Builder
	view.WriteString("Grill TUI — Create a Worksheet\n\n")
	view.WriteString("Enter a positive starting number.\n")
	fmt.Fprintf(&view, "Starting number: %s\n", model.startInput)
	if model.status != "" {
		fmt.Fprintf(&view, "\nStatus: %s\n", model.status)
	}
	view.WriteString("\nHelp: Enter create • q / Ctrl-Q / Ctrl-C quit\n")
	return view.String()
}

func (model worksheetModel) worksheetView() string {
	var view strings.Builder
	view.WriteString("Grill TUI — Worksheet\n\n")
	fmt.Fprintf(&view, "%d Answer Slots\n", len(model.worksheet.Slots))
	view.WriteString("   No. │ Answer\n")
	view.WriteString("───────┼────────────────────────────────────────\n")
	lastVisible := min(model.worksheet.Viewport+visibleAnswerSlotCount, len(model.worksheet.Slots))
	for index := model.worksheet.Viewport; index < lastVisible; index++ {
		slot := model.worksheet.Slots[index]
		marker := " "
		if index == model.worksheet.Selected {
			marker = ">"
		}
		fmt.Fprintf(&view, "%s %d │ %s\n", marker, slot.Number, truncateGridAnswer(slot.Answer))
	}
	selected := model.worksheet.Slots[model.worksheet.Selected]
	preview := selected.Answer
	if preview == "" {
		preview = "(empty)"
	}
	fmt.Fprintf(&view, "\nSelected Answer %d (full):\n%s\n", selected.Number, preview)
	status := model.status
	if status == "" {
		status = "Worksheet ready"
	}
	fmt.Fprintf(&view, "\nStatus: %s\n", status)
	view.WriteString("Help: ↑/↓ j/k Ctrl-N/Ctrl-P move • Space skip • r/y/n 1-5 a-e x answer • q quit\n")
	return view.String()
}

func truncateGridAnswer(answer string) string {
	answer = strings.ReplaceAll(answer, "\n", " ")
	return runewidth.Truncate(answer, gridAnswerWidth, "…")
}
