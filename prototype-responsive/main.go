// THROWAWAY PROTOTYPE: three responsive terminal layouts, switched with [ and ].
// Question: which layout best supports named Worksheets and multiline Insert Mode?
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

type mode uint8

const (
	modeNormal mode = iota
	modeInsert
)

type answerSlot struct {
	number int
	answer string
}

type snapshot struct {
	slots    []answerSlot
	selected int
}

type sequenceExpiredMsg struct{ token uint64 }

type model struct {
	name             string
	slots            []answerSlot
	selected         int
	offset           int
	mode             mode
	editor           textarea.Model
	undo             *snapshot
	status           string
	help             bool
	variant          int
	width            int
	height           int
	pendingG         bool
	sequenceToken    uint64
	keyEnhancements  bool
	lastCopiedAnswer string
}

var variants = []struct {
	key  string
	name string
}{
	{key: "split", name: "Split Workbench"},
	{key: "ledger", name: "Responsive Ledger"},
	{key: "focus", name: "Focus Canvas"},
}

var (
	ink       = lipgloss.Color("#E6EAF2")
	muted     = lipgloss.Color("#7E8799")
	panel     = lipgloss.Color("#252B38")
	accent    = lipgloss.Color("#7DD3FC")
	accentTwo = lipgloss.Color("#C4B5FD")
	success   = lipgloss.Color("#86EFAC")
	warning   = lipgloss.Color("#FDE68A")
	danger    = lipgloss.Color("#FDA4AF")
)

func main() {
	configured, err := parseArgs(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, "prototype:", err)
		os.Exit(2)
	}
	if _, err := tea.NewProgram(configured).Run(); err != nil {
		fmt.Fprintln(os.Stderr, "prototype:", err)
		os.Exit(1)
	}
}

func parseArgs(args []string) (model, error) {
	flags := flag.NewFlagSet("responsive-prototype", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	name := flags.String("name", "abc", "prototype Worksheet Name")
	variantName := flags.String("variant", "split", "split, ledger, or focus")
	if err := flags.Parse(args); err != nil {
		return model{}, err
	}
	if flags.NArg() > 1 {
		return model{}, errors.New("provide at most one starting number")
	}
	start := 11
	if flags.NArg() == 1 {
		parsed, err := strconv.Atoi(flags.Arg(0))
		if err != nil || parsed < 1 {
			return model{}, errors.New("starting number must be positive")
		}
		start = parsed
	}
	variant := -1
	for index, candidate := range variants {
		if candidate.key == *variantName {
			variant = index
			break
		}
	}
	if variant < 0 {
		return model{}, fmt.Errorf("unknown variant %q", *variantName)
	}
	return initialModel(*name, start, variant), nil
}

func initialModel(name string, start, variant int) model {
	slots := make([]answerSlot, 18)
	for index := range slots {
		slots[index].number = start + index
	}
	slots[0].answer = "recommended"
	slots[1].answer = "yes"
	slots[2].answer = "Keep the interface dense enough for rapid answers, but make the current action and selected answer unmistakable."
	slots[4].answer = "The editor should preserve hard newlines.\nSoft wrapping is presentation only."
	slots[7].answer = "3"
	slots[10].answer = "Use one SQLite database per named Worksheet."

	editor := textarea.New()
	editor.ShowLineNumbers = false
	editor.Prompt = ""
	editor.Placeholder = "Write a Custom Answer…"
	editor.CharLimit = 0
	editor.MaxContentHeight = 10_000
	editor.SetVirtualCursor(true)
	editor.KeyMap.InsertNewline.SetEnabled(false)
	styles := textarea.DefaultDarkStyles()
	styles.Focused.Base = lipgloss.NewStyle().Foreground(ink)
	styles.Focused.Text = lipgloss.NewStyle().Foreground(ink)
	styles.Focused.CursorLine = lipgloss.NewStyle()
	styles.Focused.Placeholder = lipgloss.NewStyle().Foreground(muted).Italic(true)
	styles.Cursor.Color = accent
	styles.Cursor.Blink = true
	editor.SetStyles(styles)

	m := model{
		name:    name,
		slots:   slots,
		variant: variant,
		width:   120,
		height:  36,
		editor:  editor,
		status:  "Prototype data loaded · use [ and ] to compare layouts",
	}
	m.configureEditor()
	m.keepVisible()
	return m
}

func (m model) Init() tea.Cmd { return textarea.Blink }

func (m model) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch message := message.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = message.Width, message.Height
		m.configureEditor()
		m.keepVisible()
		return m, nil
	case tea.KeyboardEnhancementsMsg:
		m.keyEnhancements = message.SupportsKeyDisambiguation()
		return m, nil
	case sequenceExpiredMsg:
		if m.pendingG && message.token == m.sequenceToken {
			m.pendingG = false
			m.status = "g prefix expired"
		}
		return m, nil
	case tea.KeyPressMsg:
		if m.mode == modeInsert {
			return m.updateInsert(message)
		}
		return m.updateNormal(message)
	default:
		if m.mode == modeInsert {
			var cmd tea.Cmd
			m.editor, cmd = m.editor.Update(message)
			return m, cmd
		}
	}
	return m, nil
}

