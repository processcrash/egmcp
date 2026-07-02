// Package store owns the per-instance YAML files. It reads, writes and
// watches them, mediating filesystem races via flock so two concurrent
// API requests cannot leave the file half-written.
package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"go.uber.org/zap"
	"gopkg.in/yaml.v3"

	"github.com/processcrash/egmcp/internal/log"
)

// slugRe enforces the [a-z0-9_-]{1,32} slug rule for instance names.
// It is intentionally restrictive: slugs travel in URLs and the MCP
// session id, so allowing anything else invites encoding and audit
// headaches.
var slugRe = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)

// Instance is the runtime representation of one MCP instance as
// described in <instances_dir>/<slug>.yaml.
type Instance struct {
	Slug        string        `yaml:"slug" json:"slug"`
	DisplayName string        `yaml:"display_name,omitempty" json:"display_name,omitempty"`
	Enabled     bool          `yaml:"enabled" json:"enabled"`
	APIKeys     []string      `yaml:"api_keys,omitempty" json:"api_keys,omitempty"`
	Connectors  []ConnRef     `yaml:"connectors" json:"connectors"`
}

// ConnRef binds a connector type to a per-instance name and config.
type ConnRef struct {
	Type   string                 `yaml:"type" json:"type"`
	Name   string                 `yaml:"name" json:"name"`
	Config map[string]any         `yaml:"config" json:"config"`
}

// Validate enforces invariants on an Instance spec before it is saved.
func (i *Instance) Validate() error {
	if !slugRe.MatchString(i.Slug) {
		return fmt.Errorf("slug must match %s", slugRe)
	}
	if len(i.Connectors) == 0 {
		return errors.New("at least one connector is required")
	}
	seen := make(map[string]struct{}, len(i.Connectors))
	for idx, c := range i.Connectors {
		if c.Type == "" {
			return fmt.Errorf("connectors[%d]: type is required", idx)
		}
		if c.Name == "" {
			return fmt.Errorf("connectors[%d]: name is required", idx)
		}
		key := c.Type + "/" + c.Name
		if _, dup := seen[key]; dup {
			return fmt.Errorf("connectors[%d]: duplicate connector %q", idx, key)
		}
		seen[key] = struct{}{}
	}
	return nil
}

// Store persists Instance objects to YAML files.
type Store struct {
	dir string

	mu      sync.RWMutex
	bySlug  map[string]*Instance

	watcher *fsnotify.Watcher
	logger  *zap.Logger
	cancel  context.CancelFunc
	wg      sync.WaitGroup
}

// New creates a Store rooted at dir (creating it if missing). It also
// loads every *.yaml file in the directory and primes its in-memory
// index.
func New(ctx context.Context, dir string, logger *zap.Logger) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("mkdir instances dir: %w", err)
	}

	s := &Store{
		dir:     dir,
		bySlug:  make(map[string]*Instance),
		logger:  logger,
	}
	if err := s.scanDisk(); err != nil {
		return nil, err
	}

	w, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("fsnotify: %w", err)
	}
	if err := w.Add(dir); err != nil {
		_ = w.Close()
		return nil, fmt.Errorf("watch dir: %w", err)
	}
	s.watcher = w

	cctx, cancel := context.WithCancel(ctx)
	s.cancel = cancel
	s.wg.Add(1)
	go s.watchLoop(cctx)

	return s, nil
}

// Close stops background goroutines and frees resources.
func (s *Store) Close() error {
	if s.cancel != nil {
		s.cancel()
	}
	s.wg.Wait()
	if s.watcher != nil {
		return s.watcher.Close()
	}
	return nil
}

// List returns a snapshot of all instances.
func (s *Store) List() []*Instance {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*Instance, 0, len(s.bySlug))
	for _, inst := range s.bySlug {
		c := *inst
		out = append(out, &c)
	}
	return out
}

// Get returns a copy of the named instance, or nil if unknown.
func (s *Store) Get(slug string) *Instance {
	s.mu.RLock()
	defer s.mu.RUnlock()
	inst, ok := s.bySlug[slug]
	if !ok {
		return nil
	}
	c := *inst
	return &c
}

// Save writes the instance to disk (acquiring an exclusive flock) and
// updates the in-memory index. The file content is the YAML form of
// the Instance.
func (s *Store) Save(inst *Instance) error {
	if err := inst.Validate(); err != nil {
		return err
	}

	path := filepath.Join(s.dir, inst.Slug+".yaml")
	if err := writeWithLock(path, inst); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}

	s.mu.Lock()
	s.bySlug[inst.Slug] = deepClone(inst)
	s.mu.Unlock()
	return nil
}

