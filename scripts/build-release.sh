#!/usr/bin/env bash
# Build separate Linux control-plane and node-agent archives without installing.
set -euo pipefail

project_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
component=all
target_arch=native
output_dir="$project_root/.local/releases"
release_version=dev

usage() {
  cat <<'HELP'
Usage: scripts/build-release.sh [options]
  --component all|center|node  Packages to build (default: all)
  --arch native|amd64|arm64    Target Linux architecture (default: native)
  --out DIR                   Output directory (default: .local/releases)
  --version NAME              Archive version label (default: dev)
  -h, --help                  Show this help

The node agent is statically cross-compiled for Linux. The center uses SQLite
CGO and must be built on Linux of the target architecture, with a C compiler.
This script never installs services or downloads a proxy kernel.
HELP
}
fail() { printf 'build-release: %s\n' "$*" >&2; exit 1; }
require_value() {
  if [ "$#" -lt 2 ] || [ -z "$2" ]; then fail "missing value for $1"; fi
}
while [ "$#" -gt 0 ]; do
  case "$1" in
    --component) require_value "$@"; component="$2"; shift 2 ;;
    --arch) require_value "$@"; target_arch="$2"; shift 2 ;;
    --out) require_value "$@"; output_dir="$2"; shift 2 ;;
    --version) require_value "$@"; release_version="$2"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) fail "unknown argument: $1" ;;
  esac
done
case "$component" in all|center|node) ;; *) fail "--component must be all, center or node" ;; esac
case "$release_version" in
  ''|*[!a-zA-Z0-9._-]*|.*|-*|_*) fail "--version must begin with a letter or number and contain only letters, numbers, dot, underscore or hyphen" ;;
esac
command -v go >/dev/null 2>&1 || fail "Go 1.25 or newer is required"
command -v tar >/dev/null 2>&1 || fail "tar is required"
host_os="$(go env GOHOSTOS)"
host_arch="$(go env GOHOSTARCH)"
if [ "$target_arch" = native ]; then target_arch="$host_arch"; fi
case "$target_arch" in amd64|arm64) ;; *) fail "--arch must be native, amd64 or arm64" ;; esac
if [ "$component" = all ] || [ "$component" = center ]; then
  if [ "$host_os" != linux ] || [ "$host_arch" != "$target_arch" ]; then
    fail "center requires native Linux $target_arch (current host: $host_os/$host_arch) because SQLite uses CGO; build on the target architecture or use the repository Docker image. Use --component node for cross-compilation."
  fi
  command -v npm >/dev/null 2>&1 || fail "npm and Node.js 22.12+ are required for the center web UI"
fi
if command -v sha256sum >/dev/null 2>&1; then
  checksum() { sha256sum "$1"; }
elif command -v shasum >/dev/null 2>&1; then
  checksum() { shasum -a 256 "$1"; }
else
  fail "sha256sum or shasum is required"
fi

mkdir -p "$output_dir"
output_dir="$(cd "$output_dir" && pwd)"
staging_dir="$(mktemp -d "$output_dir/.nicecloud-build.XXXXXX")"
cleanup() { rm -rf -- "$staging_dir"; }
trap cleanup EXIT
umask 022

package_component() {
  local role="$1"
  local archive_name="nicecloud-${role}-${release_version}-linux-${target_arch}"
  local package_dir="$staging_dir/$archive_name"
  mkdir -p "$package_dir/bin" "$package_dir/deploy/center" "$package_dir/deploy/node" "$package_dir/node-agent"
  cp "$project_root/deploy/README.md" "$package_dir/deploy/README.md"
  cp "$project_root/node-agent/README.md" "$package_dir/node-agent/README.md"
  cp "$project_root/node-agent/config.example.json" "$package_dir/node-agent/config.example.json"
  cp "$project_root/TRAFFIC_PROTOCOL.md" "$package_dir/TRAFFIC_PROTOCOL.md"
  cat > "$package_dir/README.md" <<DOC
# NiceCloud $role $release_version (Linux $target_arch)

This archive contains the $role service and deployment templates.
See [the deployment guide](deploy/README.md) for reviewed installation steps.
The control plane and node agent are distributed in separate archives.
No proxy kernel, real token, administrator password, or certificate is included.
Build commands in the guide are run from the source repository.
DOC
  cp "$project_root/deploy/center/nicecloud-center.service" "$project_root/deploy/center/center.env.example" "$package_dir/deploy/center/"
  cp "$project_root/deploy/node/nicecloud-node.service" "$project_root/deploy/node/config.example.json" "$package_dir/deploy/node/"
  cp "$project_root/deploy/nginx.conf.example" "$package_dir/deploy/nginx.conf.example"
  if [ "$role" = center ]; then
    if [ ! -d "$project_root/web/node_modules" ]; then npm --prefix "$project_root/web" ci; fi
    npm --prefix "$project_root/web" run build
    (cd "$project_root/backend" && GOOS=linux GOARCH="$target_arch" CGO_ENABLED=1 go build -trimpath -ldflags='-s -w' -o "$package_dir/bin/cloud" .)
    cp -R "$project_root/web/dist" "$package_dir/web"
  else
    (cd "$project_root/node-agent" && GOOS=linux GOARCH="$target_arch" CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o "$package_dir/bin/node-agent" .)
  fi
  cat > "$package_dir/RELEASE.txt" <<META
component=$role
version=$release_version
target=linux/$target_arch
proxy_kernel_included=false
META
  COPYFILE_DISABLE=1 tar -C "$staging_dir" -czf "$output_dir/$archive_name.tar.gz" "$archive_name"
  (cd "$output_dir" && checksum "$archive_name.tar.gz" > "$archive_name.tar.gz.sha256")
  printf 'Created %s\n' "$output_dir/$archive_name.tar.gz"
}

if [ "$component" = all ] || [ "$component" = center ]; then package_component center; fi
if [ "$component" = all ] || [ "$component" = node ]; then package_component node; fi
