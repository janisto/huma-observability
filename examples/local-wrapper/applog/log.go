package applog

import (
	"context"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	"github.com/janisto/huma-observability"
)

// Logger returns the request-scoped logger.
func Logger(ctx context.Context) *zap.Logger {
	return obs.Logger(ctx)
}

// Check returns a checked entry for msg at level using the request-scoped logger.
func Check(ctx context.Context, level zapcore.Level, msg string) *zapcore.CheckedEntry {
	return Logger(ctx).Check(level, msg)
}

// Log writes msg at level using the request-scoped logger.
func Log(ctx context.Context, level zapcore.Level, msg string, fields ...zap.Field) {
	if entry := Check(ctx, level, msg); entry != nil {
		entry.Write(fields...)
	}
}

func Debug(ctx context.Context, msg string, fields ...zap.Field) {
	Logger(ctx).Debug(msg, fields...)
}

func Info(ctx context.Context, msg string, fields ...zap.Field) {
	Logger(ctx).Info(msg, fields...)
}

func Warn(ctx context.Context, msg string, fields ...zap.Field) {
	Logger(ctx).Warn(msg, fields...)
}

func Error(ctx context.Context, msg string, err error, fields ...zap.Field) {
	Logger(ctx).Error(msg, withError(err, fields)...)
}

func withError(err error, fields []zap.Field) []zap.Field {
	if err == nil {
		return fields
	}
	all := make([]zap.Field, 0, len(fields)+1)
	all = append(all, zap.Error(err))
	all = append(all, fields...)
	return all
}
