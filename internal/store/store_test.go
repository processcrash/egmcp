package store

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	s, err := New(context.Background(), dir, zap.NewNop())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func sampleInstance(slug string) *Instance {
	return &Instance{
		Slug:        slug,
		DisplayName: "Sample " + slug,
		Enabled:     true,
		APIKeys:     []string{"sample-key"},
		Connectors: []ConnRef{{
			Type: "filesystem",
			Name: "root",
			Config: map[string]any{
				"root": "/tmp",
			},
		}},
	}
}

func TestStoreSaveAndGet(t *testing.T) {
	s := newStore(t)
	inst := sampleInstance("orders")

	if err := s.Save(inst); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got := s.Get("orders")
	if got == nil {
		t.Fatalf("expected orders to be in store")
	}
	if got.DisplayName != "Sample orders" {
		t.Fatalf("DisplayName: %q", got.DisplayName)
	}

	// Verify the YAML file was written.
	path := filepath.Join(s.dir, "orders.yaml")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected file at %s: %v", path, err)
	}
}

func TestStoreGetMissing(t *testing.T) {
	s := newStore(t)
	if got := s.Get("nope"); got != nil {
		t.Fatalf("expected nil, got %+v", got)
	}
}

func TestStoreValidateRejects(t *testing.T) {
	cases := []struct {
		name string
		inst *Instance
		want string
	}{
		{
			name: "bad slug uppercase",
			inst: &Instance{Slug: "Bad-Slug", Connectors: []ConnRef{{Type: "t", Name: "n"}}},
			want: "slug must match",
		},
		{
			name: "no connectors",
			inst: &Instance{Slug: "ok"},
			want: "at least one connector",
		},
		{
			name: "duplicate connectors",
			inst: &Instance{Slug: "ok", Connectors: []ConnRef{
				{Type: "t", Name: "n"},
				{Type: "t", Name: "n"},
			}},
			want: "duplicate connector",
		},
		{
			name: "missing type",
			inst: &Instance{Slug: "ok", Connectors: []ConnRef{{Name: "n"}}},
			want: "type is required",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.inst.Validate()
			if err == nil {
				t.Fatalf("expected error")
			}
			if !contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not contain %q", err.Error(), tc.want)
			}
		})
	}
}

func TestStoreDeleteIsIdempotent(t *testing.T) {
	s := newStore(t)
	if err := s.Save(sampleInstance("d")); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete("d"); err != nil {
		t.Fatal(err)
	}
	// Second delete is a no-op.
	if err := s.Delete("d"); err != nil {
		t.Fatalf("second delete should not error, got %v", err)
	}
	if s.Has("d") {
		t.Fatalf("expected d to be removed")
	}
}

func TestStoreConcurrentSaves(t *testing.T) {
	s := newStore(t)
	const N = 8
	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func(i int) {
			defer wg.Done()
			inst := sampleInstance("orders")
			inst.Connectors[0].Name = "root" + string(rune('A'+i))
			if err := s.Save(inst); err != nil {
				t.Errorf("Save %d: %v", i, err)
			}
		}(i)
	}
	wg.Wait()

	got := s.Get("orders")
	if got == nil {
		t.Fatalf("expected orders after concurrent saves")
	}
	if len(got.Connectors) != 1 {
		t.Fatalf("expected exactly 1 connector, got %d", len(got.Connectors))
	}
}

func TestStoreScanDiskReloadsExistingFiles(t *testing.T) {
	dir := t.TempDir()
	// Pre-seed the dir with a valid instance file.
	want := sampleInstance("seed")
	if err := writeWithLock(filepath.Join(dir, "seed.yaml"), want); err != nil {
		t.Fatal(err)
	}

	s, err := New(context.Background(), dir, zap.NewNop())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer s.Close()

	got := s.Get("seed")
	if got == nil {
		t.Fatalf("expected seed to be loaded from disk")
	}
	if got.DisplayName != "Sample seed" {
		t.Fatalf("DisplayName: %q", got.DisplayName)
	}
}

func TestStoreFileWatcherPicksUpChange(t *testing.T) {
	s := newStore(t)
	if err := s.Save(sampleInstance("watched")); err != nil {
		t.Fatal(err)
	}
	// Wait briefly for fsnotify to wire up.
	time.Sleep(150 * time.Millisecond)

	// Mutate the file directly (simulating a manual edit).
	mutated := sampleInstance("watched")
	mutated.DisplayName = "Mutated"
	if err := writeWithLock(filepath.Join(s.dir, "watched.yaml"), mutated); err != nil {
		t.Fatal(err)
	}

	// Poll for reconciliation (the watcher debounces by 100ms).
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if got := s.Get("watched"); got != nil && got.DisplayName == "Mutated" {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	got := s.Get("watched")
	t.Fatalf("expected watcher to reload, got %+v", got)
}

func contains(haystack, needle string) bool {
	if len(needle) == 0 {
		return true
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
