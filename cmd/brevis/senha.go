package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// semEco reads a line from the terminal with the echo turned off.
//
// The stdlib exposes no terminal control, and the alternative would be adding
// `golang.org/x/term` for a single call. Delegating to `stty` costs one child
// process in an interactive command that runs once per installation — and keeps
// the dependency tree as it is.
//
// If `stty` is not there, the password is read with echo on rather than the
// command failing: whoever is generating a hash inside a minimal container
// would rather type the password visibly than not be able to generate the hash
// at all. The warning makes the choice a conscious one.
func semEco() (string, error) {
	restaurar, err := desligarEco()
	if err != nil {
		fmt.Fprintln(os.Stderr, "\n(warning: no `stty`; the password will show on screen)")
	} else {
		// On any exit — a read error included — the terminal goes back to
		// normal. Without this, a Ctrl-C partway through leaves the operator's
		// shell mute.
		defer restaurar()
	}

	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

func desligarEco() (func(), error) {
	previous, err := stty("-g")
	if err != nil {
		return nil, err
	}
	if _, err := stty("-echo"); err != nil {
		return nil, err
	}
	return func() { _, _ = stty(previous) }, nil
}

func stty(args ...string) (string, error) {
	c := exec.Command("stty", args...)
	c.Stdin = os.Stdin
	output, err := c.Output()
	return strings.TrimSpace(string(output)), err
}
