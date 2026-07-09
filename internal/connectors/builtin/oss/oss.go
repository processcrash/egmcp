// Package oss implements the Aliyun OSS connector. It adapts the
// official aliyun-oss-go-sdk client to pkg/objectstore.Backend and
// exposes the same tool inventory as the S3 connector.
package oss

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	alioss "github.com/aliyun/aliyun-oss-go-sdk/oss"

	"github.com/processcrash/egmcp/pkg/connector"
	"github.com/processcrash/egmcp/pkg/objectstore"
)

// Config is the per-instance connector config.
type Config struct {
	// Endpoint is the OSS endpoint, e.g. oss-cn-hangzhou.aliyuncs.com.
	// For internal networks / private clouds, supply the full URL.
	Endpoint string `json:"endpoint"`
	AccessKey string `json:"access_key"`
	SecretKey string `json:"secret_key"`
	Bucket    string `json:"bucket"`
	// PresignTTL in seconds; defaults to 900.
	PresignTTL int `json:"presign_ttl_seconds"`
}

// Connector implements the Aliyun OSS backend.
type Connector struct {
	manifest connector.Manifest
	client   *alioss.Client
	bucket   *alioss.Bucket
	cfg      Config
}

// New returns a Connector with a static manifest.
func New() *Connector { return &Connector{manifest: manifestSchema} }

// Manifest returns the static description.
func (c *Connector) Manifest() connector.Manifest { return c.manifest }

// Init validates the config and opens the client + bucket.
func (c *Connector) Init(_ context.Context, raw json.RawMessage) error {
	var cfg Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return fmt.Errorf("oss: parse config: %w", err)
	}
	if cfg.AccessKey == "" || cfg.SecretKey == "" {
		return errors.New("oss: access_key and secret_key are required")
	}
	if cfg.Endpoint == "" {
		cfg.Endpoint = "oss-cn-hangzhou.aliyuncs.com"
	}
	if !strings.HasPrefix(cfg.Endpoint, "http") {
		cfg.Endpoint = "https://" + cfg.Endpoint
	}
	cli, err := alioss.New(cfg.Endpoint, cfg.AccessKey, cfg.SecretKey)
	if err != nil {
		return fmt.Errorf("oss: new client: %w", err)
	}
	c.client = cli
	if cfg.Bucket != "" {
		c.bucket, err = cli.Bucket(cfg.Bucket)
		if err != nil {
			return fmt.Errorf("oss: bucket: %w", err)
		}
	}
	c.cfg = cfg
	return nil
}

// HealthCheck checks the underlying bucket is reachable when one is
// configured; otherwise it exercises a generic endpoint operation.
func (c *Connector) HealthCheck(ctx context.Context) error {
	if c.client == nil {
		return errors.New("oss: not initialised")
	}
	if c.cfg.Bucket != "" {
		_, err := c.client.GetBucketInfo(c.cfg.Bucket)
		return err
	}
	_, err := c.client.ListBuckets()
	return err
}

// Shutdown is a no-op for OSS.
func (c *Connector) Shutdown(_ context.Context) error { return nil }

// ─────────────────────────────────────────────────────────────────────
// Backend implementation
// ─────────────────────────────────────────────────────────────────────

func (c *Connector) Name() string { return "oss" }

func (c *Connector) bucketOr(b string) (*alioss.Bucket, error) {
	if b == "" {
		if c.bucket != nil {
			return c.bucket, nil
		}
		return nil, errors.New("oss: bucket is required (set default_bucket in config or pass per-tool)")
	}
	return c.client.Bucket(b)
}

func (c *Connector) Stat(ctx context.Context, bucket, key string) (objectstore.Object, error) {
	b, err := c.bucketOr(bucket)
	if err != nil {
		return objectstore.Object{}, err
	}
	hdr, err := b.GetObjectDetailedMeta(key)
	if err != nil {
		if isNotFound(err) {
			return objectstore.Object{}, objectstore.ErrNotFound
		}
		return objectstore.Object{}, err
	}
	out := objectstore.Object{Key: key}
	if h := hdr.Get("Content-Length"); h != "" {
		var n int64
		_, _ = fmt.Sscanf(h, "%d", &n)
		out.Size = n
	}
	if h := hdr.Get("Last-Modified"); h != "" {
		t, _ := time.Parse(time.RFC1123, h)
		if !t.IsZero() {
			out.LastModified = t
		}
	}
	out.ETag = strings.Trim(hdr.Get("Etag"), `"`)
	return out, nil
}

