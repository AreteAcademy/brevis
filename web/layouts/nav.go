package layouts

import "context"

// section is one place in the sidebar.
type section struct {
	Href, Name, Key, Icon string
}

// group is a labelled run of sections.
type group struct {
	Name     string
	Sections []section
}

// groups is the sidebar, in the data's path: how it is built and what
// happened, what comes in, what reads it, and the workspace around it.
//
// ONE LIST FOR TWO READERS. The sidebar draws it and the top bar's breadcrumb
// looks the open section up in it; two lists would disagree the first time a
// section moved between groups.
//
// SQL IS CONDITIONAL: a section that answers "no SQL service is configured"
// is a section that leads nowhere, and the sidebar is the one place a reader
// trusts to say what exists.
func groups(ctx context.Context) []group {
	consume := []section{
		{"/data/endpoints", "Endpoints", "endpoints", "plug"},
	}
	if HasSQL(ctx) {
		consume = append(consume, section{"/sql", "SQL", "sql", "terminal"})
	}
	return []group{
		{"Pipelines", []section{
			{"/", "Overview", "overview", "layout-grid"},
			{"/workflows", "Workflows", "workflows", "workflow"},
			{"/runs", "Runs", "runs", "history"},
		}},
		// What comes in: a gateway's streams, and what the workflows load.
		{"Ingest", []section{
			{"/ingest/streams", "Streams", "streams", "cloud"},
			{"/ingest/loads", "Loads", "loads", "download"},
		}},
		// What reads it: the published endpoints and the workbench.
		{"Consume", consume},
		{"Workspace", []section{
			{"/projects", "Projects", "projects", "folder"},
		}},
	}
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
