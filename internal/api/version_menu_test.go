package api_test

import (
	"log/slog"
	"strings"
	"testing"

	"github.com/AreteAcademy/brevis/internal/api"
	"github.com/AreteAcademy/brevis/internal/branding"
	"github.com/AreteAcademy/brevis/internal/updates"
)

type releaseFake struct {
	r     updates.Release
	newer bool
}

func (f releaseFake) Latest() (updates.Release, bool, bool) { return f.r, f.newer, true }

func versionUI(src api.UpdateSource) *api.UI {
	return api.NewUI(nil, nil, nil, nil, nil, catalogFake{}, branding.Default(), slog.New(slog.DiscardHandler)).
		WithEndpoints(endpointsFake{}).WithVersion("0.40.0", src)
}

// THE ACCOUNT MENU SAYS WHICH BREVIS THIS IS, opens the newest release's
// notes, and -- only when that release is newer -- shows an update icon on the
// button and a notice for the script to toast once.
func TestTheAccountMenuShowsTheVersionAndANewerRelease(t *testing.T) {
	notes := `<h3>Added</h3><p>x <script>alert(1)</script></p>`
	ui := versionUI(releaseFake{r: updates.Release{Tag: "v0.41.0", URL: "https://github.com/AreteAcademy/brevis/releases/tag/v0.41.0", Notes: updates.Sanitize(notes)}, newer: true})
	_, body := get(t, ui, "/ingest/loads")
	for _, want := range []string{
		`data-dialogo="version"`, "Brevis v0.40.0", "v0.41.0 available", "data-update-icon", "data-update-notice",
		`data-version="v0.41.0"`, `id="version"`, "<h3>Added</h3>", "You are running v0.40.0",
		`href="https://github.com/AreteAcademy/brevis/releases/tag/v0.41.0"`, ">Download<",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the page lacks %s", want)
		}
	}
	if strings.Contains(body, "<script>alert") {
		t.Error("the notes carried a script")
	}
}

// CURRENT, THERE IS NO NEWS: the version still opens the notes, and nothing
// else is drawn.
func TestACurrentConsoleHasNoUpdateIconOrNotice(t *testing.T) {
	ui := versionUI(releaseFake{r: updates.Release{Tag: "v0.40.0", URL: "u", Notes: "<p>n</p>"}})
	_, body := get(t, ui, "/ingest/loads")
	if !strings.Contains(body, `data-dialogo="version"`) || !strings.Contains(body, "Brevis v0.40.0") {
		t.Error("the version does not open the notes")
	}
	for _, bad := range []string{"data-update-icon", "data-update-notice", "v0.40.0 available"} {
		if strings.Contains(body, bad) {
			t.Errorf("the page has %s with nothing new", bad)
		}
	}
}

// WITHOUT A SOURCE (a development build, or BREVIS_UPDATE_CHECK=off) it is the
// version alone, as text.
func TestWithoutAReleaseSourceTheVersionIsPlainText(t *testing.T) {
	_, body := get(t, versionUI(nil), "/ingest/loads")
	if !strings.Contains(body, "Brevis v0.40.0") || strings.Contains(body, `data-dialogo="version"`) {
		t.Error("the version is not plain text without a source")
	}
}
