package grilltui_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
)

func TestCLIHelpAliasesDescribeEveryInvocation(t *testing.T) {
	workingDir := t.TempDir()
	wantForms := []string{
		"grill-tui [--name NAME] [START]",
		"grill-tui list",
		"grill-tui result [--name NAME] [--answers]",
		"grill-tui query FROM TO [--name NAME]",
		"grill-tui config defaults",
		"grill-tui config install [--force]",
		"grill-tui help [COMMAND]",
		"grill-tui -h | grill-tui --help",
		"grill-tui version",
		"grill-tui -v | grill-tui --version",
	}

	var canonical string
	for _, args := range [][]string{{"help"}, {"-h"}, {"--help"}} {
		result := runCLI(t, workingDir, environmentOverrides{}, args...)
		if result.err != nil || result.stderr != "" {
			t.Fatalf("grill-tui %v = err %v, stderr %q", args, result.err, result.stderr)
		}
		for _, form := range wantForms {
			if !strings.Contains(result.stdout, form) {
				t.Fatalf("grill-tui %v help omits %q:\n%s", args, form, result.stdout)
			}
		}
		if canonical == "" {
			canonical = result.stdout
		} else if result.stdout != canonical {
			t.Fatalf("grill-tui %v help differs from canonical help\nwant:\n%s\ngot:\n%s", args, canonical, result.stdout)
		}
	}
	assertNoWorksheetStorage(t, workingDir)
}

func TestCLICommandHelpDescribesEachCommand(t *testing.T) {
	tests := []struct {
		command string
		want    []string
	}{
		{command: "launch", want: []string{"grill-tui [--name NAME] [START]", "--name NAME", "START"}},
		{command: "list", want: []string{"grill-tui list", "saved Worksheets"}},
		{command: "result", want: []string{"grill-tui result [--name NAME] [--answers]", "--answers"}},
		{command: "query", want: []string{"grill-tui query FROM TO [--name NAME]", "inclusive"}},
		{command: "config", want: []string{"grill-tui config defaults", "grill-tui config install [--force]"}},
	}
	for _, test := range tests {
		t.Run(test.command, func(t *testing.T) {
			workingDir := t.TempDir()
			byHelp := runCLI(t, workingDir, environmentOverrides{}, "help", test.command)
			if byHelp.err != nil || byHelp.stderr != "" {
				t.Fatalf("help %s = err %v, stderr %q", test.command, byHelp.err, byHelp.stderr)
			}
			for _, want := range test.want {
				if !strings.Contains(byHelp.stdout, want) {
					t.Fatalf("help %s omits %q:\n%s", test.command, want, byHelp.stdout)
				}
			}
			if test.command != "launch" {
				byFlag := runCLI(t, workingDir, environmentOverrides{}, test.command, "--help")
				if byFlag.err != nil || byFlag.stderr != "" || byFlag.stdout != byHelp.stdout {
					t.Fatalf("%s --help differs: err %v, stdout %q, stderr %q", test.command, byFlag.err, byFlag.stdout, byFlag.stderr)
				}
			}
			assertNoWorksheetStorage(t, workingDir)
		})
	}

}

func TestCLIVersionAliasesAreEquivalent(t *testing.T) {
	workingDir := t.TempDir()
	var canonical string
	for _, argument := range []string{"version", "-v", "--version"} {
		result := runCLI(t, workingDir, environmentOverrides{}, argument)
		if result.err != nil || result.stderr != "" {
			t.Fatalf("grill-tui %s = err %v, stderr %q", argument, result.err, result.stderr)
		}
		if canonical == "" {
			canonical = result.stdout
		} else if result.stdout != canonical {
			t.Fatalf("grill-tui %s = %q, want %q", argument, result.stdout, canonical)
		}
	}
	if !strings.HasPrefix(canonical, "grill-tui ") || !strings.HasSuffix(canonical, "\n") {
		t.Fatalf("version output is not in product format: %q", canonical)
	}
	assertNoWorksheetStorage(t, workingDir)
}

