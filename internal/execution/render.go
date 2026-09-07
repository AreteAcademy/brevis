package execution

import (
	"fmt"
	"sort"
	"strings"
	"text/template"
)

// Render substitutes the params into a step's command line.
//
// The stdlib's `text/template`, with `missingkey=error`: a param with a typo in
// the YAML fails HERE, naming what was missing, instead of becoming an empty
// string and producing a silently wrong command — `--select ` with no target, or
// `--date` with no date.
//
// So o comando e renderizado. `image:` NAO e templatavel de proposito: quem
// triggers a run would be choosing the image the pod runs, which is choosing
// the code that executes.
func Render(command string, params map[string]string) (string, error) {
	if !strings.Contains(command, "{{") {
		return command, nil
	}
	t, err := template.New("passo").Option("missingkey=error").Parse(command)
	if err != nil {
		return "", fmt.Errorf("the command has an invalid template: %w", err)
	}

	var output strings.Builder
	if err := t.Execute(&output, params); err != nil {
		return "", fmt.Errorf("%w (params disponiveis: %s)", err, keys(params))
	}
	return output.String(), nil
}

func keys(m map[string]string) string {
	if len(m) == 0 {
		return "nenhum"
	}
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return strings.Join(out, ", ")
}
