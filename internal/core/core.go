// Package core owns the runtime model of an egmcp deployment:
//
//   - it loads and reloads the per-instance YAML files (via store.Store),
//   - it instantiates the right connector.Connector for each entry,
//   - it exposes lookup APIs for the HTTP layer and (later) the MCP
//     transport.
//
// In M0 the package was a stub; in M1 it grows an in-memory
// instance registry with hot-reload. The MCP transport (M2) will
// reach into this registry to find tools/resources/prompts.
package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/processcrash/egmcp/internal/config"
	"github.com/processcrash/egmcp/internal/log"
	"github.com/processcrash/egmcp/internal/store"
	"github.com/processcrash/egmcp/pkg/connector"
)

// ErrInstanceNotFound is returned by lookup helpers when the slug
// has no instance behind it. HTTP handlers map this to 404.
var ErrInstanceNotFound = errors.New("instance not found")

// ErrConnectorNotFound is returned when a connector type is not
// registered in the connector registry.
var ErrConnectorNotFound = errors.New("connector not found")

// Router is the top-level core handle passed to the HTTP layer.
type Router struct {
	cfg        *config.Config
	logger     *zap.Logger
	start      time.Time
	registry   *connector.Registry
	instances  *store.Store
	live       sync.Map // slug -> *liveInstance
}

// liveInstance is a single MCP instance at runtime: the static
// description from the YAML plus the in-process connector handles
// resolved from the registry.
type liveInstance struct {
	Spec       *store.Instance
	Connectors map[string]connector.Connector // key: connector name (e.g. "orders-db")
}

// New constructs a Router around the loaded configuration. It also
// instantiates the in-memory connector registry and the on-disk
// instance store.
func New(ctx context.Context, cfg *config.Config, logger *zap.Logger, reg *connector.Registry) (*Router, error) {
	inst, err := store.New(ctx, cfg.InstancesDir, logger)
	if err != nil {
		return nil, fmt.Errorf("instance store: %w", err)
	}

	r := &Router{
		cfg:       cfg,
		logger:    logger,
		start:     time.Now(),
		registry:  reg,
		instances: inst,
	}
	r.reloadAll()

	// Hot-reload: any time the store reconciles a change, we re-apply
	// the live instance.
	// (We use a tiny polling bridge in M1; in M2+ this becomes an
	// explicit subscription on the store.)
	go r.watchStore(ctx)

	return r, nil
}

// Close stops background work and the underlying store.
func (r *Router) Close() error {
	if r.instances != nil {
		return r.instances.Close()
	}
	return nil
}

// Config returns the configuration the router was built with.
func (r *Router) Config() *config.Config { return r.cfg }

// StartedAt returns the time the router was built; used by /healthz.
func (r *Router) StartedAt() time.Time { return r.start }

// Registry exposes the connector registry so the HTTP layer can
// answer /api/v1/connectors/builtin.
func (r *Router) Registry() *connector.Registry { return r.registry }

// Health reports liveness + the current count of healthy instances.
func (r *Router) Health() Health {
	total, healthy := 0, 0
	r.live.Range(func(_, v any) bool {
		total++
		live := v.(*liveInstance)
		for _, c := range live.Connectors {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			if err := c.HealthCheck(ctx); err == nil {
				healthy++
			}
			cancel()
		}
		return true
	})
	return Health{
		Status:     "ok",
		Uptime:     time.Since(r.start).Truncate(time.Second).String(),
		InstanceID: r.cfg.DataDir,
		Instances:  total,
		Healthy:    healthy,
	}
}

// Health is the JSON shape returned by GET /healthz.
type Health struct {
	Status     string `json:"status"`
	Uptime     string `json:"uptime"`
	InstanceID string `json:"instance_id"`
	Instances  int    `json:"instances"`
	Healthy    int    `json:"connectors_healthy"`
}

// ListInstances returns a snapshot of all known instances.
func (r *Router) ListInstances() []*store.Instance {
	return r.instances.List()
}

// GetInstance returns one instance by slug.
func (r *Router) GetInstance(slug string) (*store.Instance, error) {
	inst := r.instances.Get(slug)
	if inst == nil {
		return nil, ErrInstanceNotFound
	}
	return inst, nil
}

// UpsertInstance writes the instance to disk and refreshes the
// in-memory live state.
func (r *Router) UpsertInstance(inst *store.Instance) error {
	if err := r.instances.Save(inst); err != nil {
		return err
	}
	return r.reloadOne(inst.Slug)
}

// DeleteInstance removes the instance file and tears down its
// connectors.
func (r *Router) DeleteInstance(slug string) error {
	if err := r.instances.Delete(slug); err != nil {
		return err
	}
	if v, ok := r.live.Load(slug); ok {
		r.shutdownLive(v.(*liveInstance))
		r.live.Delete(slug)
	}
	return nil
}

