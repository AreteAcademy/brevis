package api

import (
	"errors"
	"runtime/debug"
	"strings"
	"testing"
	"time"
)

func info(version string, settings ...string) *debug.BuildInfo {
	i := &debug.BuildInfo{Main: debug.Module{Version: version}}
	for k := 0; k+1 < len(settings); k += 2 {
		i.Settings = append(i.Settings, debug.BuildSetting{Key: settings[k], Value: settings[k+1]})
	}
	return i
}

func exeHash(h string) func() (string, error) { return func() (string, error) { return h, nil } }

var started = time.Unix(1760000000, 0)

// A CLEAN REVISION NAMES THE CODE, and is the identity.
func TestACleanRevisionIsTheBuild(t *testing.T) {
	id := identify(info("v0.6.0", "vcs.revision", "abc123", "vcs.modified", "false"), true, exeHash("h1"), started)
	if !strings.Contains(id, "abc123") || strings.Contains(id, "h1") {
		t.Errorf("identity %q", id)
	}
}

// NOTHING ELSE IS TRUSTED TO NAME THE CODE: a build without .git, with
// -buildvcs=false, or from a dirty tree all say the same thing of two
// different binaries -- and the cache would serve the old one's answers.
// The executable's own hash tells them apart.
func TestWithoutACleanRevisionTheExecutableIsTheBuild(t *testing.T) {
	for name, i := range map[string]*debug.BuildInfo{
		"no vcs": info("(devel)"),
		"dirty":  info("(devel)", "vcs.revision", "abc123", "vcs.modified", "true"),
	} {
		t.Run(name, func(t *testing.T) {
			a := identify(i, true, exeHash("h1"), started)
			b := identify(i, true, exeHash("h2"), started)
			if a == b || !strings.Contains(a, "h1") {
				t.Errorf("two executables are %q and %q", a, b)
			}
		})
	}
}

// AND WHEN NOT EVEN THAT CAN BE READ, THIS PROCESS IS THE BUILD: every
// restart is a cold cache, which is slower, and never a stale one.
func TestWithNothingToReadTheProcessIsTheBuild(t *testing.T) {
	unreadable := func() (string, error) { return "", errors.New("no /proc") }
	a := identify(info("(devel)"), true, unreadable, started)
	b := identify(nil, false, unreadable, started.Add(time.Second))
	if a == b || a == identify(info("(devel)"), true, unreadable, started.Add(time.Nanosecond)) {
		t.Errorf("two processes share %q", a)
	}
}
