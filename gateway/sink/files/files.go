// Package files writes a gateway's batches as NDJSON objects.
//
// Importing it costs nothing at all for a local path. A gs:// or s3:// path
// needs an object store registered -- gateway/store/gcs or gateway/store/s3 --
// and THAT is what costs. Keeping them apart is what lets a slim build have a
// dead letter without the AWS SDK.
package files

import (
	"context"
	"fmt"
	"strings"

	"github.com/AreteAcademy/brevis/gateway"
	"github.com/AreteAcademy/brevis/sdk"
	"github.com/AreteAcademy/brevis/sdk/to"
)

// Sink is what the YAML calls this driver.
const Sink = "files"

// New builds the driver from a config block.
//
// The Store is what makes gs:// and s3:// work, and it was once missing:
// `to.Files` takes the object-store backend as a field rather than choosing one
// inside -- which is what keeps the AWS SDK out of a gateway that writes to a
// directory -- and passing none meant a bucket path failed at WRITE time. For
// an ordinary sink that is a retry and a dead letter. For the DEAD LETTER it is
// "the dead letter refused them too, and they are lost".
//
// Resolved HERE, at startup, so a binary without the backend or a deployment
// with the wrong credentials fails to go ready rather than going ready and
// losing the first batch it buries.
func New(b gateway.Build) (gateway.Sinker, error) {
	s := b.Sink
	if strings.TrimSpace(s.Path) == "" {
		return nil, fmt.Errorf("`path` is empty (a directory, gs:// or s3://)")
	}
	store, err := b.Stores.Open(b.Ctx, s.Path)
	if err != nil {
		return nil, err
	}
	f := to.Files{Path: s.Path}
	if store != nil {
		f.Store = store
	}
	return &sink{files: f, name: "files:" + s.Path}, nil
}

type sink struct {
	files to.Files
	name  string
}

func (f *sink) Describe() string { return f.name }

func (f *sink) Write(ctx context.Context, batch []gateway.Envelope) (int64, error) {
	res, err := f.files.Write(ctx, batch, sdk.WriteOptions{})
	if res == nil {
		return 0, err
	}
	return res.RowsLoaded, err
}
