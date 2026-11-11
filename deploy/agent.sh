#!/usr/bin/env bash
# Manage the watchmydog launchd agent (ADR-0006).
#
#   deploy/agent.sh install    build, install the binary, (re)load the agent
#   deploy/agent.sh uninstall  stop and remove the agent
#   deploy/agent.sh restart    restart the running service
#   deploy/agent.sh status     show whether it is loaded and running
#
# CONFIG=/path/to/watchmydog.yaml overrides the config (default:
# deploy/watchmydog.local.yaml). Logs: ~/Library/Logs/watchmydog/.
set -euo pipefail

LABEL="com.watchmydog.agent"
REPO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CONFIG="${CONFIG:-${REPO_DIR}/deploy/watchmydog.local.yaml}"
BIN_DIR="${HOME}/.local/bin"
BIN="${BIN_DIR}/watchmydog"
LOG_DIR="${HOME}/Library/Logs/watchmydog"
PLIST="${HOME}/Library/LaunchAgents/${LABEL}.plist"
TEMPLATE="${REPO_DIR}/deploy/launchd/${LABEL}.plist.template"
APP_SRC="${REPO_DIR}/bin/WatchMyDog.app"
APP="${HOME}/Applications/WatchMyDog.app"
LAUNCHER="${APP}/Contents/MacOS/watchmydog-launcher"
DOMAIN="gui/$(id -u)"
AGENT_PATH="${BIN_DIR}:/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin"

log() { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
die() { printf '\033[1;31mError:\033[0m %s\n' "$*" >&2; exit 1; }

loaded() { launchctl print "${DOMAIN}/${LABEL}" >/dev/null 2>&1; }

status() {
  if ! loaded; then
    echo "agent: not loaded"
    return 1
  fi
  launchctl print "${DOMAIN}/${LABEL}" | awk '
    /^\tstate = /           { print "agent: " $3 }
    /^\tpid = /             { print "pid:   " $3 }
    /last exit code = /     { sub(/^[ \t]+/, ""); print $0 }
    /^\truns = /            { print "runs:  " $3 }'
}

unload() {
  if loaded; then
    log "Stopping ${LABEL}"
    launchctl bootout "${DOMAIN}/${LABEL}" || true
    # bootout returns before the process has exited; wait for it.
    for _ in $(seq 1 50); do loaded || break; sleep 0.2; done
  fi
}

install_agent() {
  [[ -f "${CONFIG}" ]] || die "config not found: ${CONFIG} (copy deploy/watchmydog.example.yaml)"
  command -v go2rtc >/dev/null || [[ -x "${BIN_DIR}/go2rtc" ]] || die "go2rtc missing: run deploy/install.sh"

  log "Building service, web client, capture helper and launcher"
  make -C "${REPO_DIR}" build web install-capture launcher >/dev/null

  # Give the ad-hoc signature a stable, readable identifier instead of
  # "a.out". (Ad-hoc identities still change with every build; see ADR-0006.)
  codesign --force --sign - --identifier com.watchmydog.service "${REPO_DIR}/bin/watchmydog" 2>/dev/null

  mkdir -p "${BIN_DIR}" "${LOG_DIR}" "$(dirname "${PLIST}")"
  unload
  install -m 755 "${REPO_DIR}/bin/watchmydog" "${BIN}"

  # Copy the launcher app only when it changed: macOS ties the camera and
  # microphone permission to its exact signature, and a new copy means the
  # permission prompts appear again on the Mac's screen.
  mkdir -p "$(dirname "${APP}")"
  if [[ -d "${APP}" ]] && diff -rq "${APP_SRC}" "${APP}" >/dev/null 2>&1; then
    log "WatchMyDog.app unchanged (camera/microphone permission kept)"
  else
    rm -rf "${APP}"
    ditto "${APP_SRC}" "${APP}"
    log "Installed ${APP}"
    printf '\033[1;33m%s\033[0m\n' "    macOS will ask on the Mac's screen to let WatchMyDog use the camera and microphone: click Allow."
  fi

  local tmp
  tmp="$(mktemp)"
  sed -e "s#__LAUNCHER__#${LAUNCHER}#g" \
      -e "s#__BIN__#${BIN}#g" \
      -e "s#__CONFIG__#${CONFIG}#g" \
      -e "s#__WORKDIR__#${REPO_DIR}#g" \
      -e "s#__PATH__#${AGENT_PATH}#g" \
      -e "s#__LOGDIR__#${LOG_DIR}#g" \
      "${TEMPLATE}" > "${tmp}"
  plutil -lint "${tmp}" >/dev/null || die "rendered plist is invalid"
  mv "${tmp}" "${PLIST}"
  chmod 644 "${PLIST}"

  log "Loading ${PLIST}"
  launchctl bootstrap "${DOMAIN}" "${PLIST}"
  launchctl enable "${DOMAIN}/${LABEL}"
  sleep 2
  status
  echo "logs:  ${LOG_DIR}/watchmydog.log"
}

case "${1:-}" in
  install)   install_agent ;;
  uninstall) unload; rm -f "${PLIST}"; log "Removed ${PLIST} (kept ${BIN} and ${APP})" ;;
  restart)   loaded || die "agent not loaded"; launchctl kickstart -k "${DOMAIN}/${LABEL}"; sleep 2; status ;;
  status)    status ;;
  *)         sed -n '2,12p' "$0" | sed 's/^# \{0,1\}//'; exit 2 ;;
esac
