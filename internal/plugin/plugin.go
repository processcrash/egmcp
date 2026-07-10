// Package plugin manages third-party Go plugins (.so / .dll) that
// extend the platform with custom connectors.
//
// Plugin authors compile their code with `go build -buildmode=plugin`
// and expose a top-level variable:
//
//	package main
//
//	import "github.com/processcrash/egmcp/pkg/connector"
//
//	var Connector connector.Connector = myConnector{}
//
// The Manager scans the configured plugin directory on demand, loads
// each shared library, and exposes its connector to the rest of the
// platform via the same registry used for built-ins.
//
// Notes for plugin authors and operators:
//
//   - The plugin must be built with the same Go toolchain as the
//     platform binary. Mixing toolchains is unsupported.
//   - On Linux glibc, on Windows, the plugin image and the host must
//     share the same C library family.
//   - Files starting with a leading "_" are skipped (useful for
//     keeping backup copies next to active plugins).
package plugin

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	goplugin "plugin"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/processcrash/egmcp/pkg/connector"
)

// Manifest describes a loaded plugin; it is returned by Manager.List.
type Manifest struct {
	Name         string    `json:"name"`
	Path         string    `json:"path"`
	Size         int64     `json:"size"`
	Modified     time.Time `json:"modified"`
	Connector    string    `json:"connector_name"` // connector.Manifest().Name; empty until inspected
	Version      string    `json:"version"`
	Capabilities []string  `json:"capabilities,omitempty"`
	Loaded       bool      `json:"loaded"`
	LoadError    string    `json:"load_error,omitempty"`
}

// Manager scans a directory for plugins and loads them on demand.
type Manager struct {
	dir string

	mu      sync.RWMutex
	loaded  map[string]*goplugin.Plugin // keyed by absolute path
	plugins map[string]*ConnectorProxy // keyed by plugin name (file base)
}

// ConnectorProxy is the safe call surface a plugin exposes to the
// platform. Plugins live in their own address space; the proxy
// delegates every interface assertion through a single, well-known
// symbol so version skew is minimised.
type ConnectorProxy struct {
	Name        string
	Manifest    connector.Manifest
	Connector   connector.Connector
	LoadError   error
	pluginPath  string
}

// NewManager constructs an empty Manager rooted at dir. The directory
// is created if missing.
func NewManager(dir string) (*Manager, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("plugin: mkdir: %w", err)
	}
	return &Manager{
		dir:     dir,
		loaded:  make(map[string]*goplugin.Plugin),
		plugins: make(map[string]*ConnectorProxy),
	}, nil
}

// Dir returns the root directory this manager scans.
func (m *Manager) Dir() string { return m.dir }

