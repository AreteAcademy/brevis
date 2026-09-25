package gateway_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/AreteAcademy/brevis/gateway"
	"github.com/AreteAcademy/brevis/gateway/metastore/memcached"
	"github.com/AreteAcademy/brevis/gateway/metastore/redis"
)

// The three backends have to behave the same, because the code above them does
// not know which it is talking to.
//
// The same table run against all of them, and `memory` is in it: it is the one
// that always compiles, so it is the one most likely to drift.
func TestEveryMetastoreBehavesTheSame(t *testing.T) {
	for _, b := range backends(t) {
		t.Run(b.name, func(t *testing.T) {
			m := b.open(t)
			ctx := context.Background()
			key := fmt.Sprintf("test:%s:%d", b.name, time.Now().UnixNano())

			// Get on an absent key is a miss and NOT an error: a cache that
			// errors on a miss is a cache every caller has to special-case.
			if _, ok, err := m.Get(ctx, key); err != nil || ok {
				t.Fatalf("an absent key gave ok=%v err=%v", ok, err)
			}

			if err := m.Put(ctx, key, "1", time.Minute); err != nil {
				t.Fatal(err)
			}
			if v, ok, err := m.Get(ctx, key); err != nil || !ok || v != "1" {
				t.Errorf("Put then Get gave %q ok=%v err=%v", v, ok, err)
			}

			// Claim is set-if-absent: the first caller gets it and the second
			// does not. This is the whole reason the shared backends exist --
			// with `memory` and N replicas each would get its own yes, which
			// is what the docs say and what makes redis the answer.
			claim := key + ":claim"
			if got, err := m.Claim(ctx, claim, time.Minute); err != nil || !got {
				t.Fatalf("the first Claim gave %v, %v", got, err)
			}
			if got, err := m.Claim(ctx, claim, time.Minute); err != nil || got {
				t.Errorf("the second Claim gave %v, %v — it is not exclusive", got, err)
			}

			// Incr starts at one and counts up.
			counter := key + ":count"
			for want := int64(1); want <= 3; want++ {
				got, err := m.Incr(ctx, counter, time.Minute)
				if err != nil || got != want {
					t.Fatalf("Incr %d gave %d, %v", want, got, err)
				}
			}

			if m.Describe() != b.name {
				t.Errorf("Describe is %q, want %q", m.Describe(), b.name)
			}
		})
	}
}

// The counter's window ROLLS, because the expiry is set when the counter is
// created and never extended.
//
// Extending it on every increment is the easy mistake and a quiet one: a busy
// key never expires, so `naming.max_new_per_hour` stops being a rate and
// becomes a lifetime total. A stream that created twenty tables in its first
// hour would then be blocked forever, and the log would say it had reached its
// hourly limit.
func TestTheCounterWindowRolls(t *testing.T) {
	for _, b := range backends(t) {
		t.Run(b.name, func(t *testing.T) {
			m := b.open(t)
			ctx := context.Background()
			key := fmt.Sprintf("rolls:%s:%d", b.name, time.Now().UnixNano())

			// The timing separates the two behaviours rather than merely
			// outlasting both, which the first version of this did and so
			// passed against the bug.
			//
			//	    t=0.0  Incr -> 1.  Correct: expires by 4.0.
			//	    t=2.5  Incr -> 2.  Buggy: expiry pushed to 6.5.
			//	    t=5.0  Incr -> correct 1 (expired)
			//	                   buggy   3 (alive until 6.5)
			//
			// Four seconds and not two, because memcached's expiry has
			// SECOND granularity: an item stored for 2s can be gone at 1.x,
			// and a 1.4s check against it failed here about one run in three.
			// The margins are now a second on each side.
			const ttl = 4 * time.Second
			if n, err := m.Incr(ctx, key, ttl); err != nil || n != 1 {
				t.Fatalf("the first Incr gave %d, %v", n, err)
			}
			time.Sleep(2500 * time.Millisecond)
			if n, err := m.Incr(ctx, key, ttl); err != nil || n != 2 {
				t.Fatalf("the second Incr gave %d, %v — it should still be inside "+
					"the window", n, err)
			}
			time.Sleep(2500 * time.Millisecond)

			n, err := m.Incr(ctx, key, ttl)
			if err != nil {
				t.Fatal(err)
			}
			if n != 1 {
				t.Errorf("after the window the counter is at %d, want 1 — the TTL "+
					"was extended, so the window never rolls", n)
			}
		})
	}
}

