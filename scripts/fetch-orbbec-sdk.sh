#!/usr/bin/env bash
# Fetch Orbbec SDK v1.10.16 for building the depth helper.
# SDK binaries are cached under .sdk/ and intentionally not committed.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
VERSION="1.10.16"
BASE="https://github.com/orbbec/OrbbecSDK/releases/download/v${VERSION}"
DEST_ROOT="${ROOT}/.sdk/orbbec/v${VERSION}"
CACHE="${ROOT}/.sdk/cache"

asset_for() {
  case "$1" in
    darwin)       echo "OrbbecSDK_C_C++_v1.10.16_20241021_c0329e3_macos_arm64_x86.zip" ;;
    linux_x64)    echo "OrbbecSDK_C_C++_v1.10.16_20241021_5113dad_linux_x64_release.zip" ;;
    linux_arm64)  echo "OrbbecSDK_C_C++_v1.10.16_20241021_5113dad_linux_arm64_release.zip" ;;
    linux_arm32)  echo "OrbbecSDK_C_C++_v1.10.16_20241021_5113dad_linux_arm32_release.zip" ;;
    win_x64)      echo "OrbbecSDK_C_C++_v1.10.16_20241021_5113dad11_win_x64_release.zip" ;;
    win_x86)      echo "OrbbecSDK_C_C++_v1.10.16_20241021_5113dad11_win_x86_release.zip" ;;
    *) return 1 ;;
  esac
}

current_platform() {
  case "$(uname -s)" in
    Darwin) echo darwin ;;
    Linux)
      case "$(uname -m)" in
        x86_64|amd64) echo linux_x64 ;;
        aarch64|arm64) echo linux_arm64 ;;
        armv7l|armv6l|arm) echo linux_arm32 ;;
        *) echo "unsupported Linux arch: $(uname -m)" >&2; return 1 ;;
      esac ;;
    MINGW*|MSYS*|CYGWIN*) echo win_x64 ;;
    *) echo "unsupported OS: $(uname -s)" >&2; return 1 ;;
  esac
}

extract_zip() {
  local zip="$1" out="$2"
  mkdir -p "$out"
  if command -v unzip >/dev/null 2>&1; then
    unzip -q -o "$zip" -d "$out"
  elif command -v python3 >/dev/null 2>&1; then
    python3 -m zipfile -e "$zip" "$out"
  else
    echo "need unzip or python3 to extract $zip" >&2
    return 1
  fi
}

fetch_one() {
  local platform="$1" force="${2:-false}"
  local asset dest zip tmp sdkdir
  asset="$(asset_for "$platform")" || { echo "unknown platform: $platform" >&2; return 1; }
  dest="${DEST_ROOT}/${platform}"
  if [[ "$force" != "true" && -d "$dest/include" && -d "$dest/lib" ]]; then
    echo "orbbec ${VERSION} ${platform}: cached at ${dest}"
    return 0
  fi

  mkdir -p "$CACHE" "$dest"
  zip="${CACHE}/${asset}"
  if [[ "$force" == "true" || ! -s "$zip" ]]; then
    echo "downloading ${asset}"
    curl -fL --retry 3 --progress-bar -o "${zip}.part" "${BASE}/${asset}"
    mv "${zip}.part" "$zip"
  fi

  tmp="$(mktemp -d)"
  extract_zip "$zip" "$tmp"
  sdkdir="$(find "$tmp" -maxdepth 4 -type d -name SDK | head -n 1)"
  if [[ -z "$sdkdir" ]]; then
    rm -rf "$tmp"
    echo "SDK directory not found in ${asset}" >&2
    return 1
  fi
  rm -rf "$dest"
  mkdir -p "$dest"
  cp -R "${sdkdir}/." "$dest/"
  rm -rf "$tmp"
  echo "orbbec ${VERSION} ${platform}: ${dest}"
}

main() {
  local force=false platforms=()
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --all) platforms=(darwin linux_x64 linux_arm64 linux_arm32 win_x64 win_x86) ;;
      --platform) shift; platforms+=("${1:?--platform needs a value}") ;;
      --force) force=true ;;
      -h|--help)
        echo "usage: $0 [--all] [--platform darwin|linux_x64|linux_arm64|linux_arm32|win_x64|win_x86] [--force]"
        exit 0 ;;
      *) echo "unknown argument: $1" >&2; exit 2 ;;
    esac
    shift
  done
  if [[ ${#platforms[@]} -eq 0 ]]; then
    platforms=("$(current_platform)")
  fi
  for p in "${platforms[@]}"; do
    fetch_one "$p" "$force"
  done
}

main "$@"
