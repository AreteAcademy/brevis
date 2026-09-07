package consumer_test

import (
	"os/exec"
	"strings"
	"testing"
)

// Dependency pruning is why the drivers live in subpackages, and it is
// measurable -- so it is asserted, not promised.
//
// Before phase 0 the root imported sdk/load, which imports BigQuery: 458 packages
// and 21 MB of binary for anyone who wanted to do Postgres -> Postgres. Go prunes
// by imported package, never by used field, so the only way not to pay for a
// driver is not to import its package.
func TestNotUsingBigQueryCompilesNoBigQuery(t *testing.T) {
	casos := []struct {
		name      string
		pacotes   []string
		forbidden bool
	}{
		{"the root alone", []string{"github.com/AreteAcademy/brevis/sdk"}, true},
		{"raiz + from", []string{
			"github.com/AreteAcademy/brevis/sdk",
			"github.com/AreteAcademy/brevis/sdk/from",
		}, true},
		// A whole files pipeline -- from and to -- still does not pull BigQuery
		// in. This case was missing in v0.20.0, and without it the defect got
		// through: to.BigQuery and to.Files lived in the same package, so writing
		// a file compiled Google.
		{"raiz + from + to (arquivos)", []string{
			"github.com/AreteAcademy/brevis/sdk",
			"github.com/AreteAcademy/brevis/sdk/from",
			"github.com/AreteAcademy/brevis/sdk/to",
		}, true},

		// E o controle: quem pede o BigQuery recebe o BigQuery. Sem isto, o
		// test would pass with an SDK that loads nothing.
		{"raiz + to/bigquery", []string{
			"github.com/AreteAcademy/brevis/sdk",
			"github.com/AreteAcademy/brevis/sdk/to/bigquery",
		}, false},
	}

	for _, c := range casos {
		t.Run(c.name, func(t *testing.T) {
			carrega := strings.Contains(deps(t, c.pacotes...), "cloud.google.com/go/bigquery")

			if c.forbidden && carrega {
				t.Error("BigQuery got into the graph of somebody who did not import it")
			}
			if !c.forbidden && !carrega {
				t.Error("importing to.BigQuery has to bring BigQuery in; " +
					"without this the test above proves nothing")
			}
		})
	}
}

// The same reasoning for the object-storage backends. from.Files serves disk, S3
// and GCS, but the backend is a value -- so reading a local CSV compiles neither
// AWS nor Google. With the three in a single package, it would.
func TestReadingALocalFileCompilesNoCloud(t *testing.T) {
	casos := []struct {
		name     string
		pacotes  []string
		procura  string
		expected bool
	}{
		{"from on its own does not bring AWS in", []string{
			"github.com/AreteAcademy/brevis/sdk/from"}, "aws-sdk-go", false},
		{"from on its own does not bring Google in", []string{
			"github.com/AreteAcademy/brevis/sdk/from"}, "cloud.google.com", false},

		// Os controles: quem pede o backend recebe o backend.
		{"store/s3 traz a AWS", []string{
			"github.com/AreteAcademy/brevis/sdk/store/s3"}, "aws-sdk-go", true},
		{"store/gcs traz o Google", []string{
			"github.com/AreteAcademy/brevis/sdk/store/gcs"}, "cloud.google.com", true},
		{"store/s3 does not bring Google in", []string{
			"github.com/AreteAcademy/brevis/sdk/store/s3"}, "cloud.google.com", false},
	}

	for _, c := range casos {
		t.Run(c.name, func(t *testing.T) {
			carrega := strings.Contains(deps(t, c.pacotes...), c.procura)
			if carrega != c.expected {
				t.Errorf("carries %q = %v, expected %v", c.procura, carrega, c.expected)
			}
		})
	}
}

// deps runs in the sdk module, which is what declares those dependencies. Running
// it from here would only resolve what the examples module already imports.
func deps(t *testing.T, pacotes ...string) string {
	t.Helper()
	cmd := exec.Command("go", append([]string{"list", "-deps"}, pacotes...)...)
	cmd.Dir = "../../sdk"
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	return string(out)
}
