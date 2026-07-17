# Release guide

This Go module is released by tagging the exact reviewed commit on `main` and
publishing a GitHub Release. There is no separate package upload. Release tags
are lightweight and immutable.

## 1. Prepare the release

1. Open a pull request titled `chore: prepare vX.Y.Z`.
2. Add the version, release date, and user-visible changes to `CHANGELOG.md`.
3. Update public documentation for any changed API or behavior.
4. Run `just qa`, `just vuln`, and `git diff --check`.
5. Merge the green pull request to `main`.

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
MODULE=github.com/janisto/huma-observability
GOPROXY=https://proxy.golang.org GOSUMDB=sum.golang.org \
  go list -m "$MODULE@$VERSION"
gh release view "$VERSION"
```

Verify the exact version page at
`https://pkg.go.dev/github.com/janisto/huma-observability@vX.Y.Z`. The Go proxy,
checksum database, and pkg.go.dev may become available at different times. Wait
for indexing; do not retag a valid release.
