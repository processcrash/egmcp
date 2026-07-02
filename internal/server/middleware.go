package server

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"runtime/debug"
	"time"

	"github.com/processcrash/egmcp/internal/log"
	"go.uber.org/zap"
)

// chain composes middleware from outermost to innermost. The first
// argument wraps the entire response chain.
func chain(mw ...func(http.Handler) http.Handler) func(http.Handler) http.Handler {
	return func(h http.Handler) http.Handler {
		for i := len(mw) - 1; i >= 0; i-- {
			h = mw[i](h)
		}
		return h
	}
}

// middlewareRecover turns panics into 500s and logs the stack.
func middlewareRecover(logger *zap.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rec := recover(); rec != nil {
					logger.Error("panic in handler",
						log.Any("error", rec),
						log.String("stack", string(debug.Stack())),
						log.String("path", r.URL.Path),
					)
					http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// middlewareRequestID attaches a short random id to every request,
// both as a request header on the way out and as a logging context.
func middlewareRequestID() func(http.Handler) http.Handler {
	const header = "X-Request-Id"
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id := r.Header.Get(header)
			if id == "" {
				id = newRequestID()
			}
			w.Header().Set(header, id)
			next.ServeHTTP(w, r)
		})
	}
}

// middlewareLog emits one structured log line per request with method,
// path, status, latency. Logging is at info level for non-error requests
// and at warn for 4xx/5xx.
func middlewareLog(logger *zap.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			sw := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(sw, r)
			latency := time.Since(start)
			level := logger.Info
			fields := []log.Field{
				log.String("method", r.Method),
				log.String("path", r.URL.Path),
				log.Int("status", sw.status),
				log.Any("latency_ms", latency.Milliseconds()),
				log.String("remote", r.RemoteAddr),
			}
			if sw.status >= 500 {
				level = logger.Error
			} else if sw.status >= 400 {
				level = logger.Warn
			}
			level("http request", fields...)
		})
	}
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func newRequestID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "unknown"
	}
	return hex.EncodeToString(b)
}
