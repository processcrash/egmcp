package s3

import (
	"context"
	"encoding/json"
	"testing"
)

func TestManifest(t *testing.T) {
	c := New()
	m := c.Manifest()
	if m.Name != "s3" {
		t.Fatalf("name: %q", m.Name)
	}
	if len(m.Capabilities) == 0 {
		t.Fatalf("missing Capabilities")
	}
}

func TestInitRequiresCredentials(t *testing.T) {
	c := New()
	cases := []struct {
		name string
		cfg  Config
	}{
		{"empty", Config{}},
		{"missing access", Config{SecretKey: "x"}},
		{"missing secret", Config{AccessKey: "x"}},
	}
	for _, c1 := range cases {
		raw, _ := json.Marshal(c1.cfg)
		if err := c.Init(context.Background(), raw); err == nil {
			t.Fatalf("%s: expected error", c1.name)
		}
	}
}

func TestInitUnreachableEndpoint(t *testing.T) {
	c := New()
	raw, _ := json.Marshal(Config{
		AccessKey: "x", SecretKey: "y",
		Region: "us-east-1", Endpoint: "http://127.0.0.1:1",
		UsePathStyle: true,
	})
	// We don't ping on Init; the SDK is lazy. Make sure the
	// endpoint configured by returning a non-error from Init.
	if err := c.Init(context.Background(), raw); err != nil {
		t.Fatalf("Init: %v", err)
	}
}

func TestTools(t *testing.T) {
	c := New()
	tools := c.Tools()
	want := map[string]bool{
		"put_object":    false,
		"get_object":    false,
		"delete_object": false,
		"list_objects":  false,
		"presign_get":   false,
		"list_buckets":  false,
	}
	for _, t1 := range tools {
		if _, ok := want[t1.Name]; ok {
			want[t1.Name] = true
		}
	}
	for name, found := range want {
		if !found {
			t.Fatalf("missing tool %q", name)
		}
	}
}

func TestBase64Helpers(t *testing.T) {
	in := []byte("hello 世界")
	enc := encodeBase64(in)
	out, err := decodeBase64(enc)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if string(out) != string(in) {
		t.Fatalf("roundtrip mismatch: %q vs %q", out, in)
	}
}
