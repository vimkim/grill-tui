package grilltui_test

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"
)

func TestDefaultTopSequenceSelectsFirstAnswerSlot(t *testing.T) {
	terminal := startTerminal(t, t.TempDir(), "5")
	terminal.send(t, "j")
	terminal.waitForSelection(t, 6)
	terminal.send(t, "j")
	terminal.waitForSelection(t, 7)
	terminal.send(t, "g")
	terminal.waitFor(t, "Pending input: g")
	terminal.send(t, "g")
	terminal.waitForSelection(t, 5)
	terminal.send(t, "q")
	terminal.waitForExit(t)
}

func TestCombinedTopSequenceSelectsFirstAnswerSlot(t *testing.T) {
	terminal := startTerminal(t, t.TempDir(), "5")
	terminal.send(t, "j")
	terminal.waitForSelection(t, 6)
	terminal.send(t, "j")
	terminal.waitForSelection(t, 7)
	terminal.send(t, "gg")
	terminal.waitForSelection(t, 5)
	terminal.send(t, "q")
	terminal.waitForExit(t)
}

func TestNonmatchingKeyReplaysAfterPendingSequence(t *testing.T) {
	terminal := startTerminal(t, t.TempDir(), "5")
	terminal.send(t, "g")
	terminal.waitFor(t, "Pending input: g")
	terminal.send(t, "x")
	screen := terminal.waitForSelection(t, 6)
	if answer := latestRenderedAnswer(t, screen, 5); answer != "explain further" {
		t.Fatalf("nonmatching x was swallowed; Answer Slot 5 = %q:\n%s", answer, screen)
	}
	terminal.send(t, "q")
	terminal.waitForExit(t)
}

func TestRemappedSequenceAndInsertFinishStayInTheirModes(t *testing.T) {
	configHome := t.TempDir()
	writeConfigFixture(t, configHome, "[keymap]\njump_first = [\"zz\"]\nquit = [\"v\"]\n[keymap.insert]\nfinish = [\"v\"]\n")
	terminal := startTerminalWithEnvironment(t, t.TempDir(), environmentOverrides{values: map[string]string{"XDG_CONFIG_HOME": configHome}}, "5")
	terminal.send(t, "j")
	terminal.waitForSelection(t, 6)
	terminal.send(t, "g")
	terminal.send(t, "z")
	terminal.waitFor(t, "Pending input: z")
	terminal.send(t, "z")
	terminal.waitForSelection(t, 5)
	terminal.send(t, "i")
	terminal.waitFor(t, "Inline Custom Answer 5:")
	terminal.send(t, "hi")
	terminal.send(t, "v")
	screen := terminal.waitForSelection(t, 6)
	if answer := latestRenderedAnswer(t, screen, 5); answer != "hi" {
		t.Fatalf("Insert Mode finish committed %q, want hi:\n%s", answer, screen)
	}
	terminal.send(t, "v")
	terminal.waitForExit(t)
}

func TestInsertModeCanFinishWithASequence(t *testing.T) {
	configHome := t.TempDir()
	writeConfigFixture(t, configHome, "[keymap.insert]\nfinish = [\"zz\"]\n")
	terminal := startTerminalWithEnvironment(t, t.TempDir(), environmentOverrides{values: map[string]string{"XDG_CONFIG_HOME": configHome}}, "5")
	terminal.send(t, "i")
	terminal.waitFor(t, "Inline Custom Answer 5:")
	terminal.send(t, "hi")
	terminal.send(t, "z")
	terminal.waitFor(t, "Pending input: z")
	terminal.send(t, "z")
	screen := terminal.waitForSelection(t, 6)
	if answer := latestRenderedAnswer(t, screen, 5); answer != "hi" {
		t.Fatalf("Insert Mode sequence committed %q, want hi:\n%s", answer, screen)
	}
	terminal.send(t, "q")
	terminal.waitForExit(t)
}

func TestPromptSubmitCanBeRemappedWithoutChangingNormalMode(t *testing.T) {
	configHome := t.TempDir()
	writeConfigFixture(t, configHome, "[keymap.prompt]\nsubmit = [\"z\"]\n")
	terminal := startTerminalWithEnvironment(t, t.TempDir(), environmentOverrides{values: map[string]string{"XDG_CONFIG_HOME": configHome}})
	terminal.send(t, "5\r")
	terminal.waitFor(t, "Starting number:")
	terminal.send(t, "z")
	terminal.waitForSelection(t, 5)
	terminal.send(t, "q")
	terminal.waitForExit(t)
}

