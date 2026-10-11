#!/usr/bin/env bash
# Copyright (c) 2026 Circle Internet Services, Inc.
# Copyright (c) 2026 web3technologies
#
# Permission is hereby granted, free of charge, to any person obtaining a copy
# of this software and associated documentation files (the "Software"), to deal
# in the Software without restriction, including without limitation the rights
# to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
# copies of the Software, and to permit persons to whom the Software is
# furnished to do so, subject to the following conditions:
#
# The above copyright notice and this permission notice shall be included in
# all copies or substantial portions of the Software.
#
# THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
# IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
# FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
# AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
# LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
# OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
# SOFTWARE.
#
# SPDX-License-Identifier: MIT
#
# Install the Smelt CLI.
# https://github.com/smeltdev/smelt
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/smeltdev/smelt/main/install.sh | bash
#
# Environment overrides:
#   VERSION   Specific version to install (e.g. 1.2.3 or v1.2.3).
#             Defaults to the latest non-prerelease. Preview/prerelease builds
#             are NOT resolved automatically; set VERSION explicitly for those.
#   DESTDIR   Install directory for the `smelt` binary. Default: $HOME/.local/bin,
#             which needs no sudo. sudo is used only for a DESTDIR you name that
#             is not writable.
#   REPO      GitHub "owner/name". Default: smeltdev/smelt.
#   SMELT_DOWNLOAD_BASE
#             Where releases are fetched from. Default: https://github.com/${REPO}.
#             Used for the /releases/latest redirect and the downloads; mainly
#             for tests.
#   SMELT_FORCE=1
#             Replace DESTDIR/smelt even if it is a symlink (e.g. from
#             `task dev-install`).
#
# Dependencies: curl, tar, grep, install, and one of sha256sum / shasum.

set -o errexit
set -o nounset
set -o pipefail

