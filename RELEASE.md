# Release Process

This document describes how to create and publish a new release for `codex-oauth-proxy`.

## Overview

A release consists of two main distribution channels:

1. **GitHub Releases (GoReleaser)**: Triggered automatically via GitHub Actions when a git tag is pushed. Builds cross-platform binaries (Darwin, Linux, Windows for `amd64`, `arm64`, `armv7`), generates `checksums.txt`, and creates GitHub Release assets.
2. **NPM Package (`npm/`)**: Published to the npm registry (`codex-oauth-proxy`). Its `postinstall.js` script downloads the corresponding prebuilt binary directly from the GitHub Release assets.
3. **Docker Image (Optional)**: Built and pushed to GitHub Container Registry (`ghcr.io/dvcrn/codex-oauth-proxy`) via `just docker-build`.

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

### 2. Bump the NPM Package Version

The npm package version in `npm/package.json` must match the release version so that `postinstall.js` downloads the correct binary from GitHub Releases:

1. Update `"version"` in [`npm/package.json`](npm/package.json):

   ```json
   {
     "name": "codex-oauth-proxy",
     "version": "1.0.2",
     ...
   }
   ```

2. Commit and push the version bump to `main`:

   ```bash
   git add npm/package.json
   git commit -m "chore: bump version to 1.0.2"
   git push origin main
   ```

### 3. Create and Push the Git Tag

Tags use standard semver (past releases use `1.0.0`, `1.0.1`):

```bash
git tag 1.0.2
git push origin 1.0.2
```

### 4. Wait for GitHub Actions (GoReleaser)

- Go to the **Actions** tab in GitHub and monitor the **Release** workflow triggered by the tag.
- GoReleaser will:
  - Compile binaries for all target architectures.
  - Package `.tar.gz` archives and create `checksums.txt`.
  - Publish the GitHub Release with an auto-generated changelog.
- **Important:** Wait until this workflow successfully finishes before publishing to npm, as `npm install` requires the binaries to already exist on GitHub Releases.

### 5. Publish to NPM

Once the GitHub Release is live with its assets:

```bash
mise run publish
# or directly:
cd npm && npm publish
```

### 6. (Optional) Publish Docker Container

If publishing a Docker image to GHCR:

```bash
just docker-build
```

---

## Local Snapshot Testing

To verify GoReleaser configuration locally without publishing:

```bash
MISE_ENV=development mise run goreleaser_snapshot
# or:
goreleaser release --snapshot --clean --config .goreleaser.yml
```
