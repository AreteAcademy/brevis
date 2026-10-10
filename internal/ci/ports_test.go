package ci

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// claimed is a port another tool on the same laptop takes, and why.
//
// THIS LIST IS PAID FOR, NOT PREDICTED. Each entry is here because the
// collision actually happened to somebody on this project, and the reason is
// written down so the next person does not have to rediscover it from a hang.
var claimed = map[int]string{
	8085: "`gcloud auth login` redirects the browser to http://localhost:8085/ " +
		"and waits there for the code. A container on that port receives the " +
		"callback instead, and the login hangs and then fails with " +
		"`(missing_code) Missing code parameter in response`.",
}

// A PUBLISHED PORT IS A CLAIM ON THE DEVELOPER'S MACHINE, not an internal
// detail. Inside the compose network a service can listen wherever it likes;
// the left-hand side of `"HOST:CONTAINER"` is the one that collides with
// whatever else somebody runs.
//
// The Pub/Sub emulator published 8085 in both compose files, and the cost
// came due the day somebody ran `gcloud auth login` with the stack up: the
// browser handed Google's callback to the emulator, and gcloud waited for a
// code that had already been delivered somewhere else.
func TestNoPublishedPortIsOneAnotherToolClaims(t *testing.T) {
	for _, file := range []string{"docker-compose.yml", "docker-compose.drivers.yml"} {
		body, err := os.ReadFile(filepath.Join("../..", file))
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range publishedPorts(string(body)) {
			if why, taken := claimed[p.port]; taken {
				t.Errorf("%s:%d publishes host port %d, and %s",
					file, p.line, p.port, why)
			}
		}
	}
}

type published struct {
	port int
	line int
}

// publishedPorts reads the HOST side of every `"HOST:CONTAINER"` mapping,
// including the default inside `${VAR:-HOST}`.
//
// A SECOND ASSERTION WAS WRITTEN HERE AND THROWN AWAY: that every published
// port must come from a variable. It fired on eight ports in the drivers
// compose -- 55432, 53306, 59092 and the rest -- which are already in high
// ranges chosen to avoid exactly this, and it was a rule nobody had paid for.
// The list above is the one that cost something.
var (
	withVar = regexp.MustCompile(`\$\{[A-Z_]+:-(\d+)\}:\d+`)
	literal = regexp.MustCompile(`"(\d+):\d+"`)
)

func publishedPorts(body string) []published {
	var out []published
	for i, l := range strings.Split(body, "\n") {
		if !strings.Contains(l, "ports:") && !strings.HasPrefix(strings.TrimSpace(l), "-") {
			continue
		}
		for _, m := range withVar.FindAllStringSubmatch(l, -1) {
			n, _ := strconv.Atoi(m[1])
			out = append(out, published{port: n, line: i + 1})
		}
		for _, m := range literal.FindAllStringSubmatch(l, -1) {
			n, _ := strconv.Atoi(m[1])
			out = append(out, published{port: n, line: i + 1})
		}
	}
	return out
}
