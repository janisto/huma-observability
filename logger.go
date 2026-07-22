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

type applicationCore struct {
	zapcore.Core
	nested bool
	preset Preset
}

func (core *applicationCore) With(fields []zap.Field) zapcore.Core {
	filtered, nested := filterApplicationFields(fields, core.nested, core.preset)
	return &applicationCore{Core: core.Core.With(filtered), nested: nested, preset: core.preset}
}

func (core *applicationCore) Check(entry zapcore.Entry, checked *zapcore.CheckedEntry) *zapcore.CheckedEntry {
	if core.Enabled(entry.Level) {
		return checked.AddCore(entry, core)
	}
	return checked
}

func (core *applicationCore) Write(entry zapcore.Entry, fields []zap.Field) error {
	filtered, _ := filterApplicationFields(fields, core.nested, core.preset)
	return core.Core.Write(entry, filtered)
}

func filterApplicationFields(fields []zap.Field, nested bool, preset Preset) ([]zap.Field, bool) {
	if nested {
		return fields, true
	}

	filtered := make([]zap.Field, 0, len(fields))
	for index, field := range fields {
		if field.Type == zapcore.NamespaceType {
			if isReservedApplicationLogField(field.Key, preset) {
				return filtered, false
			}
			filtered = append(filtered, fields[index:]...)
			return filtered, true
		}
		if field.Type == zapcore.InlineMarshalerType || !isReservedApplicationLogField(field.Key, preset) {
			filtered = append(filtered, field)
		}
	}
	if len(filtered) == len(fields) {
		return fields, false
	}
	return filtered, false
}

func isReservedApplicationLogField(key string, preset Preset) bool {
	switch key {
	case "timestamp",
		"logger",
		"caller",
		"stacktrace",
		"message",
		"request_id",
		"correlation_id",
		"trace_id",
		"parent_id",
		"trace_flags",
		"trace_sampled",
		"trace_id_random":
		return true
	}
	if key == "severity" {
		return preset == PresetGCP
	}
	if key == "level" {
		return preset != PresetGCP
	}
	return isSelectedProviderField(key, preset, false)
}

func guardApplicationLogger(logger *zap.Logger, preset Preset) *zap.Logger {
	if logger == nil {
		logger = noopLogger
	}
	return logger.WithOptions(zap.WrapCore(func(core zapcore.Core) zapcore.Core {
		if _, guarded := core.(*applicationCore); guarded {
			return core
		}
		return &applicationCore{Core: core, preset: preset}
	}))
}

func unwrapApplicationLogger(logger *zap.Logger) (*zap.Logger, bool) {
	if logger == nil {
		return noopLogger, false
	}
	guarded := false
	logger = logger.WithOptions(zap.WrapCore(func(core zapcore.Core) zapcore.Core {
		if application, ok := core.(*applicationCore); ok {
			guarded = true
			return application.Core
		}
		return core
	}))
	return logger, guarded
}

func trustedLogger(logger *zap.Logger) *zap.Logger {
	logger, _ = unwrapApplicationLogger(logger)
	return logger
}

// Preset selects a JSON logger field shape.
type Preset string

const (
	// PresetDefault uses flat generic JSON fields.
	PresetDefault Preset = ""
	// PresetGCP uses Google Cloud Logging severity field names and access-log
	// support for Cloud Logging special JSON fields.
	PresetGCP Preset = "gcp"
	// PresetAWS uses flat JSON with AWS-oriented correlation fields.
	PresetAWS Preset = "aws"
	// PresetAzure uses flat JSON with Azure-oriented correlation fields.
	PresetAzure Preset = "azure"
)

func validatePreset(preset Preset) error {
	switch preset {
	case PresetDefault, PresetGCP, PresetAWS, PresetAzure:
		return nil
	default:
		return errors.New("observability: unknown logger preset")
	}
}

// LoggerConfig configures NewLogger.
type LoggerConfig struct {
	Preset      Preset
	Level       zapcore.LevelEnabler
	Writer      io.Writer
	ErrorWriter io.Writer
	AddCaller   bool
	Development bool
}

// NewLogger creates a Zap logger that writes one compact JSON object plus LF
// per event for the selected preset.
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
	return guardApplicationLogger(zap.New(core, options...), config.Preset), nil
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
	switch {
	case level <= zapcore.DebugLevel:
		enc.AppendString("DEBUG")
	case level < zapcore.WarnLevel:
		enc.AppendString("INFO")
	case level < zapcore.ErrorLevel:
		enc.AppendString("WARNING")
	case level < zapcore.DPanicLevel:
		enc.AppendString("ERROR")
	default:
		enc.AppendString("CRITICAL")
	}
}
