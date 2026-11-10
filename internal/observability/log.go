// Package observability sets up logging, metrics and tracing.
package observability

import (
	"io"
	"log/slog"
)

// NewLogger builds the process logger: JSON in cluster, text locally.
//
// Logging rules for callers: never log secrets, session tokens, passwords or
// precise shopper coordinates. Handlers log the geohash cell (~1 km) at most.
func NewLogger(w io.Writer, format, level string) *slog.Logger {
	var lv slog.Level
	switch level {
	case "debug":
		lv = slog.LevelDebug
	case "warn":
		lv = slog.LevelWarn
	case "error":
		lv = slog.LevelError
	default:
		lv = slog.LevelInfo
	}
	opts := &slog.HandlerOptions{Level: lv}
	if format == "json" {
		return slog.New(slog.NewJSONHandler(w, opts))
	}
	return slog.New(slog.NewTextHandler(w, opts))
}
