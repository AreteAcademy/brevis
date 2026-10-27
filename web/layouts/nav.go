package layouts

import "context"

// section is one place in the sidebar. Tip is what the place is FOR, shown
// on hover and focus: the name is the engine's word, the tip the reader's.
// GoKey is the letter that goes there after `g` ("" for none); it is written
// on the link, so the script has no map of its own to fall out of step.
type section struct {
	Href, Name, Key, Icon, Tip, GoKey string
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
		{"/data/endpoints", "Endpoints", "endpoints", "plug", "Metrics published as an API, cached until the data changes", ""},
	}
	if HasSQL(ctx) {
		data = append(data, section{"/sql", "SQL", "sql", "terminal", "Query Postgres and BigQuery, read-only", "s"})
	}
	all := []group{
		{"Monitor", []section{
			{"/", "Overview", "overview", "layout-grid", "The health of everything, what failed and what fires next", "o"},
			{"/runs", "Runs", "runs", "history", "Every time a workflow ran: timing, parameters and logs", "e"},
		}},
		{"Orchestrate", []section{
			{"/workflows", "Workflows", "workflows", "workflow", "Schedules and the steps each one runs", "w"},
		}},
		{"Ingest", []section{
			{"/ingest/streams", "Streams", "streams", "cloud", "Gateway streams: HTTP events written as they arrive", "t"},
			{"/ingest/loads", "Loads", "loads", "download", "Steps that extract from HTTP, files and databases and write a destination", ""},
		}},
		{"Data", data},
		{"Configure", nil},
	}
	out := all[:0]
	for _, g := range all {
		if len(g.Sections) > 0 {
			out = append(out, g)
		}
	}
	return out
}

// projects is the page the switcher opens. Not a section: see groups.
var projects = section{"/projects", "Projects", "projects", "folder", "Every project and the workflows in it", ""}

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
	if active == projects.Key {
		c := crumb{Section: projects}
		if title != projects.Name {
			c.Title = title
		}
		return c, true
	}
	for _, g := range groups(ctx) {
		for _, s := range g.Sections {
			if s.Key == active {
				c := crumb{Group: g.Name, Section: s}
				if title != s.Name {
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
