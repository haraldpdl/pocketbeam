#!/usr/bin/env bash
# Packages the built ARM binary into the release assets and writes the
# release notes. It lives in a script rather than inline in release.yml so
# `make test` can exercise it: a broken SHA256SUMS or a notes digest the
# on-device updater cannot parse would otherwise only show up in a release.
#
# Usage: package-release.sh <dist-dir> <version>
set -euo pipefail

dist=${1:?usage: package-release.sh <dist-dir> <version>}
version=${2:?usage: package-release.sh <dist-dir> <version>}

cd "$dist"

# The zip is for people sideloading over USB (GitHub refuses a bare .app
# asset, and .gz is awkward on Windows and phones), the gzip is what the
# on-device updater fetches. The zip is deliberately not named
# pocketbeam.app.zip: Windows Explorer extracts into a folder named after
# the archive, which would hide pocketbeam.app inside a folder of the same
# name and read as "already copied".
gzip -9 -k -n pocketbeam.app
zip -q -X pocketbeam-app.zip pocketbeam.app

# Only the assets the release publishes are listed, so that
# `sha256sum -c SHA256SUMS` over a downloaded release exits clean. The raw
# binary used to be listed too, and since GitHub refuses to host it under
# that name, every verification reported it as missing.
sha256sum pocketbeam.app.gz pocketbeam-app.zip > SHA256SUMS

# The digest the device verifies is the one of the decompressed binary, so
# the notes keep it discoverable. `sha256: <hex>` is the form updater.go
# parses, so anything explaining it stays on its own line.
raw=$(sha256sum pocketbeam.app | cut -d' ' -f1)
{
  echo "pocketbeam ${version}"
  echo
  echo "Digest of pocketbeam.app, the binary inside pocketbeam-app.zip:"
  echo "sha256: ${raw}"
  echo
  # A newer release reads an older sync state file, not the other way
  # round, so a rollback needs it out of the way.
  echo "Going back to an older version? Delete"
  echo "system/config/pocketbeam.db over USB first; the next sync"
  echo "re-tracks the books already on the device."
} > notes.md
