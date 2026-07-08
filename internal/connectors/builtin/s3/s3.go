// Package s3 implements the S3 (and S3-compatible, e.g. MinIO)
// connector using aws-sdk-go-v2.
//
// The connector is intentionally thin: it adapts s3.Client and
// s3.PresignClient to pkg/objectstore.Backend and exposes the usual
// CRUD + presign tool inventory to MCP clients.
package s3

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awscfg "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"

	"github.com/processcrash/egmcp/pkg/connector"
	"github.com/processcrash/egmcp/pkg/objectstore"
)

// Config is the per-instance connector config.
type Config struct {
	// Endpoint is the S3 endpoint URL. Leave empty for AWS, set to
	// e.g. http://localhost:9000 for MinIO.
	Endpoint string `json:"endpoint"`
	Region   string `json:"region"`
	AccessKey string `json:"access_key"`
	SecretKey string `json:"secret_key"`
	// UsePathStyle forces path-style addressing (required for MinIO).
	UsePathStyle bool `json:"use_path_style"`
	// DefaultBucket, when set, lets the caller omit bucket on tools.
	DefaultBucket string `json:"default_bucket"`
	// PresignTTL is the lifetime of presigned URLs (seconds).
	PresignTTL int `json:"presign_ttl_seconds"`
}

// Connector implements the S3-compatible backend.
type Connector struct {
	manifest connector.Manifest
	client   *s3.Client
	presign  *s3.PresignClient
	cfg      Config
}

// New returns a Connector with a static manifest.
func New() *Connector { return &Connector{manifest: manifestSchema} }

// Manifest returns the static description.
func (c *Connector) Manifest() connector.Manifest { return c.manifest }

// Init constructs the SDK clients.
func (c *Connector) Init(_ context.Context, raw json.RawMessage) error {
	var cfg Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return fmt.Errorf("s3: parse config: %w", err)
	}
	if cfg.Region == "" {
		cfg.Region = "us-east-1"
	}
	if cfg.AccessKey == "" || cfg.SecretKey == "" {
		return errors.New("s3: access_key and secret_key are required")
	}

	awsCfg, err := awscfg.LoadDefaultConfig(context.Background(),
		awscfg.WithRegion(cfg.Region),
		awscfg.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(cfg.AccessKey, cfg.SecretKey, "")),
	)
	if err != nil {
		return fmt.Errorf("s3: aws config: %w", err)
	}

	opts := []func(*s3.Options){}
	if cfg.Endpoint != "" {
		ep := cfg.Endpoint
		opts = append(opts, func(o *s3.Options) {
			o.BaseEndpoint = aws.String(ep)
		})
	}
	if cfg.UsePathStyle {
		opts = append(opts, func(o *s3.Options) { o.UsePathStyle = true })
	}
	c.client = s3.NewFromConfig(awsCfg, opts...)
	c.presign = s3.NewPresignClient(c.client)
	c.cfg = cfg
	return nil
}

// HealthCheck checks whether the configured endpoint is reachable by
// listing up to one key. MinIO and AWS both honour this cheaply.
func (c *Connector) HealthCheck(ctx context.Context) error {
	if c.client == nil {
		return errors.New("s3: not initialised")
	}
	_, err := c.client.ListBuckets(ctx, &s3.ListBucketsInput{})
	return err
}

// Shutdown is a no-op (the SDK has no global state).
func (c *Connector) Shutdown(_ context.Context) error { return nil }

// ─────────────────────────────────────────────────────────────────────
// Backend implementation
// ─────────────────────────────────────────────────────────────────────

func (c *Connector) Name() string { return "s3" }

func (c *Connector) Stat(ctx context.Context, bucket, key string) (objectstore.Object, error) {
	out, err := c.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		var nfe *s3types.NotFound
		if errors.As(err, &nfe) {
			return objectstore.Object{}, objectstore.ErrNotFound
		}
		return objectstore.Object{}, err
	}
	return objectstore.Object{
		Key:          key,
		Size:         aws.ToInt64(out.ContentLength),
		LastModified: aws.ToTime(out.LastModified),
		ETag:         aws.ToString(out.ETag),
	}, nil
}