func TestCompleteHelpShowsAllNewModeBindings(t *testing.T) {
	configHome := t.TempDir()
	writeConfigFixture(t, configHome, "[keymap]\njump_first = [\"zz\"]\n[keymap.insert]\nbackspace = []\n[keymap.prompt]\nerase = [\"ctrl+h\"]\n")
	terminal := startTerminalWithEnvironment(t, t.TempDir(), environmentOverrides{values: map[string]string{"XDG_CONFIG_HOME": configHome}}, "5")
	terminal.send(t, "?")
	help := terminal.waitFor(t, "Complete Help")
	for _, want := range []string{"Jump first: zz", "Erase: (unbound) Insert; Ctrl-H prompt"} {
		if !strings.Contains(help, want) {
			t.Fatalf("Complete Help omits %q:\n%s", want, help)
		}
	}
	terminal.send(t, "q")
	terminal.waitForExit(t)
}

func TestPendingSequenceIsVisibleWhileHelpIsOpen(t *testing.T) {
	configHome := t.TempDir()
	writeConfigFixture(t, configHome, "[input]\nsequence_timeout_ms = 100\n")
	terminal := startTerminalWithEnvironment(t, t.TempDir(), environmentOverrides{values: map[string]string{"XDG_CONFIG_HOME": configHome}}, "5")
	terminal.send(t, "?")
	terminal.waitFor(t, "Complete Help")
	terminal.send(t, "g")
	terminal.waitFor(t, "Pending input: g")
	terminal.waitFor(t, "Sequence expired")
	terminal.send(t, "q")
	terminal.waitForExit(t)
}

func TestPendingSequenceRemainsVisibleAfterMouseSelection(t *testing.T) {
	configHome := t.TempDir()
	writeConfigFixture(t, configHome, "[input]\nsequence_timeout_ms = 5000\n")
	terminal := startTerminalWithEnvironment(t, t.TempDir(), environmentOverrides{values: map[string]string{"XDG_CONFIG_HOME": configHome}}, "5")
	terminal.send(t, "g")
	terminal.waitFor(t, "Pending input: g")
	terminal.send(t, sgrMousePress(10, 9, 0))
	terminal.waitForSelection(t, 8)
	mark := len(terminal.output.String())
	terminal.resize(t, 80, 24)
	screen := terminal.waitForAfter(t, mark, "Status: Pending input: g")
	if !strings.Contains(screen, "Selected Answer 8 (full):") {
		t.Fatalf("mouse selection did not select Answer Slot 8:\n%s", screen)
	}
	terminal.send(t, "q")
	terminal.waitForExit(t)
}

func TestSequenceConfigurationRejectsUnsafeOrAmbiguousBindings(t *testing.T) {
	tests := []struct {
		name   string
		config string
		want   []string
	}{
		{"timeout below minimum", "[input]\nsequence_timeout_ms = 99\n", []string{"input.sequence_timeout_ms", "100", "5000"}},
		{"timeout above maximum", "[input]\nsequence_timeout_ms = 5001\n", []string{"input.sequence_timeout_ms", "100", "5000"}},
		{"timeout not integer", "[input]\nsequence_timeout_ms = \"1000\"\n", []string{"input.sequence_timeout_ms", "integer"}},
		{"prefix conflict", "[keymap]\nexplain = [\"g\"]\n", []string{"explain", "jump_first", `"g"`, `"gg"`}},
		{"duplicate sequence", "[keymap]\nexplain = [\"gg\"]\n", []string{"explain", "jump_first", `"gg"`}},
		{"finish required", "[keymap.insert]\nfinish = []\n", []string{"finish", "at least one"}},
		{"prompt submit required", "[keymap.prompt]\nsubmit = []\n", []string{"submit", "at least one"}},
		{"prompt prefix conflict", "[keymap.prompt]\nerase = [\"q q\"]\nquit = [\"q\"]\n", []string{"erase", "quit", `"q q"`, `"q"`}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			workingDir := t.TempDir()
			configHome := t.TempDir()
			writeConfigFixture(t, configHome, test.config)
			result := runCLI(t, workingDir, environmentOverrides{values: map[string]string{"XDG_CONFIG_HOME": configHome}}, "5")
			if result.err == nil {
				t.Fatalf("configuration unexpectedly accepted: %s", test.config)
			}
			for _, want := range test.want {
				if !strings.Contains(result.stderr, want) {
					t.Fatalf("diagnostic omits %q: %s", want, result.stderr)
				}
			}
			if _, err := os.Stat(filepath.Join(workingDir, ".grill-data")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("invalid configuration created Worksheet data: %v", err)
			}
		})
	}
}

