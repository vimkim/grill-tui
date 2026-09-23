package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

const (
	configDirectoryName = "grill-tui"
	configFileName      = "config.toml"
)

type rawConfiguration struct {
	Keymap map[string][]string `toml:"keymap"`
}

func loadKeymap() (keymap, error) {
	configPath, err := configurationPath()
	if err != nil {
		return keymap{}, err
	}
	contents, err := os.ReadFile(configPath)
	if errors.Is(err, os.ErrNotExist) {
		return defaultKeymap(), nil
	}
	if err != nil {
		return keymap{}, fmt.Errorf("read configuration %s: %w", configPath, err)
	}

	bindings := defaultBindings()
	var raw rawConfiguration
	metadata, err := toml.Decode(string(contents), &raw)
	if err != nil {
		return keymap{}, fmt.Errorf("parse configuration %s: %w", configPath, err)
	}
	seenOptions := make(map[string]bool)
	for _, option := range metadata.Keys() {
		name := option.String()
		if seenOptions[name] {
			return keymap{}, fmt.Errorf("configuration %s contains duplicate option %s", configPath, name)
		}
		seenOptions[name] = true
	}
	if undecoded := metadata.Undecoded(); len(undecoded) > 0 {
		unknown := make([]string, 0, len(undecoded))
		for _, key := range undecoded {
			unknown = append(unknown, key.String())
		}
		return keymap{}, fmt.Errorf("configuration %s contains unknown option(s): %s", configPath, strings.Join(unknown, ", "))
	}
	knownActions := make(map[string]keyAction, len(actionDefinitions))
	for _, definition := range actionDefinitions {
		knownActions[string(definition.action)] = definition.action
	}
	for name, keys := range raw.Keymap {
		action, ok := knownActions[name]
		if !ok {
			return keymap{}, fmt.Errorf("configuration %s contains unknown option keymap.%s", configPath, name)
		}
		bindings[action] = keys
	}
	configured, err := buildKeymap(bindings)
	if err != nil {
		return keymap{}, fmt.Errorf("invalid configuration %s: %w", configPath, err)
	}
	return configured, nil
}

func defaultBindings() map[keyAction][]string {
	bindings := make(map[keyAction][]string, len(actionDefinitions))
	for _, definition := range actionDefinitions {
		bindings[definition.action] = append([]string(nil), definition.defaultBindings...)
	}
	return bindings
}

func defaultConfiguration() string {
	var result strings.Builder
	result.WriteString("[keymap]\n")
	for _, definition := range actionDefinitions {
		fmt.Fprintf(&result, "%s = [", definition.action)
		for index, key := range definition.defaultBindings {
			if index > 0 {
				result.WriteString(", ")
			}
			result.WriteString(strconv.Quote(key))
		}
		result.WriteString("]\n")
	}
	return result.String()
}

func configurationPath() (string, error) {
	configHome, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("locate platform configuration directory: %w", err)
	}
	return filepath.Join(configHome, configDirectoryName, configFileName), nil
}

func runConfigCommand(args []string) error {
	if len(args) == 1 && args[0] == "defaults" {
		_, err := fmt.Print(defaultConfiguration())
		return err
	}
	if len(args) == 1 && args[0] == "install" {
		return installDefaultConfiguration(false)
	}
	if len(args) == 2 && args[0] == "install" && args[1] == "--force" {
		return installDefaultConfiguration(true)
	}
	optionCandidates := cliCommands["config"].options
	if len(args) > 0 && args[0] == "install" {
		optionCandidates = append(append([]string(nil), optionCandidates...), "--force")
	}
	for _, argument := range args {
		if strings.HasPrefix(argument, "-") && argument != "--force" {
			return unknownCLIValue("option", argument, optionCandidates, configUsage)
		}
	}
	return cliFailure("invalid config command", "", configUsage)
}

func installDefaultConfiguration(force bool) error {
	configPath, err := configurationPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
		return fmt.Errorf("create configuration directory: %w", err)
	}
	if !force {
		file, err := os.OpenFile(configPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("configuration already exists at %s; use config install --force to replace it", configPath)
		}
		if err != nil {
			return fmt.Errorf("create configuration %s: %w", configPath, err)
		}
		if _, err := file.WriteString(defaultConfiguration()); err != nil {
			_ = file.Close()
			return fmt.Errorf("write configuration %s: %w", configPath, err)
		}
		if err := file.Close(); err != nil {
			return fmt.Errorf("close configuration %s: %w", configPath, err)
		}
		return nil
	}

	previous, err := os.ReadFile(configPath)
	if errors.Is(err, os.ErrNotExist) {
		return installDefaultConfiguration(false)
	}
	if err != nil {
		return fmt.Errorf("read existing configuration %s before backup: %w", configPath, err)
	}
	backupPath := configPath + ".backup-" + time.Now().UTC().Format("20060102T150405.000000000Z")
	if err := os.WriteFile(backupPath, previous, 0o600); err != nil {
		return fmt.Errorf("back up existing configuration to %s: %w", backupPath, err)
	}
	temporary, err := os.CreateTemp(filepath.Dir(configPath), ".config-*.toml")
	if err != nil {
		return fmt.Errorf("create replacement configuration: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("secure replacement configuration: %w", err)
	}
	if _, err := temporary.WriteString(defaultConfiguration()); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write replacement configuration: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close replacement configuration: %w", err)
	}
	if err := os.Rename(temporaryPath, configPath); err != nil {
		return fmt.Errorf("replace configuration %s (backup retained at %s): %w", configPath, backupPath, err)
	}
	return nil
}
