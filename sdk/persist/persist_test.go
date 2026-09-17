package persist_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/AreteAcademy/brevis/sdk/persist"
)

// The case that asked for this: 11,276 station codes, 99 KB, twenty-five times
// what the termination message holds. It has to come back whole and as text a
// query can use.
func TestAListTooBigForTheTerminationMessageSurvives(t *testing.T) {
	ctx, dir := local(t)

	codes := make([]any, 11276)
	for i := range codes {
		codes[i] = float64(26130000 + i)
	}
	if err := persist.Set(ctx, "ana.station_codes", codes); err != nil {
		t.Fatal(err)
	}

	size := stat(t, dir, "ana.station_codes")
	if size < 100_000 {
		t.Fatalf("the stored object is %d bytes; the list alone is about 99 KB", size)
	}

	back, ok, err := persist.Strings(ctx, "ana.station_codes")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("the key was written and reads as absent")
	}
	if len(back) != len(codes) {
		t.Fatalf("wrote %d codes and read %d", len(codes), len(back))
	}

	// JSON has one number type, so these arrive as float64. Rendered with
	// fmt.Sprint the first one is "2.613e+07", and a query built from that asks
	// for a station that does not exist and succeeds with no rows.
	if back[0] != "26130000" {
		t.Errorf("the first code reads %q", back[0])
	}
	if back[len(back)-1] != "26141275" {
		t.Errorf("the last code reads %q", back[len(back)-1])
	}
}

// Last writer wins, and it is a choice: the value is re-derived by the step
// that owns it, so refusing a write to protect a number the next run recomputes
// would fail a run for nothing.
func TestWritingTwiceReplaces(t *testing.T) {
	ctx, dir := local(t)

	if err := persist.Set(ctx, "k", []any{"old"}); err != nil {
		t.Fatal(err)
	}
	if err := persist.Set(ctx, "k", []any{"new"}); err != nil {
		t.Fatal(err)
	}

	got, _, err := persist.Strings(ctx, "k")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != "new" {
		t.Errorf("after two writes the key holds %v", got)
	}

	// One key is one object. to.Files would have left two, named by nanosecond,
	// and a reader would have seen both generations concatenated.
	if n := len(files(t, dir)); n != 1 {
		t.Errorf("two writes left %d objects", n)
	}
}

// Concurrent writers must leave ONE of the values, never a mixture: a torn
// object would parse as valid JSON with half the codes, which is the failure
// that looks like success.
func TestConcurrentWritesLeaveOneWholeValue(t *testing.T) {
	ctx, dir := local(t)

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			body := make([]any, 500)
			for j := range body {
				body[j] = float64(n)
			}
			_ = persist.Set(ctx, "k", body)
		}(i)
	}
	wg.Wait()

	got, ok, err := persist.Strings(ctx, "k")
	if err != nil {
		t.Fatalf("the surviving value does not parse: %v", err)
	}
	if !ok {
		t.Fatal("nothing survived eight writes")
	}
	if len(got) != 500 {
		t.Fatalf("the value holds %d items, so two writers interleaved", len(got))
	}
	for _, v := range got {
		if v != got[0] {
			t.Fatalf("the value mixes writers: %q and %q", got[0], v)
		}
	}
	if n := len(files(t, dir)); n != 1 {
		t.Errorf("eight writes left %d objects", n)
	}
}

// Absent is not an error -- sdk/context says so and this keeps it. It matters
// more here: the store cannot tell the two apart on its own, and a read that
// answered "empty" for a key nobody wrote would build an empty batch query and
// fetch nothing, quietly.
func TestAnAbsentKeyIsNotAnError(t *testing.T) {
	ctx, _ := local(t)

	v, ok, err := persist.Value(ctx, "nobody.wrote.this")
	if err != nil {
		t.Errorf("an absent key errored: %v", err)
	}
	if ok {
		t.Errorf("an absent key reported ok, holding %v", v)
	}

	// And a key that holds an empty list is PRESENT. That is the distinction
	// the whole return signature exists for.
	if err := persist.Set(ctx, "empty", []any{}); err != nil {
		t.Fatal(err)
	}
	list, ok, err := persist.Strings(ctx, "empty")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Error("a key holding an empty list reads as absent")
	}
	if len(list) != 0 {
		t.Errorf("the empty list came back as %v", list)
	}
}

// Nothing configured is the default, and it must name what is missing rather
// than fail like a network hiccup or an absent key. `brevis run` on a laptop is
// exactly this case.
func TestWithNowhereToKeepItTheErrorSaysSo(t *testing.T) {
	t.Setenv(persist.EnvURL, "")
	ctx := context.Background()

	err := persist.Set(ctx, "k", []any{"v"})
	if err == nil {
		t.Fatal("writing with no store configured succeeded")
	}
	for _, want := range []string{persist.EnvURL, "persist_context"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error does not mention %q: %v", want, err)
		}
	}

	if _, ok, err := persist.Value(ctx, "k"); err == nil {
		t.Errorf("reading with no store configured returned ok=%v and no error", ok)
	}
}