func (m model) updateInsert(key tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "esc":
		m.finishEdit(false)
		return m, nil
	case "enter":
		m.finishEdit(true)
		return m, nil
	case "ctrl+enter", "ctrl+j", "alt+enter":
		m.editor.InsertString("\n")
		m.status = "Hard newline inserted"
		return m, nil
	}
	var cmd tea.Cmd
	m.editor, cmd = m.editor.Update(key)
	return m, cmd
}

func (m model) updateNormal(key tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	pressed := key.String()
	if m.help {
		switch pressed {
		case "?":
			m.help = false
			m.status = "Help closed"
		case "q", "ctrl+q", "ctrl+c":
			return m, tea.Quit
		}
		return m, nil
	}

	if m.pendingG {
		if pressed == "g" {
			m.pendingG = false
			m.selected = 0
			m.keepVisible()
			m.status = "Moved to the first Answer Slot"
			return m, nil
		}
		if pressed != "esc" {
			m.pendingG = false
		}
	}
	if pressed == "gg" {
		m.pendingG = false
		m.selected = 0
		m.keepVisible()
		m.status = "Moved to the first Answer Slot"
		return m, nil
	}

	switch pressed {
	case "q", "ctrl+q", "ctrl+c":
		return m, tea.Quit
	case "up", "k", "ctrl+p":
		m.move(-1)
	case "down", "j", "ctrl+n":
		m.move(1)
	case "left":
		m.rebase(-1)
	case "right":
		m.rebase(1)
	case "g":
		m.pendingG = true
		m.sequenceToken++
		token := m.sequenceToken
		m.status = "g… waiting for second g"
		return m, tea.Tick(time.Second, func(time.Time) tea.Msg { return sequenceExpiredMsg{token: token} })
	case "G":
		m.goLastAnswered()
	case "i":
		return m.beginEdit()
	case "O":
		m.insertSlot(false)
	case "o":
		m.insertSlot(true)
	case "D":
		m.deleteSlot()
	case "space":
		m.clearAndAdvance()
	case "backspace", "delete":
		m.clearAndStay()
	case "u":
		m.undoOnce()
	case "s", "ctrl+s", "C", "Y":
		m.lastCopiedAnswer = m.answerList()
		m.status = fmt.Sprintf("Answer List ready · %d bytes (clipboard disabled in prototype)", len(m.lastCopiedAnswer))
	case "E":
		m.status = "External editor would open here (disabled in prototype)"
	case "?":
		m.help = true
	case "[":
		m.variant = (m.variant + len(variants) - 1) % len(variants)
		m.configureEditor()
		m.keepVisible()
		m.status = "Prototype layout · " + variants[m.variant].name
	case "]":
		m.variant = (m.variant + 1) % len(variants)
		m.configureEditor()
		m.keepVisible()
		m.status = "Prototype layout · " + variants[m.variant].name
	case "esc":
		// Deliberately a normal-mode no-op.
	default:
		if answer, ok := presetAnswer(pressed); ok {
			m.commitAndAdvance(answer, "Preset Answer committed")
		}
	}
	return m, nil
}

