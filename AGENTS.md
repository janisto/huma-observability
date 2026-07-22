# AGENTS.md

Instructions for coding agents working in this repository.

`README.md` is for human users and contributors: setup, capabilities,
architecture, operations, and contribution entry points. `AGENTS.md` is for
coding agents: execution rules, implementation constraints, and validation
policy. Do not duplicate agent instructions into the README or turn this file
into human onboarding documentation.

## Engineering priorities

- Correctness first, then readability and maintainability, then performance.
- Inspect the relevant implementation, callers, and existing tests before
  changing behavior.
- Prefer the smallest safe change that solves the problem.
- Reuse existing local patterns and utilities, refactoring them when needed,
  instead of creating parallel abstractions or adding dependencies.
- State the failure mode before architectural, security, persistence, or
  production-impacting changes.
- Do not declare completion until implementation, validation, and remaining
  risks are reported.
- Keep source comments and documentation concise. Do not add progress
  narration, generated banners, emojis, or speculative TODOs.

## Pull requests

- Format titles as `type[optional scope]: description`. Prefer no scope;
  include one only when it materially improves clarity.
- Use `feat`, `fix`, `docs`, `test`, `refactor`, `perf`, `build`, `ci`, `chore`,
  or `revert` as the type. Example: `feat: add response size field`.
- Keep each pull request focused. In the body, explain why the change is
  needed, what changed, how it was validated, and any remaining risk.
- Keep the title suitable for the final squash or merge commit.
- Add applicable user-visible changes under `CHANGELOG.md` -> `[Unreleased]`.
  Skip entries for changes without meaningful user impact.

## Commits

- Follow [Conventional Commits 1.0.0](https://www.conventionalcommits.org/en/v1.0.0/).
- Prefer no scope; include one only when it materially improves clarity. Write
  a short, imperative description. Example: `fix: preserve request ID`.
- Mark breaking changes with `!` and explain them in a `BREAKING CHANGE:`
  footer.
- Before committing, run `just qa` and `git diff --check`.

## Repository constraints

- Preserve the documented Go and Huma support lines and keep module versions
  tag-derived; do not add a separate version constant.
- Keep logging Zap-native. Do not add a global logger, OpenTelemetry, a cloud
  SDK, or application-specific logging wrappers to the package API.
- Do not log queries, bodies, credentials, cookies, arbitrary headers, or
  untrusted forwarded IPs.
- Treat exported APIs, structured log fields, defaults, and supported runtime
  versions as compatibility contracts.

## Public API and documentation

- Update applicable tests, README content, examples, Go documentation, and
  changelog entries when public behavior changes.
- Keep `CHANGELOG.md` in Keep a Changelog format with an `Unreleased` section,
  ISO-dated bracketed versions, applicable change categories, and comparison
  links.
- Keep examples minimal, runnable, and aligned with the documented API.
- Document breaking changes explicitly and provide migration guidance.

## Tests

- Use the repository's `$adversarial-testing` skill when creating, updating, or
  reviewing tests.
- Test observable behavior, boundaries, failure recovery, and forbidden side
  effects. Do not optimize for coverage numbers or mock interactions alone.
- Run `just mutation` when changing production logic or its tests. Add tests for
  meaningful `LIVED` mutants, not equivalent transformations.
- Keep mutation testing separate from `just fuzz`, which mutates parser inputs
  rather than production code.

## Workflow security

- Use full release tags for third-party GitHub Actions, for example
  `actions/checkout@v7.0.0`. Do not use commit SHAs, moving branches, or major
  version tags.
- `just qa` must run `actionlint` and `zizmor --offline .` in addition to the
  repository's language checks.
- Do not add standalone repository scripts, including under `.github`. Enforce
  repository policy through the existing native test suite and tooling.
- Keep `.github/zizmor.yml` aligned with the exact-tag policy and the
  one-day Dependabot cooldown.

## Releases

- Prepare releases from a same-repository source branch named
  `release/prepare-vX.Y.Z` through a pull request titled
  `chore: prepare vX.Y.Z` that targets `main`.
- Use the `release/` namespace only for release preparation branches.
- The conditional `Consumer image build` job on a release preparation pull
  request is a build-only packaging and integration diagnostic. It does not run
  the image, validate emitted logs, or approve a release.
- For local image-build diagnosis, run
  `just e2e-image observability-e2e-local:manual`. The Justfile prefers Podman
  and falls back to Docker.
- Keep `e2e/README.md` self-contained as the public consumer-image interface.
  Independent audits are optional and informational; they never approve or
  block publication.
- Update `CHANGELOG.md` and public documentation together. Go module versions
  come from tags; do not add a separate version constant.
- Run `just qa`, `just vuln`, and `git diff --check` before committing a release.
- Merge a green pull request to `main`, then manually tag the exact reviewed
  commit with `vX.Y.Z`. Creating that tag is the maintainer's release
  authorization.
- When drafting a stable GitHub Release, use **Generate release notes** and mark
  it as **Latest**. Edit the notes for accuracy and alignment with
  `CHANGELOG.md` before publishing.
- Never move an existing release tag or reuse a published module version.
- Verify the GitHub Release, Go module proxy, checksum database, and pkg.go.dev
  after publishing.
- Follow `RELEASE.md` for the complete maintainer workflow.
