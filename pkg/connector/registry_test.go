package connector

import (
	"context"
	"encoding/json"
	"testing"
)

type stubConnector struct {
	manifest Manifest
}

func (s *stubConnector) Manifest() Manifest                       { return s.manifest }
func (s *stubConnector) Init(ctx context.Context, _ json.RawMessage) error { return nil }
func (s *stubConnector) HealthCheck(ctx context.Context) error   { return nil }
func (s *stubConnector) Shutdown(ctx context.Context) error      { return nil }

func TestRegistryRegisterAndLookup(t *testing.T) {
	r := NewRegistry()
	called := 0
	r.MustRegister("filesystem", func() Connector {
		called++
		return &stubConnector{manifest: Manifest{Name: "filesystem"}}
	})

	f, ok := r.Get("filesystem")
	if !ok {
		t.Fatalf("filesystem not registered")
	}
	_ = f()
	if called != 1 {
		t.Fatalf("factory not invoked: called=%d", called)
	}

	names := r.Names()
	if len(names) != 1 || names[0] != "filesystem" {
		t.Fatalf("names: %v", names)
	}
}

func TestRegistryDuplicatePanics(t *testing.T) {
	r := NewRegistry()
	r.MustRegister("dup", func() Connector { return nil })
	defer func() {
		if recover() == nil {
			t.Fatalf("expected panic on duplicate register")
		}
	}()
	r.MustRegister("dup", func() Connector { return nil })
}

func TestRegistryGetUnknown(t *testing.T) {
	r := NewRegistry()
	if _, ok := r.Get("ghost"); ok {
		t.Fatalf("expected Get('ghost') to return ok=false")
	}
}

func TestManifestJSONRoundTrip(t *testing.T) {
	m := Manifest{
		Name:        "mysql",
		Version:     "0.1.0",
		DisplayName: "MySQL",
		Description: "Relational database",
		Capabilities: []string{CapabilityTools, CapabilityResources},
		ConfigSchema: JSONSchema(`{"type":"object","properties":{"dsn":{"type":"string"}}}`),
	}
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back Manifest
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.Name != m.Name || back.DisplayName != m.DisplayName {
		t.Fatalf("roundtrip mismatch: %+v", back)
	}
}
