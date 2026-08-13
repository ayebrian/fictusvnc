#!/bin/bash
# Bundle the binaries built by build.sh into per-platform release archives.
# Each archive holds the binary at its root plus config.toml and the default
# image, so it unpacks into a ready-to-run directory. tarballs for *nix,
# zips for Windows. Missing binaries are skipped, so this works on a partial
# build matrix too.
set -euo pipefail

NAME="fictusvnc"
OUTDIR="build"
DISTDIR="dist"

rm -rf "$DISTDIR"
mkdir -p "$DISTDIR"

stage() {
  # stage <workdir> <binary-name-in-archive> <source-binary>
  local workdir="$1" binname="$2" src="$3"
  mkdir -p "$workdir/images"
  cp "$src" "$workdir/$binname"
  chmod +x "$workdir/$binname"
  cp config.example.toml "$workdir/config.toml"
  cp images/default.png "$workdir/images/"
}

# --- *nix tarballs ---
for os in linux darwin; do
  for arch in amd64 arm64 386; do
    bin="$OUTDIR/${NAME}-${os}-${arch}"
    [ -f "$bin" ] || continue
    workdir="$DISTDIR/${os}-${arch}"
    stage "$workdir" "$NAME" "$bin"
    tar -C "$workdir" -czf "$OUTDIR/${NAME}-${os}-${arch}.tar.gz" .
    echo "📦 $OUTDIR/${NAME}-${os}-${arch}.tar.gz"
  done
done

# --- Windows zips ---
for arch in amd64 386; do
  bin="$OUTDIR/${NAME}-windows-${arch}.exe"
  [ -f "$bin" ] || continue
  workdir="$DISTDIR/windows-${arch}"
  stage "$workdir" "${NAME}.exe" "$bin"
  ( cd "$workdir" && zip -qr "../../$OUTDIR/${NAME}-windows-${arch}.zip" . )
  echo "📦 $OUTDIR/${NAME}-windows-${arch}.zip"
done

echo "✅ Packaging complete."
