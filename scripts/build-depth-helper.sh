#!/usr/bin/env bash
# Build the Orbbec depth helper and stage it (with SDK runtime files) in bin/.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
VERSION="1.10.16"

case "$(uname -s)" in
  Darwin) PLATFORM=darwin ;;
  Linux)
    case "$(uname -m)" in
      x86_64|amd64) PLATFORM=linux_x64 ;;
      aarch64|arm64) PLATFORM=linux_arm64 ;;
      armv7l|armv6l|arm) PLATFORM=linux_arm32 ;;
      *) echo "unsupported Linux arch: $(uname -m)" >&2; exit 1 ;;
    esac ;;
  MINGW*|MSYS*|CYGWIN*) PLATFORM=win_x64 ;;
  *) echo "unsupported OS: $(uname -s)" >&2; exit 1 ;;
esac

if [[ $# -gt 0 && "$1" != -* ]]; then
  PLATFORM="$1"
  shift
fi

SKIP_SDK=false
if [[ "${1:-}" == "--skip-sdk" ]]; then SKIP_SDK=true; fi

SDK="${ROOT}/.sdk/orbbec/v${VERSION}/${PLATFORM}"
if [[ "$SKIP_SDK" != "true" ]]; then
  "${ROOT}/scripts/fetch-orbbec-sdk.sh" --platform "$PLATFORM"
fi
if [[ ! -d "$SDK/include" || ! -d "$SDK/lib" ]]; then
  echo "Orbbec SDK not found at $SDK" >&2
  exit 1
fi

BUILD="${ROOT}/.sdk/build/orbbec-depth/${PLATFORM}"
rm -rf "$BUILD"
mkdir -p "$BUILD" "${ROOT}/bin"

CMAKE_ARGS=(-S "${ROOT}/helper/orbbec-depth" -B "$BUILD" -DORBBEC_SDK_DIR="$SDK" -DCMAKE_BUILD_TYPE=Release)
if [[ "$PLATFORM" == win_* ]] && command -v cygpath >/dev/null 2>&1; then
  CMAKE_ARGS=(-S "$(cygpath -w "${ROOT}/helper/orbbec-depth")" -B "$(cygpath -w "$BUILD")" \
    -DORBBEC_SDK_DIR="$(cygpath -w "$SDK")" -DCMAKE_BUILD_TYPE=Release)
fi
if [[ "$PLATFORM" == "darwin" ]]; then
  CMAKE_ARGS+=(-DCMAKE_OSX_ARCHITECTURES="arm64;x86_64")
fi

cmake "${CMAKE_ARGS[@]}"
BUILD_ARG="$BUILD"
if [[ "$PLATFORM" == win_* ]] && command -v cygpath >/dev/null 2>&1; then
  BUILD_ARG="$(cygpath -w "$BUILD")"
fi
cmake --build "$BUILD_ARG" --config Release -j 8

EXE_NAME="orbbec-depth"
[[ "$PLATFORM" == win_* ]] && EXE_NAME="orbbec-depth.exe"
BIN="$(find "$BUILD" -type f -name "$EXE_NAME" | head -n 1)"
if [[ -z "$BIN" ]]; then
  echo "helper binary not found under $BUILD" >&2
  exit 1
fi
# Install atomically: overwriting a binary that a process has mapped can leave
# the path in a state where every later exec blocks.
cp "$BIN" "${ROOT}/bin/${EXE_NAME}.tmp"
mv -f "${ROOT}/bin/${EXE_NAME}.tmp" "${ROOT}/bin/${EXE_NAME}"

case "$PLATFORM" in
  darwin)
    cp -a "$SDK"/lib/*.dylib "${ROOT}/bin/"
    cp "$SDK/config/OrbbecSDKConfig_v1.0.xml" "${ROOT}/bin/"
    ;;
  linux_*)
    cp -a "$SDK"/lib/*.so* "${ROOT}/bin/"
    cp "$SDK/config/OrbbecSDKConfig_v1.0.xml" "${ROOT}/bin/"
    ;;
  win_*)
    cp "$SDK"/lib/*.dll "${ROOT}/bin/"
    cp "$SDK/config/OrbbecSDKConfig_v1.0.xml" "${ROOT}/bin/"
    ;;
esac

echo "staged helper: ${ROOT}/bin/${EXE_NAME}"
if [[ "$PLATFORM" == "darwin" ]]; then
  otool -L "${ROOT}/bin/${EXE_NAME}" | head -5
  lipo -info "${ROOT}/bin/${EXE_NAME}" || true
fi
