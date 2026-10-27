#!/usr/bin/env bash
# Laranest installer: downloads the binary and starts it as a systemd user
# service. Needs no root: PHP, Composer and Node are downloaded per user on demand.
#   curl -fsSL https://github.com/nbullorini/laranest/releases/latest/download/install.sh | bash
set -euo pipefail

REPO="nbullorini/laranest"
BIN_DIR="${LARANEST_BIN_DIR:-$HOME/.local/bin}"
VERSION="${LARANEST_VERSION:-latest}"

say() { printf '\033[1;31m▸\033[0m %s\n' "$*"; }
die() { printf '\033[1;31merror:\033[0m %s\n' "$*" >&2; exit 1; }

[ "$(uname -s)" = "Linux" ] || die "Laranest only supports Linux for now."
case "$(uname -m)" in
  x86_64) ARCH=amd64 ;;
  aarch64|arm64) ARCH=arm64 ;;
  *) die "unsupported architecture: $(uname -m)" ;;
esac

# Everything runs as your user (PHP-FPM refuses to run pools as root).
[ "$(id -u)" -ne 0 ] || die "run the installer as your normal user, not root. On WSL, if the distro logs in as root, create a user (adduser <name> && usermod -aG sudo <name>), add [user] default=<name> to /etc/wsl.conf and run 'wsl --terminate <distro>' from Windows."
[ -d /run/systemd/system ] ||
  die "systemd is not running. On WSL, add [boot] systemd=true to /etc/wsl.conf and run 'wsl --shutdown'."
# WSL doesn't open logind sessions: on a fresh distro the user's systemd
# manager only runs with lingering on (allowed for yourself, no sudo).
if ! systemctl --user show-environment >/dev/null 2>&1; then
  loginctl enable-linger "$(id -un)" || die "could not enable lingering (try: sudo loginctl enable-linger $(id -un))"
  export XDG_RUNTIME_DIR="${XDG_RUNTIME_DIR:-/run/user/$(id -u)}"
  for _ in $(seq 50); do [ -S "$XDG_RUNTIME_DIR/bus" ] && break; sleep 0.2; done
  systemctl --user show-environment >/dev/null 2>&1 || die "the systemd user manager did not start"
fi

if [ "$VERSION" = "latest" ]; then
  URL="https://github.com/$REPO/releases/latest/download/laranest-linux-$ARCH"
else
  URL="https://github.com/$REPO/releases/download/$VERSION/laranest-linux-$ARCH"
fi

say "Downloading $URL"
mkdir -p "$BIN_DIR"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
if ! curl -fsSL "$URL" -o "$TMP/laranest" 2>/dev/null; then
  # Private repo (pre-release testing): fall back to an authenticated gh CLI.
  command -v gh >/dev/null && gh auth status >/dev/null 2>&1 ||
    die "could not download $URL (if the repo is private, install and log into the gh CLI)"
  say "Public download failed; using gh"
  tag=""; [ "$VERSION" != "latest" ] && tag="$VERSION"
  gh release download $tag -R "$REPO" -p "laranest-linux-$ARCH" -p SHA256SUMS -D "$TMP"
  mv "$TMP/laranest-linux-$ARCH" "$TMP/laranest"
  (cd "$TMP" && grep " laranest-linux-$ARCH\$" SHA256SUMS | sed "s/laranest-linux-$ARCH/laranest/" | sha256sum -c -) ||
    die "checksum mismatch"
fi
install -m755 "$TMP/laranest" "$BIN_DIR/laranest"

say "Installing systemd user service"
"$BIN_DIR/laranest" install

# On a fresh Ubuntu ~/.local/bin isn't on PATH until the next login (it
# didn't exist yet), so `laranest` would be "command not found".
case ":$PATH:" in
  *":$BIN_DIR:"*) ;;
  *)
    for rc in "$HOME/.bashrc" "$HOME/.zshrc"; do
      [ "$rc" = "$HOME/.bashrc" ] || [ -f "$rc" ] || continue
      grep -q "# laranest path" "$rc" 2>/dev/null ||
        printf '\nexport PATH="%s:$PATH" # laranest path\n' "$BIN_DIR" >> "$rc"
    done
    say "Added $BIN_DIR to your PATH in ~/.bashrc. In this terminal run:  export PATH=\"$BIN_DIR:\$PATH\""
    ;;
esac

cat <<MSG

Next steps:
  • Open the panel and add your projects. Missing PHP versions are downloaded
    automatically (no sudo).
  • Run commands with each project's PHP:  laranest php artisan …  /  laranest composer …
  • Optional: make plain \`php\` and \`composer\` do that too:  laranest shims enable
MSG
