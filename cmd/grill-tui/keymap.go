package main

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

type keyAction string

const (
	actionMoveDown     keyAction = "move_down"
	actionMoveUp       keyAction = "move_up"
	actionSkip         keyAction = "skip"
	actionRecommended  keyAction = "recommended"
	actionYes          keyAction = "yes"
	actionNo           keyAction = "no"
	actionChoice1      keyAction = "choice_1"
	actionChoice2      keyAction = "choice_2"
	actionChoice3      keyAction = "choice_3"
	actionChoice4      keyAction = "choice_4"
	actionChoice5      keyAction = "choice_5"
	actionChoiceA      keyAction = "choice_a"
	actionChoiceB      keyAction = "choice_b"
	actionChoiceC      keyAction = "choice_c"
	actionChoiceD      keyAction = "choice_d"
	actionChoiceE      keyAction = "choice_e"
	actionChoiceUpperA keyAction = "choice_upper_a"
	actionChoiceUpperB keyAction = "choice_upper_b"
	actionChoiceUpperC keyAction = "choice_upper_c"
	actionChoiceUpperD keyAction = "choice_upper_d"
	actionChoiceUpperE keyAction = "choice_upper_e"
	actionExplain      keyAction = "explain"
	actionInline       keyAction = "inline"
	actionExternal     keyAction = "external"
	actionClear        keyAction = "clear"
	actionUndo         keyAction = "undo"
	actionCopy         keyAction = "copy"
	actionReset        keyAction = "reset"
	actionHelp         keyAction = "help"
	actionQuit         keyAction = "quit"
)

type actionDefinition struct {
	action          keyAction
	defaultBindings []string
	answer          string
	choice          bool
	required        bool
}

var actionDefinitions = []actionDefinition{
	{action: actionMoveDown, defaultBindings: []string{"down", "j", "ctrl+n"}},
	{action: actionMoveUp, defaultBindings: []string{"up", "k", "ctrl+p"}},
	{action: actionSkip, defaultBindings: []string{"space"}},
	{action: actionRecommended, defaultBindings: []string{"r", "R"}, answer: "recommended"},
	{action: actionYes, defaultBindings: []string{"y", "Y"}, answer: "yes"},
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
	{action: actionChoiceUpperC, defaultBindings: []string{"C"}, answer: "C", choice: true},
	{action: actionChoiceUpperD, defaultBindings: []string{"D"}, answer: "D", choice: true},
	{action: actionChoiceUpperE, defaultBindings: []string{"E"}, answer: "E", choice: true},
	{action: actionExplain, defaultBindings: []string{"x"}, answer: "explain further"},
	{action: actionInline, defaultBindings: []string{"i"}},
	{action: actionExternal, defaultBindings: []string{"o"}},
	{action: actionClear, defaultBindings: []string{"esc"}},
	{action: actionUndo, defaultBindings: []string{"u"}},
	{action: actionCopy, defaultBindings: []string{"s", "ctrl+s"}, required: true},
	{action: actionReset, defaultBindings: []string{"ctrl+r"}},
	{action: actionHelp, defaultBindings: []string{"?"}},
	{action: actionQuit, defaultBindings: []string{"q", "ctrl+q", "ctrl+c"}, required: true},
}

type keymap struct {
	bindings map[keyAction][]keyBinding
	actions  map[string]keyAction
}

type keyBinding struct {
	name string
}

func defaultKeymap() keymap {
	configured, err := buildKeymap(defaultBindings())
	if err != nil {
		panic(fmt.Sprintf("invalid built-in Keymap: %v", err))
	}
	return configured
}

