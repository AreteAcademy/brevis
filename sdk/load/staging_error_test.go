package load

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"cloud.google.com/go/storage"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"

	"github.com/AreteAcademy/brevis/sdk/internal/core"
)

// TestStagingErrorNamesTheBucketAndTheWayOut: the error the consumer saw was
// "close gcs writer: googleapi: Error 404: The specified bucket does not
// exist". It said neither which bucket, nor that the default changed in
// v0.25.0, nor
// as duas saidas. Este teste fixa as quatro coisas.
func TestStagingErrorNamesTheBucketAndTheWayOut(t *testing.T) {
	l := &Loader{cfg: &core.LoadConfig{
		StagingBucket:   "projeto-brevis-staging",
		StagingPrefix:   "brevis/",
		ThresholdForGCS: 5000,
	}}

	for _, causa := range []struct {
		name string
		err  error
	}{
		{"sentinela do storage", storage.ErrBucketNotExist},
		{"404 da api json", &googleapi.Error{Code: 404, Message: "The specified bucket does not exist"}},
	} {
		t.Run(causa.name, func(t *testing.T) {
			msg := l.stagingError(context.Background(), causa.err, 12000).Error()

			for _, exigido := range []string{
				"projeto-brevis-staging", // qual bucket
				"12000",                  // why it staged
				"InlineLimit",            // saida 1
				"create the bucket",      // saida 2
				"v0.25.0",                // why the name changed
				"StagingBucket",          // how to choose another
			} {
				if !strings.Contains(msg, exigido) {
					t.Errorf("mensagem nao diz %q:\n%s", exigido, msg)
				}
			}
		})
	}
}

// TestStagingErrorInventsNoDiagnosis: a failure that is not a missing bucket
// -- network, permissions -- must not become "create the bucket". Wrapping the
// right error
// matters more than having advice to give.
func TestStagingErrorInventsNoDiagnosis(t *testing.T) {
	l := &Loader{cfg: &core.LoadConfig{StagingBucket: "b", StagingPrefix: "p/"}}
	causa := errors.New("connection reset by peer")

	err := l.stagingError(context.Background(), causa, 12000)
	if !errors.Is(err, causa) {
		t.Fatal("o erro original precisa continuar acessivel por errors.Is")
	}
	if strings.Contains(err.Error(), "create the bucket") {
		t.Errorf("aconselhou criar bucket numa falha de rede:\n%s", err)
	}
	if !strings.Contains(err.Error(), "gs://b/p/") {
		t.Errorf("nem no caminho generico diz onde estava escrevendo:\n%s", err)
	}
}

