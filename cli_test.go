package grilltui_test

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/creack/pty"
)

var grillTUIBinary string

func TestMain(m *testing.M) {
	tempDir, err := os.MkdirTemp("", "grill-tui-tests-")
	if err != nil {
		panic(err)
	}

	grillTUIBinary = filepath.Join(tempDir, "grill-tui")
	build := exec.Command("go", "build", "-o", grillTUIBinary, "./cmd/grill-tui")
	if output, err := build.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "build grill-tui: %v\n%s", err, output)
		os.Exit(1)
	}

	exitCode := m.Run()
	if err := os.RemoveAll(tempDir); err != nil {
		fmt.Fprintf(os.Stderr, "remove test binaries: %v\n", err)
		if exitCode == 0 {
			exitCode = 1
		}
	}
	os.Exit(exitCode)
}

func TestPositiveArgumentCreatesAndRendersWorksheet(t *testing.T) {
	workingDir := t.TempDir()
	terminal := startTerminal(t, workingDir, "41")

	terminal.send(t, "q")
	terminal.waitForExit(t)
	screen := cleanTerminalOutput(terminal.output.String())
	answerSlots := answerSlotLinePattern.FindAllStringSubmatch(screen, -1)
	if len(answerSlots) != 10 {
		t.Fatalf("screen contains %d rendered Answer Slots, want 10:\n%s", len(answerSlots), screen)
	}
	for index, answerSlot := range answerSlots {
		if want := fmt.Sprintf("%d", 41+index); answerSlot[1] != want {
			t.Fatalf("rendered Answer Slot %d has number %s, want %s:\n%s", index, answerSlot[1], want, screen)
		}
	}
	for _, text := range []string{"Grill TUI", "Worksheet", "No.", "Answer", "> 41", "q", "quit"} {
		if !strings.Contains(screen, text) {
			t.Fatalf("screen does not contain %q:\n%s", text, screen)
		}
	}

	stateInfo, err := os.Stat(filepath.Join(workingDir, ".grill-tui", "worksheet.json"))
	if err != nil {
		t.Fatalf("stat created Worksheet: %v", err)
	}
	if !stateInfo.Mode().IsRegular() {
		t.Fatalf("Worksheet state is not a regular file: %s", stateInfo.Mode())
	}
}

func TestPromptRejectsNonPositiveInputThenCreatesWorksheet(t *testing.T) {
	workingDir := t.TempDir()
	terminal := startTerminal(t, workingDir)

	prompt := terminal.waitFor(t, "Starting number:")
	if !strings.Contains(prompt, "positive") {
		t.Fatalf("prompt does not ask for a positive starting number:\n%s", prompt)
	}

	terminal.send(t, "0\r")
	feedback := terminal.waitFor(t, "must be a positive integer")
	if !strings.Contains(feedback, "Try again") {
		t.Fatalf("invalid-input feedback is not actionable:\n%s", feedback)
	}

	terminal.send(t, "7\r")
	worksheet := terminal.waitFor(t, "10 Answer Slots")
	if !strings.Contains(worksheet, "> 7") || !strings.Contains(worksheet, "16") {
		t.Fatalf("created Worksheet does not show slots 7 through 16:\n%s", worksheet)
	}
	terminal.send(t, "q")
	terminal.waitForExit(t)
}

func TestPromptRejectsStartThatCannotFitTenConsecutiveSlots(t *testing.T) {
	maxInt := int(^uint(0) >> 1)
	tests := []struct {
		name  string
		input string
	}{
		{name: "largest int", input: fmt.Sprintf("%d", maxInt)},
		{name: "larger than int", input: "9999999999999999999999999999999999999999"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			terminal := startTerminal(t, t.TempDir())
			terminal.send(t, test.input+"\r")
			feedback := terminal.waitFor(t, "too large")
			if !strings.Contains(feedback, "ten consecutive Answer Slots") || !strings.Contains(feedback, "Try again") {
				t.Fatalf("overflow feedback is not actionable:\n%s", feedback)
			}
			terminal.send(t, "q")
			terminal.waitForExit(t)
		})
	}
}

func TestExistingWorksheetResumesAndWinsOverSuppliedStart(t *testing.T) {
	workingDir := t.TempDir()
	firstRun := startTerminal(t, workingDir, "22")
	firstRun.send(t, "q")
	firstRun.waitForExit(t)

	statePath := filepath.Join(workingDir, ".grill-tui", "worksheet.json")
	beforeResume, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("read Worksheet before resuming: %v", err)
	}

	resumed := startTerminal(t, workingDir, "99")
	screen := resumed.waitFor(t, "10 Answer Slots")
	if !strings.Contains(screen, "> 22") || !strings.Contains(screen, "31") {
		t.Fatalf("existing Worksheet was not resumed:\n%s", screen)
	}
	if strings.Contains(screen, "99") {
		t.Fatalf("supplied start replaced the existing Worksheet:\n%s", screen)
	}
	resumed.send(t, "q")
	resumed.waitForExit(t)

	afterResume, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("read Worksheet after resuming: %v", err)
	}
	if !bytes.Equal(afterResume, beforeResume) {
		t.Fatalf("resuming with a supplied start changed the Worksheet\nbefore: %s\nafter: %s", beforeResume, afterResume)
	}
}

