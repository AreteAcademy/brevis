// Package gcs registers the Google Cloud Storage backend for a gateway's
// `files` sink.
//
// Importing it costs the Google storage client. Next to the Pub/Sub or
// BigQuery sinks that is almost free -- they bring the same stack -- and on its
// own it is about 7 MB.
package gcs

import (
	"context"

	"cloud.google.com/go/storage"

	"github.com/AreteAcademy/brevis/sdk"
	gcsstore "github.com/AreteAcademy/brevis/sdk/store/gcs"
)

// Scheme is the URL scheme this backend serves.
const Scheme = "gs"

// Open opens the client and returns the backend.
//
// At startup, for the reason the S3 backend gives: credentials that are wrong
// should fail readiness rather than the first buried batch.
func Open(ctx context.Context) (sdk.Store, error) {
	client, err := storage.NewClient(ctx)
	if err != nil {
		return nil, err
	}
	return gcsstore.New(client), nil
}