func buildKeymap(bindings map[keyAction][]string) (keymap, error) {
	configured := keymap{
		bindings: make(map[keyAction][]keyBinding, len(actionDefinitions)),
		actions:  make(map[string]keyAction),
	}
	for _, definition := range actionDefinitions {
		keys := bindings[definition.action]
		if definition.required && len(keys) == 0 {
			return keymap{}, fmt.Errorf("keymap.%s must contain at least one key binding", definition.action)
		}
		for _, configuredKey := range keys {
			binding, err := parseKeyBinding(configuredKey)
			if err != nil {
				return keymap{}, fmt.Errorf("keymap.%s: %w", definition.action, err)
			}
			if previous, exists := configured.actions[binding.name]; exists {
				return keymap{}, fmt.Errorf("keymap binding %q conflicts between %s and %s", configuredKey, previous, definition.action)
			}
			configured.actions[binding.name] = definition.action
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

func parseKeyBinding(key string) (keyBinding, error) {
	aliases := map[string]string{
		"ctrl+i": "tab",
		"ctrl+m": "enter",
		"ctrl+[": "esc",
	}
	if canonical, ok := aliases[key]; ok {
		return keyBinding{name: canonical}, nil
	}
	named := map[string]string{
		"up": "up", "down": "down", "left": "left", "right": "right",
		"enter": "enter", "esc": "esc", "space": " ", "tab": "tab",
		"shift+tab": "shift+tab", "backspace": "backspace", "delete": "delete",
		"home": "home", "end": "end", "pgup": "pgup", "pgdown": "pgdown",
	}
	if canonical, ok := named[key]; ok {
		return keyBinding{name: canonical}, nil
	}
	if strings.HasPrefix(key, "ctrl+") {
		control := strings.TrimPrefix(key, "ctrl+")
		if len(control) == 1 && ((control[0] >= 'a' && control[0] <= 'z') || strings.ContainsRune("@[\\]^_", rune(control[0]))) {
			return keyBinding{name: key}, nil
		}
		return keyBinding{}, fmt.Errorf("invalid key name %q; use ctrl+a through ctrl+z or a supported control symbol", key)
	}
	if strings.HasPrefix(key, "alt+") {
		modified := strings.TrimPrefix(key, "alt+")
		if utf8.RuneCountInString(modified) == 1 {
			r, _ := utf8.DecodeRuneInString(modified)
			if unicode.IsPrint(r) && !unicode.IsSpace(r) {
				return keyBinding{name: key}, nil
			}
		}
		return keyBinding{}, fmt.Errorf("invalid key name %q; alt bindings require one printable non-space character", key)
	}
	if strings.HasPrefix(key, "f") {
		functionNumber, err := strconv.Atoi(strings.TrimPrefix(key, "f"))
		if err == nil && functionNumber >= 1 && functionNumber <= 12 {
			return keyBinding{name: key}, nil
		}
	}
	if utf8.RuneCountInString(key) == 1 {
		r, _ := utf8.DecodeRuneInString(key)
		if unicode.IsPrint(r) && !unicode.IsSpace(r) {
			return keyBinding{name: key}, nil
		}
	}
	return keyBinding{}, fmt.Errorf("invalid key name %q", key)
}

func (configured keymap) actionFor(key string) (keyAction, bool) {
	action, ok := configured.actions[key]
	return action, ok
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
Skip: %s
Presets: %s recommended; %s yes; %s no
Choices: %s
Explain: %s
Custom: %s inline; %s external editor
Inline edit: Enter commit; Esc cancel
Correct: %s clear; %s undo
Mouse: left click select; wheel scroll
Copy: %s
Reset: %s twice within two seconds
Help: %s; Quit: %s

Press %s to close; Worksheet remains unchanged.
`,
		configured.movementHelp(),
		configured.labels(actionSkip), configured.labels(actionRecommended), configured.labels(actionYes), configured.labels(actionNo),
		configured.choiceSummary(),
		configured.labels(actionExplain), configured.labels(actionInline), configured.labels(actionExternal),
		configured.labels(actionClear), configured.labels(actionUndo), configured.labels(actionCopy), configured.labels(actionReset), configured.labels(actionHelp), configured.labelsWithSeparator(actionQuit, ", "),
		configured.labels(actionHelp))
}

func (configured keymap) compactHelp() string {
	if configured.isDefault() {
		return "Help: ↑/↓ j/k move • Space skip • r/y/n 1-5 a-e x answer • i/o custom • Esc clear • u undo • s/Ctrl-S copy • Ctrl-R reset • ? help • q quit"
	}
	return fmt.Sprintf("Help: %s move • %s skip • %s/%s/%s %s answer • %s/%s custom • %s clear • %s undo • %s copy • %s reset • %s help • %s quit",
		configured.movementCompact(), configured.labels(actionSkip),
		configured.labels(actionRecommended), configured.labels(actionYes), configured.labels(actionNo),
		configured.choiceSummary(),
		configured.labels(actionInline), configured.labels(actionExternal), configured.labels(actionClear),
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
			if err != nil || configuredKeys[index] != parsedDefault {
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
		return "1–5; a–e/A–E"
	}
	choices := make([]string, 0, len(choiceDefinitions))
	for _, definition := range choiceDefinitions {
		choices = append(choices, configured.labels(definition.action)+"→"+definition.answer)
	}
	return strings.Join(choices, " ")
}