func presetAnswer(key string) (string, bool) {
	switch key {
	case "r", "R":
		return "recommended", true
	case "y":
		return "yes", true
	case "n", "N":
		return "no", true
	case "1", "2", "3", "4", "5", "a", "b", "c", "d", "e", "A", "B":
		return key, true
	case "x":
		return "explain further", true
	}
	return "", false
}

func (m *model) beginEdit() (tea.Model, tea.Cmd) {
	m.mode = modeInsert
	m.editor.SetValue(m.slots[m.selected].answer)
	m.editor.Focus()
	m.configureEditor()
	m.status = "INSERT · Esc save & stay · Enter save & next"
	return *m, textarea.Blink
}

func (m *model) finishEdit(advance bool) {
	answer := m.editor.Value()
	m.editor.Blur()
	m.mode = modeNormal
	if advance {
		m.commitAndAdvance(answer, "Custom Answer saved")
		return
	}
	m.recordUndo()
	number := m.slots[m.selected].number
	m.slots[m.selected].answer = answer
	m.status = fmt.Sprintf("Custom Answer %d saved · selection stayed", number)
}

func (m *model) configureEditor() {
	contentWidth := min(max(48, m.width-4), 150)
	width := contentWidth - 8
	if contentWidth >= 100 {
		switch variants[m.variant].key {
		case "split":
			width = contentWidth*56/100 - 8
		case "focus":
			width = contentWidth*62/100 - 8
		}
	}
	height := min(14, max(5, m.height-14))
	if contentWidth < 100 {
		height = min(8, max(4, m.height-19))
	}
	if variants[m.variant].key == "ledger" {
		height = min(9, max(4, m.height/3))
	}
	m.editor.SetWidth(max(20, width))
	m.editor.SetHeight(height)
}

func (m *model) recordUndo() {
	m.undo = &snapshot{slots: append([]answerSlot(nil), m.slots...), selected: m.selected}
}

func (m *model) commitAndAdvance(answer, label string) {
	m.recordUndo()
	changed := m.selected
	m.slots[changed].answer = answer
	if changed == len(m.slots)-1 {
		m.slots = append(m.slots, answerSlot{number: m.slots[changed].number + 1})
	}
	m.selected = changed + 1
	m.keepVisible()
	m.status = fmt.Sprintf("%s at %d · advanced to %d", label, m.slots[changed].number, m.slots[m.selected].number)
}

func (m *model) clearAndAdvance() {
	m.recordUndo()
	changed := m.selected
	m.slots[changed].answer = ""
	if changed == len(m.slots)-1 {
		m.slots = append(m.slots, answerSlot{number: m.slots[changed].number + 1})
	}
	m.selected = changed + 1
	m.keepVisible()
	m.status = fmt.Sprintf("Answer %d blanked · advanced", m.slots[changed].number)
}

func (m *model) clearAndStay() {
	if m.slots[m.selected].answer == "" {
		m.status = "Selected Answer Slot is already blank"
		return
	}
	m.recordUndo()
	number := m.slots[m.selected].number
	m.slots[m.selected].answer = ""
	m.status = fmt.Sprintf("Answer %d cleared · selection stayed", number)
}

func (m *model) insertSlot(below bool) {
	m.recordUndo()
	index := m.selected
	if below {
		index++
	}
	number := m.slots[m.selected].number
	if below {
		number++
	}
	m.slots = append(m.slots, answerSlot{})
	copy(m.slots[index+1:], m.slots[index:len(m.slots)-1])
	m.slots[index] = answerSlot{number: number}
	for cursor := index + 1; cursor < len(m.slots); cursor++ {
		m.slots[cursor].number = m.slots[cursor-1].number + 1
	}
	m.selected = index
	m.keepVisible()
	m.status = fmt.Sprintf("Inserted blank Answer Slot %d", number)
}

