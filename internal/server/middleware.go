package server

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"runtime/debug"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/processcrash/egmcp/internal/log"
	"go.uber.org/zap"
)

// ginRecovery converts panics in handlers to 500 responses.
func ginRecovery(logger *zap.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if rec := recover(); rec != nil {
				logger.Error("panic in handler",
					log.Any("error", rec),
					log.String("stack", string(debug.Stack())),
					log.String("path", c.Request.URL.Path),
				)
				c.AbortWithStatus(http.StatusInternalServerError)
			}
		}()
		c.Next()
	}
}

// ginRequestID attaches a short random id to every request, both as
// a request header and a context value.
func ginRequestID() gin.HandlerFunc {
	const header = "X-Request-Id"
	return func(c *gin.Context) {
		id := c.GetHeader(header)
		if id == "" {
			id = newRequestID()
		}
		c.Writer.Header().Set(header, id)
		c.Next()
	}
}

// ginLogger emits one structured log line per request.
func ginLogger(logger *zap.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		latency := time.Since(start)
		fields := []log.Field{
			log.String("method", c.Request.Method),
			log.String("path", c.Request.URL.Path),
			log.Int("status", c.Writer.Status()),
			log.Any("latency_ms", latency.Milliseconds()),
			log.String("remote", c.ClientIP()),
		}
		level := logger.Info
		if c.Writer.Status() >= 500 {
			level = logger.Error
		} else if c.Writer.Status() >= 400 {
			level = logger.Warn
		}
		level("http request", fields...)
	}
}

func newRequestID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "unknown"
	}
	return hex.EncodeToString(b)
}
