package api

import (
	"context"
	"net/http"
	"strings"
	"time"
	"unicode"

	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"

	"github.com/AreteAcademy/brevis/web/layouts"

	"github.com/AreteAcademy/brevis/web/pages"
)

// catalogPage is /data (screen-catalog): every destination, worst first,
// with what the query string keeps. The search box asks for the list alone
// (X-Brevis-Fragment: answer) while somebody types.
func (u *UI) catalogPage(w http.ResponseWriter, r *http.Request) {
	if u.catalog == nil {
		http.NotFound(w, r)
		return
	}
	entries, err := u.catalog.Catalog(r.Context())
	if err != nil {
		u.failure(w, r, err)
		return
	}
	f := pages.ParseDataFilter(r.URL.Query())
	f.Path = "/data"
	v := pages.BuildCatalog(entries, f, time.Now())
	if fragment(r) == "answer" {
		u.render(w, r, pages.CatalogList(v))
		return
	}
	u.render(w, r, pages.Catalog(v))
}

// catalogTarget is /data/target?u= (screen-catalog T3): the catalog with one
// destination's drawer drawn open, so the link works without a script; the
// row's script asks for the drawer alone (X-Brevis-Fragment: target). A
// destination nobody writes is the catalog with a note saying so.
func (u *UI) catalogTarget(w http.ResponseWriter, r *http.Request) {
	target := r.URL.Query().Get("u")
	if len(target) > targetCeiling || strings.ContainsFunc(target, unicode.IsControl) {
		http.Error(w, "name a destination: /data/target?u=<target>", http.StatusBadRequest)
		return
	}
	if u.catalog == nil {
		http.NotFound(w, r)
		return
	}
	entries, err := u.catalog.Catalog(r.Context())
	if err != nil {
		u.failure(w, r, err)
		return
	}
	f := pages.ParseDataFilter(r.URL.Query())
	f.Path = "/data"
	v := pages.BuildCatalog(entries, f, time.Now())

	var d *postgres.TargetDetail
	if target != "" {
		if d, err = u.catalog.CatalogTarget(r.Context(), target); err != nil {
			u.failure(w, r, err)
			return
		}
	}
	if d == nil {
		if fragment(r) == "target" {
			http.NotFound(w, r)
			return
		}
		v.Missing = target
		u.render(w, r, pages.Catalog(v))
		return
	}
	t := pages.BuildTarget(*d, time.Now())
	if err := u.targetTools(r, &t, target); err != nil {
		u.failure(w, r, err)
		return
	}
	v.Drawer = &t
	if fragment(r) == "target" {
		u.render(w, r, pages.TargetDrawer(v))
		return
	}
	u.render(w, r, pages.Catalog(v))
}

// attentionTTL is how long the Catalog badge's count is kept: long enough
// that a page does not read the whole catalog each time, short enough that
// a destination that went stale shows within a minute.
const attentionTTL = 30 * time.Second

// withAttention counts the destinations that need a person, for the
// Catalog section's badge. A number for decoration cannot take a page down:
// when it cannot be read, the error is logged and the page is drawn without
// it.
func (u *UI) withAttention(ctx context.Context, r *http.Request) context.Context {
	if u.catalog == nil {
		return ctx
	}
	u.attentionMu.Lock()
	defer u.attentionMu.Unlock()
	if time.Since(u.attentionAt) > attentionTTL {
		entries, err := u.catalog.Catalog(ctx)
		if err != nil {
			u.log.Error("counting the destinations that need attention", "path", r.URL.Path, "error", err)
			return ctx
		}
		u.attentionN = pages.BuildCatalog(entries, pages.DataFilter{}, time.Now()).Count("attention")
		u.attentionAt = time.Now()
	}
	return layouts.WithAttention(ctx, u.attentionN)
}
