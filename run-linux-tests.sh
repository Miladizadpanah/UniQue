#!/usr/bin/env bash
# Run this repository's Go test suite on LINUX from a Windows host.
#
# Why this exists: the project builds and tests on Linux only. web/service uses
# unix.*, syscall.Ucred and netlink, so a Windows host cannot even compile that
# package ("go build ./..." fails with undefined: syscall.Ucred), which means
# "go test ./sub/..." cannot run there either — the subscription quota bug was
# shipped precisely because nobody could execute these tests locally.
#
# What it does: imports a minimal Ubuntu rootfs as a private WSL distro, installs
# the toolchain inside it (gcc, because gorm's sqlite driver is cgo; and the Go
# release go.mod asks for), then runs go test on the given packages. The distro is
# reused on later runs, so only the first run pays for the download.
#
# Requirements:
#   - Windows 11 with the WSL optional feature enabled AND a reboot since it was
#     enabled. `wsl.exe --status` must answer; if it prints "The file cannot be
#     accessed by the system", reboot first (the feature is enabled but the
#     subsystem has not been booted into service yet).
#   - Network access for the rootfs and toolchain downloads (~350 MB the first
#     time, plus the Go module downloads of this project).
#
# Usage:
#   ./run-linux-tests.sh                          # ./sub/... ./web/service/...
#   ./run-linux-tests.sh ./...                    # everything
#   ./run-linux-tests.sh -run TestGetSubs ./sub/  # extra flags go to go test
#
# Exit status is go test's, so this can be used from CI or a pre-push hook.
set -euo pipefail

DISTRO="${VPNUI_WSL_DISTRO:-vpnuites}"
ROOTFS_DIR_WIN='C:\wsl'
ROOTFS_DIR='/c/wsl'
ROOTFS_TAR="$ROOTFS_DIR/ubuntu-noble-rootfs.tar.gz"
# Canonical's "current" core image is a stable URL; the release base image is the
# fallback because Canonical moves point releases.
ROOTFS_URLS=(
  "https://partner-images.canonical.com/core/noble/current/ubuntu-noble-core-cloudimg-amd64-root.tar.gz"
  "https://cdimage.ubuntu.com/ubuntu-base/releases/24.04/release/ubuntu-base-24.04.2-base-amd64.tar.gz"
)
GO_VERSION="go1.26.2" # must match the `go` directive in go.mod

say() { printf '%s\n' "$*"; }
die() { printf 'ERROR: %s\n' "$*" >&2; exit 1; }

# ---------------------------------------------------------------- preflight ---
if ! command -v wsl.exe >/dev/null 2>&1; then
  die "wsl.exe not found. Install/enable WSL first (see the header of this script)."
fi
if ! WSL_STATUS="$(wsl.exe --status 2>&1 | tr -d '\000')"; then
  say "$WSL_STATUS"
  die "WSL is not usable yet. The 'Microsoft-Windows-Subsystem-Linux' feature is enabled but Windows has not been rebooted since; reboot and re-run this script."
fi
case "$WSL_STATUS" in
  *"cannot be accessed by the system"*)
    die "WSL reports 'The file cannot be accessed by the system', which is the pre-reboot state of a freshly enabled WSL feature. Reboot Windows and re-run this script."
    ;;
esac

# ------------------------------------------------------------- the distro ---
have_distro() {
  wsl.exe --list --quiet 2>/dev/null | tr -d '\000\r' | sed 's/^[[:space:]]*//; s/[[:space:]]*$//' | grep -qx "$DISTRO"
}

if ! have_distro; then
  if [ ! -s "$ROOTFS_TAR" ]; then
    mkdir -p "$ROOTFS_DIR"
    say "== downloading a minimal Ubuntu rootfs (one time, ~30 MB) =="
    ok=0
    for url in "${ROOTFS_URLS[@]}"; do
      say "   $url"
      if curl -fL --retry 3 --connect-timeout 20 -o "$ROOTFS_TAR.part" "$url"; then
        mv "$ROOTFS_TAR.part" "$ROOTFS_TAR"
        ok=1
        break
      fi
    done
    rm -f "$ROOTFS_TAR.part"
    [ "$ok" = 1 ] || die "could not download an Ubuntu rootfs; download one manually to $ROOTFS_TAR and re-run"
  fi
  say "== importing WSL distro '$DISTRO' (one time) =="
  wsl.exe --import "$DISTRO" "$ROOTFS_DIR_WIN\\$DISTRO" "$(cygpath -w "$ROOTFS_TAR")" --version 2
fi

# ------------------------------------------------------------------ project ---
# Windows path of this checkout, translated to the /mnt/<drive>/... form WSL sees.
PROJECT_WIN="$(cygpath -w "$PWD")"
drive="${PROJECT_WIN%%:*}"
rest="${PROJECT_WIN#*:}"
PROJECT_WSL="/mnt/$(printf '%s' "$drive" | tr 'A-Z' 'a-z')$(printf '%s' "$rest" | tr '\\' '/')"

say "== running the suite in WSL ($DISTRO) =="
say "   project : $PROJECT_WSL"
say "   packages: ${*:-./sub/... ./web/service/...}"

# The inner script is piped in rather than shipped as a second file so this stays
# one artifact; CR is stripped so an editor that saves CRLF cannot break bash.
wsl.exe -d "$DISTRO" -u root -- bash -s "$PROJECT_WSL" "$GO_VERSION" "$@" < <(
  sed 's/\r$//' <<'INNER'
set -euo pipefail

PROJECT="$1"; shift
GO_VERSION="$1"; shift
PKGS=("$@")
if [ ${#PKGS[@]} -eq 0 ]; then
  PKGS=("./sub/..." "./web/service/...")
fi

export DEBIAN_FRONTEND=noninteractive
export PATH="/usr/local/go/bin:$PATH"

if [ ! -x /usr/local/go/bin/go ]; then
  echo "== installing build-essential + $GO_VERSION inside the distro (one time) =="
  apt-get update -qq
  apt-get install -y -qq --no-install-recommends build-essential ca-certificates curl tar >/dev/null
  curl -fsSL -o /tmp/go.tgz "https://go.dev/dl/${GO_VERSION}.linux-amd64.tar.gz"
  tar -C /usr/local -xzf /tmp/go.tgz
  rm -f /tmp/go.tgz
fi
export CGO_ENABLED=1
export GOTOOLCHAIN=local

cd "$PROJECT"
echo "== toolchain =="
go version
gcc --version | head -1

echo "== go test ${PKGS[*]} =="
set +e
set -o pipefail
go test "${PKGS[@]}" 2>&1 | tee "$PROJECT/wsl-go-test.log"
status=${PIPESTATUS[0]}
set -e
echo "GO_TEST_EXIT=$status"
exit "$status"
INNER
)
