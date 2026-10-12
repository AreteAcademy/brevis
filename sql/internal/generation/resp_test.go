package generation

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeRedis speaks enough RESP2 to answer this client: AUTH, SELECT, SET
// (with PX), GET and MGET, in memory. A fake and not a mock: it parses what
// the client writes as a real server would, so a malformed command is an
// error here too.
type fakeRedis struct {
	mu       sync.Mutex
	data     map[string][]byte
	expiry   map[string]time.Time
	password string
	authed   map[net.Conn]bool
	seen     []string
	addr     string
}

func startFake(t *testing.T, password string) *fakeRedis {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeRedis{data: map[string][]byte{}, expiry: map[string]time.Time{}, password: password,
		authed: map[net.Conn]bool{}, addr: ln.Addr().String()}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go f.serve(c)
		}
	}()
	return f
}

func (f *fakeRedis) serve(c net.Conn) {
	defer c.Close()
	r := bufio.NewReader(c)
	for {
		rep, err := readReply(r, 0)
		if err != nil {
			return
		}
		args := make([]string, len(rep.elems))
		for i, e := range rep.elems {
			args[i] = string(e.str)
		}
		_, _ = c.Write(f.answer(c, args))
	}
}

func (f *fakeRedis) answer(c net.Conn, args []string) []byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seen = append(f.seen, strings.ToUpper(args[0]))
	cmd := strings.ToUpper(args[0])
	if f.password != "" && !f.authed[c] && cmd != "AUTH" {
		return []byte("-NOAUTH Authentication required.\r\n")
	}
	bulk := func(v []byte, ok bool) string {
		if !ok {
			return "$-1\r\n"
		}
		return fmt.Sprintf("$%d\r\n%s\r\n", len(v), v)
	}
	get := func(k string) ([]byte, bool) {
		if exp, has := f.expiry[k]; has && time.Now().After(exp) {
			delete(f.data, k)
			delete(f.expiry, k)
		}
		v, ok := f.data[k]
		return v, ok
	}
	switch cmd {
	case "AUTH":
		if args[len(args)-1] != f.password {
			return []byte("-WRONGPASS invalid password\r\n")
		}
		f.authed[c] = true
		return []byte("+OK\r\n")
	case "SELECT", "PING":
		return []byte("+OK\r\n")
	case "SET":
		f.data[args[1]] = []byte(args[2])
		delete(f.expiry, args[1])
		if len(args) == 5 && strings.ToUpper(args[3]) == "PX" {
			var ms int64
			fmt.Sscan(args[4], &ms)
			f.expiry[args[1]] = time.Now().Add(time.Duration(ms) * time.Millisecond)
		}
		return []byte("+OK\r\n")
	case "GET":
		v, ok := get(args[1])
		return []byte(bulk(v, ok))
	case "MGET":
		out := fmt.Sprintf("*%d\r\n", len(args)-1)
		for _, k := range args[1:] {
			v, ok := get(k)
			out += bulk(v, ok)
		}
		return []byte(out)
	}
	return []byte("-ERR unknown command\r\n")
}

