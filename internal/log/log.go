// Package log wraps uber-go/zap to provide a small, opinionated logging
// surface tailored for egmcp.
//
// The platform produces JSON logs to stdout. Container log collectors
// (Docker, k8s) pick the lines up uniformly and downstream pipelines can
// treat every line as a structured record.
package log

import (
	"fmt"
	"io"
	"log"
	"os"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// Field is a re-export of zap.Field so callers don't have to import zap
// directly. Keep this set small and stable.
type Field = zap.Field

// Helpers for the few field kinds we use most.
func String(k, v string) Field  { return zap.String(k, v) }
func Int(k string, v int) Field { return zap.Int(k, v) }
func Err(err error) Field      { return zap.Error(err) }
func Any(k string, v any) Field { return zap.Any(k, v) }

// New builds a logger configured for JSON output at the requested level.
// Unknown levels resolve to info.
func New(level string) (*zap.Logger, error) {
	lvl := zapcore.InfoLevel
	if err := lvl.UnmarshalText([]byte(level)); err != nil {
		lvl = zapcore.InfoLevel
	}

	encCfg := zap.NewProductionEncoderConfig()
	encCfg.TimeKey = "ts"
	encCfg.EncodeTime = zapcore.ISO8601TimeEncoder
	encCfg.MessageKey = "msg"
	encCfg.LevelKey = "level"
	encCfg.CallerKey = "caller"

	core := zapcore.NewCore(
		zapcore.NewJSONEncoder(encCfg),
		zapcore.Lock(os.Stdout),
		lvl,
	)
	return zap.New(core, zap.AddCaller(), zap.AddStacktrace(zapcore.ErrorLevel)), nil
}

// NewStdLogger adapts a zap.Logger to the standard library log.Logger
// interface so it can be plugged into http.Server.ErrorLog.
func NewStdLogger(l *zap.Logger) *log.Logger {
	return log.New(&zapWriter{l: l}, "", 0)
}

type zapWriter struct{ l *zap.Logger }

func (w *zapWriter) Write(p []byte) (int, error) {
	w.l.Warn("http server log", zap.String("line", string(p)))
	return len(p), nil
}

// Discard is exposed for tests and one-shot helpers that need an io.Writer
// compatible with the standard logger.
var Discard io.Writer = io.Discard

// ensureNewline returns s with a trailing newline.
func ensureNewline(s string) string { return s + "\n" }

// fmtAny is a tiny helper used by tests; production code should not need it.
var _ = ensureNewline
var _ = fmt.Sprint
