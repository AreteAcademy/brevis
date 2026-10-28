// Package i18n is the console's languages: English, Brazilian Portuguese and
// Spanish.
//
// A message is looked up by a semantic key ("nav.overview") in the catalog of
// the request's language, carried in the context. The catalogs are JSON files
// in locales/, compiled into the binary; every one has the same keys, which a
// test holds.
//
//	i18n.T(ctx, "nav.overview")
//	i18n.N(ctx, "table.showing", shown, "total", total)
//
// A message is TEXT. The template escapes it like any other string, and a
// catalog never carries HTML.
//
// MAKING A SCREEN TRANSLATABLE is three steps, and the tests hold each one:
//
//  1. Replace a sentence in the template with i18n.T(ctx, "screen.what"), or
//     i18n.N for a count; a date, a number or "3 min ago" goes through Date,
//     Number, Decimal, Since or Elapsed instead of fmt or time.Format.
//  2. Add the key to all three files in locales/, with the same placeholders.
//     English is the default and the fallback; a key it lacks shows as [key].
//  3. If ui.js writes the sentence, add the key to ScriptKeys too, and read it
//     there with t("screen.what").
//
// Product and engine terms are never translated (BigQuery, dbt, gateway,
// slot, dead letter, Workflows...); keys_test.go holds that, and that no key
// is missing or unused. es.json still needs a native speaker's review.
//
// NO LIBRARY. The three languages share one plural rule (one for 1, other for
// everything else), placeholders are named, and that is all this needs -- a
// dependency would add packages to an engine whose size a gate watches.
package i18n

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"strings"
)

// Langs are the languages the console speaks, English first: it is the
// default and the fallback.
var Langs = []string{"en", "pt-BR", "es"}

//go:embed locales/*.json
var locales embed.FS

// message is one key's text, or its two plural forms.
type message struct {
	text       string
	one, other string
}

func (m message) plural() bool { return m.text == "" }

func (m message) forms() []string {
	if m.plural() {
		return []string{m.one, m.other}
	}
	return []string{m.text}
}

type catalog map[string]message

// catalogs are read once, at start: a catalog that does not parse stops the
// process at boot rather than leaving a page half-translated.
var catalogs = func() map[string]catalog {
	out := map[string]catalog{}
	for _, lang := range Langs {
		raw, err := locales.ReadFile("locales/" + lang + ".json")
		if err != nil {
			panic(fmt.Sprintf("i18n: %s: %v", lang, err))
		}
		c, err := parse(raw)
		if err != nil {
			panic(fmt.Sprintf("i18n: %s: %v", lang, err))
		}
		out[lang] = c
	}
	return out
}()

// parse reads one catalog. A key starting with `_` is a note to whoever
// edits the file (`_review`), not a message.
func parse(raw []byte) (catalog, error) {
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	c := catalog{}
	for key, v := range doc {
		if strings.HasPrefix(key, "_") {
			continue
		}
		var text string
		if err := json.Unmarshal(v, &text); err == nil {
			c[key] = message{text: text}
			continue
		}
		var forms struct{ One, Other string }
		if err := json.Unmarshal(v, &forms); err != nil || forms.One == "" || forms.Other == "" {
			return nil, fmt.Errorf("%s is neither a string nor {\"one\", \"other\"}", key)
		}
		c[key] = message{one: forms.One, other: forms.Other}
	}
	return c, nil
}

type langKey struct{}

// WithLang carries the request's language. One the console does not speak is
// English.
func WithLang(ctx context.Context, lang string) context.Context {
	return context.WithValue(ctx, langKey{}, supported(lang))
}

// Lang is the context's language; absent is English.
func Lang(ctx context.Context) string {
	if l, ok := ctx.Value(langKey{}).(string); ok {
		return l
	}
	return "en"
}

func supported(lang string) string {
	for _, l := range Langs {
		if l == lang {
			return l
		}
	}
	return "en"
}

// T is key's message in the context's language, with each named placeholder
// filled: T(ctx, "hello", "name", "Ana") for "Hello, {name}".
func T(ctx context.Context, key string, args ...any) string {
	return translate(catalogs, ctx, key, args...)
}

// N is T for a message that depends on a count, which also fills {count}.
func N(ctx context.Context, key string, count int, args ...any) string {
	return plural(catalogs, ctx, key, count, args...)
}

func lookup(cats map[string]catalog, ctx context.Context, key string) (message, bool) {
	if m, ok := cats[Lang(ctx)][key]; ok {
		return m, true
	}
	m, ok := cats["en"][key]
	return m, ok
}

func translate(cats map[string]catalog, ctx context.Context, key string, args ...any) string {
	m, ok := lookup(cats, ctx, key)
	if !ok {
		return "[" + key + "]"
	}
	text := m.text
	if m.plural() {
		text = m.other
	}
	return fill(text, args)
}

func plural(cats map[string]catalog, ctx context.Context, key string, count int, args ...any) string {
	m, ok := lookup(cats, ctx, key)
	if !ok {
		return "[" + key + "]"
	}
	text := m.text
	if m.plural() {
		text = m.other
		if count == 1 {
			text = m.one
		}
	}
	return fill(text, append([]any{"count", count}, args...))
}

// fill replaces {name} with its value; pairs past an odd end are ignored.
func fill(text string, args []any) string {
	for i := 0; i+1 < len(args); i += 2 {
		text = strings.ReplaceAll(text, "{"+fmt.Sprint(args[i])+"}", fmt.Sprint(args[i+1]))
	}
	return text
}

// ScriptKeys are the messages ui.js writes itself. The page hands them over
// in its own language (Script), so the file holds no sentence of its own; a
// key the script needs is added here and to the three catalogs.
var ScriptKeys = []string{
	"theme.to_light", "theme.to_dark",
	"copy.done",
	"palette.empty", "palette.loading", "palette.failed", "palette.open_run",
	"palette.group.runs",
}

// Script is ScriptKeys in ctx's language, for the page's i18n block.
func Script(ctx context.Context) map[string]string {
	out := make(map[string]string, len(ScriptKeys))
	for _, k := range ScriptKeys {
		out[k] = T(ctx, k)
	}
	return out
}
