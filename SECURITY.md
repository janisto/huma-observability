# Security Policy

## Supported Versions

Security fixes are provided for the latest tagged version of
`github.com/janisto/huma-observability` available through the
[Go module ecosystem](https://pkg.go.dev/github.com/janisto/huma-observability).
Older releases, pre-v1 releases, and unreleased commits are not supported.
Upgrade to the latest release before reporting a vulnerability when possible.

The supported Go and Huma versions are documented in the
[README](README.md#requirements). A problem caused solely by an unsupported Go
or Huma version is outside this policy.

## Reporting a Vulnerability

Do not open a public issue for a suspected vulnerability. Use
[GitHub private vulnerability reporting](https://github.com/janisto/huma-observability/security/advisories/new)
instead.

Include enough information to reproduce and assess the report:

- the affected module, Go, and Huma versions;
- the HTTP adapter and relevant middleware or logger configuration;
- a minimal reproduction or clear reproduction steps;
- the security impact, attack conditions, and affected data; and
- any known mitigation or proposed fix.

Use synthetic data. Do not include credentials, cookies, request or response
bodies, private logs, or other secrets.

Please allow up to seven days for an initial response. Accepted reports will be
handled privately while a fix and coordinated disclosure are prepared. If a
report is declined, the response will explain why. Do not disclose the issue
publicly before coordinated disclosure.

Report vulnerabilities that exist solely in Go, Huma, Zap, or another
dependency to the affected upstream project. General bugs and hardening
suggestions without a security impact belong in the
[public issue tracker](https://github.com/janisto/huma-observability/issues).

This project does not currently offer a bug bounty.
