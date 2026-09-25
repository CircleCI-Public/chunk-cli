#!/usr/bin/env bash
# Copyright (c) 2026 Circle Internet Services, Inc.
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
# Install the Chunk CLI.
# https://github.com/CircleCI-Public/chunk-cli
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/CircleCI-Public/chunk-cli/main/install.sh | bash
#
# Environment overrides:
#   VERSION   Specific version to install (e.g. 1.2.3), without a leading "v".
#             Defaults to the latest non-prerelease. Preview/prerelease builds
#             are NOT resolved automatically — set VERSION explicitly for those.
#   DESTDIR   Install directory for the `chunk` binary. Default: /usr/local/bin.
#   REPO      GitHub "owner/name". Default: CircleCI-Public/chunk-cli.
#
# Dependencies: curl, tar, grep, install, and one of sha256sum / shasum.

set -o errexit
set -o nounset
set -o pipefail

# Whole installation lives in a function that is only invoked on the final
# line. If the download to a pipe (curl | bash) is truncated, bash never
# reaches the call and nothing executes half-way.
install_cli() {
	REPO="${REPO:-CircleCI-Public/chunk-cli}"
	GITHUB_BASE_URL="https://github.com/${REPO}"
	DESTDIR="${DESTDIR:-/usr/local/bin}"

	# --- colours (only when stderr is a TTY and NO_COLOR is unset) ---------
	if [ -t 2 ] && [ -z "${NO_COLOR:-}" ]; then
		BOLD=$(printf '\033[1m'); DIM=$(printf '\033[2m'); RESET=$(printf '\033[0m')
	else
		BOLD=""; DIM=""; RESET=""
	fi
	info()  { echo "${DIM}==>${RESET} $*" >&2; }
	fail()  { echo "${BOLD}error:${RESET} $*" >&2; exit 1; }

	command -v curl >/dev/null 2>&1 || fail "curl is required but was not found in PATH."
	command -v tar  >/dev/null 2>&1 || fail "tar is required but was not found in PATH."

	# --- detect platform ----------------------------------------------------
	case "$(uname -s)" in
		Linux)  OS=Linux  ;;
		Darwin) OS=Darwin ;;
		*)      fail "Unsupported operating system: $(uname -s). Use Homebrew or Snap instead." ;;
	esac

	case "$(uname -m)" in
		x86_64 | amd64)  ARCH=x86_64 ;;
		aarch64 | arm64) ARCH=arm64  ;;
		*)               fail "Unsupported architecture: $(uname -m)." ;;
	esac

	# --- resolve version ----------------------------------------------------
	if [ -z "${VERSION:-}" ]; then
		info "Resolving latest release"
		# Follow the redirect from /releases/latest to /releases/tag/vX.Y.Z and
		# keep the final path segment. Avoids a jq dependency on the API JSON.
		effective_url=$(curl -fsSLI -o /dev/null -w '%{url_effective}' "${GITHUB_BASE_URL}/releases/latest") \
			|| fail "Could not reach GitHub to determine the latest version."
		tag="${effective_url##*/}"
		VERSION="${tag#v}"
		[ -n "$VERSION" ] && [ "$VERSION" != "latest" ] \
			|| fail "Could not determine the latest version. Set VERSION explicitly and re-run."
	fi
	info "Installing Chunk CLI ${BOLD}v${VERSION}${RESET} (${OS}/${ARCH})"

	ARCHIVE="chunk_${OS}_${ARCH}.tar.gz"
	CHECKSUMS="checksums.txt"
	DL_BASE="${GITHUB_BASE_URL}/releases/download/v${VERSION}"

	# --- scratch dir; keep it on failure for debugging ----------------------
	SCRATCH=$(mktemp -d 2>/dev/null || mktemp -d -t chunk)
	keep_scratch() {
		echo "${BOLD}error:${RESET} installation failed. Files left in ${SCRATCH} for debugging." >&2
	}
	trap keep_scratch ERR

	# --- download archive + checksums ---------------------------------------
	info "Downloading ${ARCHIVE}"
	curl -fsSL --retry 3 -o "${SCRATCH}/${ARCHIVE}"   "${DL_BASE}/${ARCHIVE}" \
		|| fail "Download failed: ${DL_BASE}/${ARCHIVE} (does v${VERSION} have a ${OS}/${ARCH} build?)"
	curl -fsSL --retry 3 -o "${SCRATCH}/${CHECKSUMS}" "${DL_BASE}/${CHECKSUMS}" \
		|| fail "Could not download checksums: ${DL_BASE}/${CHECKSUMS}"

	# --- verify SHA-256 -----------------------------------------------------
	info "Verifying checksum"
	expected=$(grep " ${ARCHIVE}\$" "${SCRATCH}/${CHECKSUMS}" | awk '{print $1}')
	[ -n "$expected" ] || fail "No checksum entry for ${ARCHIVE} in ${CHECKSUMS}."
	if command -v sha256sum >/dev/null 2>&1; then
		actual=$(sha256sum "${SCRATCH}/${ARCHIVE}" | awk '{print $1}')
	elif command -v shasum >/dev/null 2>&1; then
		actual=$(shasum -a 256 "${SCRATCH}/${ARCHIVE}" | awk '{print $1}')
	else
		fail "Need sha256sum or shasum to verify the download."
	fi
	[ "$expected" = "$actual" ] \
		|| fail "Checksum mismatch for ${ARCHIVE} (expected ${expected}, got ${actual})."

	# --- unpack -------------------------------------------------------------
	tar -xzf "${SCRATCH}/${ARCHIVE}" -C "${SCRATCH}" chunk \
		|| fail "Could not extract 'chunk' from ${ARCHIVE}."
	[ -f "${SCRATCH}/chunk" ] || fail "'chunk' binary not found after extracting ${ARCHIVE}."

	# --- install, escalating with sudo only if needed -----------------------
	info "Installing to ${BOLD}${DESTDIR}${RESET}"
	if [ -w "${DESTDIR}" ] || { [ ! -e "${DESTDIR}" ] && mkdir -p "${DESTDIR}" 2>/dev/null; }; then
		install "${SCRATCH}/chunk" "${DESTDIR}/chunk"
	elif command -v sudo >/dev/null 2>&1; then
		info "${DESTDIR} is not writable; using sudo"
		sudo install "${SCRATCH}/chunk" "${DESTDIR}/chunk"
	else
		fail "${DESTDIR} is not writable and sudo is unavailable. Re-run with DESTDIR set to a writable dir."
	fi

	trap - ERR
	rm -rf "${SCRATCH}"

	installed="${DESTDIR}/chunk"
	info "Installed: ${BOLD}v${VERSION}${RESET}"
	case ":${PATH}:" in
		*":${DESTDIR}:"*) command -v chunk >/dev/null 2>&1 || true ;;
		*) info "Note: ${DESTDIR} is not on your PATH. Add it, or run ${installed} directly." ;;
	esac
}

install_cli
