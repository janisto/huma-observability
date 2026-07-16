# Repository instructions

## Documentation

- `README.md` is primarily for human users and contributors. Keep installation,
  usage, public API, and operational guidance there.
- Put instructions needed specifically by coding agents in `AGENTS.md`. When
  agent-specific guidance changes, update this file rather than adding it to
  `README.md`.

## Engineering changes

- Inspect the relevant implementation, callers, and existing tests before
  editing.
- Prefer the smallest safe change that solves the problem.
- Reuse existing patterns and utilities, refactoring them when needed, instead
  of creating parallel abstractions or adding dependencies.

## Public API and documentation

- Update applicable tests, README content, examples, type or API documentation,
  and changelog entries when public behavior changes.
- Keep `CHANGELOG.md` in Keep a Changelog format with an `Unreleased` section,
  ISO-dated bracketed versions, applicable change categories, and comparison
  links.
- Keep examples minimal, runnable, and aligned with the documented API.
- Treat exported APIs, structured log fields, defaults, and supported runtime
  versions as compatibility contracts.
- Document breaking changes explicitly and provide migration guidance when
  applicable.

## Pull requests

- Use `<type>[optional scope]: <description>` for the title. Prefer no scope;
  include one only when it materially improves clarity.
- Example: `feat: add response size field`.
- Use `feat`, `fix`, `docs`, `test`, `refactor`, `perf`, `build`, `ci`, `chore`,
  or `revert` as the type.
- Keep each pull request focused. In the body, explain why the change is needed,
  what changed, how it was validated, and any remaining risk.
- Before opening a pull request, add applicable user-visible changes under
  `CHANGELOG.md` → `[Unreleased]`. Skip entries for changes without meaningful
  user impact.
- Keep the title suitable for the final squash or merge commit.

## Commits

- Follow [Conventional Commits 1.0.0](https://www.conventionalcommits.org/en/v1.0.0/).
- Prefer no scope; include one only when it materially improves clarity. Write a
  short, imperative description.
- Example: `fix: preserve request ID`.
- Mark breaking changes with `!` and explain them in a `BREAKING CHANGE:` footer.
- Before committing, run `just qa` and `git diff --check`.
- Run `just vuln` when dependencies or the Go toolchain change.

## Tests

- Use the repository's `$adversarial-testing` skill when creating, updating, or
  reviewing tests.
- Test observable behavior, boundaries, failure recovery, and forbidden side
  effects. Do not optimize for coverage numbers or mock interactions alone.
- Run `just mutation` when changing production logic or its tests. Add tests for
  meaningful `LIVED` mutants, not equivalent transformations.
- Keep mutation testing separate from `just fuzz`, which mutates parser inputs
  rather than production code.

## Releases

- Prepare releases through a pull request titled
  `chore: prepare vX.Y.Z`.
- Update `CHANGELOG.md` and public documentation together. Go module versions
  come from tags; do not add a separate version constant.
- Run the complete commit checks, merge a green pull request to `main`, and
  release the exact reviewed commit with tag `vX.Y.Z`.
- When drafting a new GitHub Release, use **Generate release notes** and mark it
  as **Latest**. Edit the generated notes when needed for accuracy and alignment
  with `CHANGELOG.md` before publishing.
- Never move an existing release tag or reuse a published module version.
- Verify the GitHub release, Go module proxy, checksum database, and pkg.go.dev
  after publishing.
- Follow `RELEASE.md` for the complete maintainer workflow.