func (m *model) deleteSlot() {
	if len(m.slots) == 1 {
		m.status = "Cannot delete the only Answer Slot"
		return
	}
	m.recordUndo()
	index := m.selected
	number := m.slots[index].number
	m.slots = append(m.slots[:index], m.slots[index+1:]...)
	for cursor := index; cursor < len(m.slots); cursor++ {
		m.slots[cursor].number = number + cursor - index
	}
	if m.selected >= len(m.slots) {
		m.selected = len(m.slots) - 1
	}
	m.keepVisible()
	m.status = fmt.Sprintf("Deleted Answer Slot %d · following answers shifted up", number)
}

func (m *model) rebase(delta int) {
	if delta < 0 && m.slots[0].number == 1 {
		m.status = "Cannot renumber below 1"
		return
	}
	m.recordUndo()
	for index := range m.slots {
		m.slots[index].number += delta
	}
	m.status = fmt.Sprintf("Worksheet renumbered · now %d–%d", m.slots[0].number, m.slots[len(m.slots)-1].number)
}

func (m *model) undoOnce() {
	if m.undo == nil {
		m.status = "Nothing to undo"
		return
	}
	m.slots = append([]answerSlot(nil), m.undo.slots...)
	m.selected = m.undo.selected
	m.undo = nil
	m.keepVisible()
	m.status = fmt.Sprintf("Undid change · returned to %d", m.slots[m.selected].number)
}

func (m *model) move(delta int) {
	m.selected = clamp(m.selected+delta, 0, len(m.slots)-1)
	m.keepVisible()
	m.status = fmt.Sprintf("Selected Answer Slot %d", m.slots[m.selected].number)
}

func (m *model) goLastAnswered() {
	for index := len(m.slots) - 1; index >= 0; index-- {
		if m.slots[index].answer != "" {
			m.selected = index
			m.keepVisible()
			m.status = fmt.Sprintf("Moved to Last Answered Number %d", m.slots[index].number)
			return
		}
	}
	m.selected = 0
	m.keepVisible()
	m.status = "No answers yet · moved to first Answer Slot"
}

func (m *model) keepVisible() {
	rows := m.visibleRows()
	if m.selected < m.offset {
		m.offset = m.selected
	}
	if m.selected >= m.offset+rows {
		m.offset = m.selected - rows + 1
	}
	maximum := max(0, len(m.slots)-rows)
	m.offset = clamp(m.offset, 0, maximum)
}

func (m model) visibleRows() int {
	switch variants[m.variant].key {
	case "ledger":
		return min(len(m.slots), max(5, m.height/2-4))
	case "focus":
		return min(len(m.slots), max(7, m.height-11))
	default:
		detailHeight := 3
		if m.mode == modeInsert {
			detailHeight = m.editor.Height() + 2
		}
		return min(len(m.slots), max(3, m.height-detailHeight-11))
	}
}

func (m model) answeredCount() int {
	count := 0
	for _, slot := range m.slots {
		if slot.answer != "" {
			count++
		}
	}
	return count
}

func (m model) lastAnswered() int {
	for index := len(m.slots) - 1; index >= 0; index-- {
		if m.slots[index].answer != "" {
			return m.slots[index].number
		}
	}
	return 0
}

func (m model) answerList() string {
	var answers []string
	for _, slot := range m.slots {
		if slot.answer == "" {
			continue
		}
		indent := strings.Repeat(" ", len(strconv.Itoa(slot.number))+2)
		answers = append(answers, fmt.Sprintf("%d. %s", slot.number, strings.ReplaceAll(slot.answer, "\n", "\n"+indent)))
	}
	return strings.Join(answers, "\n")
}

func (m model) View() tea.View {
	view := tea.NewView(m.render())
	view.AltScreen = true
	view.MouseMode = tea.MouseModeCellMotion
	return view
}

