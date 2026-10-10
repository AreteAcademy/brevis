package api

import (
	"net/http"
	"strings"

	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
	"github.com/AreteAcademy/brevis/internal/infrastructure/sqlserve"
	"github.com/AreteAcademy/brevis/web/pages"
)

// The workbench's two fragment endpoints.
//
// THEY RETURN HTML, NOT JSON, and that is a decision rather than a shortcut.
// The island needs a connection's schemas and a relation's columns drawn into
// the tree; asking for JSON would mean the tree's classes, its ARIA and its
// "this warehouse does not say" exist twice -- once in templ, where the Go
// suite tests them, and once in a JavaScript string, where nothing would.
//
// So the server draws, as it does everywhere else here, and the script's
// whole job is to put the answer where the reload used to put it.
//
// GET AND NOT POST, because neither carries a statement. A target in a URL is
// already allowed -- `/data/target?u=` does it -- and a statement is the
// thing that must never be.

// sqlObjects draws one connection's listing.
func (u *UI) sqlObjects(w http.ResponseWriter, r *http.Request) {
	_, target, ok := u.queryableTarget(w, r, r.URL.Query().Get("target"))
	if !ok {
		return
	}
	b := pages.Branch{Target: target}
	// A WAREHOUSE THAT CANNOT LIST IS NOT AN ERROR. `Relations` is an
	// optional capability and the service answers 501 where a dialect lacks
	// one; the page treats that as an empty tree and so does this, so the
	// island draws nothing rather than an error where a tree would be.
	if objs, err := u.preview.Objects(r.Context(), target); err == nil {
		b.Tree, b.Cut = pages.BuildTree(objs.Relations), objs.Truncated
	}
	u.render(w, r, pages.Objects(b))
}

// sqlColumns draws one relation's columns.
func (u *UI) sqlColumns(w http.ResponseWriter, r *http.Request) {
	_, target, ok := u.queryableTarget(w, r, r.URL.Query().Get("target"))
	if !ok {
		return
	}
	schema, name := r.URL.Query().Get("schema"), r.URL.Query().Get("name")
	if schema == "" || name == "" {
		http.NotFound(w, r)
		return
	}
	// AND AN UNDESCRIBABLE RELATION IS NOT AN ERROR EITHER: `Describer` is
	// optional too, and the empty list draws the sentence that says so.
	var cols []sqlserve.Column
	if got, err := u.preview.Columns(r.Context(), target, schema, name); err == nil {
		cols = got
	}
	u.render(w, r, pages.Columns(cols))
}

// queryableTarget is the guard both fragments share with the page.
//
// A SECOND DOOR INTO THE SAME SERVICE. A rule enforced at one door is not a
// rule, and this repository has already paid for exactly that: `/sql`'s GET
// path checked the catalog and its POST path did not.
// THE TARGET IS AN ARGUMENT, because the third door carries it in a BODY:
// a statement must not reach a URL, so the endpoint that prices one is a
// POST and its target travels beside the SQL.
func (u *UI) queryableTarget(w http.ResponseWriter, r *http.Request, target string) (pages.SQLView, string, bool) {
	if !u.preview.Configured() {
		http.NotFound(w, r)
		return pages.SQLView{}, "", false
	}
	var entries []postgres.CatalogEntry
	if u.catalog != nil {
		var err error
		if entries, err = u.catalog.Catalog(r.Context()); err != nil {
			u.failure(w, r, err)
			return pages.SQLView{}, "", false
		}
	}
	v := pages.BuildSQL(entries)
	target = strings.TrimSpace(target)
	if target == "" || !v.Knows(target) {
		// THE SAME ANSWER `/data/target` GIVES, and it does not say which of
		// the two it was: "no such destination" and "not one you may ask
		// about" are a distinction worth nothing to somebody who should not
		// be asking.
		http.NotFound(w, r)
		return pages.SQLView{}, "", false
	}
	return v, target, true
}

// sqlEstimate prices what is in the box, before anybody presses Run.
//
// A POST, AND THE ONLY ONE OF THE THREE. The other two fragments take a
// target and a relation, which a URL may carry; this one takes SQL, and a
// statement in a URL is a statement in a proxy log, in a browser's history
// and in a Referer header.
//
// SILENCE IS AN ANSWER. A service that is busy, rate-limited or unreachable
// says nothing about the statement in the box, and a line that went red
// under somebody's half-written SQL because of a token would be a line they
// learn to ignore. 204 is what the island draws nothing for.
func (u *UI) sqlEstimate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.NotFound(w, r)
		return
	}
	_, target, ok := u.queryableTarget(w, r, r.PostFormValue("target"))
	if !ok {
		return
	}
	statement := strings.TrimSpace(r.PostFormValue("q"))
	if statement == "" {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	price, err := u.preview.Estimate(r.Context(), target, statement)
	if err != nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	u.render(w, r, pages.Cost(price))
}
