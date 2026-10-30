#!/usr/bin/env bash
# VPN-UI — one-line installer / updater for a server.
#
#   curl -fsSL https://raw.githubusercontent.com/Miladizadpanah/UniQue/main/install.sh | sudo bash
#
# This file is deliberately only a FRONT DOOR. The install itself lives in
# deploy.sh, which already knows how to tell a fresh install from an in-place
# update, how to keep an existing operator's port / login / web path on update,
# how to snap the SQLite DB before the new binary can migrate it, and how to
# install the systemd unit under whatever name the panel is configured with.
# A second copy of that logic here would be a second thing to fix when it drifts,
# and the drift would show up as an install that half-works.
#
# So: fetch deploy.sh at the SAME ref as this script and hand over. Everything
# below is about fetching it safely and running it with a real file path as $0
# (deploy.sh re-execs itself through sudo, which needs a readable script).
#
# Environment (all optional, read by deploy.sh):
#   VPNUI_REPO=Miladizadpanah/UniQue   repository to install from (default)
#   VPNUI_REF=main            git ref to install from (branch or tag)
#   PANEL_TLS=http|selfsign|letsencrypt   panel transport on a FRESH install
#   DEPLOY_DOMAIN / DEPLOY_EMAIL / DEPLOY_CF_TOKEN   Let's Encrypt details
#   IMPORT_DB=/root/backup.db move an existing 3x-ui/vpn-ui database in
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/Miladizadpanah/UniQue/main/install.sh | sudo bash
#   sudo ./install.sh                    # from a checkout
#   VPNUI_REF=v2.9.7 sudo ./install.sh   # pin a release
set -euo pipefail

REPO="${VPNUI_REPO:-Miladizadpanah/UniQue}"
REF="${VPNUI_REF:-main}"
BASE="https://raw.githubusercontent.com/$REPO/$REF"

say()  { printf '%s\n' "$*" >&2; }
die()  { printf 'ERROR: %s\n' "$*" >&2; exit 1; }

[[ $EUID -eq 0 ]] || die "must run as root — use: curl -fsSL $BASE/install.sh | sudo bash"

for tool in bash systemctl; do
    command -v "$tool" >/dev/null 2>&1 || die "'$tool' not found — this installer needs systemd."
done
if command -v curl >/dev/null 2>&1; then
    get() { curl -fsSL --retry 3 --connect-timeout 20 "$1"; }
elif command -v wget >/dev/null 2>&1; then
    get() { wget -qO- --tries=3 --timeout=20 "$1"; }
else
    die "neither curl nor wget is installed — one of them is required to download the panel."
fi

work="$(mktemp -d)"
# The helper is removed on every exit path, including a Ctrl-C mid-install: it
# holds a copy of the installer, and leaving those behind in /tmp is how a host
# accumulates stale installers nobody re-checks.
trap 'rm -rf "$work"' EXIT INT TERM

say "[VPN-UI] fetching the installer ($REPO@$REF)"
get "$BASE/deploy.sh" > "$work/deploy.sh" || die "could not download $BASE/deploy.sh"
# A truncated download or a captive-portal HTML page must not be executed as
# root: require a bash script of a plausible size before bash ever sees it.
[[ -s "$work/deploy.sh" ]] || die "downloaded installer is empty"
head -c 4096 "$work/deploy.sh" | grep -q '^#!' || die "downloaded installer is not a script (proxy or login page?)"
bash -n "$work/deploy.sh" || die "downloaded installer failed its own syntax check"

chmod +x "$work/deploy.sh"
say "[VPN-UI] handing over to deploy.sh"
# NOT exec: the trap above has to survive long enough to remove the copy. The
# exit status is deploy.sh's, so a failed install still fails this command and a
# `curl | sudo bash` caller can see it.
status=0
bash "$work/deploy.sh" "$@" || status=$?
exit "$status"