func TestInvalidSequenceConfigurationFailsInPTYBeforeWorksheetOpens(t *testing.T) {
	workingDir := t.TempDir()
	configHome := t.TempDir()
	writeConfigFixture(t, configHome, "[keymap]\nexplain = [\"g\"]\n")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, grillTUIBinary, "5")
	command.Dir = workingDir
	command.Env = overriddenEnvironment(t, environmentOverrides{values: map[string]string{"XDG_CONFIG_HOME": configHome}})
	terminal, err := pty.Start(command)
	if err != nil {
		t.Fatalf("start invalid configuration in PTY: %v", err)
	}
	defer terminal.Close()
	var output synchronizedBuffer
	copyDone := make(chan struct{})
	go func() {
		_, _ = io.Copy(&output, terminal)
		close(copyDone)
	}()
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	repliedToBackground := false
	repliedToForeground := false
	repliedToCursor := false
	for {
		raw := output.String()
		if !repliedToBackground && strings.Contains(raw, "\x1b]11;?") {
			_, _ = terminal.WriteString("\x1b]11;rgb:0000/0000/0000\x1b\\")
			repliedToBackground = true
		}
		if !repliedToForeground && strings.Contains(raw, "\x1b]10;?") {
			_, _ = terminal.WriteString("\x1b]10;rgb:ffff/ffff/ffff\x1b\\")
			repliedToForeground = true
		}
		if !repliedToCursor && strings.Contains(raw, "\x1b[6n") {
			_, _ = terminal.WriteString("\x1b[1;1R")
			repliedToCursor = true
		}
		select {
		case err := <-done:
			if err == nil {
				t.Fatalf("ambiguous configuration unexpectedly opened in PTY: %q", output.String())
			}
			<-copyDone
			goto exited
		case <-ctx.Done():
			t.Fatalf("ambiguous configuration did not exit promptly: %v; output: %q", ctx.Err(), raw)
		case <-time.After(10 * time.Millisecond):
		}
	}
exited:
	for _, want := range []string{"jump_first", "explain", `"gg"`, `"g"`} {
		if !strings.Contains(cleanTerminalOutput(output.String()), want) {
			t.Fatalf("PTY diagnostic omits %q: %q", want, output.String())
		}
	}
	if _, err := os.Stat(filepath.Join(workingDir, ".grill-data")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("invalid configuration created Worksheet data: %v", err)
	}
}

func TestDefaultConfigurationIncludesSequenceTimingAndInsertFinish(t *testing.T) {
	result := runCLI(t, t.TempDir(), environmentOverrides{}, "config", "defaults")
	if result.err != nil || result.stderr != "" {
		t.Fatalf("config defaults = err %v, stderr %q", result.err, result.stderr)
	}
	for _, want := range []string{"[input]", "sequence_timeout_ms = 1000", `jump_first = ["gg"]`, "[keymap.insert]", `finish = ["enter"]`, "[keymap.prompt]", `submit = ["enter"]`} {
		if !strings.Contains(result.stdout, want) {
			t.Fatalf("config defaults omits %q:\n%s", want, result.stdout)
		}
	}
	configHome := t.TempDir()
	writeConfigFixture(t, configHome, result.stdout)
	terminal := startTerminalWithEnvironment(t, t.TempDir(), environmentOverrides{values: map[string]string{"XDG_CONFIG_HOME": configHome}}, "5")
	terminal.send(t, "q")
	terminal.waitForExit(t)
}

func TestShortSequenceTimeoutExpiresWithoutChangingSelection(t *testing.T) {
	configHome := t.TempDir()
	writeConfigFixture(t, configHome, "[input]\nsequence_timeout_ms = 100\n")
	terminal := startTerminalWithEnvironment(t, t.TempDir(), environmentOverrides{values: map[string]string{"XDG_CONFIG_HOME": configHome}}, "5")
	terminal.send(t, "j")
	terminal.waitForSelection(t, 6)
	terminal.send(t, "j")
	terminal.waitForSelection(t, 7)
	terminal.send(t, "g")
	terminal.waitFor(t, "Pending input: g")
	terminal.waitFor(t, "Sequence expired")
	terminal.send(t, "g")
	terminal.waitFor(t, "Pending input: g")
	time.Sleep(150 * time.Millisecond)
	terminal.waitForSelection(t, 7)
	terminal.send(t, "q")
	terminal.waitForExit(t)
}
