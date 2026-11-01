// Package logging 提供统一的结构化日志构造器。
// agent 的日志由 systemd 收进 journald，因此输出到 stderr、JSON 行格式。
package logging

import (
	"fmt"
	"io"
	"log/slog"
	"strings"
)

// New 按级别构造 JSON logger。level 取 debug/info/warn/error（大小写不敏感）。
func New(level string, w io.Writer) (*slog.Logger, error) {
	var slogLevel slog.Level
	switch strings.ToLower(level) {
	case "debug":
		slogLevel = slog.LevelDebug
	case "info", "":
		slogLevel = slog.LevelInfo
	case "warn", "warning":
		slogLevel = slog.LevelWarn
	case "error":
		slogLevel = slog.LevelError
	default:
		return nil, fmt.Errorf("未知日志级别 %q（可选 debug/info/warn/error）", level)
	}

	handler := slog.NewJSONHandler(w, &slog.HandlerOptions{Level: slogLevel})
	return slog.New(handler), nil
}