// A claim expires, so a replica that dies holding one stalls nobody.
//
// It is a debounce and not a lock: there is nothing to release, and the next
// batch after the window simply tries again.
func TestAClaimExpires(t *testing.T) {
	for _, b := range backends(t) {
		t.Run(b.name, func(t *testing.T) {
			m := b.open(t)
			ctx := context.Background()
			key := fmt.Sprintf("expiry:%s:%d", b.name, time.Now().UnixNano())

			// One second is memcached's floor, so it is the floor here.
			if got, _ := m.Claim(ctx, key, time.Second); !got {
				t.Fatal("the first claim was refused")
			}
			if got, _ := m.Claim(ctx, key, time.Second); got {
				t.Fatal("the claim was not exclusive")
			}

			for i := 0; i < 40; i++ {
				time.Sleep(100 * time.Millisecond)
				if got, _ := m.Claim(ctx, key, time.Second); got {
					return
				}
			}
			t.Error("the claim never expired, so a dead holder would stall everybody")
		})
	}
}

// Concurrent claims on one key produce exactly one winner.
//
// The property the debounce rests on, and the one a naive get-then-set would
// fail: ten replicas detecting one new field would all read "absent" and all
// write, and all ten would run the ALTER.
func TestOnlyOneClaimWins(t *testing.T) {
	for _, b := range backends(t) {
		t.Run(b.name, func(t *testing.T) {
			m := b.open(t)
			key := fmt.Sprintf("race:%s:%d", b.name, time.Now().UnixNano())

			const racers = 20
			var wg sync.WaitGroup
			won := make(chan bool, racers)
			start := make(chan struct{})
			for i := 0; i < racers; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-start
					got, err := m.Claim(context.Background(), key, time.Minute)
					if err == nil {
						won <- got
					}
				}()
			}
			close(start)
			wg.Wait()
			close(won)

			winners := 0
			for w := range won {
				if w {
					winners++
				}
			}
			if winners != 1 {
				t.Errorf("%d of %d racers won the claim, want exactly 1", winners, racers)
			}
		})
	}
}

type backend struct {
	name string
	open func(*testing.T) gateway.Metastore
}

func backends(t *testing.T) []backend {
	t.Helper()
	out := []backend{{
		name: gateway.MetastoreMemory,
		open: func(*testing.T) gateway.Metastore { return gateway.NewMemoryMetastore() },
	}}
	if addr := os.Getenv("BREVIS_GATEWAY_TEST_REDIS"); addr != "" {
		out = append(out, backend{name: redis.Name, open: func(t *testing.T) gateway.Metastore {
			m, err := redis.Open(context.Background(), addr)
			if err != nil {
				t.Fatal(err)
			}
			return m
		}})
	}
	if addr := os.Getenv("BREVIS_GATEWAY_TEST_MEMCACHED"); addr != "" {
		out = append(out, backend{name: memcached.Name, open: func(t *testing.T) gateway.Metastore {
			m, err := memcached.Open(context.Background(), addr)
			if err != nil {
				t.Fatal(err)
			}
			return m
		}})
	}
	if len(out) == 1 {
		t.Log("only `memory` is under test; set BREVIS_GATEWAY_TEST_REDIS and " +
			"BREVIS_GATEWAY_TEST_MEMCACHED for the shared ones")
	}
	return out
}

