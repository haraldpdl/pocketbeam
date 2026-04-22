#!/usr/bin/env bash
# Cut a pocketbeam release: tag the current HEAD, build the ARM .app with
# the tag baked in via ldflags, push the tag, and create a Gitea release
# with the binary attached. Run from inside the pocketbeam-dev CT (125)
# where the PocketBook cross-compile toolchain lives.
#
# Usage: scripts/release.sh vX.Y.Z "one-line release summary"
#
# Assumes:
#   - CWD is the repo root
#   - Go env is preconfigured for PocketBook ARM (GOOS/GOARCH/CC)
#   - $GITEA_TOKEN is set, or ~/.gitea-token is readable
#
# Exits non-zero on any failure; no partial release state is pushed.

set -euo pipefail

if [[ $# -lt 2 ]]; then
    echo "usage: $0 vX.Y.Z 'release summary'" >&2
    exit 2
fi

TAG="$1"
SUMMARY="$2"
REPO="haraldpdl/pocketbeam"
GITEA="http://gitea.example.internal:3000"

if [[ ! "$TAG" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
    echo "tag '$TAG' does not look like vX.Y.Z" >&2
    exit 2
fi

# Refuse to release from a dirty tree. git describe --dirty would bake
# "-dirty" into the version string, which is never what we want for a tag.
if ! git diff --quiet || ! git diff --cached --quiet; then
    echo "working tree has uncommitted changes; commit or stash first" >&2
    exit 2
fi

# Refuse to re-tag. Running the script twice for the same version would
# otherwise try to overwrite and fail in confusing ways halfway through.
if git rev-parse "$TAG" >/dev/null 2>&1; then
    echo "tag $TAG already exists locally" >&2
    exit 2
fi

TOKEN="${GITEA_TOKEN:-}"
if [[ -z "$TOKEN" && -r ~/.gitea-token ]]; then
    TOKEN="$(cat ~/.gitea-token)"
fi
if [[ -z "$TOKEN" ]]; then
    echo "no Gitea token: set \$GITEA_TOKEN or populate ~/.gitea-token" >&2
    exit 2
fi

echo "==> tag $TAG"
git tag -a "$TAG" -m "$SUMMARY"

echo "==> build pocketbeam.app"
mkdir -p dist
go build -ldflags="-s -w -X main.version=$TAG" -o dist/pocketbeam.app .

# Verify the binary is ARM; bare go build on a laptop would silently
# produce an x86 binary which the PocketBook can't load.
MACHINE=$(od -An -tx1 -N2 -j18 dist/pocketbeam.app | tr -d ' ')
if [[ "$MACHINE" != "2800" ]]; then
    echo "built binary is not ARM (e_machine=$MACHINE, want 2800)" >&2
    git tag -d "$TAG"
    exit 1
fi

SHA=$(sha256sum dist/pocketbeam.app | awk '{print $1}')

# Compress a copy of the binary. The updater (v0.4.2+) prefers the .gz
# asset and decompresses on the device, so every update pulls ~half the
# bytes over Wi-Fi. Older installs still see pocketbeam.app and take the
# uncompressed path. -9 picks the best ratio; -k keeps the raw binary
# next to the .gz so both can be uploaded.
echo "==> compress pocketbeam.app.gz"
gzip -9 -k -f dist/pocketbeam.app
if [[ ! -s dist/pocketbeam.app.gz ]]; then
    echo "gzip produced an empty archive" >&2
    git tag -d "$TAG"
    exit 1
fi
RAW_SIZE=$(stat -c %s dist/pocketbeam.app)
GZ_SIZE=$(stat -c %s dist/pocketbeam.app.gz)

BODY="${SUMMARY}

sha256: ${SHA} pocketbeam.app
Compressed download: ${GZ_SIZE} B (raw ${RAW_SIZE} B)"

echo "==> push tag"
git push origin "$TAG"

echo "==> create Gitea release"
RELEASE_JSON=$(curl -fsS -X POST \
    -H "Authorization: token $TOKEN" \
    -H "Content-Type: application/json" \
    -d "$(jq -nc --arg tag "$TAG" --arg name "pocketbeam $TAG" --arg body "$BODY" \
        '{tag_name: $tag, name: $name, body: $body, draft: false, prerelease: false}')" \
    "$GITEA/api/v1/repos/$REPO/releases")

RELEASE_ID=$(printf '%s' "$RELEASE_JSON" | jq -r '.id')
if [[ -z "$RELEASE_ID" || "$RELEASE_ID" == "null" ]]; then
    echo "release create failed: $RELEASE_JSON" >&2
    exit 1
fi

echo "==> upload pocketbeam.app"
curl -fsS -X POST \
    -H "Authorization: token $TOKEN" \
    -F "attachment=@dist/pocketbeam.app" \
    "$GITEA/api/v1/repos/$REPO/releases/$RELEASE_ID/assets?name=pocketbeam.app" \
    >/dev/null

echo "==> upload pocketbeam.app.gz"
curl -fsS -X POST \
    -H "Authorization: token $TOKEN" \
    -F "attachment=@dist/pocketbeam.app.gz" \
    "$GITEA/api/v1/repos/$REPO/releases/$RELEASE_ID/assets?name=pocketbeam.app.gz" \
    >/dev/null

echo "done: $GITEA/$REPO/releases/tag/$TAG"
