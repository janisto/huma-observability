// Package obs provides request correlation, request-scoped Zap loggers, and
// structured Zap access logging middleware for Huma v2 APIs, plus request
// context middleware for standard net/http handlers.
//
// The package is intentionally small and opinionated: it uses W3C Trace Context
// for trace correlation, validates or generates request IDs, stores
// request-scoped Zap loggers on context, and emits JSON access logs with
// generic, Google Cloud, AWS, and Azure presets.
package obs
