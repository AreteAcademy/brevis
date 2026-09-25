// Package s3 registers the S3 backend for a gateway's `files` sink and for
// Redshift's staging prefix.
//
// Importing it costs the AWS SDK and its credential chain -- about 3 MB -- and
// that is the point of it being a package of its own: a gateway that writes to
// a local directory and a Postgres table should not carry it.
package s3

import (
	"context"
	"os"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/AreteAcademy/brevis/sdk"
	s3store "github.com/AreteAcademy/brevis/sdk/store/s3"
)

// Scheme is the URL scheme this backend serves.
const Scheme = "s3"

// EnvEndpoint points S3 at something that is not Amazon's: MinIO, Ceph, R2, or
// a mock in a test. It is AWS's own variable name, so a deployment that already
// sets it for other tools needs nothing new here.
const EnvEndpoint = "AWS_ENDPOINT_URL_S3"

// Open resolves the credentials and returns the backend.
//
// At startup and not on first use: a gateway whose role is wrong should fail to
// go ready, not go ready and lose the first batch it buries.
func Open(ctx context.Context) (sdk.Store, error) {
	cfg, err := awsconfig.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, err
	}
	return s3store.New(awss3.NewFromConfig(cfg, options)), nil
}

// options turns on path-style addressing when the endpoint is not Amazon's.
//
// Virtual-host addressing puts the bucket in the HOSTNAME --
// bucket.s3.amazonaws.com -- which every S3-compatible server either does not
// do or needs DNS for. Path style is the form they all accept, and it is only
// switched on when an endpoint was named, so nothing changes for Amazon.
func options(o *awss3.Options) {
	if os.Getenv(EnvEndpoint) != "" {
		o.UsePathStyle = true
	}
}