func TestCLIVersionUsesInjectedBuildMetadata(t *testing.T) {
	tests := []struct {
		name      string
		ldflags   string
		want      string
		wantMatch string
	}{
		{name: "tagged", ldflags: "-X main.buildVersion=v1.2.3", want: "grill-tui v1.2.3\n"},
		{name: "development revision", ldflags: "-X main.buildVersion=devel -X main.buildRevision=0123456789abcdef", wantMatch: `^grill-tui devel \(0123456789ab\)\n$`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			binary := buildCLIBinary(t, test.ldflags)
			command := exec.Command(binary, "version")
			command.Dir = t.TempDir()
			var stdout, stderr bytes.Buffer
			command.Stdout = &stdout
			command.Stderr = &stderr
			if err := command.Run(); err != nil {
				t.Fatalf("version failed: %v; stderr: %s", err, stderr.String())
			}
			if stderr.Len() != 0 {
				t.Fatalf("version stderr = %q", stderr.String())
			}
			if test.want != "" && stdout.String() != test.want {
				t.Fatalf("version = %q, want %q", stdout.String(), test.want)
			}
			if test.wantMatch != "" && !regexp.MustCompile(test.wantMatch).MatchString(stdout.String()) {
				t.Fatalf("version = %q, want match %s", stdout.String(), test.wantMatch)
			}
		})
	}
}

func TestCLIFailuresShowConciseUsageAndSuggestions(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want []string
	}{
		{
			name: "misspelled version flag",
			args: []string{"--versio"},
			want: []string{`unknown option "--versio"`, `Did you mean "--version"?`, "Usage:", "grill-tui [--name NAME] [START]"},
		},
		{
			name: "misspelled command",
			args: []string{"versoin"},
			want: []string{`unknown command "versoin"`, `Did you mean "version"?`, "Usage:"},
		},
		{
			name: "invalid list combination",
			args: []string{"list", "extra"},
			want: []string{"does not accept arguments", "Usage:", "grill-tui list"},
		},
		{
			name: "unknown help topic",
			args: []string{"help", "reslt"},
			want: []string{`unknown help topic "reslt"`, `Did you mean "result"?`, "Usage:", "grill-tui help [COMMAND]"},
		},
		{
			name: "misspelled list help",
			args: []string{"list", "--hlep"},
			want: []string{`unknown option "--hlep"`, `Did you mean "--help"?`, "Usage:", "grill-tui list"},
		},
		{
			name: "misspelled config force",
			args: []string{"config", "install", "--froce"},
			want: []string{`unknown option "--froce"`, `Did you mean "--force"?`, "Usage:", "grill-tui config install [--force]"},
		},
		{
			name: "misspelled help flag",
			args: []string{"help", "--hlep"},
			want: []string{`unknown option "--hlep"`, `Did you mean "--help"?`, "Usage:", "grill-tui help [COMMAND]"},
		},
		{
			name: "reversed query bounds",
			args: []string{"query", "3", "2"},
			want: []string{"must not exceed", "Usage:", "grill-tui query FROM TO [--name NAME]"},
		},
		{
			name: "invalid version combination",
			args: []string{"version", "extra"},
			want: []string{"does not accept arguments", "Usage:", "grill-tui version"},
		},
		{
			name: "invalid help combination",
			args: []string{"help", "query", "extra"},
			want: []string{"at most one command", "Usage:", "grill-tui help [COMMAND]"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			workingDir := t.TempDir()
			result := runCLI(t, workingDir, environmentOverrides{}, test.args...)
			if result.err == nil {
				t.Fatalf("grill-tui %v unexpectedly succeeded", test.args)
			}
			if result.stdout != "" {
				t.Fatalf("grill-tui %v stdout = %q, want empty", test.args, result.stdout)
			}
			for _, want := range test.want {
				if !strings.Contains(result.stderr, want) {
					t.Fatalf("grill-tui %v stderr omits %q:\n%s", test.args, want, result.stderr)
				}
			}
			if strings.Count(result.stderr, "Usage:") != 1 {
				t.Fatalf("grill-tui %v stderr is not concise:\n%s", test.args, result.stderr)
			}
			assertNoWorksheetStorage(t, workingDir)
		})
	}
}

