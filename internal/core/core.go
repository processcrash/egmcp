// Package core owns the runtime model of an egmcp deployment:
//
//   - it holds the configuration of all active MCP instances,
//   - it routes tool/resource/prompt invocations to the correct
//     connector inside a given instance,
//   - it mediates instance lifecycle (load / reload / disable).
//
// In M0 the package is intentionally tiny: it tracks the running
// configuration and exposes /healthz data. Subsequent milestones
// extend it with instance registry and connector dispatch.
package core

import (
	"context"
	"time"

	"github.com/processcrash/egmcp/internal/config"
	"github.com/processcrash/egmcp/internal/log"
	"go.uber.org/zap"
)

// Router is the top-level core handle passed to the HTTP layer.
type Router struct {
	cfg    *config.Config
	logger *zap.Logger
	start  time.Time
}

// New constructs a Router around the loaded configuration. In later
// milestones this will also load instance configs and connectors.
func New(cfg *config.Config, logger *zap.Logger) (*Router, error) {
	return &Router{
		cfg:    cfg,
		logger: logger,
		start:  time.Now(),
	}, nil
}

// Config returns the configuration the router was built with.
func (r *Router) Config() *config.Config { return r.cfg }

// StartedAt returns the time the router was built; useful for healthz.
func (r *Router) StartedAt() time.Time { return r.start }

// Health reports a coarse-grained liveness signal. Liveness is the only
// concern at the /healthz endpoint; readiness (full instance + connector
// readiness) lands in a later milestone.
func (r *Router) Health() Health {
	return Health{
		Status:     "ok",
		Uptime:     time.Since(r.start).Truncate(time.Second).String(),
		InstanceID: r.cfg.DataDir, // stable id tied to the data directory
	}
}

// Health is the JSON shape returned by GET /healthz.
type Health struct {
	Status     string `json:"status"`
	Uptime     string `json:"uptime"`
	InstanceID string `json:"instance_id"`
}

// Shutdown stops background workers (file watchers, reloaders). M0 has
// nothing to stop yet, but the method exists to keep the API stable.
func (r *Router) Shutdown(ctx context.Context) error {
	r.logger.Info("core router shutdown", log.String("instance_id", r.cfg.DataDir))
	return nil
}
