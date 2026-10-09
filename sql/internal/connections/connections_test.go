package connections

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "brevis.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// THE REGISTRY IS THE ECOSYSTEM'S AND NOT THIS SERVICE'S. One declaration
// that `serve` reads today and that the SDK and the gateway read later --
// the alternative is the same DSN written in three files, and the third one
// is the one that is wrong.
func TestADeclaredConnectionIsRead(t *testing.T) {
	t.Setenv("ANALYTICS_RO_DSN", "postgres://reader:pw@db:5432/app?sslmode=disable")
	r, err := Load(write(t, `
connections:
  - name: analytics
    dialect: bigquery
    project: acme-prod
  - name: app
    dialect: postgres
    dsn_from: ANALYTICS_RO_DSN
`))
	if err != nil {
		t.Fatalf("a good file was refused: %v", err)
	}
	if got := r.Names(); strings.Join(got, ",") != "analytics,app" {
		t.Errorf("the registry holds %v", got)
	}
}

// MATCHING IS A PURE FUNCTION, and NO MATCH IS AN ANSWER rather than an
// error: V4 has to tell somebody WHICH connection is missing, and an error
// carries nothing to put on a screen.
func TestMatchingATargetToAConnection(t *testing.T) {
	t.Setenv("APP_DSN", "postgres://reader:pw@db:5432/app?sslmode=disable")
	t.Setenv("WAREHOUSE_DSN", "postgres://reader:pw@other:5432/warehouse")
	r, err := Load(write(t, `
connections:
  - name: analytics
    dialect: bigquery
    project: acme-prod
  - name: app
    dialect: postgres
    dsn_from: APP_DSN
  - name: warehouse
    dialect: postgres
    dsn_from: WAREHOUSE_DSN
`))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		dialect, first, want string
		found                bool
	}{
		// BigQuery matches on the PROJECT, which is what a target carries.
		{"bigquery", "acme-prod", "analytics", true},
		{"bigquery", "someone-else", "", false},
		// Postgres matches on the DATABASE, read out of the DSN rather than
		// declared beside it -- a second declaration is a second thing that
		// can disagree with the connection it describes.
		{"postgres", "app", "app", true},
		{"postgres", "warehouse", "warehouse", true},
		{"postgres", "nothing_here", "", false},
		// A project id is not a database name, whatever it spells.
		{"postgres", "acme-prod", "", false},
		{"bigquery", "app", "", false},
		// A dialect nobody declared.
		{"mysql", "app", "", false},
	} {
		got, found := r.Match(c.dialect, c.first)
		if found != c.found {
			t.Errorf("%s://%s found=%v, wanted %v", c.dialect, c.first, found, c.found)
			continue
		}
		if found && got.Name != c.want {
			t.Errorf("%s://%s matched %q, wanted %q", c.dialect, c.first, got.Name, c.want)
		}
	}
}

// REFUSED AT BOOT, BY NAME. A registry that accepted these would fail on the
// first request instead, which is a readiness probe that already lied.
func TestEachBadDeclarationIsRefusedAtBootAndNamed(t *testing.T) {
	t.Setenv("GOOD_DSN", "postgres://reader:pw@db:5432/app")
	for _, c := range []struct{ name, body, says string }{
		{"a dsn_from holding the DSN itself",
			"connections:\n  - {name: app, dialect: postgres, dsn_from: 'postgres://u:p@h/db'}",
			"NAME of an environment variable"},
		{"a duplicate name",
			"connections:\n  - {name: app, dialect: postgres, dsn_from: GOOD_DSN}\n" +
				"  - {name: app, dialect: bigquery, project: acme-prod}",
			"twice"},
		{"a dialect this binary does not carry",
			"connections:\n  - {name: c, dialect: snowflake, dsn_from: GOOD_DSN}",
			"snowflake"},
		{"no name at all",
			"connections:\n  - {dialect: bigquery, project: acme-prod}",
			"name"},
		{"a variable that is not set",
			"connections:\n  - {name: app, dialect: postgres, dsn_from: NOT_SET_ANYWHERE}",
			"NOT_SET_ANYWHERE"},
		{"a BigQuery connection with no project",
			"connections:\n  - {name: c, dialect: bigquery}",
			"project"},
		{"a Postgres connection with no dsn_from",
			"connections:\n  - {name: c, dialect: postgres}",
			"dsn_from"},
		{"both a project and a dsn_from",
			"connections:\n  - {name: c, dialect: postgres, project: acme-prod, dsn_from: GOOD_DSN}",
			"both"},
		{"a DSN that does not parse",
			"connections:\n  - {name: c, dialect: postgres, dsn_from: BAD_DSN}",
			"c"},
		{"not a file this can read",
			"connections: [[[",
			"read"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("BAD_DSN", "this is not a dsn")
			_, err := Load(write(t, c.body))
			if err == nil {
				t.Fatal("accepted")
			}
			if !strings.Contains(err.Error(), c.says) {
				t.Errorf("the refusal never says %q: %v", c.says, err)
			}
		})
	}
}

// A REFUSAL NEVER REPEATS A DSN. It reaches a log and a terminal, and a
// connection string carries a password -- the rule `--dsn-from` already
// follows one command over.
func TestARefusalNeverRepeatsADSN(t *testing.T) {
	const secret = "postgres://reader:hunter2@db:5432/app"
	t.Setenv("APP_DSN", secret)
	for _, body := range []string{
		"connections:\n  - {name: app, dialect: postgres, dsn_from: '" + secret + "'}",
		"connections:\n  - {name: app, dialect: snowflake, dsn_from: APP_DSN}",
	} {
		_, err := Load(write(t, body))
		if err == nil {
			t.Fatal("accepted")
		}
		if strings.Contains(err.Error(), "hunter2") {
			t.Errorf("the refusal repeated a password: %v", err)
		}
	}
}

// A FILE THAT IS NOT THERE IS NOT A FAILURE. `serve` with no registry is
// BigQuery-only, which is exactly what V1 and V2 shipped -- and a service
// that refused to start without a file nobody had written yet would be a
// service nobody could run.
func TestNoFileIsAnEmptyRegistry(t *testing.T) {
	r, err := Load(filepath.Join(t.TempDir(), "absent.yaml"))
	if err != nil {
		t.Fatalf("a missing registry was an error: %v", err)
	}
	if len(r.Names()) != 0 {
		t.Errorf("it holds %v", r.Names())
	}
	if _, found := r.Match("postgres", "app"); found {
		t.Error("an empty registry matched something")
	}
}
