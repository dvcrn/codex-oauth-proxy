# Release Process

This document describes how to create and publish a new release for `codex-oauth-proxy`.

## Overview

A release consists of three distribution channels:

1. **GitHub Releases (GoReleaser)**: Triggered automatically via GitHub Actions when a git tag is pushed. Builds cross-platform binaries (Darwin, Linux, Windows for `amd64`, `arm64`, `armv7`), generates `checksums.txt`, and creates GitHub Release assets.
2. **NPM Package (`npm/`)**: Published to the npm registry (`codex-oauth-proxy`) by the `npm` job in the **Release** workflow after GoReleaser finishes, using npm trusted publishing. The version comes from the tag. Its `postinstall.js` script downloads the corresponding prebuilt binary directly from the GitHub Release assets.
3. **Docker Image**: The **Container image** GitHub Actions workflow automatically builds and publishes `linux/amd64` and `linux/arm64` images to GitHub Container Registry (`ghcr.io/dvcrn/codex-oauth-proxy`) on pushes to `main` and on all Git tag pushes.

---

## Step-by-Step Release Workflow

### 1. Pre-Release Verification

Ensure all tests pass and formatting/build is clean:

```bash
# Run tests
just test

# Verify format and compilation
just build
```

### 2. Create and Push the Git Tag

Tags use standard semver (past releases use `1.0.0`, `1.0.1`):

```bash
git tag 1.0.2
git push origin 1.0.2
```

### 3. Verify the Release Workflow

The tag starts the **Release** workflow. GoReleaser builds the binaries, archives, and `checksums.txt` and publishes the GitHub Release. The `npm` job then sets `npm/package.json` to the tag version and publishes the wrapper. Do not run `npm publish` by hand for a version you are about to tag, or the CI publish fails.

```bash
gh run list --workflow release.yml --limit 1
npm view codex-oauth-proxy@1.0.2 dist.attestations --json
```

### 4. Verify Docker Publishing

The Git tag push also starts the **Container image** workflow independently of GoReleaser. It builds and smoke tests the container before publishing the multi-platform image. No manual Docker publishing step or registry secret is required: the publishing job uses `GITHUB_TOKEN` with `packages: write` permission.

For tag `1.0.2`, verify the published manifest:

```bash
docker buildx imagetools inspect ghcr.io/dvcrn/codex-oauth-proxy:1.0.2
```

See the [image tag table](README.md#docker-images) for the tags each push publishes.

For a local image without publishing, run `just docker-build`. This builds and loads `codex-oauth-proxy:local` into the local Docker image store.

For the first publication, a repository maintainer may need to make the GHCR package public in its package settings so unauthenticated users can pull it. If the package already exists, ensure this repository has Actions access to it.

---

## Local Snapshot Testing

To verify GoReleaser configuration locally without publishing:

```bash
MISE_ENV=development mise run goreleaser_snapshot
# or:
goreleaser release --snapshot --clean --config .goreleaser.yml
```
