package layouts

import (
	"context"

	"github.com/AreteAcademy/brevis/web/i18n"
)

// section is one place in the sidebar. Tip is what the place is FOR, shown
// on hover and focus: the name is the engine's word, the tip the reader's.
// GoKey is the letter that goes there after `g` ("" for none); it is written
// on the link, so the script has no map of its own to fall out of step.
//
// Name and Tip are in the reader's language; en is the English name, which
// is what a page still passes as its title until its screen is translated.
type section struct {
	Href, Name, Key, Icon, Tip, GoKey string
	en                                string
}

// place is a section in ctx's language: its name and hint come from the
// catalog under nav.<key> and nav.<key>.tip.
func place(ctx context.Context, href, key, icon, goKey string) section {
	return section{
		Href: href, Key: key, Icon: icon, GoKey: goKey,
		Name: i18n.T(ctx, "nav."+key),
		Tip:  i18n.T(ctx, "nav."+key+".tip"),
		en:   i18n.T(context.Background(), "nav."+key),
	}
}

// group is a labelled run of sections.
type group struct {
	Name     string
	Sections []section
}

// groups is the sidebar, by what somebody came to do: watch what happened,
// shape what runs, see what comes in, reach the data. (It was grouped by the
// data's path -- Pipelines, Ingest, Consume, Workspace -- which asked a
// reader to know the engine's model before finding a page.)
//
// ONE LIST FOR TWO READERS. The sidebar draws it and the top bar's breadcrumb
// looks the open section up in it; two lists would disagree the first time a
// section moved between groups.
//
// SQL IS CONDITIONAL: a section that answers "no SQL service is configured"
// is a section that leads nowhere, and the sidebar is the one place a reader
// trusts to say what exists. For the same reason a group with no section is
// dropped: Configure has none until Connections exists, and Models and the
// catalog join their groups when their screens do.
//
// PROJECTS IS NOT HERE. It is the scope a reader works in, drawn as a
// switcher above the sections; the page it opens is still /projects.
func groups(ctx context.Context) []group {
	data := []section{
		place(ctx, "/data", "catalog", "database", "d"),
		place(ctx, "/data/endpoints", "endpoints", "plug", ""),
	}
	if HasSQL(ctx) {
		data = append(data, place(ctx, "/sql", "sql", "terminal", "s"))
	}
	all := []group{
		{i18n.T(ctx, "nav.group.monitor"), []section{
			place(ctx, "/", "overview", "layout-grid", "o"),
			place(ctx, "/runs", "runs", "history", "e"),
		}},
		{i18n.T(ctx, "nav.group.orchestrate"), []section{
			place(ctx, "/workflows", "workflows", "workflow", "w"),
		}},
		{i18n.T(ctx, "nav.group.ingest"), []section{
			place(ctx, "/ingest/streams", "streams", "cloud", "t"),
			place(ctx, "/ingest/loads", "loads", "download", ""),
		}},
		{i18n.T(ctx, "nav.group.data"), data},
		{i18n.T(ctx, "nav.group.configure"), nil},
	}
	out := all[:0]
	for _, g := range all {
		if len(g.Sections) > 0 {
			out = append(out, g)
		}
	}
	return out
}

// projectsPlace is the page the switcher opens. Not a section: see groups.
func projectsPlace(ctx context.Context) section {
	return place(ctx, "/projects", "projects", "folder", "")
}

// goKeys are the sections that have a letter, in the sidebar's order: what the
// shortcuts modal lists. The letters are the handoff's (o, w, e for runs --
// "execuções" --, s, t for streams -- "tempo real"), kept so the design and
// the console teach the same keys; the catalog's d, Models' m and
// Connections' c arrive with their screens.
func goKeys(ctx context.Context) []section {
	var out []section
	for _, g := range groups(ctx) {
		for _, s := range g.Sections {
			if s.GoKey != "" {
				out = append(out, s)
			}
		}
	}
	return out
}

// crumb is where a page sits: its section's group, the section, and the
// page's own title when it is not the section itself (a workflow's name under
// Workflows). Empty when the page names no section, which only the login
// screen does.
type crumb struct {
	Group   string
	Section section
	Title   string
}

func where(ctx context.Context, active, title string) (crumb, bool) {
	// The projects page is opened from the switcher, not from a section, so
	// it has no group to sit under: its breadcrumb is its own name.
	if projects := projectsPlace(ctx); active == projects.Key {
		c := crumb{Section: projects}
		if title != projects.Name && title != projects.en {
			c.Title = title
		}
		return c, true
	}
	for _, g := range groups(ctx) {
		for _, s := range g.Sections {
			if s.Key == active {
				c := crumb{Group: g.Name, Section: s}
				if title != s.Name && title != s.en {
					c.Title = title
				}
				return c, true
			}
		}
	}
	return crumb{}, false
}

// Section is one place in the sidebar, with the group it sits in.
type Section struct {
	Group, Href, Name, Icon string
}

// Sections is the sidebar's list, flat: what the command palette offers
// under Sections. Read from groups, the one list the sidebar is drawn from,
// so the two cannot disagree -- SQL appears in both or in neither.
func Sections(ctx context.Context) []Section {
	var out []Section
	for _, g := range groups(ctx) {
		for _, s := range g.Sections {
			out = append(out, Section{Group: g.Name, Href: s.Href, Name: s.Name, Icon: s.Icon})
		}
	}
	return out
}

// crumbTitle is what the breadcrumb ends on: the page's Crumb when it names
// something more precise than its title, the title otherwise.
func crumbTitle(p Page) string {
	if p.Crumb != "" {
		return p.Crumb
	}
	return p.Title
}
