package to_test

import (
	"context"
	"fmt"
	"io"
	"iter"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AreteAcademy/brevis/sdk"
	"github.com/AreteAcademy/brevis/sdk/from"
	"github.com/AreteAcademy/brevis/sdk/to"
)

func lote(n int) []sdk.Envelope {
	out := make([]sdk.Envelope, n)
	for i := range out {
		out[i] = sdk.Envelope{Payload: map[string]any{"i": i, "nome": fmt.Sprintf("linha %d", i)}}
	}
	return out
}

// TestFilesSaysWhatItWrote is the defect: the driver chooses the file's name --
// it carries a timestamp, so a second load does not overwrite the first -- and
// did not say which one it chose.
//
// Whoever wrote did not know what they wrote, and the log said "strategy=file"
// without saying which file: the information that is missing at three in the
// morning.
func TestFilesSaysWhatItWrote(t *testing.T) {
	dir := t.TempDir()

	res, err := to.Files{Path: dir}.Write(context.Background(), lote(3), sdk.WriteOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Objects) != 1 {
		t.Fatalf("Objects = %v, esperado um caminho", res.Objects)
	}

	if _, err := os.Stat(res.Objects[0]); err != nil {
		t.Errorf("o caminho reportado não existe: %v", err)
	}
	if filepath.Dir(res.Objects[0]) != dir {
		t.Errorf("o caminho %q não está no diretório configurado %q", res.Objects[0], dir)
	}
}

// TestFilesPathGoesBackIntoFromFiles is the proof that matters to whoever splits
// extract and load into two steps: the path the write produces has to be able to
// go back into a read, with nobody reassembling the scheme and the bucket.
func TestFilesPathGoesBackIntoFromFiles(t *testing.T) {
	dir := t.TempDir()

	res, err := to.Files{Path: dir}.Write(context.Background(), lote(4), sdk.WriteOptions{})
	if err != nil {
		t.Fatal(err)
	}

	data, err := sdk.Extract(context.Background(), sdk.Source{
		From: from.Files{Path: res.Objects[0]},
	})
	if err != nil {
		t.Fatalf("Extract do caminho reportado: %v", err)
	}
	n := 0
	for env, err := range data.Records {
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := env.Payload.(map[string]any)["nome"]; !ok {
			t.Errorf("linha sem os campos: %v", env.Payload)
		}
		n++
	}
	if n != 4 {
		t.Errorf("releu %d linhas, escreveu 4", n)
	}
}

// TestFilesDescribeIsStillTheDirectory: they are different things, and a
// Describe that changed on every load would stop identifying the destination in
// the log.
func TestFilesDescribeIsStillTheDirectory(t *testing.T) {
	f := to.Files{Path: "s3://bucket/landing/"}
	if f.Describe() != "s3://bucket/landing/" {
		t.Errorf("Describe = %q", f.Describe())
	}
}

// TestFilesWithFlushEveryReportsAllOfThem: with a batched load there are several
// files, and reporting only the last would make the next step read a
// fragment.
func TestFilesWithFlushEveryReportsAllOfThem(t *testing.T) {
	dir := t.TempDir()

	data, err := sdk.Extract(context.Background(), sdk.Source{From: sourceOfN{10}})
	if err != nil {
		t.Fatal(err)
	}
	res, err := sdk.Load(context.Background(), data, sdk.Target{
		To: to.Files{Path: dir}, FlushEvery: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Objects) != 4 {
		t.Fatalf("Objects = %v, esperado 4 arquivos (3+3+3+1)", res.Objects)
	}
	for _, path := range res.Objects {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("%s não existe: %v", path, err)
		}
	}
}

type sourceOfN struct{ n int }

func (sourceOfN) Describe() string { return "fonte de teste" }
func (f sourceOfN) Read(context.Context, sdk.ReadOptions) (iter.Seq2[sdk.Envelope, error], error) {
	return func(yield func(sdk.Envelope, error) bool) {
		for i := 0; i < f.n; i++ {
			if !yield(sdk.Envelope{Payload: map[string]any{"i": i}}, nil) {
				return
			}
		}
	}, nil
}

// TestFilesWritesIntoTheConfiguredDirectory is the defect the path test
// uncovered, and it is worse than the one that motivated the change.
//
// ParseLocation is written for READING, where the last segment with no slash is
// an object's name. In to.Files the file's name belongs to the driver, so Path
// is always a directory -- and without this `to.Files{Path: "s3://bucket/landing"}`
// wrote to `s3://bucket/parte-...`, discarding the "landing" as if it were a
// file name. Nothing said so; the file turned up one level above, and whoever
// looked for it in the configured place would not find it.
func TestFilesWritesIntoTheConfiguredDirectory(t *testing.T) {
	base := t.TempDir()

	for _, sufixo := range []string{"", "/"} {
		dir := filepath.Join(base, "landing")
		res, err := to.Files{Path: dir + sufixo}.Write(
			context.Background(), lote(1), sdk.WriteOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if got := filepath.Dir(res.Objects[0]); got != dir {
			t.Errorf("Path %q escreveu em %q, esperado %q", dir+sufixo, got, dir)
		}
	}
}

// TestFilesObjectInObjectStorage: with a scheme, the reported path is the full
// URI -- it is what has to go back into a from.Files with no reassembly.
func TestFilesObjectInObjectStorage(t *testing.T) {
	var escritos []string
	f := to.Files{Path: "s3://meu-bucket/landing", Store: storeFalso{&escritos}}

	res, err := f.Write(context.Background(), lote(1), sdk.WriteOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Objects) != 1 {
		t.Fatalf("Objects = %v", res.Objects)
	}
	if !strings.HasPrefix(res.Objects[0], "s3://meu-bucket/landing/parte-") {
		t.Errorf("caminho = %q; esperava o URI completo dentro de landing/", res.Objects[0])
	}
	// And it is the same one handed to the store, with no scheme and no bucket.
	if len(escritos) != 1 || !strings.HasPrefix(escritos[0], "landing/parte-") {
		t.Errorf("a chave entregue ao store foi %v", escritos)
	}
}

type storeFalso struct{ keys *[]string }

func (storeFalso) Scheme() string                                         { return "s3" }
func (storeFalso) List(context.Context, string, string) ([]string, error) { return nil, nil }
func (storeFalso) Open(context.Context, string, string) (io.ReadCloser, error) {
	return nil, nil
}
func (s storeFalso) Create(_ context.Context, _, key string, r io.Reader) error {
	*s.keys = append(*s.keys, key)
	_, _ = io.ReadAll(r)
	return nil
}
