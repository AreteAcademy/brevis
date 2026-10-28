package i18n

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// sources are the files that can ask for a message: the console's Go and
// templates, and its script. Generated and test files are not.
func sources(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, root := range []string{"../../web", "../../internal/api"} {
		err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			if strings.HasSuffix(p, "_test.go") || strings.HasSuffix(p, "_templ.go") || strings.Contains(p, "/vendor/") {
				return nil
			}
			if ext := filepath.Ext(p); ext == ".go" || ext == ".templ" || (ext == ".js" && filepath.Base(p) == "ui.js") {
				raw, err := os.ReadFile(p)
				if err != nil {
					return err
				}
				// Comments are prose about the code -- an example call in a
				// package doc is not a call.
				out[p] = regexp.MustCompile(`(?m)^\s*//.*$`).ReplaceAllString(string(raw), "")
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return out
}

// families are the keys built from a name rather than written whole: a
// section's name and tip, a state's word, a monogram's title, a palette
// group's label. A key in one counts as used when its name appears as a
// quoted literal somewhere in the code.
var families = []string{"nav.group.", "nav.", "state.", "logo.", "palette.group."}

// EVERY MESSAGE ASKED FOR EXISTS, AND EVERY MESSAGE IN THE CATALOG IS ASKED
// FOR. A key with no message prints as [key] on somebody's screen; a message
// nobody asks for is a translation somebody will keep paying for. And the
// script may only ask for what the page hands it.
func TestEveryKeyUsedExistsAndEveryKeyIsUsed(t *testing.T) {
	src := sources(t)
	all := strings.Join(mapValues(src), "\n")
	literal := regexp.MustCompile(`i18n\.[TN]\(ctx,\s*"([a-z_.]+)"`)
	script := regexp.MustCompile(`\bt\("([a-z_.]+)"`)
	en := catalogs["en"]

	for path, code := range src {
		for _, m := range literal.FindAllStringSubmatch(code, -1) {
			// A literal ending in a dot is a family's prefix, completed at
			// run time ("nav." + key): the families below account for it.
			if strings.HasSuffix(m[1], ".") {
				continue
			}
			if _, ok := en[m[1]]; !ok {
				t.Errorf("%s asks for %s, which the catalog lacks", path, m[1])
			}
		}
		if strings.HasSuffix(path, "ui.js") {
			for _, m := range script.FindAllStringSubmatch(code, -1) {
				if !contains(ScriptKeys, m[1]) {
					t.Errorf("ui.js asks for %s, which is not in ScriptKeys, so the page never hands it over", m[1])
				}
			}
		}
	}
	for _, k := range ScriptKeys {
		if _, ok := en[k]; !ok {
			t.Errorf("ScriptKeys has %s, which the catalog lacks", k)
		}
	}

	for key := range en {
		if strings.Contains(all, `"`+key+`"`) || contains(ScriptKeys, key) {
			continue
		}
		if used := byFamily(key, all); !used {
			t.Errorf("nothing asks for %s", key)
		}
	}
}

func byFamily(key, code string) bool {
	for _, f := range families {
		name, ok := strings.CutPrefix(key, f)
		if !ok {
			continue
		}
		name = strings.TrimSuffix(name, ".tip")
		if regexp.MustCompile(`(?i)"` + regexp.QuoteMeta(name) + `"`).MatchString(code) {
			return true
		}
	}
	return false
}

// THE WORDS THAT ARE NEVER TRANSLATED STAY AS THEY ARE in every catalog:
// product and service names, and the engine's own terms the handoff's
// glossary fixes. "Data" once became "Date" in a prototype.
func TestTheGlossarysFixedTermsAreNotTranslated(t *testing.T) {
	fixed := []string{"Brevis", "brevis-sql", "BigQuery", "Pub/Sub", "Postgres", "dbt", "gateway", "SDK",
		"dead letter", "slot", "TTL", "generation", "HTTP", "API", "SQL", "Workflows", "Endpoints"}
	for key, m := range catalogs["en"] {
		for i, form := range m.forms() {
			for _, term := range fixed {
				if !regexp.MustCompile(`(?i)\b` + regexp.QuoteMeta(term) + `\b`).MatchString(form) {
					continue
				}
				for _, lang := range Langs[1:] {
					got := catalogs[lang][key].forms()[i]
					if !strings.Contains(strings.ToLower(got), strings.ToLower(term)) {
						t.Errorf("%s: %s says %q in English and %q in %s, without it", key, term, form, got, lang)
					}
				}
			}
		}
	}
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func mapValues(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for _, v := range m {
		out = append(out, v)
	}
	return out
}