func (c *Connector) Get(ctx context.Context, bucket, key string) (io.ReadCloser, objectstore.Object, error) {
	b, err := c.bucketOr(bucket)
	if err != nil {
		return nil, objectstore.Object{}, err
	}
	body, err := b.GetObject(key)
	if err != nil {
		return nil, objectstore.Object{}, err
	}
	meta := objectstore.Object{Key: key}
	return body, meta, nil
}

func (c *Connector) Put(ctx context.Context, bucket, key string, body io.Reader, contentType string) error {
	b, err := c.bucketOr(bucket)
	if err != nil {
		return err
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	opts := []alioss.Option{alioss.ContentType(contentType)}
	return b.PutObject(key, body, opts...)
}

func (c *Connector) Delete(ctx context.Context, bucket, key string) error {
	b, err := c.bucketOr(bucket)
	if err != nil {
		return err
	}
	if err := b.DeleteObject(key); err != nil && !isNotFound(err) {
		return err
	}
	return nil
}

func (c *Connector) List(ctx context.Context, bucket, prefix, marker, continuationToken string, maxKeys int) (objectstore.ListPage, error) {
	b, err := c.bucketOr(bucket)
	if err != nil {
		return objectstore.ListPage{}, err
	}
	if err := objectstore.PrefixMust(prefix); err != nil {
		return objectstore.ListPage{}, err
	}
	maxKeys = objectstore.Limit(maxKeys)
	if continuationToken != "" {
		marker = continuationToken
	}
	opts := []alioss.Option{
		alioss.Prefix(prefix),
		alioss.MaxKeys(maxKeys),
	}
	if marker != "" {
		opts = append(opts, alioss.Marker(marker))
	}
	out, err := b.ListObjects(opts...)
	if err != nil {
		return objectstore.ListPage{}, err
	}
	page := objectstore.ListPage{
		Objects:     make([]objectstore.Object, 0, len(out.Objects)),
		IsTruncated: out.IsTruncated,
		NextMarker:  out.NextMarker,
	}
	page.Prefixes = append(page.Prefixes, out.CommonPrefixes...)
	for _, o := range out.Objects {
		page.Objects = append(page.Objects, objectstore.Object{
			Key:          o.Key,
			Size:         o.Size,
			LastModified: o.LastModified,
			ETag:         strings.Trim(o.ETag, `"`),
		})
	}
	return page, nil
}

func (c *Connector) PresignGet(ctx context.Context, bucket, key string, ttl time.Duration) (string, error) {
	b, err := c.bucketOr(bucket)
	if err != nil {
		return "", err
	}
	if ttl <= 0 {
		ttl = 15 * time.Minute
	}
	str, err := b.SignURL(key, alioss.HTTPGet, int64(ttl.Seconds()))
	if err != nil {
		return "", err
	}
	return str, nil
}

func (c *Connector) ListBuckets(ctx context.Context) ([]string, error) {
	lb, err := c.client.ListBuckets()
	if err != nil {
		return nil, err
	}
	var names []string
	for _, b := range lb.Buckets {
		names = append(names, b.Name)
	}
	return names, nil
}

// ─────────────────────────────────────────────────────────────────────
// MCP tools (same shape as S3)
// ─────────────────────────────────────────────────────────────────────

func (c *Connector) Tools() []connector.ToolSpec {
	return s3LikeTools()
}

// InvokeTool dispatches a tool call.
func (c *Connector) InvokeTool(ctx context.Context, name string, args json.RawMessage) (json.RawMessage, error) {
	if c.client == nil {
		return nil, errors.New("oss: not initialised")
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
		if c.cfg.Bucket != "" && a.Bucket == "" {
			a.Bucket = c.cfg.Bucket
		}
		if a.Bucket == "" || a.Key == "" {
			return nil, errors.New("oss: bucket and key are required")
		}
		var body io.Reader
		if a.Base64 {
			raw, err := base64.StdEncoding.DecodeString(a.Content)
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
		if c.cfg.Bucket != "" && a.Bucket == "" {
			a.Bucket = c.cfg.Bucket
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
			"content":  base64.StdEncoding.EncodeToString(data),
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
		if c.cfg.Bucket != "" && a.Bucket == "" {
			a.Bucket = c.cfg.Bucket
		}
		if err := c.Delete(ctx, a.Bucket, a.Key); err != nil {
			return nil, err
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
		if c.cfg.Bucket != "" && a.Bucket == "" {
			a.Bucket = c.cfg.Bucket
		}
		page, err := c.List(ctx, a.Bucket, a.Prefix, "", "", a.MaxKeys)
		if err != nil {
			return nil, err
		}
		return json.Marshal(page)
	case "presign_get":
		var a struct {
			Bucket  string `json:"bucket"`
			Key     string `json:"key"`
			TTLSecs int    `json:"ttl_seconds"`
		}
		if err := json.Unmarshal(args, &a); err != nil {
			return nil, err
		}
		if c.cfg.Bucket != "" && a.Bucket == "" {
			a.Bucket = c.cfg.Bucket
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
		return nil, fmt.Errorf("oss: unknown tool %q", name)
	}
}

// ─────────────────────────────────────────────────────────────────────
// helpers
// ─────────────────────────────────────────────────────────────────────

// isNotFound detects OSS "object not found" / "bucket not found"
// errors. The aliyun SDK exposes them as service errors with a known
// status code pattern.
func isNotFound(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "NoSuchKey") ||
		strings.Contains(err.Error(), "Not Found") ||
		strings.Contains(err.Error(), "404")
}

// s3LikeTools returns the connector.ToolSpec list shared by both
// the S3 and OSS connectors. The two drivers expose identical
// semantics at the MCP layer, so the JSON-Schema lives in one place.
func s3LikeTools() []connector.ToolSpec {
	return []connector.ToolSpec{
		{Name: "put_object", Description: "Upload an object.",
			InputSchema: merge(map[string]any{
				"type":     "object",
				"required": []string{"bucket", "key", "content"},
				"properties": map[string]any{
					"bucket":       map[string]any{"type": "string"},
					"key":          map[string]any{"type": "string"},
					"content":      map[string]any{"type": "string"},
					"content_type": map[string]any{"type": "string", "default": "application/octet-stream"},
					"base64":       map[string]any{"type": "boolean", "default": false},
				},
			})},
		{Name: "get_object", Description: "Download an object (returns base64).",
			InputSchema: merge(map[string]any{
				"type":     "object",
				"required": []string{"bucket", "key"},
				"properties": map[string]any{
					"bucket": map[string]any{"type": "string"},
					"key":    map[string]any{"type": "string"},
				},
			})},
		{Name: "delete_object", Description: "Delete an object. Idempotent.",
			InputSchema: merge(map[string]any{
				"type":     "object",
				"required": []string{"bucket", "key"},
				"properties": map[string]any{
					"bucket": map[string]any{"type": "string"},
					"key":    map[string]any{"type": "string"},
				},
			})},
		{Name: "list_objects", Description: "List objects in a bucket under an optional prefix.",
			InputSchema: merge(map[string]any{
				"type": "object",
				"properties": map[string]any{
					"bucket":   map[string]any{"type": "string"},
					"prefix":   map[string]any{"type": "string"},
					"max_keys": map[string]any{"type": "integer", "default": 200},
				},
			})},
		{Name: "presign_get", Description: "Issue a presigned GET URL.",
			InputSchema: merge(map[string]any{
				"type":     "object",
				"required": []string{"bucket", "key"},
				"properties": map[string]any{
					"bucket":      map[string]any{"type": "string"},
					"key":         map[string]any{"type": "string"},
					"ttl_seconds": map[string]any{"type": "integer", "default": 900},
				},
			})},
		{Name: "list_buckets", Description: "List buckets visible to this credential.",
			InputSchema: merge(map[string]any{"type": "object"})},
	}
}

func merge(m map[string]any) connector.JSONSchema {
	b, _ := json.Marshal(m)
	return b
}

// manifestSchema is registered at boot.
var manifestSchema = connector.Manifest{
	Name:        "oss",
	Version:     "0.1.0",
	DisplayName: "Aliyun OSS",
	Description: "Aliyun Object Storage Service (and OSS-compatible private clouds).",
	Capabilities: []string{
		connector.CapabilityTools,
	},
	ConfigSchema: connector.JSONSchema(`{
  "type": "object",
  "required": ["access_key", "secret_key"],
  "properties": {
    "endpoint":   {"type": "string", "title": "Endpoint", "description": "e.g. oss-cn-hangzhou.aliyuncs.com"},
    "access_key": {"type": "string", "title": "Access key"},
    "secret_key": {"type": "string", "title": "Secret key", "format": "password"},
    "bucket":     {"type": "string", "title": "Default bucket"},
    "presign_ttl_seconds": {"type": "integer", "title": "Presign TTL (s)", "default": 900}
  }
}`),
}