# Whole installation lives in a function that is only invoked on the final
# line. If the download to a pipe (curl | bash) is truncated, bash never
# reaches the call and nothing executes half-way.
install_cli() {
	REPO="${REPO:-smeltdev/smelt}"
	DOWNLOAD_BASE="${SMELT_DOWNLOAD_BASE:-https://github.com/${REPO}}"
	DEFAULT_DESTDIR="${HOME:-}/.local/bin"
	DESTDIR_SET="${DESTDIR:-}"
	DESTDIR="${DESTDIR:-${DEFAULT_DESTDIR}}"

	# --- colours (only when stderr is a TTY and NO_COLOR is unset) ---------
	if [ -t 2 ] && [ -z "${NO_COLOR:-}" ]; then
		BOLD=$(printf '\033[1m'); DIM=$(printf '\033[2m'); RESET=$(printf '\033[0m')
	else
		BOLD=""; DIM=""; RESET=""
	fi
	info()  { echo "${DIM}==>${RESET} $*" >&2; }
	warn()  { echo "${BOLD}warning:${RESET} $*" >&2; }
	fail()  { echo "${BOLD}error:${RESET} $*" >&2; exit 1; }

	[ -n "${HOME:-}" ] || [ -n "$DESTDIR_SET" ] || fail "HOME is not set; set DESTDIR to choose where to install."
	command -v curl >/dev/null 2>&1 || fail "curl is required but was not found in PATH."
	command -v tar  >/dev/null 2>&1 || fail "tar is required but was not found in PATH."

	# --- detect platform ----------------------------------------------------
	case "$(uname -s)" in
		Linux)  OS=Linux  ;;
		Darwin) OS=Darwin ;;
		*)      fail "Unsupported operating system: $(uname -s). See https://github.com/${REPO}/releases for other builds." ;;
	esac

	case "$(uname -m)" in
		x86_64 | amd64)  ARCH=x86_64 ;;
		aarch64 | arm64) ARCH=arm64  ;;
		*)               fail "Unsupported architecture: $(uname -m). See https://github.com/${REPO}/releases for other builds." ;;
	esac

	# --- refuse to clobber a dev symlink ------------------------------------
	# `task dev-install` symlinks DESTDIR/smelt to a working-tree build so hooks
	# pick up each `task build`. Replacing it would silently break that setup.
	if [ -L "${DESTDIR}/smelt" ] && [ "${SMELT_FORCE:-}" != "1" ]; then
		fail "${DESTDIR}/smelt is a symlink (to $(readlink "${DESTDIR}/smelt" 2>/dev/null || echo "somewhere")), probably from 'task dev-install'. Not replacing it. Run 'task dev-uninstall' first, or re-run with SMELT_FORCE=1 to replace the symlink."
	fi

	# --- resolve version ----------------------------------------------------
	VERSION="${VERSION:-}"
	VERSION="${VERSION#v}"
	if [ -z "$VERSION" ]; then
		info "Resolving latest release"
		# Follow the redirect from /releases/latest to /releases/tag/vX.Y.Z and
		# keep the final path segment. Avoids a jq dependency on the API JSON.
		effective_url=$(curl -fsSLI -o /dev/null -w '%{url_effective}' "${DOWNLOAD_BASE}/releases/latest") \
			|| fail "Could not reach ${DOWNLOAD_BASE} to determine the latest version (does the repository have a release yet?)."
		tag="${effective_url##*/}"
		VERSION="${tag#v}"
		if [ -z "$VERSION" ] || [ "$VERSION" = "latest" ]; then
			fail "Could not determine the latest version. Set VERSION explicitly and re-run."
		fi
	fi
	info "Installing Smelt CLI ${BOLD}v${VERSION}${RESET} (${OS}/${ARCH})"

	ARCHIVE="smelt_${OS}_${ARCH}.tar.gz"
	CHECKSUMS="checksums.txt"
	DL_BASE="${DOWNLOAD_BASE}/releases/download/v${VERSION}"

	# --- scratch dir, removed on every exit ----------------------------------
	SCRATCH=$(mktemp -d 2>/dev/null || mktemp -d -t smelt)
	STAGED=""
	STAGED_WITH_SUDO=""
	cleanup() {
		rm -rf "${SCRATCH}"
		[ -n "${STAGED}" ] || return 0
		if [ -n "${STAGED_WITH_SUDO}" ]; then
			sudo rm -f "${STAGED}" 2>/dev/null || true
		else
			rm -f "${STAGED}" 2>/dev/null || true
		fi
	}
	trap cleanup EXIT

	# --- download archive + checksums ---------------------------------------
	info "Downloading ${ARCHIVE}"
	curl -fsSL --retry 3 -o "${SCRATCH}/${ARCHIVE}"   "${DL_BASE}/${ARCHIVE}" \
		|| fail "Download failed: ${DL_BASE}/${ARCHIVE} (does v${VERSION} exist, with a ${OS}/${ARCH} build?)"
	curl -fsSL --retry 3 -o "${SCRATCH}/${CHECKSUMS}" "${DL_BASE}/${CHECKSUMS}" \
		|| fail "Could not download checksums: ${DL_BASE}/${CHECKSUMS}"

	# --- verify SHA-256 -----------------------------------------------------
	info "Verifying checksum"
	# No match must fall through to the check below, not trip errexit.
	expected=$(grep " ${ARCHIVE}\$" "${SCRATCH}/${CHECKSUMS}" | awk '{print $1}') || true
	[ -n "$expected" ] || fail "No checksum entry for ${ARCHIVE} in ${CHECKSUMS}."
	if command -v sha256sum >/dev/null 2>&1; then
		actual=$(sha256sum "${SCRATCH}/${ARCHIVE}" | awk '{print $1}')
	elif command -v shasum >/dev/null 2>&1; then
		actual=$(shasum -a 256 "${SCRATCH}/${ARCHIVE}" | awk '{print $1}')
	else
		fail "Need sha256sum or shasum to verify the download."
	fi
	[ "$expected" = "$actual" ] \
		|| fail "Checksum mismatch for ${ARCHIVE} (expected ${expected}, got ${actual}). Nothing was installed."

	# --- unpack -------------------------------------------------------------
	tar -xzf "${SCRATCH}/${ARCHIVE}" -C "${SCRATCH}" smelt \
		|| fail "Could not extract 'smelt' from ${ARCHIVE}."
	[ -f "${SCRATCH}/smelt" ] || fail "'smelt' binary not found after extracting ${ARCHIVE}."

	# --- install: stage next to the target, then rename over it -------------
	# The rename is atomic, so a smelt that is running (or a failure half-way)
	# never leaves a partial binary at DESTDIR/smelt.
	info "Installing to ${BOLD}${DESTDIR}${RESET}"
	staging="${DESTDIR}/.smelt.tmp.$$"
	if { [ -d "${DESTDIR}" ] || mkdir -p "${DESTDIR}" 2>/dev/null; } && [ -w "${DESTDIR}" ]; then
		STAGED="${staging}"
		install -m 0755 "${SCRATCH}/smelt" "${STAGED}"
		mv -f "${STAGED}" "${DESTDIR}/smelt"
	elif [ -n "${DESTDIR_SET}" ] && command -v sudo >/dev/null 2>&1; then
		info "${DESTDIR} is not writable; using sudo"
		sudo mkdir -p "${DESTDIR}"
		STAGED="${staging}"
		STAGED_WITH_SUDO=1
		sudo install -m 0755 "${SCRATCH}/smelt" "${STAGED}"
		sudo mv -f "${STAGED}" "${DESTDIR}/smelt"
	else
		fail "${DESTDIR} is not writable. Re-run with DESTDIR set to a writable directory."
	fi
	STAGED=""

	info "Installed: ${BOLD}v${VERSION}${RESET} at ${DESTDIR}/smelt"
	print_path_hint
	check_prerequisites
	echo >&2
	info "Next: ${BOLD}cd your-repo && smelt init${RESET}"
}