func (m model) render() string {
	if m.width < 48 || m.height < 15 {
		return lipgloss.NewStyle().Width(max(20, m.width)).Height(max(5, m.height)).Align(lipgloss.Center, lipgloss.Center).
			Render("Grill TUI\n\nResize to at least 48×15.\nYour prototype data is still in memory.")
	}
	contentWidth := min(m.width-2, 150)
	var content string
	if m.help {
		content = m.renderHelp(contentWidth)
	} else {
		switch variants[m.variant].key {
		case "ledger":
			content = m.renderLedger(contentWidth)
		case "focus":
			content = m.renderFocus(contentWidth)
		default:
			content = m.renderSplit(contentWidth)
		}
	}
	content = lipgloss.JoinVertical(lipgloss.Left, content, m.renderSwitcher(contentWidth))
	return lipgloss.PlaceHorizontal(m.width, lipgloss.Center, content)
}

func (m model) renderHeader(width int) string {
	last := "—"
	if number := m.lastAnswered(); number > 0 {
		last = strconv.Itoa(number)
	}
	title := lipgloss.NewStyle().Bold(true).Foreground(accent).Render("GRILL TUI")
	name := lipgloss.NewStyle().Bold(true).Foreground(ink).Render(m.name)
	stats := fmt.Sprintf("%d–%d  ·  %d/%d answered  ·  last %s", m.slots[0].number, m.slots[len(m.slots)-1].number, m.answeredCount(), len(m.slots), last)
	left := title + "  " + lipgloss.NewStyle().Foreground(muted).Render("Worksheet") + " " + name
	right := lipgloss.NewStyle().Foreground(muted).Render(stats)
	gap := max(1, width-lipgloss.Width(left)-lipgloss.Width(right))
	return left + strings.Repeat(" ", gap) + right
}

func (m model) renderSplit(width int) string {
	header := m.renderHeader(width)
	if width < 100 {
		grid := m.renderGrid(width-2, m.visibleRows(), true)
		detailHeight := 3
		if m.mode == modeInsert {
			detailHeight = m.editor.Height() + 2
		}
		detail := m.renderDetail(width-2, detailHeight)
		return lipgloss.JoinVertical(lipgloss.Left, header, "", grid, detail, m.renderFooter(width))
	}
	leftWidth := width * 42 / 100
	rightWidth := width - leftWidth - 2
	grid := m.renderGrid(leftWidth, m.visibleRows(), true)
	detail := m.renderDetail(rightWidth, max(10, m.height-9))
	body := lipgloss.JoinHorizontal(lipgloss.Top, grid, "  ", detail)
	return lipgloss.JoinVertical(lipgloss.Left, header, "", body, m.renderFooter(width))
}

func (m model) renderLedger(width int) string {
	header := m.renderHeader(width)
	grid := m.renderGrid(width, m.visibleRows(), false)
	detailHeight := max(5, m.height-lipgloss.Height(grid)-9)
	detail := m.renderDetail(width, detailHeight)
	return lipgloss.JoinVertical(lipgloss.Left, header, lipgloss.NewStyle().Foreground(muted).Render("RESPONSIVE LEDGER · answers stay central at every width"), "", grid, detail, m.renderFooter(width))
}

func (m model) renderFocus(width int) string {
	header := m.renderHeader(width)
	if width < 100 {
		rail := m.renderNumberRail(width)
		detail := m.renderDetail(width, max(8, m.height-10))
		return lipgloss.JoinVertical(lipgloss.Left, header, rail, detail, m.renderFooter(width))
	}
	railWidth := 24
	guideWidth := 25
	mainWidth := width - railWidth - guideWidth - 4
	rail := m.renderCompactRail(railWidth)
	canvas := m.renderDetail(mainWidth, max(12, m.height-9))
	guide := m.renderGuide(guideWidth, max(12, m.height-9))
	body := lipgloss.JoinHorizontal(lipgloss.Top, rail, "  ", canvas, "  ", guide)
	return lipgloss.JoinVertical(lipgloss.Left, header, "", body, m.renderFooter(width))
}