// Delete removes the instance file. The in-memory entry is removed
// regardless of whether the file existed (idempotent).
func (s *Store) Delete(slug string) error {
	path := filepath.Join(s.dir, slug+".yaml")
	err := os.Remove(path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove: %w", err)
	}
	s.mu.Lock()
	delete(s.bySlug, slug)
	s.mu.Unlock()
	return nil
}

// Has reports whether an instance is currently known.
func (s *Store) Has(slug string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.bySlug[slug]
	return ok
}

// ─────────────────────────────────────────────────────────────────────
// Disk scanning
// ─────────────────────────────────────────────────────────────────────

func (s *Store) scanDisk() error {
	matches, err := filepath.Glob(filepath.Join(s.dir, "*.yaml"))
	if err != nil {
		return fmt.Errorf("glob: %w", err)
	}
	for _, p := range matches {
		inst, err := readInstance(p)
		if err != nil {
			s.logger.Warn("skipping unparseable instance",
				log.String("path", p), log.Err(err))
			continue
		}
		if err := inst.Validate(); err != nil {
			s.logger.Warn("skipping invalid instance",
				log.String("slug", inst.Slug), log.Err(err))
			continue
		}
		s.bySlug[inst.Slug] = inst
	}
	return nil
}

func (s *Store) watchLoop(ctx context.Context) {
	defer s.wg.Done()
	// Debounce fsnotify events; editors often emit several in quick
	// succession for one logical change.
	const debounce = 100 * time.Millisecond

	var pending *time.Timer
	for {
		select {
		case <-ctx.Done():
			if pending != nil {
				pending.Stop()
			}
			return
		case ev, ok := <-s.watcher.Events:
			if !ok {
				return
			}
			if !strings.HasSuffix(ev.Name, ".yaml") {
				continue
			}
			if ev.Op&(fsnotify.Create|fsnotify.Write|fsnotify.Rename|fsnotify.Remove) == 0 {
				continue
			}
			if pending != nil {
				pending.Stop()
			}
			pending = time.AfterFunc(debounce, func() {
				s.reconcileFile(ev.Name, ev.Op)
			})
		case err, ok := <-s.watcher.Errors:
			if !ok {
				return
			}
			s.logger.Warn("fsnotify error", log.Err(err))
		}
	}
}

func (s *Store) reconcileFile(path string, op fsnotify.Op) {
	slug := strings.TrimSuffix(filepath.Base(path), ".yaml")
	if !slugRe.MatchString(slug) {
		return
	}

	if op&(fsnotify.Remove|fsnotify.Rename) != 0 {
		s.mu.Lock()
		delete(s.bySlug, slug)
		s.mu.Unlock()
		s.logger.Info("instance removed from disk", log.String("slug", slug))
		return
	}

	// Briefly wait to let writers release the lock before we read.
	for i := 0; i < 5; i++ {
		inst, err := readInstance(path)
		if err == nil {
			if err := inst.Validate(); err != nil {
				s.logger.Warn("ignoring invalid instance update",
					log.String("slug", slug), log.Err(err))
				return
			}
			s.mu.Lock()
			s.bySlug[slug] = inst
			s.mu.Unlock()
			s.logger.Info("instance reloaded from disk", log.String("slug", slug))
			return
		}
		if !isTransientReadErr(err) {
			s.logger.Warn("could not reload instance",
				log.String("path", path), log.Err(err))
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	s.logger.Warn("gave up waiting on instance reload",
		log.String("path", path))
}

func isTransientReadErr(err error) bool {
	if err == nil {
		return false
	}
	var pe *os.PathError
	if errors.As(err, &pe) {
		// Locked file (Windows sharing violation) or busy file.
		return pe.Op == "open" || pe.Op == "read"
	}
	return false
}

// ─────────────────────────────────────────────────────────────────────
// File I/O
// ─────────────────────────────────────────────────────────────────────

func readInstance(path string) (*Instance, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var inst Instance
	if err := yaml.Unmarshal(data, &inst); err != nil {
		return nil, err
	}
	return &inst, nil
}

func deepClone(in *Instance) *Instance {
	if in == nil {
		return nil
	}
	out := *in
	if in.APIKeys != nil {
		out.APIKeys = append([]string(nil), in.APIKeys...)
	}
	if in.Connectors != nil {
		out.Connectors = make([]ConnRef, len(in.Connectors))
		for i, c := range in.Connectors {
			out.Connectors[i] = c
			if c.Config != nil {
				out.Connectors[i].Config = make(map[string]any, len(c.Config))
				for k, v := range c.Config {
					out.Connectors[i].Config[k] = v
				}
			}
		}
	}
	return &out
}
