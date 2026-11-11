#!/usr/bin/env bash
# Checks everything the Mac mini needs to keep the dog camera reachable, and
# says how to fix what is wrong. Read-only: it never changes settings.
#
#   deploy/doctor.sh        (or: make doctor)
#
# Exit status 1 when a required check fails; warnings do not fail.
set -uo pipefail

REPO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
LABEL="com.watchmydog.agent"
fails=0

ok()   { printf '  \033[32m✓\033[0m %s\n' "$1"; }
warn() { printf '  \033[33m!\033[0m %s\n      %s\n' "$1" "$2"; }
fail() { printf '  \033[31m✗\033[0m %s\n      %s\n' "$1" "$2"; fails=$((fails + 1)); }
section() { printf '\n\033[1m%s\033[0m\n' "$1"; }

section "Power"
sleep_min="$(pmset -g custom | awk '/^AC Power/{ac=1} ac && $1=="sleep"{print $2; exit}')"
if [[ "${sleep_min}" == "0" ]]; then
  ok "System sleep is off (Energy: prevent automatic sleeping)"
else
  warn "System sleep after ${sleep_min} min" \
       "The service keeps the Mac awake while it runs, but turn on System Settings > Energy > \"Prevent automatic sleeping when the display is off\" as a second line of defence."
fi
if pmset -g assertions | grep -q "watchmydog: serving the camera stream"; then
  ok "Service holds its keep-awake lock"
else
  fail "No keep-awake lock from the service" "Is the service running with keep_awake: true? See: make agent-status"
fi
if [[ "$(pmset -g | awk '/autorestart /{print $2}')" == "1" ]]; then
  ok "Restarts automatically after a power failure"
else
  warn "Does not restart after a power failure" \
       "Run: sudo pmset -a autorestart 1   (System Settings > Energy > Start up automatically after a power failure)"
fi

section "Login after reboot"
fv="$(fdesetup status 2>/dev/null)"
autologin="$(defaults read /Library/Preferences/com.apple.loginwindow autoLoginUser 2>/dev/null || true)"
if [[ "${fv}" == *"On"* ]]; then
  warn "FileVault is on: after any reboot the Mac waits for the password" \
       "Nothing (Tailscale, the service) runs until someone logs in. Planned restarts: sudo fdesetup authrestart. See docs/OPERATIONS.md."
elif [[ -n "${autologin}" ]]; then
  ok "Automatic login as ${autologin}"
else
  warn "No automatic login" "System Settings > Users & Groups > Automatically log in as. Otherwise the service waits for a login after reboots."
fi

section "Tailscale"
if ! command -v tailscale >/dev/null; then
  fail "tailscale CLI not found" "See docs/TAILSCALE.md"
else
  ts_json="$(tailscale status --json 2>/dev/null)"
  state="$(printf '%s' "${ts_json}" | python3 -I -c 'import json,sys; print(json.load(sys.stdin).get("BackendState",""))' 2>/dev/null)"
  dns="$(printf '%s' "${ts_json}" | python3 -I -c 'import json,sys; print(json.load(sys.stdin)["Self"]["DNSName"].rstrip("."))' 2>/dev/null)"
  if [[ "${state}" == "Running" ]]; then ok "Connected as ${dns}"; else fail "Tailscale is ${state:-not running}" "Open the Tailscale app and log in."; fi
  if tailscale serve status 2>/dev/null | grep -q "proxy http://127.0.0.1:8080"; then
    ok "tailscale serve publishes https://${dns}"
  else
    fail "tailscale serve is not configured" "Run: make serve"
  fi
fi

section "Service"
uid="$(id -u)"
if launchctl print "gui/${uid}/${LABEL}" >/dev/null 2>&1; then
  pid="$(launchctl print "gui/${uid}/${LABEL}" | awk '/^\tpid = /{print $3}')"
  if [[ -n "${pid}" ]]; then ok "launchd agent running (pid ${pid}), starts at login"; else fail "launchd agent loaded but not running" "See: make logs"; fi
else
  fail "launchd agent not installed" "Run: make install-agent"
fi
health="$(curl -s -m 5 http://127.0.0.1:8080/api/health 2>/dev/null)"
if [[ "${health}" == *'"status":"ok"'* ]]; then
  ok "Health check ok (media engine running)"
elif [[ -n "${health}" ]]; then
  fail "Health check degraded" "${health}"
else
  fail "Service not answering on 127.0.0.1:8080" "See: make logs"
fi
if [[ -n "${dns:-}" ]]; then
  code="$(curl -s -m 10 -o /dev/null -w '%{http_code}' "https://${dns}/api/cameras" 2>/dev/null)"
  if [[ "${code}" == "401" ]]; then
    ok "HTTPS on the tailnet works and requires login"
  else
    fail "https://${dns}/api/cameras returned ${code:-no answer} (expected 401)" "Check tailscale serve and the service."
  fi
fi
secret="${REPO_DIR}/data/secret"
if [[ -f "${secret}" ]]; then
  perm="$(stat -f '%Lp' "${secret}")"
  if [[ "${perm}" == "600" ]]; then ok "Login secret file is private (0600)"; else warn "Login secret file mode ${perm}" "Run: chmod 600 ${secret}"; fi
fi

printf '\n'
if [[ ${fails} -gt 0 ]]; then
  printf '\033[31m%d problem(s) found.\033[0m\n' "${fails}"
  exit 1
fi
printf '\033[32mAll required checks passed.\033[0m\n'
