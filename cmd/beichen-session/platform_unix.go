//go:build !windows

package main

import (
	"os"
	"os/exec"
	"runtime"
	"strings"
)

func openDefaultBrowser(address string) error {
	launcher := "xdg-open"
	if runtime.GOOS == "darwin" {
		launcher = "open"
	}
	return exec.Command(launcher, address).Run()
}

func disableTerminalEcho(input *os.File) (func() error, error) {
	terminalState := exec.Command("stty", "-g")
	terminalState.Stdin = input
	state, err := terminalState.Output()
	if err != nil {
		return nil, err
	}
	setTerminal := func(setting string) error {
		command := exec.Command("stty", setting)
		command.Stdin = input
		return command.Run()
	}
	if err := setTerminal("-echo"); err != nil {
		return nil, err
	}
	return func() error { return setTerminal(strings.TrimSpace(string(state))) }, nil
}

func restrictExportPermissions(file *os.File) error {
	return file.Chmod(0600)
}

func exportPermissionsNote() string { return "" }
