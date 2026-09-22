package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/mattn/go-runewidth"
)

const gridAnswerWidth = 40

type interactionMode uint8

const (
	normalMode interactionMode = iota
	startingNumberMode
	inlineAnswerMode
)

type worksheetModel struct {
	worksheet  worksheet
	mode       interactionMode
	startInput string
	editInput  string
	status     string
}

type externalEditorFinishedMsg struct {
	answer string
	err    error
}

func (worksheetModel) Init() tea.Cmd {
	return nil
}

func (model worksheetModel) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	if finished, ok := message.(externalEditorFinishedMsg); ok {
		if finished.err != nil {
			model.status = fmt.Sprintf("External editor failed; Answer Slot unchanged: %v", finished.err)
			return model, nil
		}
		return model.commitAnswer(finished.answer, "Custom Answer committed from external editor"), nil
	}
	if key, ok := message.(tea.KeyMsg); ok {
		if model.mode == inlineAnswerMode {
			switch key.String() {
			case "enter":
				model.mode = normalMode
				return model.commitAnswer(model.editInput, "Custom Answer committed"), nil
			case "esc":
				model.mode = normalMode
				model.editInput = ""
				model.status = "Inline Custom Answer cancelled"
				return model, nil
			case "backspace":
				runes := []rune(model.editInput)
				if len(runes) > 0 {
					model.editInput = string(runes[:len(runes)-1])
				}
				return model, nil
			}
			if len(key.Runes) > 0 {
				for _, typed := range key.Runes {
					if typed != '\n' && typed != '\r' {
						model.editInput += string(typed)
					}
				}
			}
			return model, nil
		}
		switch key.String() {
		case "q", "ctrl+q", "ctrl+c":
			return model, tea.Quit
		}
		if model.mode == startingNumberMode {
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
				model.mode = normalMode
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
		case "esc":
			selected := model.worksheet.Slots[model.worksheet.Selected]
			if selected.Answer == "" {
				model.status = fmt.Sprintf("Answer Slot %d is already empty", selected.Number)
				return model, nil
			}
			candidate := model.worksheet.withClearedAnswer()
			return model.persistWorksheet(candidate, fmt.Sprintf("Answer Slot %d cleared", selected.Number)), nil
		case "u":
			if model.worksheet.Undo == nil {
				model.status = "Nothing to undo"
				return model, nil
			}
			candidate, number := model.worksheet.withUndo()
			return model.persistWorksheet(candidate, fmt.Sprintf("Undid Answer Slot %d", number)), nil
		case "i":
			model.mode = inlineAnswerMode
			model.editInput = model.worksheet.Slots[model.worksheet.Selected].Answer
			model.status = "Editing Custom Answer inline"
			return model, nil
		case "o":
			return model.openExternalEditor()
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

func (model worksheetModel) openExternalEditor() (worksheetModel, tea.Cmd) {
	editor := os.Getenv("VISUAL")
	if editor == "" {
		editor = os.Getenv("EDITOR")
	}
	if editor == "" {
		model.status = "Could not open external editor: set $VISUAL or $EDITOR to an executable path"
		return model, nil
	}

	draft, err := os.CreateTemp("", "grill-tui-custom-answer-*")
	if err != nil {
		model.status = fmt.Sprintf("Could not create external-editor draft; Answer Slot unchanged: %v", err)
		return model, nil
	}
	draftPath := draft.Name()
	answer := model.worksheet.Slots[model.worksheet.Selected].Answer
	if _, err := draft.WriteString(answer); err != nil {
		_ = draft.Close()
		_ = os.Remove(draftPath)
		model.status = fmt.Sprintf("Could not seed external-editor draft; Answer Slot unchanged: %v", err)
		return model, nil
	}
	if err := draft.Close(); err != nil {
		_ = os.Remove(draftPath)
		model.status = fmt.Sprintf("Could not prepare external-editor draft; Answer Slot unchanged: %v", err)
		return model, nil
	}

	model.status = fmt.Sprintf("Editing Custom Answer in %s", editor)
	command := exec.Command(editor, draftPath)
	return model, tea.ExecProcess(command, func(editorErr error) tea.Msg {
		defer os.Remove(draftPath)
		if editorErr != nil {
			return externalEditorFinishedMsg{err: fmt.Errorf("%w while running external editor %q", editorErr, editor)}
		}
		contents, readErr := os.ReadFile(draftPath)
		if readErr != nil {
			return externalEditorFinishedMsg{err: fmt.Errorf("read saved draft: %w", readErr)}
		}
		return externalEditorFinishedMsg{answer: string(contents)}
	})
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
	if model.mode == startingNumberMode {
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
	if model.mode == inlineAnswerMode {
		fmt.Fprintf(&view, "\nInline Custom Answer %d: %s\n", selected.Number, singleLineAnswer(model.editInput))
		view.WriteString("Help: Enter commit • Esc cancel\n")
		return view.String()
	}
	status := model.status
	if status == "" {
		status = "Worksheet ready"
	}
	fmt.Fprintf(&view, "\nStatus: %s\n", status)
	view.WriteString("Help: ↑/↓ j/k Ctrl-N/Ctrl-P move • Space skip • q quit\n")
	view.WriteString("Answers: r/y/n 1-5 a-e x preset • i inline • o editor • Esc clear • u undo\n")
	return view.String()
}

func truncateGridAnswer(answer string) string {
	return runewidth.Truncate(singleLineAnswer(answer), gridAnswerWidth, "…")
}

func singleLineAnswer(answer string) string {
	return strings.NewReplacer("\r\n", " ", "\r", " ", "\n", " ").Replace(answer)
}