func TestCLITypoSuggestionsAreValidForTheCommand(t *testing.T) {
	workingDir := t.TempDir()
	result := runCLI(t, workingDir, environmentOverrides{}, "query", "1", "2", "--versio")
	if result.err == nil || !strings.Contains(result.stderr, `unknown option "--versio"`) {
		t.Fatalf("contextual option failure = err %v, stderr %q", result.err, result.stderr)
	}
	if strings.Contains(result.stderr, `Did you mean "--version"?`) {
		t.Fatalf("query suggested an option that is invalid in query position:\n%s", result.stderr)
	}
	assertNoWorksheetStorage(t, workingDir)

	workingDir = t.TempDir()
	result = runCLI(t, workingDir, environmentOverrides{}, "config", "defaults", "--froce")
	if result.err == nil || !strings.Contains(result.stderr, `unknown option "--froce"`) {
		t.Fatalf("config defaults option failure = err %v, stderr %q", result.err, result.stderr)
	}
	if strings.Contains(result.stderr, `Did you mean "--force"?`) {
		t.Fatalf("config defaults suggested an option that is valid only for install:\n%s", result.stderr)
	}
	assertNoWorksheetStorage(t, workingDir)
}

func TestCLIHelpAndVersionIgnoreMalformedConfigurationAndLockedWorksheet(t *testing.T) {
	workingDir := t.TempDir()
	configHome := t.TempDir()
	configDirectory := filepath.Join(configHome, "grill-tui")
	if err := os.MkdirAll(configDirectory, 0o700); err != nil {
		t.Fatalf("create configuration directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(configDirectory, "config.toml"), []byte("not = [valid"), 0o600); err != nil {
		t.Fatalf("write malformed configuration: %v", err)
	}
	worksheetDirectory := filepath.Join(workingDir, ".grill-data", "untitled")
	if err := os.MkdirAll(worksheetDirectory, 0o700); err != nil {
		t.Fatalf("create malformed Worksheet directory: %v", err)
	}
	databasePath := filepath.Join(worksheetDirectory, "worksheet.sqlite")
	databaseEvidence := []byte("not a SQLite database")
	if err := os.WriteFile(databasePath, databaseEvidence, 0o600); err != nil {
		t.Fatalf("write malformed Worksheet Database: %v", err)
	}
	lockPath := filepath.Join(worksheetDirectory, "worksheet.lock")
	lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatalf("open Worksheet lock: %v", err)
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatalf("hold Worksheet lock: %v", err)
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)

	overrides := environmentOverrides{values: map[string]string{"XDG_CONFIG_HOME": configHome}}
	for _, args := range [][]string{{"help"}, {"help", "query"}, {"version"}} {
		result := runCLI(t, workingDir, overrides, args...)
		if result.err != nil || result.stdout == "" || result.stderr != "" {
			t.Fatalf("grill-tui %v with malformed/locked state = err %v, stdout %q, stderr %q", args, result.err, result.stdout, result.stderr)
		}
	}
	contents, err := os.ReadFile(databasePath)
	if err != nil || !bytes.Equal(contents, databaseEvidence) {
		t.Fatalf("informational commands changed malformed database: contents %q, err %v", contents, err)
	}
	entries, err := os.ReadDir(filepath.Join(workingDir, ".grill-data"))
	if err != nil {
		t.Fatalf("read data root: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "untitled" {
		t.Fatalf("informational commands changed data root entries: %v", entries)
	}
	worksheetEntries, err := os.ReadDir(worksheetDirectory)
	if err != nil {
		t.Fatalf("read Worksheet directory: %v", err)
	}
	if len(worksheetEntries) != 2 {
		t.Fatalf("informational commands created Worksheet artifacts: %v", worksheetEntries)
	}
}

func buildCLIBinary(t *testing.T, ldflags string) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "grill-tui")
	args := []string{"build", "-o", binary}
	if ldflags != "" {
		args = append(args, "-ldflags", ldflags)
	}
	args = append(args, "./cmd/grill-tui")
	command := exec.Command("go", args...)
	command.Dir = "."
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build injected grill-tui: %v\n%s", err, output)
	}
	return binary
}

func assertNoWorksheetStorage(t *testing.T, workingDir string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(workingDir, ".grill-data")); !os.IsNotExist(err) {
		t.Fatalf("informational command created .grill-data; stat error = %v", err)
	}
}