func TestSetGetAndMGetRoundTripBytes(t *testing.T) {
	f := startFake(t, "")
	c, err := Open(f.addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx := context.Background()

	// BYTES, not text: a cached answer is Brotli, and a client that read a
	// bulk string up to a CRLF would cut it at the first one inside.
	value := []byte("a\r\nb\x00c")
	if err := c.Set(ctx, "k", value, 0); err != nil {
		t.Fatal(err)
	}
	got, found, err := c.Get(ctx, "k")
	if err != nil || !found || !bytes.Equal(got, value) {
		t.Errorf("GET k = %q, %v, %v", got, found, err)
	}
	if _, found, err := c.Get(ctx, "absent"); found || err != nil {
		t.Errorf("an absent key was found, or errored: %v", err)
	}
	vals, err := c.MGet(ctx, "k", "absent")
	if err != nil || len(vals) != 2 || !bytes.Equal(vals[0], value) || vals[1] != nil {
		t.Errorf("MGET = %q, %v", vals, err)
	}
}

func TestATTLExpires(t *testing.T) {
	f := startFake(t, "")
	c, _ := Open(f.addr)
	defer c.Close()
	ctx := context.Background()
	if err := c.Set(ctx, "k", []byte("v"), 20*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	time.Sleep(40 * time.Millisecond)
	if _, found, _ := c.Get(ctx, "k"); found {
		t.Error("the key outlived its ttl")
	}
}

// THE PASSWORD COMES FROM THE URL, and is sent once per connection before
// anything else.
func TestAPasswordInTheURLAuthenticates(t *testing.T) {
	f := startFake(t, "s3cret")
	c, err := Open("redis://:s3cret@" + f.addr + "/2")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.Set(context.Background(), "k", []byte("v"), 0); err != nil {
		t.Fatalf("an authenticated SET failed: %v", err)
	}
	if f.seen[0] != "AUTH" || f.seen[1] != "SELECT" {
		t.Errorf("the connection opened with %v", f.seen)
	}

	wrong, _ := Open("redis://:nope@" + f.addr)
	defer wrong.Close()
	err = wrong.Set(context.Background(), "k", []byte("v"), 0)
	if err == nil || strings.Contains(err.Error(), "nope") {
		t.Errorf("a wrong password: %v (and it must never be quoted)", err)
	}
}

func TestAnAddressThatIsNotOneIsRefused(t *testing.T) {
	for _, addr := range []string{"", "http://x:1", "redis://", "redis://h:1/notadb", "unix:///tmp/r"} {
		if _, err := Open(addr); err == nil {
			t.Errorf("%q opened", addr)
		}
	}
}

// AN ERROR NEVER QUOTES THE ADDRESS: it may carry a password, and an error
// ends up in a log.
func TestAnUnreachableRedisSaysSoWithoutTheURL(t *testing.T) {
	c, err := Open("redis://:pw@127.0.0.1:1")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	_, _, err = c.Get(ctx, "k")
	if err == nil || strings.Contains(err.Error(), "pw") {
		t.Errorf("got %v", err)
	}
}

// THE PARSER IS FED WHAT A SERVER SENDS, and a server can be wrong, slow or
// hostile: no input panics, and none makes it allocate past its limits.
func FuzzReadReply(f *testing.F) {
	for _, seed := range []string{"+OK\r\n", "-ERR x\r\n", ":12\r\n", "$3\r\nabc\r\n", "$-1\r\n",
		"*2\r\n$1\r\na\r\n$-1\r\n", "*-1\r\n", "$999999999999\r\n", "*99999999\r\n", "*1\r\n*1\r\n*1\r\n*1\r\n*1\r\n:1\r\n"} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, in []byte) {
		_, _ = readReply(bufio.NewReader(bytes.NewReader(in)), 0)
	})
}

func TestTheParserRefusesWhatItWillNotHold(t *testing.T) {
	for _, in := range []string{"$999999999999\r\n", "*99999999\r\n", "*1\r\n*1\r\n*1\r\n*1\r\n*1\r\n*1\r\n:1\r\n", "?\r\n", "$3\r\nabcd\r\n", ":x\r\n"} {
		if _, err := readReply(bufio.NewReader(strings.NewReader(in)), 0); err == nil {
			t.Errorf("%q was read", in)
		}
	}
}

// AGAINST A REAL REDIS, when one is named: the fake is this client's
// reading of the protocol, and the server is the protocol.
func TestIntegrationAgainstARealRedis(t *testing.T) {
	addr := os.Getenv("BREVIS_SQL_IT_REDIS")
	if addr == "" {
		t.Skip("BREVIS_SQL_IT_REDIS not set")
	}
	c, err := Open(addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx := context.Background()
	key := fmt.Sprintf("brevis:it:{brevis}:%d", time.Now().UnixNano())
	value := []byte("bin\r\n\x00ary")
	if err := c.Set(ctx, key, value, time.Minute); err != nil {
		t.Fatal(err)
	}
	vals, err := c.MGet(ctx, key, key+":absent")
	if err != nil || !bytes.Equal(vals[0], value) || vals[1] != nil {
		t.Errorf("MGET = %q, %v", vals, err)
	}
}
