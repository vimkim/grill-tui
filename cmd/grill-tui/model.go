package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

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

const resetConfirmationWindow = 2 * time.Second

type interactionMode uint8

const (
	normalMode interactionMode = iota
	startingNumberMode
	resumeNumberMode
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
	resetArmed bool
	resetToken uint64
	resetBy    time.Time
	keymap     keymap
}

type externalEditorFinishedMsg struct {
	answer string
	err    error
}

type resetExpiredMsg struct {
	token uint64
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
	if expired, ok := message.(resetExpiredMsg); ok {
		if model.resetArmed && expired.token == model.resetToken {
			model.resetArmed = false
			model.resetBy = time.Time{}
			model.status = "Reset disarmed after 2 seconds; Worksheet unchanged"
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
		if model.mode != startingNumberMode && model.mode != resumeNumberMode && !model.terminalTooSmall() {
			model = model.ensureSelectionVisible(model.mode, model.status)
		}
		return model, nil
	}
	if mouse, ok := message.(tea.MouseMsg); ok {
		return model.handleMouse(tea.MouseEvent(mouse)), nil
	}
	if key, ok := message.(tea.KeyMsg); ok {
		action, hasAction := model.keymap.actionFor(key.String())
		if model.resetArmed && (!hasAction || action != actionReset) {
			model.resetArmed = false
			model.resetToken++
			model.resetBy = time.Time{}
			model.status = "Reset disarmed; Worksheet unchanged"
		}
		if model.terminalTooSmall() {
			if hasAction && action == actionQuit {
				return model, tea.Quit
			}
			return model, nil
		}
		if model.mode == inlineAnswerMode {
			return model.updateInlineAnswer(key), nil
		}
		if hasAction && action == actionQuit {
			return model, tea.Quit
		}
		if model.helpOpen {
			if hasAction && action == actionHelp {
				model.helpOpen = false
			}
			return model, nil
		}
		if model.mode == startingNumberMode {
			return model.updateStartingNumber(key), nil
		}
		if model.mode == resumeNumberMode {
			return model.updateResumeNumber(key), nil
		}
		if hasAction && action == actionHelp {
			model.helpOpen = true
			return model, nil
		}
		if hasAction && action == actionReset {
			now := time.Now()
			if model.resetArmed && now.Before(model.resetBy) {
				model.resetArmed = false
				model.resetToken++
				model.resetBy = time.Time{}
				if err := resetWorksheetState(); err != nil {
					model.status = fmt.Sprintf("Could not reset Worksheet; state preserved where possible: %v", err)
					return model, nil
				}
				model.worksheet = worksheet{}
				model.mode = startingNumberMode
				model.startInput = ""
				model.editInput = ""
				model.status = "Worksheet reset; enter a positive starting number"
				return model, nil
			}
			model.resetArmed = true
			model.resetToken++
			model.resetBy = now.Add(resetConfirmationWindow)
			token := model.resetToken
			model.status = fmt.Sprintf("Reset armed — press %s again within 2 seconds to discard this Worksheet", model.keymap.labels(actionReset))
			return model, tea.Tick(resetConfirmationWindow, func(time.Time) tea.Msg {
				return resetExpiredMsg{token: token}
			})
		}
		if !hasAction {
			return model, nil
		}
		switch action {
		case actionClear:
			selected := model.worksheet.Selected
			if model.worksheet.answer(selected) == "" {
				model.status = fmt.Sprintf("Answer Slot %d is already empty", selected)
				return model, nil
			}
			candidate := model.worksheet.withClearedAnswer()
			status := fmt.Sprintf("Answer Slot %d cleared", selected)
			return model.persistWorksheet(candidate, status, revealSelectedSlot), nil
		case actionUndo:
			if model.worksheet.Undo == nil {
				model.status = "Nothing to undo"
				return model, nil
			}
			candidate, number := model.worksheet.withUndo()
			status := fmt.Sprintf("Undid Answer Slot %d", number)
			return model.persistWorksheet(candidate, status, revealSelectedSlot), nil
		case actionCopy:
			return model, copyAnswerList(model.worksheet)
		case actionInline:
			model.mode = inlineAnswerMode
			model.editInput = model.worksheet.answer(model.worksheet.Selected)
			model.status = "Editing Custom Answer inline"
			return model, nil
		case actionExternal:
			return model.openExternalEditor()
		case actionMoveDown, actionSkip:
			return model.moveSelection(1), nil
		case actionMoveUp:
			return model.moveSelection(-1), nil
		}
		if answer, ok := model.keymap.answerFor(action); ok {
			status := "Preset Answer committed"
			if action == actionExplain {
				status = "Answer committed"
			}
			return model.commitAnswer(answer, status), nil
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
	}
	return model.updateNumberInput(key)
}

func (model worksheetModel) updateNumberInput(key tea.KeyMsg) worksheetModel {
	if key.String() == "backspace" {
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

func (model worksheetModel) updateResumeNumber(key tea.KeyMsg) worksheetModel {
	switch key.String() {
	case "enter":
		start := model.worksheet.defaultResumeNumber()
		var err error
		if model.startInput != "" {
			start, err = parsePositiveNumber(model.startInput)
		}
		if err != nil {
			model.startInput = ""
			model.status = "Starting number must be a positive integer. Try again."
			return model
		}
		candidate := model.worksheet.expandAndSelect(start)
		model.mode = normalMode
		model.startInput = ""
		return model.persistWorksheet(candidate, "Worksheet resumed", revealSelectedSlot)
	}
	return model.updateNumberInput(key)
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
	answer := model.worksheet.answer(model.worksheet.Selected)
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
		rowOffset := mouse.Y - gridFirstRow
		if rowOffset < 0 || rowOffset >= model.visibleAnswerSlots() || rowOffset > model.worksheet.LastNumber-model.worksheet.Viewport {
			return model
		}
		number := model.worksheet.Viewport + rowOffset
		candidate := model.worksheet
		candidate.Selected = number
		return model.persistWorksheet(candidate, "Answer Slot selected", revealSelectedSlot)
	default:
		return model
	}
}

func (model worksheetModel) scrollViewport(change int) worksheetModel {
	maximumViewport := max(model.worksheet.FirstNumber, model.worksheet.LastNumber-model.visibleAnswerSlots()+1)
	viewport := model.worksheet.Viewport
	if change < 0 {
		if change < model.worksheet.FirstNumber-viewport {
			viewport = model.worksheet.FirstNumber
		} else {
			viewport += change
		}
	} else if change > maximumViewport-viewport {
		viewport = maximumViewport
	} else {
		viewport += change
	}
	if viewport == model.worksheet.Viewport {
		return model
	}
	candidate := model.worksheet
	candidate.Viewport = viewport
	return model.persistWorksheet(candidate, "Worksheet scrolled", preserveViewport)
}

func (model worksheetModel) commitAnswer(answer, successStatus string) worksheetModel {
	candidate := model.worksheet.withCommittedAnswer(answer)
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
	if model.mode == startingNumberMode || model.mode == resumeNumberMode {
		return model.promptView()
	}
	return model.worksheetView()
}

func (model worksheetModel) promptView() string {
	var view strings.Builder
	if model.mode == resumeNumberMode {
		view.WriteString(model.styled(headingStyle, "Grill TUI — Resume Worksheet") + "\n\n")
		lastAnswered, answered := model.worksheet.lastAnsweredNumber()
		last := "none"
		if answered {
			last = strconv.Itoa(lastAnswered)
		}
		fmt.Fprintf(&view, "Last answered: %s. Start at [%d]: %s\n", last, model.worksheet.defaultResumeNumber(), model.startInput)
		if model.status != "" {
			fmt.Fprintf(&view, "\n%s\n", runewidth.Wrap("Status: "+model.status, model.size.width))
		}
		fmt.Fprintf(&view, "\nHelp: Enter resume • %s quit\n", model.keymap.labels(actionQuit))
		return strings.TrimSuffix(view.String(), "\n")
	}
	view.WriteString(model.styled(headingStyle, "Grill TUI — Create a Worksheet") + "\n\n")
	view.WriteString("Enter a positive starting number.\n")
	fmt.Fprintf(&view, "Starting number: %s\n", model.startInput)
	if model.status != "" {
		fmt.Fprintf(&view, "\n%s\n", runewidth.Wrap("Status: "+model.status, model.size.width))
	}
	fmt.Fprintf(&view, "\nHelp: Enter create • %s quit\n", model.keymap.labels(actionQuit))
	return strings.TrimSuffix(view.String(), "\n")
}

func (model worksheetModel) worksheetView() string {
	if model.helpOpen {
		return model.helpView()
	}
	var view strings.Builder
	view.WriteString(model.styled(headingStyle, "Grill TUI — Worksheet") + "\n\n")
	fmt.Fprintf(&view, "%d Answer Slots\n", model.worksheet.slotCount())
	view.WriteString("   No. │ Answer\n")
	view.WriteString("───────┼────────────────────────────────────────\n")
	visibleCount := min(model.visibleAnswerSlots(), model.worksheet.LastNumber-model.worksheet.Viewport+1)
	lastVisible := model.worksheet.Viewport + visibleCount - 1
	answerWidth := model.gridAnswerDisplayWidth(lastVisible)
	for number := model.worksheet.Viewport; number <= lastVisible; number++ {
		marker := " "
		if number == model.worksheet.Selected {
			marker = ">"
		}
		line := fmt.Sprintf("%s %d │ %s", marker, number, truncateGridAnswer(model.worksheet.answer(number), answerWidth))
		if number == model.worksheet.Selected {
			line = model.styled(selectedStyle, line)
		}
		view.WriteString(line + "\n")
		if number == lastVisible {
			break
		}
	}
	selected := model.worksheet.Selected
	preview := selectedPreview(model.worksheet)
	fmt.Fprintf(&view, "\nSelected Answer %d (full):\n%s\n", selected, runewidth.Wrap(preview, model.size.width))
	if model.mode == inlineAnswerMode {
		prefix := fmt.Sprintf("Inline Custom Answer %d: ", selected)
		inputWidth := max(1, model.size.width-runewidth.StringWidth(prefix))
		fmt.Fprintf(&view, "\n%s%s\n", prefix, runewidth.Truncate(singleLineAnswer(model.editInput), inputWidth, "…"))
		view.WriteString("Help: Enter commit • Esc cancel\n")
		return strings.TrimSuffix(view.String(), "\n")
	}
	status := displayedStatus(model.status)
	fmt.Fprintf(&view, "\n%s\n", model.styled(statusStyle, runewidth.Wrap("Status: "+status, model.size.width)))
	view.WriteString(runewidth.Wrap(model.keymap.compactHelp(), model.size.width) + "\n")
	return strings.TrimSuffix(view.String(), "\n")
}

func (model worksheetModel) helpView() string {
	help := model.keymap.completeHelp()
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
		helpRows := wrappedLineCount(model.keymap.compactHelp(), model.size.width)
		overhead = worksheetFixedRows + previewRows + statusRows + helpRows
	}
	return max(1, min(visibleAnswerSlotCount, model.size.height-overhead))
}

func (model worksheetModel) smallTerminalView() string {
	if model.mode == startingNumberMode {
		return model.renderSmallTerminalView("Please resize to create a Worksheet.", "No Worksheet has been created yet.")
	}
	if model.mode == resumeNumberMode {
		return model.renderSmallTerminalView("Please resize to resume the Worksheet.", "Worksheet data is safe.")
	}
	selected := model.worksheet.Selected
	assurance := fmt.Sprintf("Selected Answer Slot %d remains selected.\nWorksheet data is safe.", selected)
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
	prefixWidth := runewidth.StringWidth(strconv.Itoa(lastVisible)) + 5
	return max(1, min(gridAnswerWidth, model.size.width-prefixWidth))
}

func truncateGridAnswer(answer string, width int) string {
	return runewidth.Truncate(singleLineAnswer(answer), width, "…")
}

func singleLineAnswer(answer string) string {
	return strings.NewReplacer("\r\n", " ", "\r", " ", "\n", " ").Replace(answer)
}

func selectedPreview(storedWorksheet worksheet) string {
	preview := storedWorksheet.answer(storedWorksheet.Selected)
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