func TestDefaultQuitBindingsExit(t *testing.T) {
	tests := []struct {
		name string
		key  string
	}{
		{name: "q", key: "q"},
		{name: "Ctrl-Q", key: "\x11"},
		{name: "Ctrl-C", key: "\x03"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			terminal := startTerminal(t, t.TempDir(), "5")
			terminal.send(t, test.key)
			terminal.waitForExit(t)
		})
	}
}

type testTerminal struct {
	cmd    *exec.Cmd
	pty    *os.File
	output synchronizedBuffer
	done   chan error
}

type synchronizedBuffer struct {
	mutex  sync.Mutex
	buffer bytes.Buffer
}

func (buffer *synchronizedBuffer) Write(data []byte) (int, error) {
	buffer.mutex.Lock()
	defer buffer.mutex.Unlock()
	return buffer.buffer.Write(data)
}

func (buffer *synchronizedBuffer) String() string {
	buffer.mutex.Lock()
	defer buffer.mutex.Unlock()
	return buffer.buffer.String()
}

func startTerminal(t *testing.T, workingDir string, args ...string) *testTerminal {
	t.Helper()
	cmd := exec.Command(grillTUIBinary, args...)
	cmd.Dir = workingDir
	cmd.Env = append(os.Environ(), "TERM=xterm-256color", "NO_COLOR=1")

	ptyFile, err := pty.Start(cmd)
	if err != nil {
		t.Fatalf("start grill-tui in PTY: %v", err)
	}
	if err := pty.Setsize(ptyFile, &pty.Winsize{Rows: 30, Cols: 100}); err != nil {
		_ = ptyFile.Close()
		t.Fatalf("size PTY: %v", err)
	}

	terminal := &testTerminal{cmd: cmd, pty: ptyFile, done: make(chan error, 1)}
	t.Cleanup(func() {
		_ = terminal.pty.Close()
		if terminal.cmd.Process != nil {
			_ = terminal.cmd.Process.Kill()
		}
	})
	go func() {
		_, _ = io.Copy(&terminal.output, ptyFile)
	}()
	go func() { terminal.done <- cmd.Wait() }()
	deadline := time.Now().Add(5 * time.Second)
	repliedToBackgroundQuery := false
	repliedToForegroundQuery := false
	cursorReplies := 0
	for time.Now().Before(deadline) {
		output := terminal.output.String()
		if !repliedToBackgroundQuery && strings.Contains(output, "\x1b]11;?") {
			_, _ = ptyFile.WriteString("\x1b]11;rgb:0000/0000/0000\x1b\\")
			repliedToBackgroundQuery = true
		}
		if !repliedToForegroundQuery && strings.Contains(output, "\x1b]10;?") {
			_, _ = ptyFile.WriteString("\x1b]10;rgb:ffff/ffff/ffff\x1b\\")
			repliedToForegroundQuery = true
		}
		for cursorReplies < strings.Count(output, "\x1b[6n") {
			_, _ = ptyFile.WriteString("\x1b[1;1R")
			cursorReplies++
		}
		if strings.Contains(cleanTerminalOutput(output), "Grill TUI") {
			return terminal
		}
		select {
		case err := <-terminal.done:
			t.Fatalf("grill-tui exited before its initial screen: %v; output: %q", err, output)
		default:
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("grill-tui did not render its initial screen; output: %q", terminal.output.String())
	return terminal
}

func (terminal *testTerminal) send(t *testing.T, input string) {
	t.Helper()
	if _, err := terminal.pty.WriteString(input); err != nil {
		t.Fatalf("send %q: %v", input, err)
	}
}

func (terminal *testTerminal) waitFor(t *testing.T, text string) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		output := cleanTerminalOutput(terminal.output.String())
		if strings.Contains(output, text) {
			return output
		}
		select {
		case err := <-terminal.done:
			t.Fatalf("grill-tui exited before rendering %q: %v; output:\n%s", text, err, output)
		default:
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %q; output:\n%s", text, cleanTerminalOutput(terminal.output.String()))
	return ""
}

func (terminal *testTerminal) waitForExit(t *testing.T) {
	t.Helper()
	select {
	case err := <-terminal.done:
		if err != nil {
			t.Fatalf("grill-tui exited with error: %v; output:\n%s", err, cleanTerminalOutput(terminal.output.String()))
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for grill-tui to exit; output:\n%s", cleanTerminalOutput(terminal.output.String()))
	}
}

var ansiSequence = regexp.MustCompile(`\x1b(?:\[[0-?]*[ -/]*[@-~]|\][^\x07]*(?:\x07|\x1b\\)|[()][0-2A-Z])`)
var answerSlotLinePattern = regexp.MustCompile(`(?m)^[ >] ([0-9]+) │`)

func cleanTerminalOutput(output string) string {
	return ansiSequence.ReplaceAllString(strings.ReplaceAll(output, "\r", ""), "")
}
