package execution_test

import (
	"strings"
	"testing"

	"github.com/AreteAcademy/brevis/internal/execution"
)

// The commands the converter generates have to RENDER with the real defaults. A
// template that only fails at run time costs somebody a night.
func TestConvertedCommandsRender(t *testing.T) {
	casos := []struct {
		name     string
		command  string
		params   map[string]string
		contains string
		lacks    string
	}{
		{
			name:     "lawsuit: dry_run=false NAO passa a flag",
			command:  `main.py {{ if .limit }} --limit {{ .limit }}{{ end }} {{ if eq .dry_run "true" }} --dry-run{{ end }} --rpm {{ .rpm }}`,
			params:   map[string]string{"limit": "1000", "dry_run": "false", "rpm": "60"},
			contains: "--limit 1000",
			lacks:    "--dry-run",
		},
		{
			name:     "lawsuit: dry_run=true passa a flag",
			command:  `main.py {{ if eq .dry_run "true" }} --dry-run{{ end }}`,
			params:   map[string]string{"dry_run": "true"},
			contains: "--dry-run",
		},
		{
			name:     "agents: an or over two falses",
			command:  `--vars '{"load_full":"{{ if or (eq .full_refresh "true") (eq .load_full "true") }}true{{ else }}false{{ end }}"}'`,
			params:   map[string]string{"full_refresh": "false", "load_full": "false"},
			contains: `"load_full":"false"`,
		},
		{
			name:     "agents: an or with one true",
			command:  `--vars '{"load_full":"{{ if or (eq .full_refresh "true") (eq .load_full "true") }}true{{ else }}false{{ end }}"}'`,
			params:   map[string]string{"full_refresh": "false", "load_full": "true"},
			contains: `"load_full":"true"`,
		},
	}

	for _, c := range casos {
		t.Run(c.name, func(t *testing.T) {
			got, err := execution.Render(c.command, c.params)
			if err != nil {
				t.Fatalf("it did not render: %v", err)
			}
			if !strings.Contains(got, c.contains) {
				t.Errorf("%q is missing from: %s", c.contains, got)
			}
			if c.lacks != "" && strings.Contains(got, c.lacks) {
				t.Errorf("it should not contain %q: %s", c.lacks, got)
			}
		})
	}
}