func (m model) renderGrid(width, rows int, boxed bool) string {
	inner := max(20, width-4)
	if boxed {
		// Lip Gloss applies Width before the pane's padding and border budget.
		inner = max(20, width-8)
	}
	answerWidth := max(10, inner-10)
	lines := []string{
		lipgloss.NewStyle().Foreground(muted).Render(fmt.Sprintf("  %-6s %s", "NO.", "ANSWER")),
		lipgloss.NewStyle().Foreground(panel).Render(strings.Repeat("─", inner)),
	}
	end := min(len(m.slots), m.offset+rows)
	for index := m.offset; index < end; index++ {
		slot := m.slots[index]
		marker := " "
		style := lipgloss.NewStyle().Foreground(ink)
		if index == m.selected {
			marker = "›"
			style = style.Bold(true).Foreground(accent)
		}
		answer := slot.answer
		if answer == "" {
			answer = "·"
			style = style.Foreground(muted)
		}
		answer = strings.ReplaceAll(answer, "\n", " ↵ ")
		line := fmt.Sprintf("%s %-6d %s", marker, slot.number, ansi.Truncate(answer, answerWidth, "…"))
		lines = append(lines, style.Render(line))
	}
	content := strings.Join(lines, "\n")
	if !boxed {
		return lipgloss.NewStyle().Width(width).Render(content)
	}
	return paneStyle(width).Render(content)
}

func (m model) renderDetail(width, height int) string {
	slot := m.slots[m.selected]
	title := fmt.Sprintf("ANSWER %d", slot.number)
	var body string
	if m.mode == modeInsert {
		title = fmt.Sprintf("INSERT · ANSWER %d", slot.number)
		body = m.editor.View()
	} else if slot.answer == "" {
		body = lipgloss.NewStyle().Foreground(muted).Italic(true).Render("No answer yet. Press i to write one.")
	} else {
		body = lipgloss.NewStyle().Width(max(10, width-8)).Foreground(ink).Render(slot.answer)
		body = cropLines(body, max(1, height-2))
	}
	labelColor := accentTwo
	if m.mode == modeInsert {
		labelColor = success
	}
	label := lipgloss.NewStyle().Bold(true).Foreground(labelColor).Render(title)
	return paneStyle(width).Height(max(4, height)).Render(label + "\n\n" + body)
}

func (m model) renderNumberRail(width int) string {
	var pieces []string
	end := min(len(m.slots), m.offset+m.visibleRows())
	for index := m.offset; index < end; index++ {
		style := lipgloss.NewStyle().Foreground(muted)
		marker := "○"
		if m.slots[index].answer != "" {
			marker = "●"
		}
		if index == m.selected {
			style = style.Bold(true).Foreground(accent)
			marker = "◆"
		}
		pieces = append(pieces, style.Render(fmt.Sprintf("%s %d", marker, m.slots[index].number)))
	}
	return paneStyle(width).Render("QUESTIONS  " + strings.Join(pieces, "   "))
}

func (m model) renderCompactRail(width int) string {
	lines := []string{lipgloss.NewStyle().Bold(true).Foreground(accentTwo).Render("QUESTION MAP"), ""}
	end := min(len(m.slots), m.offset+m.visibleRows())
	for index := m.offset; index < end; index++ {
		marker := "○"
		style := lipgloss.NewStyle().Foreground(muted)
		if m.slots[index].answer != "" {
			marker = "●"
			style = style.Foreground(ink)
		}
		if index == m.selected {
			marker = "◆"
			style = style.Bold(true).Foreground(accent)
		}
		lines = append(lines, style.Render(fmt.Sprintf("%s  %d", marker, m.slots[index].number)))
	}
	return paneStyle(width).Render(strings.Join(lines, "\n"))
}

func (m model) renderGuide(width, height int) string {
	sections := []string{
		lipgloss.NewStyle().Bold(true).Foreground(accentTwo).Render("QUICK ACTIONS"),
		"",
		keyHint("i", "edit answer"),
		keyHint("Space", "blank + next"),
		keyHint("O / o", "insert slot"),
		keyHint("D", "delete slot"),
		keyHint("gg / G", "top / bottom"),
		keyHint("C / Y", "copy answers"),
		"",
		lipgloss.NewStyle().Foreground(muted).Render("? opens every binding"),
	}
	return paneStyle(width).Height(height).Render(strings.Join(sections, "\n"))
}

