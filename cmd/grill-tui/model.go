package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/mattn/go-runewidth"
)

const (
	gridAnswerWidth    = 40
	worksheetFixedRows = 8
	inlineFixedRows    = 10
	gridFirstRow       = 5
	mouseWheelStep     = 3
	headingStyle       = "1;36"
	selectedStyle      = "1;33"
	statusStyle        = "36"
	warningStyle       = "1;31"
)

type terminalSize struct {
	width  int
	height int
}

var (
	defaultTerminalSize = terminalSize{width: 80, height: 24}
	minimumTerminalSize = terminalSize{width: 50, height: 15}
)

type interactionMode uint8

const (
	normalMode interactionMode = iota
	startingNumberMode
	inlineAnswerMode
)

type viewportPolicy uint8

const (
	revealSelectedSlot viewportPolicy = iota
	preserveViewport
)

type worksheetModel struct {
	worksheet  worksheet
	mode       interactionMode
	startInput string
	editInput  string
	status     string
	size       terminalSize
	helpOpen   bool
	useColor   bool
}

type externalEditorFinishedMsg struct {
	answer string
	err    error
}

func (worksheetModel) Init() tea.Cmd {
	return nil
}

func (model worksheetModel) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	if copied, ok := message.(clipboardResultMsg); ok {
		if copied.empty {
			model.status = "No answers to copy; clipboard unchanged"
		} else if copied.err != nil {
			model.status = fmt.Sprintf("Could not copy Answer List: %v", copied.err)
		} else {
			model.status = fmt.Sprintf("Answer List copied using %s", copied.backend)
		}
		return model, nil
	}
	if finished, ok := message.(externalEditorFinishedMsg); ok {
		if finished.err != nil {
			model.status = fmt.Sprintf("External editor failed; Answer Slot unchanged: %v", finished.err)
			return model, nil
		}
		return model.commitAnswer(finished.answer, "Custom Answer committed from external editor"), nil
	}
	if size, ok := message.(tea.WindowSizeMsg); ok {
		model.size = terminalSize{width: size.Width, height: size.Height}
		if model.mode != startingNumberMode && !model.terminalTooSmall() {
			model = model.ensureSelectionVisible(model.mode, model.status)
		}
		return model, nil
	}
	if mouse, ok := message.(tea.MouseMsg); ok {
		return model.handleMouse(tea.MouseEvent(mouse)), nil
	}
	if key, ok := message.(tea.KeyMsg); ok {
		if model.terminalTooSmall() {
			switch key.String() {
			case "q", "ctrl+q", "ctrl+c":
				return model, tea.Quit
			default:
				return model, nil
			}
		}
		if model.mode == inlineAnswerMode {
			return model.updateInlineAnswer(key), nil
		}
		switch key.String() {
		case "q", "ctrl+q", "ctrl+c":
			return model, tea.Quit
		}
		if model.helpOpen {
			if key.String() == "?" {
				model.helpOpen = false
			}
			return model, nil
		}
		if model.mode == startingNumberMode {
			return model.updateStartingNumber(key), nil
		}
		if key.String() == "?" {
			model.helpOpen = true
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
			status := fmt.Sprintf("Answer Slot %d cleared", selected.Number)
			return model.persistWorksheet(candidate, status, revealSelectedSlot), nil
		case "u":
			if model.worksheet.Undo == nil {
				model.status = "Nothing to undo"
				return model, nil
			}
			candidate, number := model.worksheet.withUndo()
			status := fmt.Sprintf("Undid Answer Slot %d", number)
			return model.persistWorksheet(candidate, status, revealSelectedSlot), nil
		case "s", "ctrl+s":
			return model, copyAnswerList(model.worksheet)
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

func (model worksheetModel) updateInlineAnswer(key tea.KeyMsg) worksheetModel {
	switch key.String() {
	case "enter":
		model.mode = normalMode
		return model.commitAnswer(model.editInput, "Custom Answer committed")
	case "esc":
		model.mode = normalMode
		model.editInput = ""
		model.status = "Inline Custom Answer cancelled"
		return model.ensureSelectionVisible(model.mode, model.status)
	case "backspace":
		runes := []rune(model.editInput)
		if len(runes) > 0 {
			model.editInput = string(runes[:len(runes)-1])
		}
		return model
	}
	for _, typed := range key.Runes {
		if typed != '\n' && typed != '\r' {
			model.editInput += string(typed)
		}
	}
	return model
}

func (model worksheetModel) updateStartingNumber(key tea.KeyMsg) worksheetModel {
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
			return model
		}
		created := newWorksheet(start)
		if err := saveWorksheet(created); err != nil {
			model.status = fmt.Sprintf("Could not create Worksheet: %v", err)
			return model
		}
		model.worksheet = created
		model.mode = normalMode
		model.startInput = ""
		model.status = "Worksheet created"
		return model
	case "backspace":
		if len(model.startInput) > 0 {
			model.startInput = model.startInput[:len(model.startInput)-1]
		}
		return model
	}
	if len(key.Runes) > 0 {
		for _, typed := range key.Runes {
			if typed < '0' || typed > '9' {
				model.status = "Starting number must contain digits only. Try again."
				return model
			}
		}
		model.startInput += string(key.Runes)
		model.status = ""
		return model
	}
	model.status = "Starting number must contain digits only. Try again."
	return model
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

func (model worksheetModel) handleMouse(mouse tea.MouseEvent) worksheetModel {
	if model.mode != normalMode || model.helpOpen || model.terminalTooSmall() {
		return model
	}
	switch mouse.Button {
	case tea.MouseButtonWheelUp:
		return model.scrollViewport(-mouseWheelStep)
	case tea.MouseButtonWheelDown:
		return model.scrollViewport(mouseWheelStep)
	case tea.MouseButtonLeft:
		if mouse.Action != tea.MouseActionPress {
			return model
		}
		index := model.worksheet.Viewport + mouse.Y - gridFirstRow
		if index < model.worksheet.Viewport || index >= len(model.worksheet.Slots) || index >= model.worksheet.Viewport+model.visibleAnswerSlots() {
			return model
		}
		candidate := model.worksheet
		candidate.Selected = index
		return model.persistWorksheet(candidate, "Answer Slot selected", revealSelectedSlot)
	default:
		return model
	}
}

func (model worksheetModel) scrollViewport(change int) worksheetModel {
	maximumViewport := max(0, len(model.worksheet.Slots)-model.visibleAnswerSlots())
	viewport := max(0, min(maximumViewport, model.worksheet.Viewport+change))
	if viewport == model.worksheet.Viewport {
		return model
	}
	candidate := model.worksheet
	candidate.Viewport = viewport
	return model.persistWorksheet(candidate, "Worksheet scrolled", preserveViewport)
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
	return model.persistWorksheet(candidate, successStatus, revealSelectedSlot)
}

func (model worksheetModel) moveSelection(change int) worksheetModel {
	candidate, moved := model.worksheet.withSelection(change)
	if !moved {
		return model
	}
	const status = "Selection moved"
	return model.persistWorksheet(candidate, status, revealSelectedSlot)
}

func (model worksheetModel) persistWorksheet(candidate worksheet, successStatus string, viewport viewportPolicy) worksheetModel {
	if viewport == revealSelectedSlot {
		candidate.revealSelectionWithin(model.visibleAnswerSlotsFor(candidate, model.mode, successStatus))
	}
	if err := saveWorksheet(candidate); err != nil {
		model.status = fmt.Sprintf("Could not save Worksheet: %v", err)
		model.worksheet.revealSelectionWithin(model.visibleAnswerSlotsFor(model.worksheet, model.mode, model.status))
		return model
	}
	model.worksheet = candidate
	model.status = successStatus
	return model
}

func (model worksheetModel) ensureSelectionVisible(mode interactionMode, status string) worksheetModel {
	candidate := model.worksheet
	candidate.revealSelectionWithin(model.visibleAnswerSlotsFor(candidate, mode, status))
	if candidate.Viewport == model.worksheet.Viewport {
		return model
	}
	return model.persistWorksheet(candidate, status, preserveViewport)
}

func (model worksheetModel) View() string {
	if model.terminalTooSmall() {
		return model.smallTerminalView()
	}
	if model.mode == startingNumberMode {
		return model.promptView()
	}
	return model.worksheetView()
}

func (model worksheetModel) promptView() string {
	var view strings.Builder
	view.WriteString(model.styled(headingStyle, "Grill TUI — Create a Worksheet") + "\n\n")
	view.WriteString("Enter a positive starting number.\n")
	fmt.Fprintf(&view, "Starting number: %s\n", model.startInput)
	if model.status != "" {
		fmt.Fprintf(&view, "\n%s\n", runewidth.Wrap("Status: "+model.status, model.size.width))
	}
	view.WriteString("\nHelp: Enter create • q / Ctrl-Q / Ctrl-C quit\n")
	return strings.TrimSuffix(view.String(), "\n")
}

func (model worksheetModel) worksheetView() string {
	if model.helpOpen {
		return model.helpView()
	}
	var view strings.Builder
	view.WriteString(model.styled(headingStyle, "Grill TUI — Worksheet") + "\n\n")
	fmt.Fprintf(&view, "%d Answer Slots\n", len(model.worksheet.Slots))
	view.WriteString("   No. │ Answer\n")
	view.WriteString("───────┼────────────────────────────────────────\n")
	lastVisible := min(model.worksheet.Viewport+model.visibleAnswerSlots(), len(model.worksheet.Slots))
	answerWidth := model.gridAnswerDisplayWidth(lastVisible)
	for index := model.worksheet.Viewport; index < lastVisible; index++ {
		slot := model.worksheet.Slots[index]
		marker := " "
		if index == model.worksheet.Selected {
			marker = ">"
		}
		line := fmt.Sprintf("%s %d │ %s", marker, slot.Number, truncateGridAnswer(slot.Answer, answerWidth))
		if index == model.worksheet.Selected {
			line = model.styled(selectedStyle, line)
		}
		view.WriteString(line + "\n")
	}
	selected := model.worksheet.Slots[model.worksheet.Selected]
	preview := selectedPreview(model.worksheet)
	fmt.Fprintf(&view, "\nSelected Answer %d (full):\n%s\n", selected.Number, runewidth.Wrap(preview, model.size.width))
	if model.mode == inlineAnswerMode {
		prefix := fmt.Sprintf("Inline Custom Answer %d: ", selected.Number)
		inputWidth := max(1, model.size.width-runewidth.StringWidth(prefix))
		fmt.Fprintf(&view, "\n%s%s\n", prefix, runewidth.Truncate(singleLineAnswer(model.editInput), inputWidth, "…"))
		view.WriteString("Help: Enter commit • Esc cancel\n")
		return strings.TrimSuffix(view.String(), "\n")
	}
	status := displayedStatus(model.status)
	fmt.Fprintf(&view, "\n%s\n", model.styled(statusStyle, runewidth.Wrap("Status: "+status, model.size.width)))
	view.WriteString(runewidth.Wrap(compactHelp, model.size.width) + "\n")
	return strings.TrimSuffix(view.String(), "\n")
}

func (model worksheetModel) helpView() string {
	help := `Grill TUI — Complete Help

Move: ↑/↓, j/k, Ctrl-N/Ctrl-P
Skip: Space
Presets: r/R recommended; y/Y yes; n/N no
Choices: 1–5; a–e/A–E
Explain: x
Custom: i inline; o external editor
Inline edit: Enter commit; Esc cancel
Correct: Esc clear; u undo
Mouse: left click select; wheel scroll
Copy: s/Ctrl-S
Help: ?; Quit: q, Ctrl-Q, Ctrl-C

Press ? to close; Worksheet remains unchanged.
`
	help = runewidth.Wrap(strings.TrimSuffix(help, "\n"), model.size.width)
	return strings.Replace(help, "Grill TUI — Complete Help", model.styled(headingStyle, "Grill TUI — Complete Help"), 1)
}

func (model worksheetModel) terminalTooSmall() bool {
	return model.size.width < minimumTerminalSize.width || model.size.height < minimumTerminalSize.height
}

func (model worksheetModel) visibleAnswerSlots() int {
	return model.visibleAnswerSlotsFor(model.worksheet, model.mode, model.status)
}

func (model worksheetModel) visibleAnswerSlotsFor(storedWorksheet worksheet, mode interactionMode, status string) int {
	previewRows := wrappedLineCount(selectedPreview(storedWorksheet), model.size.width)
	overhead := inlineFixedRows + previewRows
	if mode != inlineAnswerMode {
		statusRows := wrappedLineCount("Status: "+displayedStatus(status), model.size.width)
		helpRows := wrappedLineCount(compactHelp, model.size.width)
		overhead = worksheetFixedRows + previewRows + statusRows + helpRows
	}
	return max(1, min(visibleAnswerSlotCount, model.size.height-overhead))
}

func (model worksheetModel) smallTerminalView() string {
	if model.mode == startingNumberMode {
		return model.renderSmallTerminalView("Please resize to create a Worksheet.", "No Worksheet has been created yet.")
	}
	selected := model.worksheet.Slots[model.worksheet.Selected]
	assurance := fmt.Sprintf("Selected Answer Slot %d remains selected.\nWorksheet data is safe.", selected.Number)
	return model.renderSmallTerminalView("Please resize to return to the Worksheet.", assurance)
}

func (model worksheetModel) renderSmallTerminalView(instruction, assurance string) string {
	const heading = "Grill TUI — Terminal too small"
	view := fmt.Sprintf("%s\n\n%d×%d available; at least %d×%d is required.\n%s\n\n%s",
		heading, model.size.width, model.size.height, minimumTerminalSize.width, minimumTerminalSize.height, instruction, assurance)
	return strings.Replace(view, heading, model.styled(warningStyle, heading), 1)
}

func (model worksheetModel) styled(code, text string) string {
	if !model.useColor {
		return text
	}
	return "\x1b[" + code + "m" + text + "\x1b[0m"
}

func (model worksheetModel) gridAnswerDisplayWidth(lastVisible int) int {
	lastNumber := model.worksheet.Slots[lastVisible-1].Number
	prefixWidth := runewidth.StringWidth(strconv.Itoa(lastNumber)) + 5
	return max(1, min(gridAnswerWidth, model.size.width-prefixWidth))
}

func truncateGridAnswer(answer string, width int) string {
	return runewidth.Truncate(singleLineAnswer(answer), width, "…")
}

func singleLineAnswer(answer string) string {
	return strings.NewReplacer("\r\n", " ", "\r", " ", "\n", " ").Replace(answer)
}

func selectedPreview(storedWorksheet worksheet) string {
	preview := storedWorksheet.Slots[storedWorksheet.Selected].Answer
	if preview == "" {
		return "(empty)"
	}
	return preview
}

func displayedStatus(status string) string {
	if status == "" {
		return "Worksheet ready"
	}
	return status
}

func wrappedLineCount(text string, width int) int {
	return strings.Count(runewidth.Wrap(text, width), "\n") + 1
}

const compactHelp = "Help: ↑/↓ j/k move • Space skip • r/y/n 1-5 a-e x answer • i/o custom • Esc clear • u undo • s/Ctrl-S copy • ? help • q quit"
