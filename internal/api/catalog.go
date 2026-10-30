package api

import (
	"context"
	"net/http"
	"time"

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
