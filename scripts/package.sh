#!/usr/bin/env bash
# Create a self-contained source + Linux binary release without runtime secrets.
set -euo pipefail

repo_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
cd "$repo_dir"

release_arch=${GOARCH:-amd64}
case "$release_arch" in
  amd64|arm64) ;;
  *) printf 'Unsupported GOARCH: %s\n' "$release_arch" >&2; exit 1 ;;
esac
release_version=${VERSION:-$(date -u +%Y%m%dT%H%M%SZ)}
release_revision=$(git rev-parse HEAD 2>/dev/null || printf 'unknown')
if [[ ! "$release_version" =~ ^[a-zA-Z0-9][a-zA-Z0-9._-]*$ ]]; then
  printf 'VERSION must be a safe filename component.\n' >&2
  exit 1
fi

export CGO_ENABLED=0 GOOS=linux GOARCH="$release_arch"
# Do not add macOS resource-fork metadata to archives deployed on Linux.
export COPYFILE_DISABLE=1
export GOCACHE=${GOCACHE:-"$repo_dir/.cache/go/build"}
export GOMODCACHE=${GOMODCACHE:-"$repo_dir/.cache/go/pkg/mod"}
mkdir -p "$GOCACHE" "$GOMODCACHE" "$repo_dir/dist"
stage_dir=$(mktemp -d "${TMPDIR:-/tmp}/tunnelapanel-package.XXXXXX")
trap 'rm -rf -- "$stage_dir"' EXIT
mkdir -p "$stage_dir/dist"

go build -trimpath -buildvcs=false -ldflags="-s -w -X main.version=$release_version" -o "$stage_dir/dist/panel" ./cmd/panel
go build -trimpath -buildvcs=false -ldflags='-s -w' -o "$stage_dir/dist/endpoint-agent" ./cmd/endpoint-agent

# Explicit allowlist. Never archive .git, runtime/, .env, .cache/, or old releases.
source_paths=()
for source_path in cmd internal deploy scripts docs go.mod go.sum README.md README.ru.md Makefile; do
  if [[ -e "$source_path" ]]; then
    source_paths+=("$source_path")
  fi
done
tar --no-xattrs --exclude='.env' --exclude='*.local' --exclude='__pycache__' \
  -cf - "${source_paths[@]}" | tar -xf - -C "$stage_dir"
printf 'version=%s\nos=linux\narch=%s\ntrusttunnel=1.1.0\nsource_revision=%s\n' \
  "$release_version" "$release_arch" "$release_revision" > "$stage_dir/RELEASE"

release_name="tunnelapanel-${release_version}-linux-${release_arch}.tar.gz"
release_path="$repo_dir/dist/$release_name"
tar --no-xattrs -czf "$release_path" -C "$stage_dir" .
if command -v sha256sum >/dev/null 2>&1; then
  (cd "$repo_dir/dist" && sha256sum "$release_name" > "$release_name.sha256")
else
  (cd "$repo_dir/dist" && shasum -a 256 "$release_name" > "$release_name.sha256")
fi
printf 'Release: %s\nChecksum: %s.sha256\n' "$release_path" "$release_path"
