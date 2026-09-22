package main

import (
	"errors"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

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
	}
	return model, nil
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
	view.WriteString("10 Answer Slots\n")
	view.WriteString("   No. │ Answer\n")
	view.WriteString("───────┼────────────────────────────────────────\n")
	for index, slot := range model.worksheet.Slots {
		marker := " "
		if index == 0 {
			marker = ">"
		}
		fmt.Fprintf(&view, "%s %d │ %s\n", marker, slot.Number, slot.Answer)
	}
	status := model.status
	if status == "" {
		status = "Worksheet ready"
	}
	fmt.Fprintf(&view, "\nStatus: %s\n", status)
	view.WriteString("Help: q / Ctrl-Q / Ctrl-C quit\n")
	return view.String()
}