# print_path_hint says how to put DESTDIR on PATH, in the user's own shell's
# syntax, when it is not there already.
print_path_hint() {
	case ":${PATH}:" in
		*":${DESTDIR}:"*) return 0 ;;
	esac
	# Show $HOME rather than its expansion, as it would be written in an rc file.
	shown="${DESTDIR}"
	if [ -n "${HOME:-}" ] && [ "${DESTDIR#"${HOME}"/}" != "${DESTDIR}" ]; then
		shown="\$HOME/${DESTDIR#"${HOME}"/}"
	fi
	echo >&2
	warn "${DESTDIR} is not on your PATH. Add it with:"
	case "$(basename "${SHELL:-sh}")" in
		zsh)  echo "  echo 'export PATH=\"${shown}:\$PATH\"' >> ~/.zshrc && source ~/.zshrc" >&2 ;;
		bash)
			rc=".bashrc"
			[ "$(uname -s)" != "Darwin" ] || rc=".bash_profile"
			echo "  echo 'export PATH=\"${shown}:\$PATH\"' >> ~/${rc} && source ~/${rc}" >&2 ;;
		fish) echo "  fish_add_path ${shown}" >&2 ;;
		*)    echo "  export PATH=\"${shown}:\$PATH\"   # add this to your shell's startup file" >&2 ;;
	esac
	echo "Or run ${DESTDIR}/smelt directly." >&2
}

# check_prerequisites warns about tools smelt uses that are missing. Nothing
# here fails the install: smelt is useful without some of them.
check_prerequisites() {
	missing=0
	check_tool() {
		command -v "$1" >/dev/null 2>&1 && return 0
		if [ "$missing" -eq 0 ]; then
			echo >&2
			warn "Some tools smelt works with were not found:"
		fi
		missing=1
		echo "  - $1: $2" >&2
	}
	check_tool git    "https://git-scm.com/downloads"
	check_tool docker "https://docs.docker.com/get-docker/ (the Docker daemon must be running for factory runs)"
	check_tool rsync  "install with your package manager (brew install rsync / apt-get install rsync)"
	check_tool claude "https://docs.claude.com/en/docs/claude-code/setup"
	if command -v docker >/dev/null 2>&1 && ! docker info >/dev/null 2>&1; then
		echo >&2
		warn "docker is installed but its daemon is not reachable; start it before running factory runs."
	fi
	return 0
}

install_cli
