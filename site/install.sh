#!/bin/sh
# upper installer: downloads the latest build, verifies its checksum and
# installs it to ~/.local/bin. Made for Ubuntu on WSL; works on any Linux.
#
#   curl -fsSL https://lengh.github.io/upper/install.sh | sh
#
# Environment overrides:
#   UPPER_INSTALL_DIR  where to put the binary (default: ~/.local/bin)
#   UPPER_BASE_URL     where to download from (default: the project site)
#   UPPER_NO_MODIFY_PATH=1  don't touch shell startup files
#
# Everything runs inside main(), so a partially downloaded script does nothing.

set -eu

BASE_URL="${UPPER_BASE_URL:-https://lengh.github.io/upper}"
INSTALL_DIR="${UPPER_INSTALL_DIR:-$HOME/.local/bin}"

if [ -t 1 ]; then
	bold=$(printf '\033[1m'); dim=$(printf '\033[2m'); red=$(printf '\033[31m')
	green=$(printf '\033[32m'); mauve=$(printf '\033[38;5;183m'); reset=$(printf '\033[0m')
else
	bold=""; dim=""; red=""; green=""; mauve=""; reset=""
fi

say() { printf '%s\n' "$*"; }
step() { printf '%s›%s %s\n' "$mauve" "$reset" "$*"; }
fail() { printf '%serror:%s %s\n' "$red" "$reset" "$*" >&2; exit 1; }

fetch() { # fetch URL DEST
	if command -v curl >/dev/null 2>&1; then
		curl -fsSL --retry 3 -o "$2" "$1"
	elif command -v wget >/dev/null 2>&1; then
		wget -q -O "$2" "$1"
	else
		fail "curl or wget is required (sudo apt install curl)"
	fi
}

main() {
	say ""
	say "  ${bold}${mauve}upper${reset} ${dim}· a terminal Discord client${reset}"
	say ""

	[ "$(uname -s)" = "Linux" ] || fail "upper runs on Linux and WSL; this is $(uname -s)"
	case "$(uname -m)" in
		x86_64 | amd64) arch=amd64 ;;
		aarch64 | arm64) arch=arm64 ;;
		*) fail "no build for $(uname -m) yet; build from source: go build ./cmd/upper" ;;
	esac
	if grep -qi microsoft /proc/version 2>/dev/null; then
		step "detected WSL on $arch"
	else
		step "detected Linux on $arch"
	fi

	command -v tar >/dev/null 2>&1 || fail "tar is required (sudo apt install tar)"
	command -v sha256sum >/dev/null 2>&1 || fail "sha256sum is required (sudo apt install coreutils)"

	tmp=$(mktemp -d)
	trap 'rm -rf "$tmp"' EXIT INT TERM

	file="upper_linux_${arch}.tar.gz"
	step "downloading $file"
	fetch "$BASE_URL/dl/$file" "$tmp/$file" || fail "download failed: $BASE_URL/dl/$file"
	fetch "$BASE_URL/dl/SHA256SUMS" "$tmp/SHA256SUMS" || fail "could not download checksums"

	step "verifying checksum"
	(cd "$tmp" && grep " $file\$" SHA256SUMS | sha256sum -c --status) ||
		fail "checksum mismatch: the download is corrupt or was tampered with"

	tar -xzf "$tmp/$file" -C "$tmp"
	mkdir -p "$INSTALL_DIR"
	install -m 0755 "$tmp/upper" "$INSTALL_DIR/upper"
	version=$("$INSTALL_DIR/upper" --version 2>/dev/null || echo "upper")
	step "installed ${bold}$version${reset} to $INSTALL_DIR/upper"

	case ":$PATH:" in
		*":$INSTALL_DIR:"*) on_path=1 ;;
		*) on_path=0 ;;
	esac
	if [ "$on_path" = 0 ] && [ "${UPPER_NO_MODIFY_PATH:-}" != 1 ]; then
		shown_dir=$INSTALL_DIR
		case "$INSTALL_DIR" in "$HOME"/*) shown_dir="\$HOME/${INSTALL_DIR#"$HOME"/}" ;; esac
		line="export PATH=\"$shown_dir:\$PATH\" # added by upper installer"
		for rc in "$HOME/.bashrc" "$HOME/.zshrc"; do
			if [ -f "$rc" ] && ! grep -qF "# added by upper installer" "$rc"; then
				printf '\n%s\n' "$line" >>"$rc"
				step "added $INSTALL_DIR to PATH in ${rc#"$HOME"/}"
			fi
		done
	fi

	say ""
	say "  ${green}${bold}Done.${reset}"
	if [ "$on_path" = 0 ]; then
		say "  Open a new terminal (or run ${bold}export PATH=\"$INSTALL_DIR:\$PATH\"${reset}), then:"
	else
		say "  Next:"
	fi
	say ""
	say "    ${bold}upper --demo${reset}   ${dim}try it with a fake account first${reset}"
	say "    ${bold}upper${reset}          ${dim}log in with your token${reset}"
	say ""
	say "  ${dim}Run this command again any time to update.${reset}"
	say ""
}

main "$@"
