#!/bin/sh
# lport installer and updater.
#
#   curl -fsSL https://raw.githubusercontent.com/IqbalLx/lport/main/install.sh | sh
#
# Running it again updates lport. It only ever replaces files in ~/.lport/bin.
#
# Overrides:
#   LPORT_VERSION=v0.1.0        install that release (default: the latest)
#   LPORT_HOME=...              install somewhere else (default: ~/.lport)
#   LPORT_NO_MODIFY_PATH=1      do not add lport to PATH in your shell's startup file
#   LPORT_FORCE=1               reinstall even if this version is already installed
set -eu

REPO="IqbalLx/lport"
APP_HOME="${LPORT_HOME:-$HOME/.lport}"
BIN_DIR="$APP_HOME/bin"
DOWNLOAD_BASE="${LPORT_DOWNLOAD_BASE:-https://github.com/$REPO/releases/download}"
PATH_BEGIN="# >>> lport >>>"
PATH_END="# <<< lport <<<"

if [ -t 1 ] && [ -z "${NO_COLOR:-}" ]; then
  B=$(printf '\033[1m'); D=$(printf '\033[2m'); G=$(printf '\033[32m'); R=$(printf '\033[31m'); N=$(printf '\033[0m')
else
  B=""; D=""; G=""; R=""; N=""
fi
say() { printf '%s\n' "$*"; }
ok() { printf '  %s✓%s %s\n' "$G" "$N" "$*"; }
err() { printf '\n  %serror:%s %s\n\n' "$R" "$N" "$*" >&2; exit 1; }
have() { command -v "$1" >/dev/null 2>&1; }

# ── tools ──
if have curl; then fetch() { curl -fsSL "$1"; }; fetch_to() { curl -fsSL -o "$2" "$1"; }
elif have wget; then fetch() { wget -qO- "$1"; }; fetch_to() { wget -qO "$2" "$1"; }
else err "lport needs curl or wget to download"; fi
have tar || err "lport needs tar to unpack the download"
if have sha256sum; then sha256() { sha256sum "$1" | cut -d' ' -f1; }
elif have shasum; then sha256() { shasum -a 256 "$1" | cut -d' ' -f1; }
else err "lport needs sha256sum or shasum to check the download"; fi
have ssh || say "  ${D}note: lport drives the ssh command; install OpenSSH to use it${N}"

# ── platform ──
os=$(uname -s)
arch=$(uname -m)
case "$os" in
  Darwin)
    # A shell running under Rosetta reports x86_64 on Apple Silicon; the native build is better there.
    if [ "$arch" = "x86_64" ] && [ "$(sysctl -n sysctl.proc_translated 2>/dev/null || echo 0)" = "1" ]; then arch="arm64"; fi
    case "$arch" in
      arm64|aarch64) platform="darwin-arm64" ;;
      x86_64) platform="darwin-x64" ;;
      *) err "unsupported Mac architecture: $arch" ;;
    esac ;;
  Linux)
    case "$arch" in
      x86_64|amd64) platform="linux-x64" ;;
      aarch64|arm64) platform="linux-arm64" ;;
      *) err "unsupported Linux architecture: $arch" ;;
    esac ;;
  *) err "lport's installer supports macOS and Linux (found $os). Elsewhere: go install github.com/$REPO@latest" ;;
esac

# ── version ──
if [ -n "${LPORT_VERSION:-}" ]; then
  tag="$LPORT_VERSION"
else
  tag=$(fetch "https://api.github.com/repos/$REPO/releases/latest" | grep '"tag_name"' | head -1 | cut -d'"' -f4) || true
  [ -n "$tag" ] || err "could not find the latest release (set LPORT_VERSION=vX.Y.Z to pick one)"
fi

installed=""
if [ -x "$BIN_DIR/lport" ]; then installed=$("$BIN_DIR/lport" --version 2>/dev/null || true); fi

say ""
if [ -n "$installed" ] && [ "$installed" = "$tag" ] && [ -z "${LPORT_FORCE:-}" ]; then
  ok "lport $tag is already installed and up to date ${D}($BIN_DIR)${N}"
  unchanged=1
  update_only=1