// Several gateways sharing one Redis behave like one: exactly one of them
// runs the DDL for a table, and the rest come back and find it done.
//
// This is the whole reason the shared backends exist. With `memory` each
// replica has its own claim, so all of them attempt it — ten replicas meeting
// one new field is ten ALTERs against BigQuery's five metadata operations per
// table per ten seconds, and the quota is gone in a second.
//
// It is proven against a real Postgres because the DDL has to be real: the
// question is how many ALTERs reach a server, not how many the code intended.
func TestIntegrationReplicasSharingARedisRunOneDDL(t *testing.T) {
	dsn := os.Getenv("BREVIS_GATEWAY_TEST_PG_DSN")
	addr := os.Getenv("BREVIS_GATEWAY_TEST_REDIS")
	if dsn == "" || addr == "" {
		t.Skip("BREVIS_GATEWAY_TEST_PG_DSN and BREVIS_GATEWAY_TEST_REDIS are needed")
	}
	shared, err := redis.Open(context.Background(), addr)
	if err != nil {
		t.Fatal(err)
	}

	table := fmt.Sprintf("gwauto_race_%d", time.Now().UnixNano())
	key := "brevis:gw:tables:ddl:" + table + ":id string,"

	// Twenty replicas meeting the same shape at the same instant.
	const replicas = 20
	var wg sync.WaitGroup
	claimed := make(chan bool, replicas)
	start := make(chan struct{})
	for i := 0; i < replicas; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			got, err := shared.Claim(context.Background(), key, 3*time.Second)
			if err == nil {
				claimed <- got
			}
		}()
	}
	close(start)
	wg.Wait()
	close(claimed)

	winners := 0
	for c := range claimed {
		if c {
			winners++
		}
	}
	if winners != 1 {
		t.Fatalf("%d of %d replicas would have run the DDL, want exactly 1",
			winners, replicas)
	}

	// And the same key with `memory` — one store per replica — gives every
	// one of them a yes, which is the failure the shared backend prevents.
	local := 0
	for i := 0; i < replicas; i++ {
		own := gateway.NewMemoryMetastore()
		if got, _ := own.Claim(context.Background(), key, 3*time.Second); got {
			local++
		}
	}
	if local != replicas {
		t.Errorf("with memory %d of %d claimed; the point is that ALL of them do",
			local, replicas)
	}
}

// Two gateways sharing a Redis, meeting the same new field at the same moment:
// BOTH events land.
//
// The first version buried one of them. The claim window was three seconds and
// the pipe's four retries are spent in roughly two, so the loser never got
// back in — it exhausted its attempts inside the window and went to the dead
// letter. A debounce that can bury a batch is not a debounce, and no unit test
// could have shown it: it needs two processes, one Redis and a real ALTER.
func TestIntegrationTwoReplicasBothLand(t *testing.T) {
	dsn := os.Getenv("BREVIS_GATEWAY_TEST_PG_DSN")
	addr := os.Getenv("BREVIS_GATEWAY_TEST_REDIS")
	if dsn == "" || addr == "" {
		t.Skip("BREVIS_GATEWAY_TEST_PG_DSN and BREVIS_GATEWAY_TEST_REDIS are needed")
	}
	t.Setenv("PG_DSN", dsn)
	t.Setenv("REDIS_ADDR", addr)
	table := uniqueTable(t, dsn)

	// Two servers, each its own router and its own in-process cache, sharing
	// one metastore — which is what two pods are.
	var servers []*gateway.Server
	var urls []string
	for i := 0; i < 2; i++ {
		srv := autoTableGateway(t, dsn, "", "merge", "columns", "1", "redis")
		ts := httptest.NewServer(srv.Handler())
		defer ts.Close()
		servers = append(servers, srv)
		urls = append(urls, ts.URL)
	}

	// Both meet the new field at the same instant.
	var wg sync.WaitGroup
	for i, u := range urls {
		wg.Add(1)
		go func(i int, u string) {
			defer wg.Done()
			postKeyed(t, u+"/v1/tables", fmt.Sprintf(
				`[{"table_name":%q,"data":{"id":"R-%d","total":10,"novo_campo":"v%d"}}]`,
				table, i, i), http.StatusAccepted)
		}(i, u)
	}
	wg.Wait()
	for _, srv := range servers {
		if err := srv.Close(context.Background()); err != nil {
			t.Fatalf("draining: %v", err)
		}
	}

	rows := query(t, dsn, `SELECT brevis_record_key FROM `+table+` ORDER BY brevis_record_key`)
	if len(rows) != 2 {
		t.Errorf("%d of 2 events landed: %v — the loser of the claim was buried "+
			"instead of retrying through", len(rows), rows)
	}

	// And the column exists exactly once, whichever replica made it.
	cols := query(t, dsn, fmt.Sprintf(
		`SELECT count(*)::text FROM information_schema.columns
		 WHERE table_name='%s' AND column_name='novo_campo'`, table))
	if cols[0] != "1" {
		t.Errorf("novo_campo exists %s times", cols[0])
	}
}
