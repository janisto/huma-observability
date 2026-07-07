package obs

import (
	"context"
	"errors"
	"io"
	"os"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

var noopLogger = zap.NewNop()

// Preset selects a JSON logger field shape.
type Preset string

const (
	// PresetDefault uses flat generic JSON fields.
	PresetDefault Preset = ""
	// PresetGCP uses Google Cloud Logging severity field names and access-log
	// support for Cloud Logging special JSON fields.
	PresetGCP Preset = "gcp"
	// PresetAWS uses flat JSON fields suitable for CloudWatch Logs ingestion.
	PresetAWS Preset = "aws"
	// PresetAzure uses flat JSON fields suitable for Azure Monitor ingestion.
	PresetAzure Preset = "azure"
)

// LoggerConfig configures NewLogger.
type LoggerConfig struct {
	Preset      Preset
	Level       zapcore.LevelEnabler
	Writer      io.Writer
	ErrorWriter io.Writer
	AddCaller   bool
	Development bool
}

// NewLogger creates a JSON Zap logger for the selected preset.
func NewLogger(config LoggerConfig) (*zap.Logger, error) {
	levelKey, levelEncoder, err := presetLevelConfig(config.Preset)
	if err != nil {
		return nil, err
	}

	level := config.Level
	if level == nil {
		level = zapcore.InfoLevel
	}
	writer := config.Writer
	if writer == nil {
		writer = os.Stdout
	}
	errorWriter := config.ErrorWriter
	if errorWriter == nil {
		errorWriter = os.Stderr
	}

	encoderConfig := zapcore.EncoderConfig{
		TimeKey:        "timestamp",
		LevelKey:       levelKey,
		NameKey:        "logger",
		MessageKey:     "message",
		CallerKey:      "caller",
		StacktraceKey:  "stacktrace",
		LineEnding:     zapcore.DefaultLineEnding,
		EncodeLevel:    levelEncoder,
		EncodeTime:     utcRFC3339NanoTimeEncoder,
		EncodeDuration: zapcore.SecondsDurationEncoder,
		EncodeCaller:   zapcore.ShortCallerEncoder,
	}

	core := zapcore.NewCore(
		zapcore.NewJSONEncoder(encoderConfig),
		zapcore.Lock(zapcore.AddSync(writer)),
		level,
	)

	options := []zap.Option{zap.ErrorOutput(zapcore.Lock(zapcore.AddSync(errorWriter)))}
	if config.AddCaller {
		options = append(options, zap.AddCaller())
	}
	if config.Development {
		options = append(options, zap.Development())
	}
	return zap.New(core, options...), nil
}

// Logger returns the request-scoped logger from ctx, or a no-op logger when no
// request logger has been installed.
func Logger(ctx context.Context) *zap.Logger {
	metadata := metadataFromContext(ctx)
	if metadata == nil || metadata.Logger == nil {
		return noopLogger
	}
	return metadata.Logger
}

func presetLevelConfig(preset Preset) (string, zapcore.LevelEncoder, error) {
	switch preset {
	case PresetDefault, PresetAWS, PresetAzure:
		return "level", zapcore.CapitalLevelEncoder, nil
	case PresetGCP:
		return "severity", gcpLevelEncoder, nil
	default:
		return "", nil, errors.New("observability: unknown logger preset")
	}
}

func utcRFC3339NanoTimeEncoder(t time.Time, enc zapcore.PrimitiveArrayEncoder) {
	enc.AppendString(t.UTC().Format(time.RFC3339Nano))
}

func gcpLevelEncoder(level zapcore.Level, enc zapcore.PrimitiveArrayEncoder) {
	switch level {
	case zapcore.DebugLevel:
		enc.AppendString("DEBUG")
	case zapcore.InfoLevel:
		enc.AppendString("INFO")
	case zapcore.WarnLevel:
		enc.AppendString("WARNING")
	case zapcore.ErrorLevel:
		enc.AppendString("ERROR")
	case zapcore.DPanicLevel:
		enc.AppendString("CRITICAL")
	case zapcore.PanicLevel:
		enc.AppendString("ALERT")
	case zapcore.FatalLevel:
		enc.AppendString("EMERGENCY")
	default:
		enc.AppendString(level.CapitalString())
	}
}