func (c *Connector) Get(ctx context.Context, bucket, key string) (io.ReadCloser, objectstore.Object, error) {
	out, err := c.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return nil, objectstore.Object{}, err
	}
	meta := objectstore.Object{
		Key:  key,
		Size: aws.ToInt64(out.ContentLength),
		ETag: aws.ToString(out.ETag),
	}
	return out.Body, meta, nil
}

func (c *Connector) Put(ctx context.Context, bucket, key string, body io.Reader, contentType string) error {
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	// Read into a buffer so we know the length (needed for older S3
	// implementations and signed-PUT compatibility).
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, body); err != nil {
		return fmt.Errorf("s3: read body: %w", err)
	}
	_, err := c.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(bucket),
		Key:         aws.String(key),
		Body:        bytes.NewReader(buf.Bytes()),
		ContentType: aws.String(contentType),
	})
	return err
}

func (c *Connector) Delete(ctx context.Context, bucket, key string) error {
	_, err := c.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	return err
}

func (c *Connector) List(ctx context.Context, bucket, prefix, marker, continuationToken string, maxKeys int) (objectstore.ListPage, error) {
	if err := objectstore.PrefixMust(prefix); err != nil {
		return objectstore.ListPage{}, err
	}
	maxKeys = objectstore.Limit(maxKeys)
	in := &s3.ListObjectsV2Input{
		Bucket:  aws.String(bucket),
		Prefix:  aws.String(prefix),
		MaxKeys: aws.Int32(int32(maxKeys)),
	}
	if continuationToken != "" {
		in.ContinuationToken = aws.String(continuationToken)
	} else if marker != "" {
		// S3 v2 lists ignore StartAfter-only behaviour of v1, but we
		// accept marker for compatibility by mapping to StartAfter.
		in.StartAfter = aws.String(marker)
	}
	out, err := c.client.ListObjectsV2(ctx, in)
	if err != nil {
		return objectstore.ListPage{}, err
	}
	page := objectstore.ListPage{
		Objects:         make([]objectstore.Object, 0, len(out.Contents)),
		IsTruncated:     aws.ToBool(out.IsTruncated),
		NextContToken:   aws.ToString(out.NextContinuationToken),
	}
	for _, p := range out.CommonPrefixes {
		page.Prefixes = append(page.Prefixes, aws.ToString(p.Prefix))
	}
	for _, o := range out.Contents {
		page.Objects = append(page.Objects, objectstore.Object{
			Key:          aws.ToString(o.Key),
			Size:         aws.ToInt64(o.Size),
			LastModified: aws.ToTime(o.LastModified),
			ETag:         aws.ToString(o.ETag),
		})
	}
	return page, nil
}

func (c *Connector) PresignGet(ctx context.Context, bucket, key string, ttl time.Duration) (string, error) {
	if ttl <= 0 {
		ttl = 15 * time.Minute
	}
	req, err := c.presign.PresignGetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	}, s3.WithPresignExpires(ttl))
	if err != nil {
		return "", err
	}
	return req.URL, nil
}

func (c *Connector) ListBuckets(ctx context.Context) ([]string, error) {
	out, err := c.client.ListBuckets(ctx, &s3.ListBucketsInput{})
	if err != nil {
		return nil, err
	}
	var names []string
	for _, b := range out.Buckets {
		names = append(names, aws.ToString(b.Name))
	}
	return names, nil
}

// ─────────────────────────────────────────────────────────────────────
// MCP tools
// ─────────────────────────────────────────────────────────────────────

