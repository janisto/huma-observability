package obs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

var noopLogger = zap.NewNop()

type applicationCore struct {
	zapcore.Core
}

func (core *applicationCore) With(fields []zap.Field) zapcore.Core {
	return &applicationCore{Core: core.Core.With(filterApplicationFields(fields))}
}

func (core *applicationCore) Check(entry zapcore.Entry, checked *zapcore.CheckedEntry) *zapcore.CheckedEntry {
	if core.Enabled(entry.Level) {
		return checked.AddCore(entry, core)
	}
	return checked
}

func (core *applicationCore) Write(entry zapcore.Entry, fields []zap.Field) error {
	return core.Core.Write(entry, filterApplicationFields(fields))
}

func filterApplicationFields(fields []zap.Field) []zap.Field {
	hasReserved := false
	for _, field := range fields {
		if field.Type != zapcore.InlineMarshalerType && isReservedLogField(field.Key) {
			hasReserved = true
			break
		}
	}
	if !hasReserved {
		return fields
	}
	filtered := make([]zap.Field, 0, len(fields))
	for _, field := range fields {
		if field.Type == zapcore.InlineMarshalerType || !isReservedLogField(field.Key) {
			filtered = append(filtered, field)
		}
	}
	return filtered
}

func guardApplicationLogger(logger *zap.Logger) *zap.Logger {
	if logger == nil {
		logger = noopLogger
	}
	return logger.WithOptions(zap.WrapCore(func(core zapcore.Core) zapcore.Core {
		if _, guarded := core.(*applicationCore); guarded {
			return core
		}
		return &applicationCore{Core: core}
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

// GCPProfileVersion identifies a specification-defined Google Cloud profile.
type GCPProfileVersion string

// AWSProfileVersion identifies a specification-defined AWS profile.
type AWSProfileVersion string

// AzureProfileVersion identifies a specification-defined Azure profile.
type AzureProfileVersion string

const (
	// GCPProfileVersionV0_1_0 is the current supported Google Cloud profile.
	GCPProfileVersionV0_1_0 GCPProfileVersion = "0.1.0"
	// AWSProfileVersionV0_1_0 is the current supported AWS profile.
	AWSProfileVersionV0_1_0 AWSProfileVersion = "0.1.0"
	// AzureProfileVersionV0_1_0 is the current supported Azure profile.
	AzureProfileVersionV0_1_0 AzureProfileVersion = "0.1.0"
)

// ResolveGCPProfileVersion resolves an omitted GCP profile version to the
// newest version supported by this installed package.
func ResolveGCPProfileVersion(preset Preset, version GCPProfileVersion) (GCPProfileVersion, error) {
	if err := validatePreset(preset); err != nil {
		return "", err
	}
	if preset != PresetGCP {
		if version != "" {
			return "", errors.New("observability: GCP profile version requires GCP preset")
		}
		return "", nil
	}

	if version == "" {
		return GCPProfileVersionV0_1_0, nil
	}
	if version != GCPProfileVersionV0_1_0 {
		return "", fmt.Errorf("observability: unsupported GCP profile version %q", version)
	}
	return version, nil
}

func ResolveAWSProfileVersion(preset Preset, version AWSProfileVersion) (AWSProfileVersion, error) {
	if err := validatePreset(preset); err != nil {
		return "", err
	}
	if preset != PresetAWS {
		if version != "" {
			return "", errors.New("observability: AWS profile version requires AWS preset")
		}
		return "", nil
	}
	if version == "" {
		return AWSProfileVersionV0_1_0, nil
	}
	if version != AWSProfileVersionV0_1_0 {
		return "", fmt.Errorf("observability: unsupported AWS profile version %q", version)
	}
	return version, nil
}

func ResolveAzureProfileVersion(preset Preset, version AzureProfileVersion) (AzureProfileVersion, error) {
	if err := validatePreset(preset); err != nil {
		return "", err
	}
	if preset != PresetAzure {
		if version != "" {
			return "", errors.New("observability: Azure profile version requires Azure preset")
		}
		return "", nil
	}
	if version == "" {
		return AzureProfileVersionV0_1_0, nil
	}
	if version != AzureProfileVersionV0_1_0 {
		return "", fmt.Errorf("observability: unsupported Azure profile version %q", version)
	}
	return version, nil
}

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
	Preset              Preset
	GCPProfileVersion   GCPProfileVersion
	AWSProfileVersion   AWSProfileVersion
	AzureProfileVersion AzureProfileVersion
	Level               zapcore.LevelEnabler
	Writer              io.Writer
	ErrorWriter         io.Writer
	AddCaller           bool
	Development         bool
}

// NewLogger creates a Zap logger that writes one compact JSON object plus LF
// per event for the selected preset.
func NewLogger(config LoggerConfig) (*zap.Logger, error) {
	if _, err := ResolveGCPProfileVersion(config.Preset, config.GCPProfileVersion); err != nil {
		return nil, err
	}
	if _, err := ResolveAWSProfileVersion(config.Preset, config.AWSProfileVersion); err != nil {
		return nil, err
	}
	if _, err := ResolveAzureProfileVersion(config.Preset, config.AzureProfileVersion); err != nil {
		return nil, err
	}
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
	return guardApplicationLogger(zap.New(core, options...)), nil
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
