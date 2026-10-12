package generation

import (
	"context"
	"strings"
	"testing"
)

// EVERY KEY CARRIES THE {brevis} HASH TAG, so an MGET of several models'
// generations lands on one Redis Cluster slot instead of being refused.
func TestAGenerationKeyIsHashTagged(t *testing.T) {
	if got := Key("marts.clicks_daily"); got != "brevis:gen:{brevis}:marts.clicks_daily" {
		t.Errorf("key %q", got)
	}
}

func TestTwoTokensAreNeverTheSame(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 1000; i++ {
		tok := NewToken()
		if seen[tok] {
			t.Fatalf("token %s twice", tok)
		}
		seen[tok] = true
	}
}

// WRITTEN, THEN READ IN ONE ROUND TRIP, with an absent model as "" -- a
// model nobody has built through `--generations-from` is a generation of
// nothing, and the cache key says so rather than failing.
func TestGenerationsAreWrittenAndReadBack(t *testing.T) {
	f := startFake(t, "")
	c, _ := Open(f.addr)
	defer c.Close()
	ctx := context.Background()
	if err := Write(ctx, c, []string{"marts.a", "marts.b"}, "tok-1"); err != nil {
		t.Fatal(err)
	}
	f.seen = nil
	got, err := Read(ctx, c, []string{"marts.a", "marts.never", "marts.b"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, ",") != "tok-1,,tok-1" {
		t.Errorf("read %q", got)
	}
	if len(f.seen) != 1 || f.seen[0] != "MGET" {
		t.Errorf("reading took %v", f.seen)
	}
}

func TestReadingNothingAsksNothing(t *testing.T) {
	f := startFake(t, "")
	c, _ := Open(f.addr)
	defer c.Close()
	got, err := Read(context.Background(), c, nil)
	if err != nil || len(got) != 0 || len(f.seen) != 0 {
		t.Errorf("%v %v %v", got, err, f.seen)
	}
}