// TestLoadViaGCSUsesStagingError is the test that matters: the two above prove
// the
// function, this one proves the point of use. Without it, swapping the call back
// for a
// fmt.Errorf("close gcs writer: %w", err) would pass green -- which is what
// happened when only the ones above were written.
//
// The fake GCS answers 404 to everything, which is what a missing bucket looks
// like.
func TestLoadViaGCSUsesStagingError(t *testing.T) {
	gcs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"code":404,"message":"The specified bucket does not exist"}}`))
	}))
	defer gcs.Close()

	ctx := context.Background()
	client, err := storage.NewClient(ctx,
		option.WithEndpoint(gcs.URL),
		option.WithoutAuthentication(),
	)
	if err != nil {
		t.Fatalf("cliente falso: %v", err)
	}
	defer func() { _ = client.Close() }()

	l := &Loader{
		cfg: &core.LoadConfig{
			Format:          "ndjson",
			StagingBucket:   "projeto-brevis-staging",
			StagingPrefix:   "brevis/",
			ThresholdForGCS: 5000,
		},
		gcs: client,
	}

	_, _, _, err = l.loadViaGCS(ctx, nil, []byte(`{"a":1}`+"\n"), 12000)
	if err == nil {
		t.Fatal("bucket ausente precisa falhar")
	}
	msg := err.Error()
	if strings.Contains(msg, "close gcs writer") {
		t.Errorf("voltou a mensagem crua do writer:\n%s", msg)
	}
	for _, exigido := range []string{"projeto-brevis-staging", "InlineLimit", "12000"} {
		if !strings.Contains(msg, exigido) {
			t.Errorf("o caminho real nao diz %q:\n%s", exigido, msg)
		}
	}
}

// gcsWhere builds a client against a fake GCS that answers `found` with 200 for
// the buckets named in it, and 404 for everything else.
func gcsWhere(t *testing.T, found ...string) *storage.Client {
	t.Helper()
	exists := map[string]bool{}
	for _, b := range found {
		exists[b] = true
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The client is pointed at the fake with option.WithEndpoint, so the
		// path arrives WITHOUT the /storage/v1 prefix it carries against the
		// real API: GET /b/<name>. Trimming the wrong prefix made every probe
		// answer 404 and the test blame the code.
		name := strings.TrimPrefix(r.URL.Path, "/b/")
		if exists[name] {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"name":"` + name + `"}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"code":404,"message":"Not Found"}}`))
	}))
	t.Cleanup(srv.Close)

	c, err := storage.NewClient(context.Background(),
		option.WithEndpoint(srv.URL), option.WithoutAuthentication())
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// TestTheErrorSaysTheOldBucketIsStillThere.
//
// The message already MENTIONED the v0.25.0 rename, and mentioning is not
// answering: whoever read it still had to go and check whether the old bucket
// was there. This is that check.
//
// The case is real: a pipeline under the inline limit never touches a bucket at
// all, so the rename stays invisible until the first day the extract grows past
// it -- and on that day this sentence is the difference between "something
// changed three releases ago" and "your data is in the bucket beside this one".
func TestTheErrorSaysTheOldBucketIsStillThere(t *testing.T) {
	l := &Loader{
		cfg: &core.LoadConfig{
			StagingBucket:   "projeto-brevis-staging",
			StagingPrefix:   "extracts/",
			ThresholdForGCS: 5000,
		},
		gcs: gcsWhere(t, "projeto-bravis-staging"),
	}

	msg := l.stagingError(context.Background(), storage.ErrBucketNotExist, 5156).Error()
	for _, wanted := range []string{
		"projeto-bravis-staging",    // the one that IS there
		"DOES exist",                // said plainly
		"BREVIS_SDK_STAGING_BUCKET", // how to point at it without a rebuild
	} {
		if !strings.Contains(msg, wanted) {
			t.Errorf("the message does not say %q:\n%s", wanted, msg)
		}
	}
}

// TestNoHintWhenTheOldBucketIsGoneToo.
//
// "It is not there either" costs a line and says nothing. The message stays
// what it was.
func TestNoHintWhenTheOldBucketIsGoneToo(t *testing.T) {
	l := &Loader{
		cfg: &core.LoadConfig{StagingBucket: "projeto-brevis-staging", ThresholdForGCS: 5000},
		gcs: gcsWhere(t), // nothing exists
	}
	if msg := l.stagingError(context.Background(), storage.ErrBucketNotExist, 5156).Error(); strings.Contains(msg, "DOES exist") {
		t.Errorf("it claimed a bucket that is not there:\n%s", msg)
	}
}

// TestNoHintForABucketSomebodyNamed.
//
// Whoever chose their own bucket name is not living through the rename, and
// probing a `-bravis-staging` next to it would be a guess dressed as a finding.
func TestNoHintForABucketSomebodyNamed(t *testing.T) {
	l := &Loader{
		cfg: &core.LoadConfig{StagingBucket: "our-data-lake", ThresholdForGCS: 5000},
		// The fake would answer 200 for anything asked, so a probe here would
		// show up in the message.
		gcs: gcsWhere(t, "our-data-lake", "our-data-lake-bravis-staging", "our-data-bravis-staging"),
	}
	if msg := l.stagingError(context.Background(), storage.ErrBucketNotExist, 5156).Error(); strings.Contains(msg, "DOES exist") {
		t.Errorf("it probed a bucket nobody defaulted into:\n%s", msg)
	}
}

// The probe never fails the error it is decorating: no client is the case every
// unit test above has, and a cancelled run still gets the full message.
func TestTheProbeNeverBreaksTheError(t *testing.T) {
	l := &Loader{cfg: &core.LoadConfig{StagingBucket: "projeto-brevis-staging", ThresholdForGCS: 5000}}
	if msg := l.stagingError(context.Background(), storage.ErrBucketNotExist, 5156).Error(); !strings.Contains(msg, "InlineLimit") {
		t.Errorf("with no gcs client the message lost its advice:\n%s", msg)
	}

	// A context that is already done. The load has already failed; the message
	// must not be worse for it.
	dead, cancel := context.WithCancel(context.Background())
	cancel()
	l.gcs = gcsWhere(t, "projeto-bravis-staging")
	if msg := l.stagingError(dead, storage.ErrBucketNotExist, 5156).Error(); !strings.Contains(msg, "DOES exist") {
		t.Errorf("a cancelled run lost the hint:\n%s", msg)
	}
}
