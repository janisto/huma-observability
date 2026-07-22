# Release guide

This Go module is released by manually pushing a lightweight `vX.Y.Z` tag for
the exact reviewed commit on `main`, then publishing a GitHub Release for that
tag. There is no separate package upload: Go module versions are derived from
immutable Git tags, not from a package-owned version constant.

## Release preparation branch and consumer image

Every release preparation uses a same-repository source branch named
`release/prepare-vX.Y.Z`, targets `main`, and uses the pull request title
`chore: prepare vX.Y.Z`. The branch and title versions must agree with the
version being released. When this repository permits a prerelease, use its
exact reviewed prerelease suffix in both the branch and title. Use the
`release/` namespace only for this process.

The conditional `Consumer image build` CI job runs for a same-repository
release preparation pull request targeting `main`. It builds the
production-shaped consumer with
`just e2e-image observability-e2e-local:ci`.

The build verifies packaging and integration only. It does not run the image,
validate emitted logs, compare implementations, or approve a release. It is
not a required status check.

For local image diagnosis, run:

```bash
just e2e-image observability-e2e-local:manual
```

The recipe prefers Podman and falls back to Docker.

Optional independent tooling may exercise the public contract documented in
[e2e/README.md](e2e/README.md). Any audit result is informational only; it is
never a publication requirement and neither approves nor blocks publication.
The maintainer authorizes publication by manually pushing the exact reviewed
Go module tag and publishing its reviewed GitHub Release.

## Maintainer release guide

### 1. Prepare the release

Create `release/prepare-vX.Y.Z` from the current `main` branch and:

1. move the applicable entries under a dated `CHANGELOG.md` release heading,
   keep an empty `Unreleased` heading, and update the comparison links;
2. update public documentation for every changed API, structured field, or
   behavior;
3. confirm that `go.mod` still declares the intended `/v2` module path and the
   documented Go and Huma support lines; and
4. run the release checks:

```bash
just qa
just vuln
git diff --check
```

`just qa` includes formatting, lint, build, tests, race tests, actionlint, and
`zizmor --offline .`. `just vuln` runs `govulncheck ./...` separately.

Merge only after the required pull-request checks pass. Record the final merge
commit reported by the merged release-preparation pull request. Do not tag the
pre-merge branch head or a newer, unrelated `main` commit.

### 2. Create the lightweight tag

Fetch the public state and set `TARGET` explicitly to the exact final merge
commit reported by the merged release-preparation pull request. Replace the
placeholder before running the checks:

```bash
VERSION=vX.Y.Z
TARGET="<exact-merged-release-commit>"

git fetch origin main --tags
test "$(git cat-file -t "$TARGET")" = commit
git merge-base --is-ancestor "$TARGET" origin/main
test -z "$(git status --porcelain)"
```

Verify that neither the tag nor GitHub Release already exists. Then create the
lightweight tag at that exact commit, verify it, and push it:

```bash
git tag "$VERSION" "$TARGET"
test "$(git rev-parse "$VERSION^{commit}")" = "$TARGET"
git push origin "$VERSION"
```

Pushing the public Go module tag is the irreversible package-publication step.
Never move, delete, or reuse a published tag or version.

### 3. Publish the GitHub Release

Create a draft from the existing tag with generated notes:

```bash
gh release create "$VERSION" \
  --verify-tag \
  --title "$VERSION" \
  --generate-notes \
  --latest \
  --draft

gh release view "$VERSION" --web
```

Review the target tag, generated previous tag, merged pull requests,
contributors, full-changelog link, and user-visible notes. The notes must agree
with `CHANGELOG.md`. Publish the unchanged stable draft as Latest:

```bash
gh release edit "$VERSION" --draft=false --latest
```

For a prerelease, use `--prerelease --latest=false` instead of `--latest` when
creating the draft, and preserve those labels when publishing it.

### 4. Verify the public release

```bash
MODULE=github.com/janisto/huma-observability/v2
RELEASE_CACHE="$(mktemp -d)"
trap 'rm -rf "$RELEASE_CACHE"' EXIT

test "$(git ls-remote --exit-code origin "refs/tags/$VERSION" | cut -f1)" = "$TARGET"
GOMODCACHE="$RELEASE_CACHE" GOPROXY=https://proxy.golang.org \
  GONOPROXY=none GOSUMDB=sum.golang.org GONOSUMDB=none \
  go mod download "$MODULE@$VERSION"
GOMODCACHE="$RELEASE_CACHE" GOPROXY=https://proxy.golang.org \
  GONOPROXY=none GOSUMDB=sum.golang.org GONOSUMDB=none \
  go list -m "$MODULE@$VERSION"
gh release view "$VERSION"
```

The remote lightweight tag must still resolve to the reviewed `TARGET`, and the
module must resolve through the public proxy and checksum database from a fresh
module cache. Verify the exact version page at
`https://pkg.go.dev/github.com/janisto/huma-observability/v2@vX.Y.Z`. The Go
proxy, checksum database, GitHub Release, and pkg.go.dev may become available at
different times.

## Failure and recovery

- Before pushing the tag, fix the release preparation and rerun every release
  check on the new exact candidate commit.
- If the correct tag was pushed but GitHub Release creation or publication
  fails, retry the GitHub Release step for that same immutable tag.
- If a public tag contains a defect or points to the wrong commit, do not move,
  delete, or reuse it. Correct the problem in a new patch release.
- If the Go proxy, checksum database, or pkg.go.dev is delayed, wait for
  indexing. Do not retag a valid release.
