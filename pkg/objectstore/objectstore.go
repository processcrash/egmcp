// Package objectstore is a driver-agnostic abstraction over
// object-storage services (AWS S3, MinIO, Aliyun OSS, GCS, Azure
// Blob). Connector implementations live under
// internal/connectors/builtin/<driver> and adapt their native client
// to the Backend interface.
package objectstore

import (
	"context"
	"errors"
	"io"
	"time"
)

// Object is the metadata returned by Stat / List.
type Object struct {
	Key          string            `json:"key"`
	Size         int64             `json:"size,omitempty"`
	LastModified time.Time         `json:"last_modified,omitempty"`
	ETag         string            `json:"etag,omitempty"`
	Metadata     map[string]string `json:"metadata,omitempty"`
}

// ListPage is a single page of object listings plus optional
// continuation information.
type ListPage struct {
	Objects       []Object `json:"objects"`
	Prefixes      []string `json:"prefixes,omitempty"`
	IsTruncated   bool     `json:"is_truncated"`
	NextMarker    string   `json:"next_marker,omitempty"`
	NextContToken string   `json:"next_continuation_token,omitempty"`
}

// Backend is the cross-driver interface that connector authors
// implement on top of their preferred SDK.
type Backend interface {
	// Name returns a short identifier (e.g. "s3", "oss"). It is used
	// for logging and in error messages.
	Name() string

	// Stat returns an object's metadata.
	Stat(ctx context.Context, bucket, key string) (Object, error)

	// Get fetches the object's bytes.
	Get(ctx context.Context, bucket, key string) (io.ReadCloser, Object, error)

	// Put uploads bytes (size unknown in advance is supported via
	// ReaderAt on some backends; we keep the simple io.Reader
	// shape and rely on the connector to pre-compute the length
	// where required).
	Put(ctx context.Context, bucket, key string, body io.Reader, contentType string) error

	// Delete removes an object. Idempotent — deleting a non-existent
	// object is not an error.
	Delete(ctx context.Context, bucket, key string) error

	// List returns a page of objects under a prefix. The implementation
	// respects the supplied marker / continuation token when present.
	List(ctx context.Context, bucket, prefix, marker, continuationToken string, maxKeys int) (ListPage, error)

	// PresignGet returns a temporary URL that grants time-limited GET
	// access to the object. Useful for clients that need to stream
	// large objects directly from the storage backend.
	PresignGet(ctx context.Context, bucket, key string, ttl time.Duration) (string, error)

	// ListBuckets enumerates the buckets the connection can see.
	ListBuckets(ctx context.Context) ([]string, error)
}

// ErrNotFound is returned by Stat/Get when an object doesn't exist.
// Connectors may translate vendor-specific errors to this.
var ErrNotFound = errors.New("objectstore: object not found")

// Limit normalises the user-supplied maxKeys to a sensible range.
func Limit(maxKeys int) int {
	if maxKeys <= 0 {
		return 1000
	}
	if maxKeys > 10_000 {
		return 10_000
	}
	return maxKeys
}

// PrefixMust ensures the supplied prefix doesn't accidentally
// produce a path-traversal risk. Today's implementations pass the
// prefix straight through to the SDK; future hardening could add a
// sandbox the way the filesystem connector does.
func PrefixMust(prefix string) error {
	if prefix == "" {
		return nil
	}
	// Reserved: a real implementation would check for escapes here.
	return nil
}
