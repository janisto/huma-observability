# Release guide

This Go module is released by tagging the exact reviewed commit on `main` and
publishing a GitHub Release. There is no separate package upload. Release tags
are lightweight and immutable.

## Release preparation branch and E2E gates

Every release preparation uses a same-repository source branch named
`release/prepare-vX.Y.Z`, targets `main`, and uses the pull request title
`chore: prepare vX.Y.Z`. The branch and title versions must agree with the
version being released. When this repository permits a prerelease, use its
exact reviewed prerelease suffix in both the branch and title. The `release/`
namespace is reserved for this process;
a release branch from a fork, a different target, or a malformed name fails
the gate.

The CI workflow separates two checks:

- `E2E consumer image` runs only for a valid release preparation pull request
  and builds the production-shaped consumer with
  `just e2e-image observability-e2e-local:ci`.
- `Release E2E gate` reports on every pull request. It passes as not applicable
  for an ordinary branch, but for `release/` it succeeds only when the branch,
  title, repository, target, and image-build result are valid.

Require `Release E2E gate` in the `main` ruleset; do not require the conditional
`E2E consumer image` job. Land the workflow on `main` and let the gate report
once before adding its exact check name to the ruleset. Preserve every existing
required check. For local image diagnosis, run:

```bash
just e2e-image observability-e2e-local:manual
```

The sibling image job proves only that the consumer image builds. It does not
verify actual log output. After the release preparation merges, stop before
creating a tag or publishing a GitHub Release. Update this sibling's revision
in the central [`janisto/observability`](https://github.com/janisto/observability)
repository to the final merged `main` commit, then follow its `RELEASE.md` and
run the complete `just e2e --authoritative` matrix on Docker Engine from clean,
pinned checkouts. Tag and publish only after that central result passes.

## 1. Prepare the release

1. Create `release/prepare-vX.Y.Z` from current `main`, then open a pull
   request titled `chore: prepare vX.Y.Z` that targets `main`.
2. Add the version, release date, and user-visible changes to `CHANGELOG.md`.
3. Update public documentation for any changed API or behavior.
4. Run `just qa`, `just vuln`, and `git diff --check`.
5. Merge the green pull request only after `Release E2E gate` passes. Complete
   the central authoritative gate described above before continuing to
   tagging.

## 2. Tag the reviewed commit

Fetch `origin/main` and verify that the tag and GitHub Release do not already
exist. Set `TARGET` to the exact reviewed commit on `origin/main`.

```bash
VERSION=vX.Y.Z
git fetch origin main --tags
TARGET="$(git rev-parse origin/main)"

git tag "$VERSION" "$TARGET"
git push origin "$VERSION"
```

Pushing a public Go module tag is irreversible. Never move or reuse it.

## 3. Publish the GitHub Release

Create a draft with generated notes, review it against `CHANGELOG.md`, and then
publish the unchanged release as Latest.

```bash
gh release create "$VERSION" \
  --verify-tag \
  --title "$VERSION" \
  --generate-notes \
  --latest \
  --draft

gh release view "$VERSION" --web
gh release edit "$VERSION" --draft=false --latest
```

## 4. Verify publication

```bash
MODULE=github.com/janisto/huma-observability/v2
GOPROXY=https://proxy.golang.org GOSUMDB=sum.golang.org \
  go list -m "$MODULE@$VERSION"
gh release view "$VERSION"
```

Verify the exact version page at
`https://pkg.go.dev/github.com/janisto/huma-observability/v2@vX.Y.Z`. The Go proxy,
checksum database, and pkg.go.dev may become available at different times. Wait
for indexing; do not retag a valid release.
