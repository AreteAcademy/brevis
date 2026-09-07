package context

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The suite runs in-package because the module-level state -- the once, the map
// -- has to be reset between cases, and a real step never needs to.
func reset(t *testing.T, input string) string {
	t.Helper()
	once = resetOnce()
	incoming, inputErr = nil, nil
	published = map[string]any{}

	out := filepath.Join(t.TempDir(), "output")
	t.Setenv(EnvInput, input)
	t.Setenv(EnvOutput, out)
	return out
}

const pipeline = `{
  "extract":   {"bucket": "s3://landing/2026-09-07", "rows": 48213},
  "transform": {"bucket": "s3://silver/2026-09-07", "ratio": 1.5}
}`

func TestItReadsWhatAStepItDependsOnPublished(t *testing.T) {
	reset(t, pipeline)

	got, err := String("extract.bucket")
	if err != nil || got != "s3://landing/2026-09-07" {
		t.Errorf("String = %q, %v", got, err)
	}
	if n, err := Int("extract.rows"); err != nil || n != 48213 {
		t.Errorf("Int = %d, %v", n, err)
	}
}

// The isolation property, from the reader's side: both steps published `bucket`
// and neither lost it. That is what keying by step buys, and why the qualified
// form is not ceremony.
func TestTwoStepsPublishingTheSameKeyBothSurvive(t *testing.T) {
	reset(t, pipeline)

	a, _ := String("extract.bucket")
	b, _ := String("transform.bucket")
	if a == b || a == "" || b == "" {
		t.Errorf("the two buckets collapsed: %q and %q", a, b)
	}
}

func TestABareKeyIsRefusedNamingTheStepsThatPublishedIt(t *testing.T) {
	reset(t, pipeline)

	_, _, err := Value("bucket")
	if err == nil {
		t.Fatal("a bare key was accepted; searching and picking one is precedence by accident")
	}
	for _, want := range []string{"extract.bucket", "transform.bucket"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error does not offer %q: %v", want, err)
		}
	}
}

func TestAStepItDoesNotDependOnPointsAtDependsOn(t *testing.T) {
	reset(t, pipeline)

	_, err := String("load.anything")
	if err == nil || !strings.Contains(err.Error(), "depends_on") {
		t.Fatalf("err = %v; want it to point at the fix", err)
	}
	if !strings.Contains(err.Error(), "extract, transform") {
		t.Errorf("the error does not list what IS visible: %v", err)
	}
}

func TestAnAbsentKeyIsNotAnError(t *testing.T) {
	reset(t, pipeline)

	v, ok, err := Value("extract.nope")
	if err != nil || ok || v != nil {
		t.Errorf("got %v, %v, %v; want the zero values and no error", v, ok, err)
	}
}

// A fractional number is refused rather than truncated. Silently turning 1.5
// into 1 is the class of difference this project paid for once, in ingestion_id.
func TestIntRefusesAFractionalNumber(t *testing.T) {
	reset(t, pipeline)

	if _, err := Int("transform.ratio"); err == nil {
		t.Fatal("1.5 was accepted as an int")
	}
}

func TestIntoDecodesAWholeStepsObject(t *testing.T) {
	reset(t, pipeline)

	var got struct {
		Bucket string `json:"bucket"`
		Rows   int    `json:"rows"`
	}
	if err := Into("extract", &got); err != nil {
		t.Fatal(err)
	}
	if got.Bucket == "" || got.Rows != 48213 {
		t.Errorf("got %+v", got)
	}
}

// The question the request asked, in Go: two calls, both keys survive.
func TestTwoCallsMergeRatherThanReplace(t *testing.T) {
	out := reset(t, pipeline)

	if err := Set("name", "Daniel"); err != nil {
		t.Fatal(err)
	}
	if err := Set("label", "Nome"); err != nil {
		t.Fatal(err)
	}

	var got map[string]any
	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got["name"] != "Daniel" || got["label"] != "Nome" {
		t.Errorf("published = %v; both calls have to survive, or a helper "+
			"publishing one key silently erases what its caller published", got)
	}
}

func TestTheSameKeyTwiceKeepsTheLastWrite(t *testing.T) {
	reset(t, pipeline)
	_ = Set("rows", 1)
	_ = Set("rows", 2)
	if Published()["rows"] != 2 {
		t.Errorf("published = %v", Published())
	}
}

// Writing on every Set is the deliberate difference from the Python library,
// which defers to atexit. Go has no atexit, and a forgotten Flush() would be
// silent data loss.
func TestSetReachesTheFileImmediately(t *testing.T) {
	out := reset(t, pipeline)

	if err := Set("rows", 1); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(out); err != nil {
		t.Fatalf("nothing was written, so a step with no Flush() would publish "+
			"nothing: %v", err)
	}
}

func TestAValueThatIsNotJSONIsRefusedNamingTheKey(t *testing.T) {
	reset(t, pipeline)

	err := Set("thing", make(chan int))
	if err == nil || !strings.Contains(err.Error(), "thing") {
		t.Fatalf("err = %v; want a refusal naming the key", err)
	}
}

// The refusal that matters most: the platform truncates rather than refuses, so
// an oversized object arrives cut in half and reads downstream as "published
// nothing".
func TestGoingOverTheCeilingIsRefusedAndLeavesNothingBehind(t *testing.T) {
	out := reset(t, pipeline)

	err := Set("rows", strings.Repeat("x", MaxBytes))
	if err == nil {
		t.Fatal("an oversized value was accepted")
	}
	if !strings.Contains(err.Error(), "rows") {
		t.Errorf("the error does not name the culprit: %v", err)
	}
	if _, ok := Published()["rows"]; ok {
		t.Error("a refused Set left the value behind, so the next Set would " +
			"fail on a key the caller thinks it never set")
	}
	if _, err := os.Stat(out); err == nil {
		t.Error("a refused Set still wrote the file")
	}
}

func TestTheCeilingCountsEverythingNotOneCall(t *testing.T) {
	reset(t, pipeline)

	if err := Set("a", strings.Repeat("x", 2000)); err != nil {
		t.Fatal(err)
	}
	if err := Set("b", strings.Repeat("y", 3000)); err == nil {
		t.Fatal("two values under the limit that together exceed it were accepted")
	}
}

// Outside Brevis nothing reads the output, so Set validates and does not write.
// A step that cannot be run by hand cannot be developed.
func TestRunningByHandStillValidates(t *testing.T) {
	reset(t, pipeline)
	t.Setenv(EnvOutput, "")

	if err := Set("rows", 1); err != nil {
		t.Errorf("Set failed with no engine: %v", err)
	}
	if err := Set("thing", make(chan int)); err == nil {
		t.Error("the guards were skipped outside the engine, so a value that " +
			"works on a laptop would fail in production")
	}
}

// The wire format, which three implementations read: this package, the Python
// library, and a bash step with jq. A wrapper added here would be invisible
// until one of the other two failed to find a key.
func TestTheWireFormatIsOneFlatObject(t *testing.T) {
	out := reset(t, pipeline)

	_ = Set("bucket", "s3://x")
	_ = Set("rows", 48213)

	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("what was written is not JSON: %q", raw)
	}
	if len(got) != 2 || got["bucket"] != "s3://x" {
		t.Errorf("wrote %q; the engine expects one flat object keyed by name", raw)
	}
}
