#!/usr/bin/env bash
# Installs the external tools watchmydog depends on, on the Mac mini.
#
#   ffmpeg  - webcam capture (Homebrew)
#   go2rtc  - WebRTC media engine (pinned GitHub release, not in Homebrew)
#
# Idempotent: safe to re-run. Later increments add Tailscale and the launchd agent.
set -euo pipefail

GO2RTC_VERSION="v1.9.15"
GO2RTC_SHA256="d0cf858a71803dce2377423bc830639137ee96c335f88e9912e7f7898374266d" # go2rtc_mac_arm64.zip
BIN_DIR="${HOME}/.local/bin"

log() { printf '\033[1;34m==>\033[0m %s\n' "$*"; }

if ! command -v brew >/dev/null; then
  echo "Homebrew is required: https://brew.sh" >&2
  exit 1
fi

if command -v ffmpeg >/dev/null; then
  log "ffmpeg already installed: $(ffmpeg -version | head -1)"
else
  log "Installing ffmpeg"
  brew install ffmpeg
fi

mkdir -p "${BIN_DIR}"
if [[ -x "${BIN_DIR}/go2rtc" ]] && "${BIN_DIR}/go2rtc" --version | grep -q "${GO2RTC_VERSION#v}"; then
  log "go2rtc already installed: $("${BIN_DIR}/go2rtc" --version)"
else
  log "Installing go2rtc ${GO2RTC_VERSION}"
  tmp="$(mktemp -d)"
  trap 'rm -rf "${tmp}"' EXIT
  curl -fsSL -o "${tmp}/go2rtc.zip" \
    "https://github.com/AlexxIT/go2rtc/releases/download/${GO2RTC_VERSION}/go2rtc_mac_arm64.zip"
  echo "${GO2RTC_SHA256}  ${tmp}/go2rtc.zip" | shasum -a 256 -c -
  unzip -q "${tmp}/go2rtc.zip" -d "${tmp}"
  install -m 755 "${tmp}/go2rtc" "${BIN_DIR}/go2rtc"
  xattr -d com.apple.quarantine "${BIN_DIR}/go2rtc" 2>/dev/null || true
  log "go2rtc installed: $("${BIN_DIR}/go2rtc" --version)"
fi

# Native capture helper (ADR-0004). Needs Xcode Command Line Tools for swiftc.
REPO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
if command -v swiftc >/dev/null; then
  log "Building wmd-capture"
  (cd "${REPO_DIR}" && make install-capture)
else
  echo "swiftc not found: run 'xcode-select --install' then re-run this script" >&2
  exit 1
fi

case ":${PATH}:" in
  *":${BIN_DIR}:"*) ;;
  *) echo "NOTE: add ${BIN_DIR} to your PATH" ;;
esac
