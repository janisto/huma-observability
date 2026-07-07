// Package obs provides request correlation and structured Zap access
// logging middleware for Huma APIs.
//
// The package is intentionally small and opinionated: it uses W3C Trace Context
// for trace correlation, validates or generates request IDs, stores
// request-scoped Zap loggers on context, and emits JSON access logs with generic,
// Google Cloud, AWS, and Azure presets.
package obs