func (m model) renderFooter(width int) string {
	statusStyle := lipgloss.NewStyle().Foreground(muted)
	if strings.Contains(strings.ToLower(m.status), "cannot") {
		statusStyle = statusStyle.Foreground(danger)
	}
	status := ansi.Truncate(m.status, width, "…")
	var actions string
	if m.mode == modeInsert {
		newline := "Ctrl-J newline"
		if m.keyEnhancements {
			newline = "Ctrl-Enter newline"
		}
		actions = "Esc save & stay  ·  Enter save & next  ·  " + newline + "  ·  Ctrl-W delete word"
	} else {
		actions = "↑↓ move  ·  Space blank & next  ·  i edit  ·  C/Y copy  ·  ? all keys"
	}
	return lipgloss.JoinVertical(lipgloss.Left,
		statusStyle.Render("● "+status),
		lipgloss.NewStyle().Foreground(ink).Render(ansi.Truncate(actions, width, "…")),
	)
}

func (m model) renderHelp(width int) string {
	section := func(title, body string) string {
		return lipgloss.NewStyle().Bold(true).Foreground(accentTwo).Render(title) + "\n" + lipgloss.NewStyle().Foreground(ink).Render(body)
	}
	columns := []string{
		section("NAVIGATION", "↑/↓ j/k move\ngg first · G last answered\n←/→ renumber all"),
		section("ANSWERING", "r/y/n · 1–5 · a–e · A/B\nx explain further\nSpace blank + next"),
		section("EDITING", "i Insert Mode · E external\nBackspace/Delete clear\nu undo"),
		section("STRUCTURE", "O insert above · o below\nD delete and shift up"),
		section("OUTPUT", "s/Ctrl-S/C/Y copy\nquery and result in production"),
		section("WORKSHEET", "? close help\nq/Ctrl-Q/Ctrl-C quit\n[ / ] prototype variant"),
	}
	if width >= 110 {
		colWidth := (width - 4) / 3
		for index := range columns {
			columns[index] = paneStyle(colWidth).Height(8).Render(columns[index])
		}
		rowOne := lipgloss.JoinHorizontal(lipgloss.Top, columns[0], "  ", columns[1], "  ", columns[2])
		rowTwo := lipgloss.JoinHorizontal(lipgloss.Top, columns[3], "  ", columns[4], "  ", columns[5])
		return lipgloss.JoinVertical(lipgloss.Left, m.renderHeader(width), "", rowOne, "", rowTwo, m.renderFooter(width))
	}
	return lipgloss.JoinVertical(lipgloss.Left, m.renderHeader(width), "", paneStyle(width).Render(strings.Join(columns, "\n\n")), m.renderFooter(width))
}

func (m model) renderSwitcher(width int) string {
	label := fmt.Sprintf("← [   %s · %s   ] →", strings.ToUpper(variants[m.variant].key), variants[m.variant].name)
	return lipgloss.NewStyle().Width(width).Align(lipgloss.Center).Bold(true).Foreground(lipgloss.Color("#111827")).Background(accent).Render(label)
}

func paneStyle(width int) lipgloss.Style {
	// Width is the complete pane budget. Border and horizontal padding consume
	// four cells, so reserve those before setting the content width.
	return lipgloss.NewStyle().Width(max(8, width-4)).Border(lipgloss.RoundedBorder()).BorderForeground(panel).Padding(0, 1)
}

func keyHint(key, label string) string {
	return lipgloss.NewStyle().Bold(true).Foreground(accent).Render(key) + "  " + lipgloss.NewStyle().Foreground(ink).Render(label)
}

func cropLines(value string, count int) string {
	lines := strings.Split(value, "\n")
	if len(lines) <= count {
		return value
	}
	lines = lines[:count]
	last := len(lines) - 1
	lines[last] = ansi.Truncate(lines[last], max(1, lipgloss.Width(lines[last])-1), "…")
	return strings.Join(lines, "\n")
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func clamp(value, low, high int) int {
	return min(max(value, low), high)
}
