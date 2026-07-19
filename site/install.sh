#!/bin/sh

set -eu

repository="${AWP_REPOSITORY:-Manifestro/awp}"
requested_version="${AWP_VERSION:-latest}"
install_directory="${AWP_INSTALL_DIR:-${HOME}/.local/bin}"

fail() {
  printf 'awp installer: %s\n' "$1" >&2
  exit 1
}

command -v curl >/dev/null 2>&1 || fail "curl is required"
command -v tar >/dev/null 2>&1 || fail "tar is required"

case "$(uname -s)" in
  Darwin) operating_system="darwin" ;;
  Linux) operating_system="linux" ;;
  *) fail "unsupported operating system: $(uname -s)" ;;
esac

case "$(uname -m)" in
  x86_64|amd64) architecture="amd64" ;;
  arm64|aarch64) architecture="arm64" ;;
  *) fail "unsupported architecture: $(uname -m)" ;;
esac

if [ "$requested_version" = "latest" ]; then
  release_url="$(curl -LsSf -o /dev/null -w '%{url_effective}' "https://github.com/${repository}/releases/latest")"
  release_tag="${release_url##*/}"
else
  release_tag="$requested_version"
  case "$release_tag" in
    v*) ;;
    *) release_tag="v${release_tag}" ;;
  esac
fi

case "$release_tag" in
  v|v*[!0-9A-Za-z._-]*) fail "invalid release version: ${release_tag}" ;;
  v*) ;;
  *) fail "invalid release version: ${release_tag}" ;;
esac

version="${release_tag#v}"
archive="awp_${version}_${operating_system}_${architecture}.tar.gz"
checksums="awp_${version}_checksums.txt"
download_base="https://github.com/${repository}/releases/download/${release_tag}"

temporary_directory="$(mktemp -d 2>/dev/null || mktemp -d -t awp-install)"
trap 'rm -rf "$temporary_directory"' EXIT HUP INT TERM

printf 'Downloading AWP %s for %s/%s...\n' "$version" "$operating_system" "$architecture"
curl -LsSf "${download_base}/${archive}" -o "${temporary_directory}/${archive}"
curl -LsSf "${download_base}/${checksums}" -o "${temporary_directory}/${checksums}"

expected_checksum="$(awk -v archive="$archive" '$2 == archive { print $1 }' "${temporary_directory}/${checksums}")"
[ -n "$expected_checksum" ] || fail "release checksum is missing for ${archive}"

if command -v sha256sum >/dev/null 2>&1; then
  actual_checksum="$(sha256sum "${temporary_directory}/${archive}" | awk '{ print $1 }')"
elif command -v shasum >/dev/null 2>&1; then
  actual_checksum="$(shasum -a 256 "${temporary_directory}/${archive}" | awk '{ print $1 }')"
else
  fail "sha256sum or shasum is required"
fi

[ "$actual_checksum" = "$expected_checksum" ] || fail "checksum verification failed"

archive_contents="$(tar -tzf "${temporary_directory}/${archive}")"
[ "$archive_contents" = "awp" ] || fail "release archive contains unexpected files"
tar -xzf "${temporary_directory}/${archive}" -C "$temporary_directory" awp
[ -f "${temporary_directory}/awp" ] || fail "release archive does not contain the awp binary"

mkdir -p "$install_directory"
if command -v install >/dev/null 2>&1; then
  install -m 0755 "${temporary_directory}/awp" "${install_directory}/awp"
else
  cp "${temporary_directory}/awp" "${install_directory}/awp"
  chmod 0755 "${install_directory}/awp"
fi

printf 'AWP %s installed to %s/awp\n' "$version" "$install_directory"
case ":${PATH}:" in
  *":${install_directory}:"*) ;;
  *) printf 'Add %s to PATH to run awp from any directory.\n' "$install_directory" ;;
esac
