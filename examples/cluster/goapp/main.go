// vendor is a stand-in for the binary a vendor actually ships.
//
// It exists to be run BY BREVIS, INSIDE A POD BREVIS DID NOT CREATE, and to
// prove both halves of that sentence while it runs:
//
//   - `vendor info` prints the hostname it is running on and how long that
//     container has been up. A container older than the run is a container
//     nobody started for the run.
//   - `vendor fetch` writes its result to $BREVIS_OUTPUT, the same contract a
//     step gets in a pod.
//   - `vendor report` reads $BREVIS_INPUT and names the step that filled it.
//
// Nothing here imports Brevis. That is the point of the mode: the engine sends
// a command and follows its output, so this program is indifferent to whether
// it was started in a pod per step, in a pod that was already up, or by hand
// in a terminal. The two environment variables are the whole interface, and
// both are optional -- run it yourself and it says so rather than failing.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

// The version of the VENDOR's tool, which has nothing to do with the engine's.
// A client upgrading Brevis does not upgrade this, and that independence is
// the reason the mode exists.
const version = "1.4.0"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "info":
		err = info()
	case "fetch":
		err = fetch(os.Args[2:])
	case "report":
		err = report()
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "vendor:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintf(os.Stderr, `vendor %s -- a stand-in for the binary you ship

  vendor info                       where am I running, and since when
  vendor fetch --rows N [--delay D] pretend to fetch, publish the count
  vendor report                     read what an earlier step published
`, version)
}

// info answers the question the whole mode is about: WHERE did this run?
func info() error {
	host, _ := os.Hostname()
	fmt.Printf("vendor %s, built with %s\n", version, runtime.Version())
	fmt.Printf("running on %s as uid %d\n", host, os.Getuid())
	if age, ok := containerAge(); ok {
		fmt.Printf("this container has been up for %s, so it was not created for this step\n",
			age.Truncate(time.Second))
	} else {
		// Said rather than guessed. A wrong number here would be read as
		// evidence, and the one thing this command is for is evidence.
		fmt.Println("this container's age is not readable on this platform")
	}
	return nil
}

// fetch does the work, slowly enough to watch, and publishes what it found.
func fetch(args []string) error {
	fs := flag.NewFlagSet("fetch", flag.ContinueOnError)
	rows := fs.Int("rows", 0, "how many rows to pretend to fetch")
	source := fs.String("source", "partner-api", "where they came from")
	delay := fs.Duration("delay", 300*time.Millisecond, "pause between batches, so the stream has something in it")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *rows <= 0 {
		return fmt.Errorf("--rows wants a positive number, got %d", *rows)
	}

	const batch = 32
	for done := 0; done < *rows; {
		n := batch
		if left := *rows - done; left < n {
			n = left
		}
		done += n
		// Unbuffered and one line at a time: this is what the engine streams
		// back, and a progress bar nobody can follow is not progress.
		fmt.Printf("fetched %d/%d rows from %s\n", done, *rows, *source)
		time.Sleep(*delay)
	}

	host, _ := os.Hostname()
	return publish(map[string]any{"rows": *rows, "source": *source, "host": host})
}

// report reads what an earlier step published.
func report() error {
	raw := os.Getenv("BREVIS_INPUT")
	if raw == "" {
		return fmt.Errorf("BREVIS_INPUT is empty: either no step upstream published anything, " +
			"or this step does not depend on the one that did")
	}
	var byStep map[string]struct {
		Rows   int    `json:"rows"`
		Source string `json:"source"`
		Host   string `json:"host"`
	}
	if err := json.Unmarshal([]byte(raw), &byStep); err != nil {
		return fmt.Errorf("reading BREVIS_INPUT: %w", err)
	}

	// Sorted, because a map's order is random and a demo that prints its steps
	// in a different order every run looks like it did something different.
	ids := make([]string, 0, len(byStep))
	for id := range byStep {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	total := 0
	for _, id := range ids {
		p := byStep[id]
		fmt.Printf("step %q fetched %d rows from %s on %s\n", id, p.Rows, p.Source, p.Host)
		total += p.Rows
	}
	host, _ := os.Hostname()
	fmt.Printf("%d rows in all, counted on %s\n", total, host)
	return nil
}

// publish writes the step's result where Brevis will read it.
//
// THE PATH IS THE AGENT'S, not one this program picks: it arrives in
// $BREVIS_OUTPUT and the agent turns the file into the step's context. Empty
// means nobody is listening -- a terminal, a `docker run` -- and that is worth
// a line on stderr rather than an error, because the fetch itself succeeded.
func publish(v any) error {
	path := os.Getenv("BREVIS_OUTPUT")
	if path == "" {
		fmt.Fprintln(os.Stderr, "vendor: BREVIS_OUTPUT is unset, so nothing was published")
		return nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("encoding the result: %w", err)
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}

// containerAge is how long PID 1 in this container has been running.
//
// It is the evidence for "no pod was created for this step", and it is read
// rather than asserted: /proc/1 is the agent, started when the pod started, so
// an age larger than the run's own age settles the question.
//
// Linux only, and false rather than a number on anything else.
func containerAge() (time.Duration, bool) {
	up, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0, false
	}
	var upSec float64
	if _, err := fmt.Sscanf(string(up), "%f", &upSec); err != nil {
		return 0, false
	}
	stat, err := os.ReadFile("/proc/1/stat")
	if err != nil {
		return 0, false
	}
	// The second field is the executable's name, in parentheses, and it may
	// contain spaces -- splitting the whole line on whitespace is the classic
	// way to read the wrong column. Everything after the LAST ')' is field 3
	// onwards.
	end := bytes.LastIndexByte(stat, ')')
	if end < 0 {
		return 0, false
	}
	fields := strings.Fields(string(stat[end+1:]))
	const starttime = 22 - 3 // field 22 of proc(5), counted from field 3
	if len(fields) <= starttime {
		return 0, false
	}
	ticks, err := strconv.ParseFloat(fields[starttime], 64)
	if err != nil {
		return 0, false
	}
	const hz = 100 // USER_HZ, which is 100 on every Linux this runs on
	age := upSec - ticks/hz
	if age < 0 {
		return 0, false
	}
	return time.Duration(age * float64(time.Second)), true
}