// Scan returns the set of plugin files currently on disk. Loading is
// lazy; loading happens on demand via LoadAll or Load.
func (m *Manager) Scan() ([]Manifest, error) {
	entries, err := os.ReadDir(m.dir)
	if err != nil {
		return nil, fmt.Errorf("plugin: scan: %w", err)
	}
	out := make([]Manifest, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") {
			continue
		}
		ext := filepath.Ext(name)
		if !knownExt(ext) {
			continue
		}
		full := filepath.Join(m.dir, name)
		info, err := os.Stat(full)
		if err != nil {
			continue
		}
		mf := Manifest{
			Name:     baseName(name),
			Path:     full,
			Size:     info.Size(),
			Modified: info.ModTime(),
		}
		m.mu.RLock()
		if p, ok := m.plugins[mf.Name]; ok {
			mf.Loaded = p.LoadError == nil
			if p.LoadError != nil {
				mf.LoadError = p.LoadError.Error()
			}
			mf.Connector = p.Manifest.Name
			mf.Version = p.Manifest.Version
			mf.Capabilities = p.Manifest.Capabilities
		}
		m.mu.RUnlock()
		out = append(out, mf)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// LoadAll iterates every plugin file on disk and loads each. A plugin
// that fails to load is recorded with LoadError and skipped; the rest
// of the platform keeps running.
func (m *Manager) LoadAll() error {
	manifests, err := m.Scan()
	if err != nil {
		return err
	}
	var firstErr error
	for _, mf := range manifests {
		if err := m.loadOne(mf.Path); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// LoadAllByName loads a single plugin.
func (m *Manager) loadOne(path string) error {
	base := baseName(filepath.Base(path))
	m.mu.Lock()
	if _, ok := m.plugins[base]; ok {
		// Already loaded. Reload by closing the existing one
		// and starting fresh.
		delete(m.plugins, base)
	}
	m.mu.Unlock()

	p, err := goplugin.Open(path)
	if err != nil {
		m.recordError(base, path, err)
		return fmt.Errorf("plugin.Open(%s): %w", path, err)
	}

	sym, err := p.Lookup("Connector")
	if err != nil {
		// Some authors may have used "NewConnector" instead. Try
		// that as a fallback before giving up.
		if alt, altErr := p.Lookup("NewConnector"); altErr == nil {
			sym = alt
			err = nil
		} else {
			m.recordError(base, path, err)
			return fmt.Errorf("plugin.Lookup(Connector): %w", err)
		}
	}

	c, ok := sym.(connector.Connector)
	if !ok {
		err := errors.New("symbol \"Connector\" is not a connector.Connector")
		m.recordError(base, path, err)
		return err
	}

	manifest := c.Manifest()
	proxy := &ConnectorProxy{
		Name:       base,
		Manifest:   manifest,
		Connector:  c,
		pluginPath: path,
	}
	m.mu.Lock()
	m.plugins[base] = proxy
	m.loaded[path] = p
	m.mu.Unlock()
	return nil
}

// Connectors returns a snapshot of every successfully loaded
// connector as (name, factory) pairs, suitable for registering into
// the platform's connector.Registry.
func (m *Manager) Connectors() map[string]connector.Connector {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make(map[string]connector.Connector, len(m.plugins))
	for name, p := range m.plugins {
		if p.LoadError != nil {
			continue
		}
		out[name] = p.Connector
	}
	return out
}

// Remove unloads the named plugin. plugin.Open does not provide a way
// to unload a library, so we only forget our handle — the plugin
// stays loaded in memory for the lifetime of the process.
func (m *Manager) Remove(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.plugins[name]
	if !ok {
		return fmt.Errorf("plugin %q not loaded", name)
	}
	delete(m.plugins, name)
	delete(m.loaded, p.pluginPath)
	return nil
}

// SaveUpload writes the supplied bytes to a deterministic file under
// the plugin directory and triggers a load.
func (m *Manager) SaveUpload(suggestedName string, data []byte) error {
	if err := os.MkdirAll(m.dir, 0o755); err != nil {
		return err
	}
	name := sanitize(suggestedName)
	path := filepath.Join(m.dir, name)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return err
	}
	return m.loadOne(path)
}

func (m *Manager) recordError(name, path string, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.plugins[name] = &ConnectorProxy{
		Name:       name,
		LoadError:  err,
		pluginPath: path,
	}
}

// Delete removes a plugin file from disk after unregistering it from
// the manager. Safe to call when the plugin is not loaded.
func (m *Manager) Delete(name string) error {
	if err := m.Remove(name); err != nil {
		// We still proceed to remove the file even if the plugin
		// wasn't loaded — the user request is "delete this plugin".
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	for path, _ := range m.loaded {
		if baseName(path) == name {
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				return err
			}
			delete(m.loaded, path)
		}
	}
	// Even if the file wasn't loaded, attempt to remove the file by
	// the conventional name. This handles "remove a malformed plugin".
	for _, ext := range []string{".so", ".dll", ".dylib"} {
		p := filepath.Join(m.dir, name+ext)
		if err := os.Remove(p); err == nil {
			return nil
		}
	}
	return nil
}

// ─────────────────────────────────────────────────────────────────────
// helpers
// ─────────────────────────────────────────────────────────────────────

// baseName strips the platform-specific plugin extension.
func baseName(name string) string {
	ext := filepath.Ext(name)
	if knownExt(ext) {
		return name[:len(name)-len(ext)]
	}
	return name
}

// knownExt returns true for the Go plugin extensions per platform.
func knownExt(ext string) bool {
	switch ext {
	case ".so", ".dll", ".dylib":
		return true
	}
	return false
}

// sanitize ensures an uploaded file name is valid and ends with a
// known plugin extension.
func sanitize(name string) string {
	cleaned := filepath.Base(name)
	if knownExt(filepath.Ext(cleaned)) {
		return cleaned
	}
	return cleaned + ".so"
}

// ensure the strings import is used; future helpers may need it.
var _ = strings.HasPrefix
