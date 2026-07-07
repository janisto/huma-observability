package applog

import (
	"context"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	"github.com/janisto/huma-observability"
)

// Log writes msg at level using the request-scoped logger.
func Log(ctx context.Context, level zapcore.Level, msg string, fields ...zap.Field) {
	if entry := obs.Logger(ctx).Check(level, msg); entry != nil {
		entry.Write(fields...)
	}
}

func Debug(ctx context.Context, msg string, fields ...zap.Field) {
	obs.Logger(ctx).Debug(msg, fields...)
}

func Info(ctx context.Context, msg string, fields ...zap.Field) {
	obs.Logger(ctx).Info(msg, fields...)
}

func Warn(ctx context.Context, msg string, fields ...zap.Field) {
	obs.Logger(ctx).Warn(msg, fields...)
}

func Error(ctx context.Context, msg string, err error, fields ...zap.Field) {
	obs.Logger(ctx).Error(msg, withError(err, fields)...)
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
