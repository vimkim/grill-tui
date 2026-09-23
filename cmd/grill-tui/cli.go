package main

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"runtime/debug"
	"sort"
	"strings"
)

const (
	launchInvocation         = "grill-tui [--name NAME] [START]"
	listInvocation           = "grill-tui list"
	resultInvocation         = "grill-tui result [--name NAME] [--answers]"
	queryInvocation          = "grill-tui query FROM TO [--name NAME]"
	configDefaultsInvocation = "grill-tui config defaults"
	configInstallInvocation  = "grill-tui config install [--force]"
	helpInvocation           = "grill-tui help [COMMAND]"
	versionInvocation        = "grill-tui version"

	launchUsage  = "Usage:\n  " + launchInvocation
	listUsage    = "Usage:\n  " + listInvocation
	resultUsage  = "Usage:\n  " + resultInvocation
	queryUsage   = "Usage:\n  " + queryInvocation
	configUsage  = "Usage:\n  " + configDefaultsInvocation + "\n  " + configInstallInvocation
	helpUsage    = "Usage:\n  " + helpInvocation
	versionUsage = "Usage:\n  " + versionInvocation

	topLevelUsage = "Usage:\n" +
		"  " + launchInvocation + "\n" +
		"  " + listInvocation + "\n" +
		"  " + resultInvocation + "\n" +
		"  " + queryInvocation + "\n" +
		"  " + configDefaultsInvocation + "\n" +
		"  " + configInstallInvocation + "\n" +
		"  " + helpInvocation + "\n" +
		"  grill-tui -h | grill-tui --help\n" +
		"  " + versionInvocation + "\n" +
		"  grill-tui -v | grill-tui --version\n"
)

const topLevelHelp = topLevelUsage + `

Commands:
  list      List saved Worksheets.
  result    Print a Worksheet Database path or Answer List.
  query     Print answers from Answer Slots in an inclusive range.
  config    Print or install configuration.
  help      Show top-level or command help.
  version   Show build version information.

Options:
  -h, --help       Show help.
  -v, --version    Show version information.

Run "grill-tui help launch" for interactive launch options or
"grill-tui help COMMAND" for command details.
`

type cliCommand struct {
	help    string
	options []string
	run     func([]string) error
}

var cliCommands map[string]cliCommand

func init() {
	cliCommands = map[string]cliCommand{
		"config": {
			help: configUsage + `

Print the complete default configuration or install it.
`,
			options: []string{"--help"},
			run:     runConfigCommand,
		},
		"help": {
			help: helpUsage + `

Show top-level help or help for one command.
`,
			options: []string{"--help"},
			run:     runHelpCommand,
		},
		"list": {
			help: listUsage + `

List saved Worksheets by most recent update.
`,
			options: []string{"--help"},
			run:     runListCommand,
		},
		"query": {
			help: queryUsage + `

Print answers from Answer Slots in the inclusive FROM-to-TO range.
`,
			options: []string{"--help", "--name"},
			run:     runQueryCommand,
		},
		"result": {
			help: resultUsage + `

Print the Worksheet Database path, or print its Answer List with --answers.
`,
			options: []string{"--answers", "--help", "--name"},
			run:     runResultCommand,
		},
		"version": {
			help: versionUsage + `

Show build version information.
`,
			options: []string{"--help"},
			run:     runVersionCommand,
		},
	}
}

const launchHelp = launchUsage + `

Launch the interactive Worksheet.

Arguments:
  START          Positive initial Answer Slot number.

Options:
  --name NAME    Select a named Worksheet. Omit it for untitled.
  -h, --help     Show top-level help.
`

var (
	pseudoVersionPattern = regexp.MustCompile(`-\d{14}-[0-9a-f]+`)
	buildVersion         string
	buildRevision        string
)

func runCLICommand(args []string) (bool, error) {
	if len(args) == 0 {
		return false, nil
	}
	if args[0] == "-h" || args[0] == "--help" {
		if len(args) != 1 {
			return true, cliFailure(fmt.Sprintf("%s does not accept arguments", args[0]), "", topLevelUsage)
		}
		_, err := fmt.Print(topLevelHelp)
		return true, err
	}
	if args[0] == "-v" || args[0] == "--version" {
		return true, runVersionCommand(args[1:])
	}
	if command, found := cliCommands[args[0]]; found {
		commandArgs := args[1:]
		for _, argument := range commandArgs {
			if argument == "-h" || argument == "--help" {
				_, err := fmt.Print(command.help)
				return true, err
			}
		}
		return true, command.run(commandArgs)
	}
	if strings.HasPrefix(args[0], "-") && args[0] != "--name" && !strings.HasPrefix(args[0], "--name=") {
		return true, unknownCLIValue("option", args[0], []string{"--help", "--name", "--version"}, topLevelUsage)
	}
	if !strings.HasPrefix(args[0], "-") && !looksLikeStartingNumber(args[0]) {
		return true, unknownCLIValue("command", args[0], cliCommandNames(), topLevelUsage)
	}
	return false, nil
}