// GetConnector returns the in-process connector handle for a given
// instance and connector name (the ConnRef.Name from the YAML).
func (r *Router) GetConnector(slug, name string) (connector.Connector, error) {
	v, ok := r.live.Load(slug)
	if !ok {
		return nil, ErrInstanceNotFound
	}
	live := v.(*liveInstance)
	c, ok := live.Connectors[name]
	if !ok {
		return nil, fmt.Errorf("connector %q not in instance %q", name, slug)
	}
	return c, nil
}

// HasInstance reports whether an instance is currently loaded.
func (r *Router) HasInstance(slug string) bool {
	return r.instances.Has(slug)
}

// ValidateConnector runs HealthCheck on every connector of an
// instance. The returned map records per-name errors (nil on
// success).
func (r *Router) ValidateConnector(slug string) (map[string]error, error) {
	v, ok := r.live.Load(slug)
	if !ok {
		return nil, ErrInstanceNotFound
	}
	live := v.(*liveInstance)
	out := make(map[string]error, len(live.Connectors))
	for name, c := range live.Connectors {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		out[name] = c.HealthCheck(ctx)
		cancel()
	}
	return out, nil
}

// ─────────────────────────────────────────────────────────────────────
// Live-instance maintenance
// ─────────────────────────────────────────────────────────────────────

func (r *Router) reloadAll() {
	for _, inst := range r.instances.List() {
		if err := r.reloadOne(inst.Slug); err != nil {
			r.logger.Warn("failed to load instance",
				log.String("slug", inst.Slug), log.Err(err))
		}
	}
}

func (r *Router) reloadOne(slug string) error {
	inst := r.instances.Get(slug)
	if inst == nil {
		// Tearing down.
		if v, ok := r.live.Load(slug); ok {
			r.shutdownLive(v.(*liveInstance))
			r.live.Delete(slug)
		}
		return nil
	}

	// Tear down the previous version (if any) before constructing
	// the new one — connectors may hold network resources.
	if v, ok := r.live.Load(slug); ok {
		r.shutdownLive(v.(*liveInstance))
	}

	live := &liveInstance{
		Spec:       inst,
		Connectors: make(map[string]connector.Connector, len(inst.Connectors)),
	}
	for _, ref := range inst.Connectors {
		factory, ok := r.registry.Get(ref.Type)
		if !ok {
			err := fmt.Errorf("connector type %q is not registered", ref.Type)
			r.logger.Warn("instance has unknown connector type",
				log.String("slug", slug), log.String("type", ref.Type), log.Err(err))
			// We still keep the instance in the live set so the
			// /api/v1/instances/{slug}/test endpoint can report the
			// failure to the operator.
			continue
		}
		c := factory()
		cfgBytes, err := json.Marshal(ref.Config)
		if err != nil {
			r.logger.Warn("connector config marshal failed",
				log.String("slug", slug),
				log.String("connector", ref.Name),
				log.String("type", ref.Type),
				log.Err(err))
			continue
		}
		if err := c.Init(context.Background(), cfgBytes); err != nil {
			r.logger.Warn("connector init failed",
				log.String("slug", slug),
				log.String("connector", ref.Name),
				log.String("type", ref.Type),
				log.Err(err))
			continue
		}
		live.Connectors[ref.Name] = c
	}
	r.live.Store(slug, live)
	r.logger.Info("instance reloaded",
		log.String("slug", slug),
		log.Int("connectors", len(live.Connectors)))
	return nil
}

func (r *Router) shutdownLive(live *liveInstance) {
	for name, c := range live.Connectors {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := c.Shutdown(ctx); err != nil {
			r.logger.Warn("connector shutdown error",
				log.String("connector", name), log.Err(err))
		}
		cancel()
	}
}

// watchLoop polls the store for changes (the store's own
// reconciliation has already updated the disk; we only need to
// rebuild live connectors). In M2+ this becomes an event subscription.
func (r *Router) watchStore(ctx context.Context) {
	known := make(map[string]struct{})
	for _, inst := range r.instances.List() {
		known[inst.Slug] = struct{}{}
	}
	ticker := time.NewTicker(750 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			current := r.instances.List()
			currentSet := make(map[string]struct{}, len(current))
			for _, inst := range current {
				currentSet[inst.Slug] = struct{}{}
				if _, ok := known[inst.Slug]; !ok {
					if err := r.reloadOne(inst.Slug); err != nil {
						r.logger.Warn("reload on add failed",
							log.String("slug", inst.Slug), log.Err(err))
					}
				} else {
					// Changed?  We can be smarter with a content
					// hash in M2; for M1 the simple reload on
					// detection is enough.
					if err := r.reloadOne(inst.Slug); err != nil {
						r.logger.Warn("reload on tick failed",
							log.String("slug", inst.Slug), log.Err(err))
					}
				}
			}
			for slug := range known {
				if _, ok := currentSet[slug]; !ok {
					r.reloadOne(slug) // tears down
				}
			}
			known = currentSet
		}
	}
}