// The declaration is in the YAML, and a key outside it is refused by name. It
// is not a sandbox -- the pod holds the store's credential either way -- it is
// what catches a typo and what puts the dependency in the file.
func TestAKeyTheStepDidNotDeclareIsRefused(t *testing.T) {
	ctx, _ := local(t)
	t.Setenv(persist.EnvKeys, "ana.station_codes")

	if err := persist.Set(ctx, "ana.station_codes", []any{"ok"}); err != nil {
		t.Fatalf("the declared key was refused: %v", err)
	}

	err := persist.Set(ctx, "something.else", []any{"no"})
	if err == nil {
		t.Fatal("an undeclared key was written")
	}
	if !strings.Contains(err.Error(), "persist_context") {
		t.Errorf("the refusal does not name the declaration: %v", err)
	}

	// A step that declared nothing is the common mistake, and its message has
	// to say the flag is missing rather than list an empty set.
	t.Setenv(persist.EnvKeys, "")
	err = persist.Set(ctx, "ana.station_codes", []any{"no"})
	if err == nil {
		t.Fatal("a step that declared nothing wrote anyway")
	}
	if !strings.Contains(err.Error(), "declared no persisted context") {
		t.Errorf("the message for a step with no declaration is: %v", err)
	}
}

// A key becomes an object name. A separator would silently make a directory and
// `..` would climb out of the prefix the installation chose.
func TestAKeyThatIsAPathIsRefused(t *testing.T) {
	ctx, _ := local(t)

	for _, key := range []string{"a/b", `a\b`, "..", "", strings.Repeat("k", 201)} {
		if err := persist.Set(ctx, key, []any{"v"}); err == nil {
			t.Errorf("the key %q was accepted", key)
		}
	}
}

// The ceiling is inherited, not chosen: every executor reads a step's stdout
// with a 1 MB line limit and a ConfigMap stops at 1 MB. Above it, the refusal
// has to name where the value belongs instead.
func TestAValueOverTheCeilingIsRefused(t *testing.T) {
	ctx, _ := local(t)

	big := make([]any, 0, 200000)
	for i := 0; i < 200000; i++ {
		big = append(big, "0123456789")
	}
	err := persist.Set(ctx, "k", big)
	if err == nil {
		t.Fatal("a value over the ceiling was written")
	}
	if !strings.Contains(err.Error(), "to.Files") {
		t.Errorf("the refusal does not name the alternative: %v", err)
	}
}

// The feature has a default, and the default is off. A consumer that never
// touches persist must not be asked for a store.
func TestNothingConfiguredCostsNothing(t *testing.T) {
	if os.Getenv(persist.EnvURL) != "" {
		t.Skip("the environment already configures a store")
	}
	// Reaching for it is what fails, and only then. Importing the package,
	// which every SDK consumer does transitively, asks for nothing.
	if persist.MaxBytes != 1<<20 {
		t.Errorf("MaxBytes is %d", persist.MaxBytes)
	}
}

func local(t *testing.T) (context.Context, string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv(persist.EnvURL, dir)
	persist.Use(nil)
	return context.Background(), dir
}

func stat(t *testing.T, dir, key string) int64 {
	t.Helper()
	fi, err := os.Stat(filepath.Join(dir, key+".json"))
	if err != nil {
		t.Fatal(err)
	}
	return fi.Size()
}

func files(t *testing.T, dir string) []os.DirEntry {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	return entries
}

// A single text value is a shape of its own -- a watermark, a cursor -- and it
// must not be confused with a list. Flattening one into the other is the kind
// of difference somebody finds three weeks later.
func TestStringRefusesAListAndViceVersa(t *testing.T) {
	ctx, _ := local(t)

	if err := persist.Set(ctx, "cursor", "2026-09-16T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	got, ok, err := persist.String(ctx, "cursor")
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if got != "2026-09-16T00:00:00Z" {
		t.Errorf("read %q", got)
	}

	// A list read as a string is an error naming the shape, not "a,b".
	if err := persist.Set(ctx, "codes", []any{"57998000", "2140002"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := persist.String(ctx, "codes"); err == nil {
		t.Error("a stored list came back as text")
	} else if !strings.Contains(err.Error(), "Strings") {
		t.Errorf("the error does not name the accessor to use: %v", err)
	}

	// And the other way, which already held: a string read as a list.
	if _, _, err := persist.Strings(ctx, "cursor"); err == nil {
		t.Error("a stored string came back as a list")
	}

	// Absent is ok=false and no error, like everything else here.
	if _, ok, err := persist.String(ctx, "never.written"); ok || err != nil {
		t.Errorf("absent: ok=%v err=%v", ok, err)
	}
}
