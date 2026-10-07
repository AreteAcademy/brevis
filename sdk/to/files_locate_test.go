package to

import (
	"os"
	"path/filepath"
	"testing"
)

// The directory, never the object: a load names its file with a timestamp, and
// naming the file would make every run a new destination.
func TestFilesLocateNamesTheDirectory(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct{ path, want string }{
		{"s3://acme-landing/vendors", "s3://acme-landing/vendors/"},
		{"s3://acme-landing/vendors/", "s3://acme-landing/vendors/"},
		{"gs://acme-landing/day=1/", "gs://acme-landing/day=1/"},
		{"gs://acme-landing", "gs://acme-landing/"},
		{"/var/data/out", "file:///var/data/out/"},
		{"file:///var/data/out/", "file:///var/data/out/"},
		// Relative to the step's working directory, which is the only thing a
		// relative path can mean -- so it is made absolute here.
		{"out", "file://" + filepath.ToSlash(filepath.Join(wd, "out")) + "/"},
		{"", ""},
	}
	for _, c := range cases {
		if got := (Files{Path: c.path}).Locate(); got != c.want {
			t.Errorf("Files{Path: %q}.Locate() = %q, want %q", c.path, got, c.want)
		}
	}
}