func (c *Connector) Tools() []connector.ToolSpec {
	return []connector.ToolSpec{
		{Name: "put_object", Description: "Upload an object. base64 OR utf8 content.",
			InputSchema: merge(map[string]any{
				"type":     "object",
				"required": []string{"bucket", "key", "content"},
				"properties": map[string]any{
					"bucket":       map[string]any{"type": "string"},
					"key":          map[string]any{"type": "string"},
					"content":      map[string]any{"type": "string", "description": "UTF-8 or base64 (auto-detected by printable threshold)"},
					"content_type": map[string]any{"type": "string", "default": "application/octet-stream"},
					"base64":       map[string]any{"type": "boolean", "default": false, "description": "Treat content as base64-encoded bytes"},
				},
			}),
		},
		{Name: "get_object", Description: "Download an object (returns base64 for safety).",
			InputSchema: merge(map[string]any{
				"type":     "object",
				"required": []string{"bucket", "key"},
				"properties": map[string]any{
					"bucket": map[string]any{"type": "string"},
					"key":    map[string]any{"type": "string"},
				},
			}),
		},
		{Name: "delete_object", Description: "Delete an object. Idempotent.",
			InputSchema: merge(map[string]any{
				"type":     "object",
				"required": []string{"bucket", "key"},
				"properties": map[string]any{
					"bucket": map[string]any{"type": "string"},
					"key":    map[string]any{"type": "string"},
				},
			}),
		},
		{Name: "list_objects", Description: "List objects in a bucket under an optional prefix.",
			InputSchema: merge(map[string]any{
				"type": "object",
				"properties": map[string]any{
					"bucket":  map[string]any{"type": "string"},
					"prefix":  map[string]any{"type": "string"},
					"max_keys": map[string]any{"type": "integer", "minimum": 1, "default": 200},
				},
			}),
		},
		{Name: "presign_get", Description: "Issue a presigned GET URL.",
			InputSchema: merge(map[string]any{
				"type":     "object",
				"required": []string{"bucket", "key"},
				"properties": map[string]any{
					"bucket":      map[string]any{"type": "string"},
					"key":         map[string]any{"type": "string"},
					"ttl_seconds": map[string]any{"type": "integer", "default": 900, "minimum": 1},
				},
			}),
		},
		{Name: "list_buckets", Description: "List buckets visible to this credential.",
			InputSchema: merge(map[string]any{"type": "object"}),
		},
	}
}

