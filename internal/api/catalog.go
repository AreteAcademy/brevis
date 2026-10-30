package api

import (
	"net/http"
	"time"

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
