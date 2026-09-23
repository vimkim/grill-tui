package main

import (
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"unicode/utf16"

	tea "charm.land/bubbletea/v2"
)

type clipboardResultMsg struct {
	backend string
	err     error
	empty   bool
}

type clipboardCommand struct {
	name       string
	executable string
	args       []string
	input      []byte
}

func copyAnswerList(storedWorksheet worksheet) tea.Cmd {
	answerList := storedWorksheet.answerList()
	return func() tea.Msg {
		if answerList == "" {
			return clipboardResultMsg{empty: true}
		}
		for _, backend := range availableClipboardCommands(answerList) {
			if err := runClipboardCommand(backend); err == nil {
				return clipboardResultMsg{backend: backend.name}
			}
		}
		if err := writeOSC52(answerList); err != nil {
			return clipboardResultMsg{err: fmt.Errorf("all backends failed; check clipboard tools or OSC 52 support: %w", err)}
		}
		return clipboardResultMsg{backend: "OSC 52"}
	}
}

func availableClipboardCommands(answerList string) []clipboardCommand {
	var commands []clipboardCommand
	if os.Getenv("WSL_DISTRO_NAME") != "" || os.Getenv("WSL_INTEROP") != "" {
		commands = appendAvailableClipboardCommand(commands, "clip.exe", nil, encodeWindowsClipboard(answerList))
	}
	if os.Getenv("WAYLAND_DISPLAY") != "" {
		commands = appendAvailableClipboardCommand(commands, "wl-copy", nil, []byte(answerList))
	}
	if os.Getenv("DISPLAY") != "" {
		commands = appendAvailableClipboardCommand(commands, "xclip", []string{"-selection", "clipboard"}, []byte(answerList))
		commands = appendAvailableClipboardCommand(commands, "xsel", []string{"--clipboard", "--input"}, []byte(answerList))
	}
	return commands
}

func appendAvailableClipboardCommand(commands []clipboardCommand, executable string, args []string, input []byte) []clipboardCommand {
	path, err := exec.LookPath(executable)
	if err != nil {
		return commands
	}
	return append(commands, clipboardCommand{name: executable, executable: path, args: args, input: input})
}

func writeOSC52(answerList string) error {
	if terminalType := os.Getenv("TERM"); terminalType == "" || terminalType == "dumb" {
		return fmt.Errorf("terminal type %q does not support OSC 52", terminalType)
	}
	terminal, err := os.OpenFile("/dev/tty", os.O_WRONLY, 0)
	if err != nil {
		return fmt.Errorf("open controlling terminal: %w", err)
	}
	sequence := "\x1b]52;c;" + base64.StdEncoding.EncodeToString([]byte(answerList)) + "\a"
	_, writeErr := terminal.WriteString(sequence)
	closeErr := terminal.Close()
	if writeErr != nil {
		return fmt.Errorf("write controlling terminal: %w", writeErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close controlling terminal: %w", closeErr)
	}
	return nil
}

func runClipboardCommand(backend clipboardCommand) error {
	command := exec.Command(backend.executable, backend.args...)
	stdin, err := command.StdinPipe()
	if err != nil {
		return fmt.Errorf("open %s stdin: %w", backend.name, err)
	}
	if err := command.Start(); err != nil {
		_ = stdin.Close()
		return fmt.Errorf("start %s: %w", backend.name, err)
	}
	written, writeErr := stdin.Write(backend.input)
	if writeErr == nil && written != len(backend.input) {
		writeErr = io.ErrShortWrite
	}
	closeErr := stdin.Close()
	waitErr := command.Wait()
	if writeErr != nil {
		return fmt.Errorf("write %s stdin: %w", backend.name, writeErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close %s stdin: %w", backend.name, closeErr)
	}
	if waitErr != nil {
		return fmt.Errorf("run %s: %w", backend.name, waitErr)
	}
	return nil
}

func encodeWindowsClipboard(answerList string) []byte {
	lineFeedText := strings.ReplaceAll(strings.ReplaceAll(answerList, "\r\n", "\n"), "\r", "\n")
	codeUnits := utf16.Encode([]rune(strings.ReplaceAll(lineFeedText, "\n", "\r\n")))
	encoded := make([]byte, len(codeUnits)*2)
	for index, codeUnit := range codeUnits {
		binary.LittleEndian.PutUint16(encoded[index*2:], codeUnit)
	}
	return encoded
}
