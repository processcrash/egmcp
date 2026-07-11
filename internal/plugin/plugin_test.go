package plugin

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNewManagerCreatesDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "plugins")
	m, err := NewManager(dir)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("expected dir to be created: %v", err)
	}
	if m.Dir() != dir {
		t.Fatalf("Dir: %q", m.Dir())
	}
}

func TestScanEmpty(t *testing.T) {
	m, err := NewManager(t.TempDir())
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	manifests, err := m.Scan()
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(manifests) != 0 {
		t.Fatalf("expected empty, got %d", len(manifests))
	}
}

func TestScanSkipsHiddenAndNonPluginFiles(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{
		"good.so",
		".hidden.so",
		"_backup.so",
		"readme.txt",
		"data.json",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	m, _ := NewManager(dir)
	manifests, _ := m.Scan()
	if len(manifests) != 1 {
		t.Fatalf("expected 1, got %d (%+v)", len(manifests), manifests)
	}
	if manifests[0].Name != "good" {
		t.Fatalf("name: %q", manifests[0].Name)
	}
}

func TestLoadOneNonExistent(t *testing.T) {
	m, _ := NewManager(t.TempDir())
	err := m.loadOne(filepath.Join(t.TempDir(), "missing.so"))
	if err == nil {
		t.Fatalf("expected error loading missing plugin")
	}
}

func TestBaseName(t *testing.T) {
	cases := map[string]string{
		"hello.so":  "hello",
		"hello.dll": "hello",
		"hello":     "hello",
	}
	for in, want := range cases {
		if got := baseName(in); got != want {
			t.Fatalf("baseName(%q): want %q, got %q", in, want, got)
		}
	}
}

func TestSanitize(t *testing.T) {
	cases := map[string]string{
		"hello.so":     "hello.so",
		"hello.dll":    "hello.dll",
		"hello":        "hello.so",
		"/path/hello":  "hello.so",
		"../escape.so": "escape.so",
	}
	for in, want := range cases {
		if got := sanitize(in); got != want {
			t.Fatalf("sanitize(%q): want %q, got %q", in, want, got)
		}
	}
}

func TestConnectorsEmptyWhenLoadErrors(t *testing.T) {
	m, _ := NewManager(t.TempDir())
	// No files; Connectors should return an empty map.
	if cs := m.Connectors(); len(cs) != 0 {
		t.Fatalf("expected empty, got %d", len(cs))
	}
}

func TestKnownExt(t *testing.T) {
	for _, ext := range []string{".so", ".dll", ".dylib"} {
		if !knownExt(ext) {
			t.Fatalf("expected %q to be a plugin extension", ext)
		}
	}
	for _, ext := range []string{".txt", ".json", ".yaml"} {
		if knownExt(ext) {
			t.Fatalf("did not expect %q to be a plugin extension", ext)
		}
	}
}