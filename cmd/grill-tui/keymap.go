package main

import (
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

type keyAction string

const (
	actionMoveDown      keyAction = "move_down"
	actionMoveUp        keyAction = "move_up"
	actionRebaseDown    keyAction = "rebase_down"
	actionRebaseUp      keyAction = "rebase_up"
	actionJumpFirst     keyAction = "jump_first"
	actionJumpLast      keyAction = "jump_last"
	actionClearAndNext  keyAction = "clear_and_next"
	actionInsertAbove   keyAction = "insert_above"
	actionInsertBelow   keyAction = "insert_below"
	actionDeleteSlot    keyAction = "delete_slot"
	actionRecommended   keyAction = "recommended"
	actionYes           keyAction = "yes"
	actionNo            keyAction = "no"
	actionChoice1       keyAction = "choice_1"
	actionChoice2       keyAction = "choice_2"
	actionChoice3       keyAction = "choice_3"
	actionChoice4       keyAction = "choice_4"
	actionChoice5       keyAction = "choice_5"
	actionChoiceA       keyAction = "choice_a"
	actionChoiceB       keyAction = "choice_b"
	actionChoiceC       keyAction = "choice_c"
	actionChoiceD       keyAction = "choice_d"
	actionChoiceE       keyAction = "choice_e"
	actionChoiceUpperA  keyAction = "choice_upper_a"
	actionChoiceUpperB  keyAction = "choice_upper_b"
	actionExplain       keyAction = "explain"
	actionInline        keyAction = "inline"
	actionExternal      keyAction = "external"
	actionClear         keyAction = "clear"
	actionUndo          keyAction = "undo"
	actionCopy          keyAction = "copy"
	actionReset         keyAction = "reset"
	actionHelp          keyAction = "help"
	actionQuit          keyAction = "quit"
	actionCommitNext    keyAction = "commit"
	actionFinish        keyAction = "finish"
	actionInsertNewline keyAction = "newline"
	actionBackspace     keyAction = "backspace"
	actionPromptSubmit  keyAction = "submit"
	actionPromptErase   keyAction = "erase"
	actionPromptQuit    keyAction = "prompt_quit"
)

type actionDefinition struct {
	action          keyAction
	defaultBindings []string
	mode            interactionMode
	configName      string
	answer          string
	choice          bool
	required        bool
}

var actionDefinitions = []actionDefinition{
	{action: actionMoveDown, defaultBindings: []string{"down", "j", "ctrl+n"}},
	{action: actionMoveUp, defaultBindings: []string{"up", "k", "ctrl+p"}},
	{action: actionRebaseDown, defaultBindings: []string{"left"}},
	{action: actionRebaseUp, defaultBindings: []string{"right"}},
	{action: actionJumpFirst, defaultBindings: []string{"gg"}},
	{action: actionJumpLast, defaultBindings: []string{"G"}},
	{action: actionClearAndNext, configName: "skip", defaultBindings: []string{"space"}},
	{action: actionInsertAbove, defaultBindings: []string{"O"}},
	{action: actionInsertBelow, defaultBindings: []string{"o"}},
	{action: actionDeleteSlot, defaultBindings: []string{"D"}},
	{action: actionRecommended, defaultBindings: []string{"r", "R"}, answer: "recommended"},
	{action: actionYes, defaultBindings: []string{"y"}, answer: "yes"},
	{action: actionNo, defaultBindings: []string{"n", "N"}, answer: "no"},
	{action: actionChoice1, defaultBindings: []string{"1"}, answer: "1", choice: true},
	{action: actionChoice2, defaultBindings: []string{"2"}, answer: "2", choice: true},
	{action: actionChoice3, defaultBindings: []string{"3"}, answer: "3", choice: true},
	{action: actionChoice4, defaultBindings: []string{"4"}, answer: "4", choice: true},
	{action: actionChoice5, defaultBindings: []string{"5"}, answer: "5", choice: true},
	{action: actionChoiceA, defaultBindings: []string{"a"}, answer: "a", choice: true},
	{action: actionChoiceB, defaultBindings: []string{"b"}, answer: "b", choice: true},
	{action: actionChoiceC, defaultBindings: []string{"c"}, answer: "c", choice: true},
	{action: actionChoiceD, defaultBindings: []string{"d"}, answer: "d", choice: true},
	{action: actionChoiceE, defaultBindings: []string{"e"}, answer: "e", choice: true},
	{action: actionChoiceUpperA, defaultBindings: []string{"A"}, answer: "A", choice: true},
	{action: actionChoiceUpperB, defaultBindings: []string{"B"}, answer: "B", choice: true},
	{action: actionExplain, defaultBindings: []string{"x"}, answer: "explain further"},
	{action: actionInline, defaultBindings: []string{"i"}},
	{action: actionExternal, defaultBindings: []string{"E"}},
	{action: actionClear, defaultBindings: []string{"backspace", "delete"}},
	{action: actionUndo, defaultBindings: []string{"u"}},
	{action: actionCopy, defaultBindings: []string{"s", "ctrl+s"}, required: true},
	{action: actionReset, defaultBindings: []string{"ctrl+r"}},
	{action: actionHelp, defaultBindings: []string{"?"}},
	{action: actionQuit, defaultBindings: []string{"q", "ctrl+q", "ctrl+c"}, required: true},
	{action: actionCommitNext, mode: inlineAnswerMode, defaultBindings: []string{"enter"}, required: true},
	{action: actionFinish, mode: inlineAnswerMode, defaultBindings: []string{"esc"}, required: true},
	{action: actionInsertNewline, mode: inlineAnswerMode, defaultBindings: []string{"ctrl+enter", "ctrl+j", "alt+enter"}},
	{action: actionBackspace, mode: inlineAnswerMode, defaultBindings: []string{"backspace"}},
	{action: actionPromptSubmit, mode: promptBindingsMode, defaultBindings: []string{"enter"}, required: true},
	{action: actionPromptErase, mode: promptBindingsMode, defaultBindings: []string{"backspace"}},
	{action: actionPromptQuit, mode: promptBindingsMode, configName: "quit", defaultBindings: []string{"q", "ctrl+q", "ctrl+c"}, required: true},
}

func (definition actionDefinition) name() string {
	if definition.configName != "" {
		return definition.configName
	}
	return string(definition.action)
}

type keymap struct {
	bindings        map[keyAction][]keyBinding
	actions         map[interactionMode]map[string]keyAction
	prefixes        map[interactionMode]map[string]bool
	sequenceTimeout time.Duration
}

type keyBinding struct {
	name  string
	steps []string
}

func defaultKeymap() keymap {
	configured, err := buildKeymap(defaultBindings(), 1000)
	if err != nil {
		panic(fmt.Sprintf("invalid built-in Keymap: %v", err))
	}
	return configured
}

func buildKeymap(bindings map[keyAction][]string, timeoutMS int) (keymap, error) {
	if timeoutMS < 100 || timeoutMS > 5000 {
		return keymap{}, fmt.Errorf("input.sequence_timeout_ms must be within 100–5000 milliseconds, got %d", timeoutMS)
	}
	configured := keymap{
		bindings:        make(map[keyAction][]keyBinding, len(actionDefinitions)),
		actions:         map[interactionMode]map[string]keyAction{normalMode: {}, inlineAnswerMode: {}, promptBindingsMode: {}},
		prefixes:        map[interactionMode]map[string]bool{normalMode: {}, inlineAnswerMode: {}, promptBindingsMode: {}},
		sequenceTimeout: time.Duration(timeoutMS) * time.Millisecond,
	}
	type assignedBinding struct {
		actionName string
		name       string
		steps      []string
	}
	assigned := map[interactionMode][]assignedBinding{}
	for _, definition := range actionDefinitions {
		keys := bindings[definition.action]
		if definition.required && len(keys) == 0 {
			return keymap{}, fmt.Errorf("keymap.%s must contain at least one key binding", definition.name())
		}
		for _, configuredKey := range keys {
			binding, err := parseKeyBinding(configuredKey)
			if err != nil {
				return keymap{}, fmt.Errorf("keymap.%s: %w", definition.name(), err)
			}
			for _, previous := range assigned[definition.mode] {
				if sequenceID(previous.steps) == sequenceID(binding.steps) {
					return keymap{}, fmt.Errorf("keymap binding %q conflicts between %s and %s (bindings %q and %q)", binding.name, previous.actionName, definition.name(), previous.name, binding.name)
				}
				if isSequencePrefix(previous.steps, binding.steps) || isSequencePrefix(binding.steps, previous.steps) {
					return keymap{}, fmt.Errorf("keymap binding %q for %s conflicts with binding %q for %s in the same mode", binding.name, definition.name(), previous.name, previous.actionName)
				}
			}
			assigned[definition.mode] = append(assigned[definition.mode], assignedBinding{actionName: definition.name(), name: binding.name, steps: binding.steps})
			configured.actions[definition.mode][sequenceID(binding.steps)] = definition.action
			for length := 1; length < len(binding.steps); length++ {
				configured.prefixes[definition.mode][sequenceID(binding.steps[:length])] = true
			}
			configured.bindings[definition.action] = append(configured.bindings[definition.action], binding)
		}
	}
	copyHasDependableRoute := false
	for _, binding := range configured.bindings[actionCopy] {
		if binding.name != "ctrl+s" {
			copyHasDependableRoute = true
			break
		}
	}
	if !copyHasDependableRoute {
		return keymap{}, fmt.Errorf("keymap.copy must include a binding other than ctrl+s because terminals may intercept Ctrl-S")
	}
	return configured, nil
}

func isSequencePrefix(prefix, sequence []string) bool {
	if len(prefix) > len(sequence) {
		return false
	}
	for index, step := range prefix {
		if sequence[index] != step {
			return false
		}
	}
	return true
}

func sequenceID(steps []string) string {
	return strings.Join(steps, "\x00")
}

func parseKeyBinding(key string) (keyBinding, error) {
	if single, err := parseKeyName(key); err == nil {
		return keyBinding{name: single, steps: []string{single}}, nil
	}
	parts := strings.Fields(key)
	if len(parts) == 1 && utf8.RuneCountInString(key) > 1 && !strings.Contains(key, "+") {
		parts = make([]string, 0, utf8.RuneCountInString(key))
		for _, character := range key {
			parts = append(parts, string(character))
		}
	}
	if len(parts) < 2 {
		return keyBinding{}, fmt.Errorf("invalid key name %q", key)
	}
	steps := make([]string, 0, len(parts))
	for _, part := range parts {
		step, err := parseKeyName(part)
		if err != nil {
			return keyBinding{}, fmt.Errorf("invalid key sequence %q: %w", key, err)
		}
		steps = append(steps, step)
	}
	return keyBinding{name: key, steps: steps}, nil
}

func parseKeyName(key string) (string, error) {
	aliases := map[string]string{
		"ctrl+i": "tab",
		"ctrl+m": "enter",
		"ctrl+[": "esc",
	}
	if canonical, ok := aliases[key]; ok {
		return canonical, nil
	}
	named := map[string]string{
		"up": "up", "down": "down", "left": "left", "right": "right",
		"enter": "enter", "esc": "esc", "space": " ", "tab": "tab",
		"shift+tab": "shift+tab", "backspace": "backspace", "delete": "delete",
		"home": "home", "end": "end", "pgup": "pgup", "pgdown": "pgdown",
	}
	if canonical, ok := named[key]; ok {
		return canonical, nil
	}
	if strings.HasPrefix(key, "ctrl+") {
		control := strings.TrimPrefix(key, "ctrl+")
		if control == "enter" {
			return key, nil
		}
		if len(control) == 1 && ((control[0] >= 'a' && control[0] <= 'z') || strings.ContainsRune("@[\\]^_", rune(control[0]))) {
			return key, nil
		}
		return "", fmt.Errorf("invalid key name %q; use ctrl+a through ctrl+z or a supported control symbol", key)
	}
	if strings.HasPrefix(key, "alt+") {
		modified := strings.TrimPrefix(key, "alt+")
		if modified == "enter" {
			return key, nil
		}
		if utf8.RuneCountInString(modified) == 1 {
			r, _ := utf8.DecodeRuneInString(modified)
			if unicode.IsPrint(r) && !unicode.IsSpace(r) {
				return key, nil
			}
		}
		return "", fmt.Errorf("invalid key name %q; alt bindings require one printable non-space character", key)
	}
	if strings.HasPrefix(key, "f") {
		functionNumber, err := strconv.Atoi(strings.TrimPrefix(key, "f"))
		if err == nil && functionNumber >= 1 && functionNumber <= 12 {
			return key, nil
		}
	}
	if utf8.RuneCountInString(key) == 1 {
		r, _ := utf8.DecodeRuneInString(key)
		if unicode.IsPrint(r) && !unicode.IsSpace(r) {
			return key, nil
		}
	}
	return "", fmt.Errorf("invalid key name %q", key)
}

func (configured keymap) actionFor(mode interactionMode, steps []string) (keyAction, bool) {
	action, ok := configured.actions[mode][sequenceID(steps)]
	return action, ok
}

func (configured keymap) hasPrefix(mode interactionMode, steps []string) bool {
	return configured.prefixes[mode][sequenceID(steps)]
}

func (configured keymap) mightMatchAction(action keyAction, steps []string) bool {
	for _, binding := range configured.bindings[action] {
		if isSequencePrefix(steps, binding.steps) {
			return true
		}
	}
	return false
}

func (configured keymap) answerFor(action keyAction) (string, bool) {
	for _, definition := range actionDefinitions {
		if definition.action == action && definition.answer != "" {
			return definition.answer, true
		}
	}
	return "", false
}

func (configured keymap) labels(action keyAction) string {
	return configured.labelsWithSeparator(action, "/")
}

func (configured keymap) labelsWithSeparator(action keyAction, separator string) string {
	labels := make([]string, 0, len(configured.bindings[action]))
	for _, binding := range configured.bindings[action] {
		labels = append(labels, binding.display())
	}
	if len(labels) == 0 {
		return "(unbound)"
	}
	return strings.Join(labels, separator)
}

func (binding keyBinding) display() string {
	if len(binding.steps) > 1 {
		if !strings.ContainsAny(binding.name, " \t\n") {
			return binding.name
		}
		labels := make([]string, 0, len(binding.steps))
		for _, step := range binding.steps {
			labels = append(labels, (keyBinding{name: step}).display())
		}
		return strings.Join(labels, " ")
	}
	switch binding.name {
	case " ":
		return "Space"
	case "up":
		return "↑"
	case "down":
		return "↓"
	case "left":
		return "←"
	case "right":
		return "→"
	case "esc":
		return "Esc"
	case "enter":
		return "Enter"
	case "backspace":
		return "Backspace"
	case "delete":
		return "Delete"
	case "tab":
		return "Tab"
	case "shift+tab":
		return "Shift-Tab"
	}
	if strings.HasPrefix(binding.name, "ctrl+") {
		return "Ctrl-" + strings.ToUpper(strings.TrimPrefix(binding.name, "ctrl+"))
	}
	if strings.HasPrefix(binding.name, "alt+") {
		return "Alt-" + strings.TrimPrefix(binding.name, "alt+")
	}
	if len(binding.name) > 1 && binding.name[0] == 'f' {
		return strings.ToUpper(binding.name)
	}
	return binding.name
}

func (configured keymap) completeHelp() string {
	return fmt.Sprintf(`Grill TUI — Complete Help
%s
Jump: %s/%s; Rebase: %s/%s; Next: %s
Presets: %s recommended; %s yes; %s no
Choices: %s
Explain: %s; Prompt submit: %s
Custom: %s inline; %s external editor
Slots: %s/%s insert; %s delete
Insert Mode: %s commit+next; %s finish; %s hard newline
Erase: %s Insert; %s prompt
Correct: %s clear; %s undo
Mouse: click/wheel; Copy: %s
Reset: %s twice within two seconds
Help: %s; Quit: %s
Prompt quit: %s
`,
		configured.movementHelp(), configured.labels(actionJumpFirst), configured.labels(actionJumpLast), configured.labels(actionRebaseDown), configured.labels(actionRebaseUp), configured.labels(actionClearAndNext),
		configured.labels(actionRecommended), configured.labels(actionYes), configured.labels(actionNo),
		configured.choiceSummary(),
		configured.labels(actionExplain), configured.labels(actionPromptSubmit),
		configured.labels(actionInline), configured.labels(actionExternal),
		configured.labels(actionInsertAbove), configured.labels(actionInsertBelow), configured.labels(actionDeleteSlot),
		configured.labels(actionCommitNext), configured.labels(actionFinish), configured.labels(actionInsertNewline),
		configured.labels(actionBackspace), configured.labels(actionPromptErase),
		configured.labels(actionClear), configured.labels(actionUndo), configured.labels(actionCopy), configured.labels(actionReset), configured.labels(actionHelp), configured.labelsWithSeparator(actionQuit, ", "),
		configured.labels(actionPromptQuit))
}

func (configured keymap) compactHelp() string {
	if configured.isDefault() {
		return "Help: ↑/↓ j/k move • ←/→ rebase • gg/G jump • Space clear+next • r/y/n 1-5 a-e A/B x answer • i/E edit • O/o insert D delete • Backspace/Delete clear • u undo • s/Ctrl-S copy • Ctrl-R reset • ? help • q quit"
	}
	return fmt.Sprintf("Help: %s move • %s/%s rebase • %s/%s jump • %s clear+next • %s/%s/%s %s answer • %s/%s edit • %s/%s insert %s delete • %s clear • %s undo • %s copy • %s reset • %s help • %s quit",
		configured.movementCompact(), configured.labels(actionRebaseDown), configured.labels(actionRebaseUp), configured.labels(actionJumpFirst), configured.labels(actionJumpLast), configured.labels(actionClearAndNext),
		configured.labels(actionRecommended), configured.labels(actionYes), configured.labels(actionNo),
		configured.choiceSummary(),
		configured.labels(actionInline), configured.labels(actionExternal), configured.labels(actionInsertAbove), configured.labels(actionInsertBelow), configured.labels(actionDeleteSlot), configured.labels(actionClear),
		configured.labels(actionUndo), configured.labels(actionCopy), configured.labels(actionReset), configured.labels(actionHelp), configured.labels(actionQuit))
}

func (configured keymap) isDefault() bool {
	for _, definition := range actionDefinitions {
		configuredKeys := configured.bindings[definition.action]
		if len(configuredKeys) != len(definition.defaultBindings) {
			return false
		}
		for index, defaultBinding := range definition.defaultBindings {
			parsedDefault, err := parseKeyBinding(defaultBinding)
			if err != nil || configuredKeys[index].name != parsedDefault.name {
				return false
			}
		}
	}
	return true
}

func (configured keymap) movementHelp() string {
	if configured.hasDefaultMovementBindings() {
		return "Move: ↑/↓, j/k, Ctrl-N/Ctrl-P"
	}
	return fmt.Sprintf("Move down: %s; up: %s", configured.labels(actionMoveDown), configured.labels(actionMoveUp))
}

func (configured keymap) movementCompact() string {
	if configured.hasDefaultMovementBindings() {
		return "↑/↓ j/k"
	}
	return configured.labels(actionMoveDown) + "/" + configured.labels(actionMoveUp)
}

func (configured keymap) hasDefaultMovementBindings() bool {
	return configured.labels(actionMoveDown) == "↓/j/Ctrl-N" && configured.labels(actionMoveUp) == "↑/k/Ctrl-P"
}

func (configured keymap) choiceSummary() string {
	choiceDefinitions := make([]actionDefinition, 0, 15)
	for _, definition := range actionDefinitions {
		if definition.choice {
			choiceDefinitions = append(choiceDefinitions, definition)
		}
	}
	allDefault := true
	for _, definition := range choiceDefinitions {
		if configured.labels(definition.action) != definition.answer {
			allDefault = false
			break
		}
	}
	if allDefault {
		return "1–5; a–e; A/B"
	}
	choices := make([]string, 0, len(choiceDefinitions))
	for _, definition := range choiceDefinitions {
		choices = append(choices, configured.labels(definition.action)+"→"+definition.answer)
	}
	return strings.Join(choices, " ")
}
