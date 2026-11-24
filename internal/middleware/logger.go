package middleware

import (
	"log/slog"
	"strings"
	"time"
	"unicode"

	"github.com/gin-gonic/gin"

	"projectlens/internal/telemetry"
)

func Logger(log *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		parent := telemetry.Restore(c.Request.Context(), c.GetHeader("traceparent"))
		ctx, span := telemetry.Span(parent, "http.request")
		defer span.End()
		c.Request = c.Request.WithContext(ctx)
		start := time.Now()
		c.Next()

		status := c.Writer.Status()
		telemetry.HTTP.WithLabelValues(telemetry.StatusClass(status)).Observe(time.Since(start).Seconds())
		level := slog.LevelInfo
		switch {
		case status >= 500:
			level = slog.LevelError
		case status >= 400:
			level = slog.LevelWarn
		}
		auth := c.GetString(authStateKey)
		if auth == "" {
			auth = "disabled"
		}
		log.LogAttrs(c.Request.Context(), level, "http request",
			slog.String("trace_id", telemetry.TraceID(ctx)),
			slog.String("request_id", GetRequestID(c)),
			slog.String("method", c.Request.Method),
			slog.String("path", c.Request.URL.Path),
			slog.String("route", c.FullPath()),
			slog.Int("status", status),
			slog.Int64("latency_ms", time.Since(start).Milliseconds()),
			slog.Int("bytes_out", max(c.Writer.Size(), 0)),
			slog.Int64("bytes_in", c.Request.ContentLength),
			slog.String("client_ip", c.ClientIP()),
			slog.String("user_agent", sanitizeUserAgent(c.Request.UserAgent())),
			slog.String("auth", auth),
		)
	}
}

func sanitizeUserAgent(value string) string {
	var clean strings.Builder
	count := 0
	for _, r := range value {
		if count == 256 {
			break
		}
		if !unicode.IsPrint(r) {
			r = ' '
		}
		clean.WriteRune(r)
		count++
	}
	return clean.String()
}