func cliCommandNames() []string {
	names := make([]string, 0, len(cliCommands))
	for name := range cliCommands {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func runHelpCommand(args []string) error {
	if len(args) == 0 {
		_, err := fmt.Print(topLevelHelp)
		return err
	}
	if len(args) != 1 {
		return cliFailure("help accepts at most one command", "", helpUsage)
	}
	if strings.HasPrefix(args[0], "-") {
		return unknownCLIValue("option", args[0], cliCommands["help"].options, helpUsage)
	}
	if args[0] == "launch" {
		_, err := fmt.Print(launchHelp)
		return err
	}
	command, found := cliCommands[args[0]]
	if !found {
		candidates := append(cliCommandNames(), "launch")
		return unknownCLIValue("help topic", args[0], candidates, helpUsage)
	}
	_, err := fmt.Print(command.help)
	return err
}

func runVersionCommand(args []string) error {
	if len(args) != 0 {
		if strings.HasPrefix(args[0], "-") {
			return unknownCLIValue("option", args[0], []string{"--help"}, versionUsage)
		}
		return cliFailure("version does not accept arguments", "", versionUsage)
	}
	_, err := fmt.Fprintln(os.Stdout, buildVersionLine())
	return err
}

func buildVersionLine() string {
	version := buildVersion
	revision := buildRevision
	if buildInfo, ok := debug.ReadBuildInfo(); ok {
		if version == "" {
			version = buildInfo.Main.Version
		}
		if revision == "" {
			for _, setting := range buildInfo.Settings {
				if setting.Key == "vcs.revision" {
					revision = setting.Value
					break
				}
			}
		}
	}
	if version != "" && version != "(devel)" && version != "devel" && !pseudoVersionPattern.MatchString(version) {
		return "grill-tui " + version
	}
	if len(revision) > 12 {
		revision = revision[:12]
	}
	if revision != "" {
		return fmt.Sprintf("grill-tui devel (%s)", revision)
	}
	return "grill-tui devel"
}

func looksLikeStartingNumber(argument string) bool {
	if argument == "" {
		return false
	}
	if argument[0] == '+' {
		argument = argument[1:]
	}
	if argument == "" {
		return false
	}
	for _, character := range argument {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

func unknownCLIValue(kind, value string, candidates []string, usage string) error {
	suggestion := closestCLIValue(value, candidates)
	return cliFailure(fmt.Sprintf("unknown %s %q", kind, value), suggestion, strings.TrimSuffix(usage, "\n"))
}

func cliFailure(message, suggestion, usage string) error {
	var failure strings.Builder
	failure.WriteString(message)
	if suggestion != "" {
		fmt.Fprintf(&failure, "\nDid you mean %q?", suggestion)
	}
	failure.WriteByte('\n')
	failure.WriteString(strings.TrimSuffix(usage, "\n"))
	return errors.New(failure.String())
}

func addCLIUsage(err error, usage string) error {
	if err == nil || strings.Contains(err.Error(), "\nUsage:") {
		return err
	}
	return fmt.Errorf("%w\n%s", err, strings.TrimSuffix(usage, "\n"))
}

func closestCLIValue(value string, candidates []string) string {
	bestDistance := 3
	best := ""
	ambiguous := false
	for _, candidate := range candidates {
		distance := editDistance(value, candidate)
		if distance < bestDistance {
			bestDistance = distance
			best = candidate
			ambiguous = false
		} else if distance == bestDistance {
			ambiguous = true
		}
	}
	if ambiguous {
		return ""
	}
	return best
}

func editDistance(left, right string) int {
	leftRunes := []rune(left)
	rightRunes := []rune(right)
	previous := make([]int, len(rightRunes)+1)
	for index := range previous {
		previous[index] = index
	}
	for leftIndex, leftRune := range leftRunes {
		current := make([]int, len(rightRunes)+1)
		current[0] = leftIndex + 1
		for rightIndex, rightRune := range rightRunes {
			cost := 1
			if leftRune == rightRune {
				cost = 0
			}
			current[rightIndex+1] = min(
				current[rightIndex]+1,
				previous[rightIndex+1]+1,
				previous[rightIndex]+cost,
			)
		}
		previous = current
	}
	return previous[len(rightRunes)]
}
