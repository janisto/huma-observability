package main

import (
	"context"
	"crypto/subtle"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
	obs "github.com/janisto/huma-observability/v2"
	"go.uber.org/zap"
)

type caseConfig struct {
	preset       obs.Preset
	traceLevel   obs.TraceContextLevel
	gcpVersion   obs.GCPProfileVersion
	awsVersion   obs.AWSProfileVersion
	azureVersion obs.AzureProfileVersion
}

type traceInput struct {
	Authorization string `header:"Authorization" required:"false"`
}

type traceOutput struct {
	Body traceBody
}

type traceBody struct {
	OK             bool   `json:"ok"`
	RequestID      string `json:"request_id"`
	CanaryReceived bool   `json:"canary_received"`
}

var nestedConfiguration = map[string]any{
	"system_id": "sys-402",
	"server_settings": map[string]any{
		"nodes": []map[string]any{{
			"hostname":    "srv-01",
			"port":        8080,
			"ssl_enabled": true,
		}},
	},
}

func main() {
	selected, err := configuredCase(requiredEnvironment("OBS_E2E_CASE"))
	if err != nil {
		log.Fatal(err)
	}
	canary := requiredEnvironment("OBS_E2E_SECRET_CANARY")
	port, err := configuredPort()
	if err != nil {
		log.Fatal(err)
	}
	logger, err := obs.NewLogger(obs.LoggerConfig{
		Preset:              selected.preset,
		GCPProfileVersion:   selected.gcpVersion,
		AWSProfileVersion:   selected.awsVersion,
		AzureProfileVersion: selected.azureVersion,
	})
	if err != nil {
		log.Fatal("logger configuration failed")
	}

	mux := http.NewServeMux()
	humaConfig := huma.DefaultConfig("Observability E2E", "0.0.0")
	// The default creation hook enriches response objects with a $schema field.
	// Keep the fixture response identical to the portable E2E response contract.
	humaConfig.CreateHooks = nil
	api := humago.New(mux, humaConfig)
	api.UseMiddleware(obs.RequestContext(obs.RequestContextConfig{
		Logger:            logger,
		Preset:            selected.preset,
		TraceContextLevel: selected.traceLevel,
	}))
	accessConfig := obs.AccessLoggerConfig{
		Logger:              logger,
		Preset:              selected.preset,
		TraceContextLevel:   selected.traceLevel,
		GCPProfileVersion:   selected.gcpVersion,
		AWSProfileVersion:   selected.awsVersion,
		AzureProfileVersion: selected.azureVersion,
	}
	if selected.preset == obs.PresetGCP {
		accessConfig.ExtraFields = func(huma.Context) []zap.Field {
			return []zap.Field{zap.Any("e2e_configuration", nestedConfiguration)}
		}
	}
	api.UseMiddleware(obs.AccessLogger(accessConfig))
	huma.Register(api, huma.Operation{
		OperationID: "trace",
		Method:      http.MethodGet,
		Path:        "/trace",
	}, traceHandler(canary))

	server := &http.Server{
		Addr:              fmt.Sprintf("0.0.0.0:%d", port),
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal("server failed")
	}
}

func traceHandler(canary string) func(context.Context, *traceInput) (*traceOutput, error) {
	expected := []byte("Bearer " + canary)
	return func(ctx context.Context, input *traceInput) (*traceOutput, error) {
		if subtle.ConstantTimeCompare([]byte(input.Authorization), expected) != 1 {
			return nil, huma.Error401Unauthorized("unauthorized")
		}
		obs.Logger(ctx).Info("handler", zap.String("event", "trace"))
		return &traceOutput{Body: traceBody{
			OK:             true,
			RequestID:      obs.RequestID(ctx),
			CanaryReceived: true,
		}}, nil
	}
}

func requiredEnvironment(name string) string {
	value := os.Getenv(name)
	if value == "" {
		log.Fatalf("%s must be nonempty", name)
	}
	return value
}

func configuredCase(name string) (caseConfig, error) {
	config := caseConfig{traceLevel: obs.TraceContextLevel1}
	switch name {
	case "common_level1":
		return config, nil
	case "common_level2":
		config.traceLevel = obs.TraceContextLevel2
		return config, nil
	case "aws_level1":
		config.preset = obs.PresetAWS
		config.awsVersion = obs.AWSProfileVersionV0_1_0
		return config, nil
	case "azure_level1":
		config.preset = obs.PresetAzure
		config.azureVersion = obs.AzureProfileVersionV0_1_0
		return config, nil
	case "gcp_level1":
		config.preset = obs.PresetGCP
		config.gcpVersion = obs.GCPProfileVersionV0_1_0
		return config, nil
	default:
		return caseConfig{}, fmt.Errorf("unsupported OBS_E2E_CASE %q", name)
	}
}

func configuredPort() (int, error) {
	raw := os.Getenv("PORT")
	if raw == "" {
		raw = "8080"
	}
	port, err := strconv.Atoi(raw)
	if err != nil || port < 1 || port > 65535 {
		return 0, fmt.Errorf("PORT must be between 1 and 65535")
	}
	return port, nil
}