else
  unchanged=""
  if [ -n "$installed" ]; then
    say "  ${B}Updating lport${N} $installed → $tag ${D}($platform)${N}"
    update_only=1
  else
    say "  ${B}Installing lport${N} $tag ${D}($platform)${N}"
    update_only=""
  fi

  asset="lport-$tag-$platform.tar.gz"
  tmp=$(mktemp -d)
  trap 'rm -rf "$tmp"' EXIT INT TERM
  fetch_to "$DOWNLOAD_BASE/$tag/$asset" "$tmp/$asset" || err "download failed: $DOWNLOAD_BASE/$tag/$asset"
  fetch_to "$DOWNLOAD_BASE/$tag/SHA256SUMS" "$tmp/SHA256SUMS" || err "could not download the checksums for $tag"
  expected=$(grep " $asset\$" "$tmp/SHA256SUMS" | cut -d' ' -f1 | head -1)
  [ -n "$expected" ] || err "$asset is not listed in the release checksums"
  actual=$(sha256 "$tmp/$asset")
  [ "$expected" = "$actual" ] || err "checksum mismatch for $asset (expected $expected, got $actual); nothing was installed"
  ok "downloaded and verified $asset"

  tar -xzf "$tmp/$asset" -C "$tmp" || err "could not unpack $asset"
  [ -f "$tmp/lport" ] || err "$asset did not contain lport"
  chmod +x "$tmp/lport"
  "$tmp/lport" --version >/dev/null 2>&1 || err "the downloaded lport does not run on this machine; nothing was installed"

  # Copy next to the target, then rename over it: atomic, and a running lport keeps its old file.
  mkdir -p "$BIN_DIR"
  cp "$tmp/lport" "$BIN_DIR/.lport.new"
  mv -f "$BIN_DIR/.lport.new" "$BIN_DIR/lport"
  ok "installed $BIN_DIR/lport"
fi

# ── PATH ──
case "$BIN_DIR" in
  "$HOME"/*) path_value="\$HOME/${BIN_DIR#"$HOME"/}" ;;
  *) path_value="$BIN_DIR" ;;
esac
rc=""
if [ -z "${LPORT_NO_MODIFY_PATH:-}" ]; then
  case "$(basename "${SHELL:-sh}")" in
    zsh) rc="${ZDOTDIR:-$HOME}/.zshrc" ;;
    bash) if [ "$os" = "Darwin" ]; then rc="$HOME/.bash_profile"; else rc="$HOME/.bashrc"; fi ;;
    fish) rc="$HOME/.config/fish/conf.d/lport.fish" ;;
    *) rc="$HOME/.profile" ;;
  esac
  if [ -f "$rc" ] && grep -qF "$PATH_BEGIN" "$rc"; then
    : # already there
  else
    mkdir -p "$(dirname "$rc")"
    # shellcheck disable=SC2016 # $PATH is written literally, to expand when the rc file runs
    case "$rc" in
      *.fish) printf '%s\nfish_add_path %s\n%s\n' "$PATH_BEGIN" "$path_value" "$PATH_END" >> "$rc" ;;
      *) printf '\n%s\nexport PATH="%s:$PATH"\n%s\n' "$PATH_BEGIN" "$path_value" "$PATH_END" >> "$rc" ;;
    esac
    ok "added $BIN_DIR to PATH in $rc"
  fi
fi

# ── next steps ──
say ""
case ":$PATH:" in
  *":$BIN_DIR:"*) on_path=1 ;;
  *) on_path="" ;;
esac
if [ -n "$unchanged" ] || [ -n "$on_path" ]; then
  say "  Run ${B}lport${N} to start."
elif [ -n "$update_only" ]; then
  say "  Run ${B}lport${N} to start. Forwards from a detached session are picked up again."
elif [ -n "$rc" ]; then
  say "  Open a new terminal, or run ${B}export PATH=\"$BIN_DIR:\$PATH\"${N}, then ${B}lport${N} to start."
else
  say "  Add $BIN_DIR to your PATH, then run ${B}lport${N} to start."
fi
say ""