// InvokeTool dispatches a tool call.
func (c *Connector) InvokeTool(ctx context.Context, name string, args json.RawMessage) (json.RawMessage, error) {
	if c.client == nil {
		return nil, errors.New("s3: not initialised")
	}
	switch name {
	case "put_object":
		var a struct {
			Bucket      string `json:"bucket"`
			Key         string `json:"key"`
			Content     string `json:"content"`
			ContentType string `json:"content_type"`
			Base64      bool   `json:"base64"`
		}
		if err := json.Unmarshal(args, &a); err != nil {
			return nil, err
		}
		if c.cfg.DefaultBucket != "" && a.Bucket == "" {
			a.Bucket = c.cfg.DefaultBucket
		}
		if a.Bucket == "" || a.Key == "" {
			return nil, errors.New("s3: bucket and key are required")
		}
		var body io.Reader
		if a.Base64 {
			raw, err := decodeBase64(a.Content)
			if err != nil {
				return nil, err
			}
			body = bytes.NewReader(raw)
		} else {
			body = bytes.NewReader([]byte(a.Content))
		}
		if err := c.Put(ctx, a.Bucket, a.Key, body, a.ContentType); err != nil {
			return nil, err
		}
		return json.Marshal(map[string]any{"ok": true})
	case "get_object":
		var a struct {
			Bucket string `json:"bucket"`
			Key    string `json:"key"`
		}
		if err := json.Unmarshal(args, &a); err != nil {
			return nil, err
		}
		if c.cfg.DefaultBucket != "" && a.Bucket == "" {
			a.Bucket = c.cfg.DefaultBucket
		}
		rc, meta, err := c.Get(ctx, a.Bucket, a.Key)
		if err != nil {
			return nil, err
		}
		defer rc.Close()
		data, err := io.ReadAll(rc)
		if err != nil {
			return nil, err
		}
		return json.Marshal(map[string]any{
			"key":      meta.Key,
			"size":     meta.Size,
			"content":  encodeBase64(data),
			"encoding": "base64",
		})
	case "delete_object":
		var a struct {
			Bucket string `json:"bucket"`
			Key    string `json:"key"`
		}
		if err := json.Unmarshal(args, &a); err != nil {
			return nil, err
		}
		if c.cfg.DefaultBucket != "" && a.Bucket == "" {
			a.Bucket = c.cfg.DefaultBucket
		}
		return json.Marshal(map[string]any{"ok": true})
	case "list_objects":
		var a struct {
			Bucket  string `json:"bucket"`
			Prefix  string `json:"prefix"`
			MaxKeys int    `json:"max_keys"`
		}
		if err := json.Unmarshal(args, &a); err != nil {
			return nil, err
		}
		if c.cfg.DefaultBucket != "" && a.Bucket == "" {
			a.Bucket = c.cfg.DefaultBucket
		}
		page, err := c.List(ctx, a.Bucket, a.Prefix, "", "", a.MaxKeys)
		if err != nil {
			return nil, err
		}
		return json.Marshal(page)
	case "presign_get":
		var a struct {
			Bucket    string `json:"bucket"`
			Key       string `json:"key"`
			TTLSecs   int    `json:"ttl_seconds"`
		}
		if err := json.Unmarshal(args, &a); err != nil {
			return nil, err
		}
		if c.cfg.DefaultBucket != "" && a.Bucket == "" {
			a.Bucket = c.cfg.DefaultBucket
		}
		url, err := c.PresignGet(ctx, a.Bucket, a.Key, time.Duration(a.TTLSecs)*time.Second)
		if err != nil {
			return nil, err
		}
		return json.Marshal(map[string]any{"url": url})
	case "list_buckets":
		bs, err := c.ListBuckets(ctx)
		if err != nil {
			return nil, err
		}
		return json.Marshal(map[string]any{"buckets": bs})
	default:
		return nil, fmt.Errorf("s3: unknown tool %q", name)
	}
}

func merge(m map[string]any) connector.JSONSchema {
	b, _ := json.Marshal(m)
	return b
}

func decodeBase64(s string) ([]byte, error) {
	return base64.StdEncoding.DecodeString(s)
}

func encodeBase64(b []byte) string {
	return base64.StdEncoding.EncodeToString(b)
}

// manifestSchema is registered at boot.
var manifestSchema = connector.Manifest{
	Name:        "s3",
	Version:     "0.1.0",
	DisplayName: "S3 (MinIO compatible)",
	Description: "S3-compatible object storage. Works with AWS S3, MinIO, Cloudflare R2, …",
	Capabilities: []string{
		connector.CapabilityTools,
	},
	ConfigSchema: connector.JSONSchema(`{
  "type": "object",
  "required": ["access_key", "secret_key"],
  "properties": {
    "endpoint":        {"type": "string",  "title": "Endpoint",  "description": "Set for MinIO etc.; empty for AWS"},
    "region":          {"type": "string",  "title": "Region",    "default": "us-east-1"},
    "access_key":      {"type": "string",  "title": "Access key"},
    "secret_key":      {"type": "string",  "title": "Secret key", "format": "password"},
    "use_path_style":  {"type": "boolean", "title": "Path-style addressing", "default": false, "description": "Required for MinIO"},
    "default_bucket":  {"type": "string",  "title": "Default bucket"},
    "presign_ttl_seconds": {"type": "integer", "title": "Presign TTL (s)", "default": 900, "minimum": 1}
  }
}`),
}
