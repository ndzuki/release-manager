package config

import (
	"fmt"
	"log/slog"
	"strings"
)

// DefaultLogLevel is the level applied when log_level is empty. It preserves
// the pre-wiring hardcoded Debug level so existing deployments keep their
// observable behavior when the key is absent.
const DefaultLogLevel = slog.LevelDebug

// ParseLogLevel maps the configured log_level string to a slog level.
// Matching is case-insensitive on the trimmed value; an unset value selects
// DefaultLogLevel and an unknown value is an error (the caller decides the
// fallback). "warn" and "warning" are accepted aliases.
func ParseLogLevel(raw string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "":
		return DefaultLogLevel, nil
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return DefaultLogLevel, fmt.Errorf("unknown log_level %q (want debug, info, warn or error)", raw)
	}
}
