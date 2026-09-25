package gateway_test

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/AreteAcademy/brevis/gateway"
	"github.com/AreteAcademy/brevis/gateway/store/s3"
	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
)

// A `files` sink pointed at s3:// actually writes to S3.
//
// It did not. `to.Files` takes the object-store backend as a FIELD rather than
// choosing one inside -- which is what keeps the AWS SDK out of a fetcher that
// reads GCS -- and the gateway passed none. So `s3://` and `gs://` failed at
// WRITE time with "the Path needs a s3 Store", after the config had loaded and
// the pod had gone ready.
//
// For an ordinary sink that is a retry and a dead letter. For the DEAD LETTER
// it is "the dead letter refused them too, and they are lost" -- the one
// outcome it exists to prevent, arriving on the first bad day in production.
// The docs said all three paths worked the whole time.
func TestIntegrationADeadLetterInABucketActuallyKeepsTheEvents(t *testing.T) {
	endpoint := os.Getenv("BREVIS_GATEWAY_TEST_S3_ENDPOINT")
	if endpoint == "" {
		t.Skip("BREVIS_GATEWAY_TEST_S3_ENDPOINT is not set")
	}
	bucket := fmt.Sprintf("gw-dead-%d", time.Now().UnixNano())
	client := s3Client(t, endpoint)
	if _, err := client.CreateBucket(context.Background(),
		&awss3.CreateBucketInput{Bucket: aws.String(bucket)}); err != nil {
		t.Fatal(err)
	}

	// The gateway builds its own client from the environment, which is the
	// path a pod takes. AWS's own variable name, so a deployment that already
	// sets it for other tools needs nothing new.
	t.Setenv(s3.EnvEndpoint, endpoint)
	t.Setenv("AWS_REGION", "us-east-1")
	t.Setenv("AWS_ACCESS_KEY_ID", "test")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test")

	// A sink that cannot work, so everything lands in the dead letter -- which
	// is the thing under test.
	srv := s3Gateway(t,
		`{type: files, path: /dev/null/impossible/}`,
		fmt.Sprintf(`{type: files, path: "s3://%s/dead/"}`, bucket))
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	post(t, ts.URL+"/v1/clicks", event("e-1", "a.b"), http.StatusAccepted)
	_ = srv.Close(context.Background())

	body := onlyObject(t, client, bucket)
	if !strings.Contains(body, `"event_id":"e-1"`) {
		t.Errorf("the event did not survive into the bucket: %s", body)
	}
	if !strings.Contains(body, "_dead_letter_reason") {
		t.Errorf("the reason did not travel with it: %s", body)
	}
}

// And the ordinary path: a sink that IS a bucket.
//
// The endpoint has to be a HOSTNAME for this to prove what it claims. Without
// path-style addressing the bucket goes in the host -- bucket.minio:9000 --
// which no S3-compatible server answers to without DNS for it. The AWS SDK
// switches to path style on its own when the endpoint is a bare IP, so against
// 127.0.0.1 this passes either way and proves nothing.
func TestIntegrationAFilesSinkWritesToABucket(t *testing.T) {
	endpoint := os.Getenv("BREVIS_GATEWAY_TEST_S3_ENDPOINT")
	if endpoint == "" {
		t.Skip("BREVIS_GATEWAY_TEST_S3_ENDPOINT is not set")
	}
	if host := hostOf(endpoint); net.ParseIP(host) != nil {
		t.Fatalf("the endpoint is the IP %s; point it at a hostname "+
			"(http://localhost:… works) or this passes without exercising "+
			"path-style addressing at all", host)
	}
	bucket := fmt.Sprintf("gw-sink-%d", time.Now().UnixNano())
	client := s3Client(t, endpoint)
	if _, err := client.CreateBucket(context.Background(),
		&awss3.CreateBucketInput{Bucket: aws.String(bucket)}); err != nil {
		t.Fatal(err)
	}

	t.Setenv(s3.EnvEndpoint, endpoint)
	t.Setenv("AWS_REGION", "us-east-1")
	t.Setenv("AWS_ACCESS_KEY_ID", "test")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test")

	srv := s3Gateway(t,
		fmt.Sprintf(`{type: files, path: "s3://%s/landing/"}`, bucket),
		`{type: files, path: `+t.TempDir()+`/}`)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	post(t, ts.URL+"/v1/clicks", event("e-1", "a.b"), http.StatusAccepted)
	if err := srv.Close(context.Background()); err != nil {
		t.Fatalf("draining: %v", err)
	}

	body := onlyObject(t, client, bucket)
	if !strings.Contains(body, `"ingestion_id"`) {
		t.Errorf("the object does not carry the identity: %s", body)
	}
}

// hostOf is the endpoint's host without its port.
func hostOf(endpoint string) string {
	u, err := url.Parse(endpoint)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

func s3Gateway(t *testing.T, sink, dead string) *gateway.Server {
	t.Helper()
	yaml := fmt.Sprintf(`
name: s3_gateway
streams:
  - name: clicks
    path: /v1/clicks
    identity: {provider: web, entity: click, source_key: event_id, record_ts: occurred_at}
    buffer: {flush: {records: 500, every: 1h}}
    retry: {attempts: 1}
    sink: %s
    dead_letter: %s
`, sink, dead)

	file := t.TempDir() + "/g.yaml"
	if err := os.WriteFile(file, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := gateway.Load(file)
	if err != nil {
		t.Fatalf("loading: %v", err)
	}
	srv, err := gateway.New(cfg, nil, everything()...)
	if err != nil {
		t.Fatalf("starting: %v", err)
	}
	return srv
}

func s3Client(t *testing.T, endpoint string) *awss3.Client {
	t.Helper()
	t.Setenv("AWS_ACCESS_KEY_ID", "test")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test")
	cfg, err := awsconfig.LoadDefaultConfig(context.Background(),
		awsconfig.WithRegion("us-east-1"))
	if err != nil {
		t.Fatal(err)
	}
	return awss3.NewFromConfig(cfg, func(o *awss3.Options) {
		o.BaseEndpoint = aws.String(endpoint)
		o.UsePathStyle = true
	})
}

// onlyObject reads the single object the gateway wrote. Single on purpose: a
// test that concatenated several would pass while the gateway wrote one object
// per event, which is the thing batching exists to avoid.
func onlyObject(t *testing.T, client *awss3.Client, bucket string) string {
	t.Helper()
	list, err := client.ListObjectsV2(context.Background(),
		&awss3.ListObjectsV2Input{Bucket: aws.String(bucket)})
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Contents) != 1 {
		t.Fatalf("the bucket holds %d objects, want 1", len(list.Contents))
	}
	obj, err := client.GetObject(context.Background(), &awss3.GetObjectInput{
		Bucket: aws.String(bucket), Key: list.Contents[0].Key,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = obj.Body.Close() }()
	body, err := io.ReadAll(obj.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}
