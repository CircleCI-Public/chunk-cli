#!/usr/bin/env bash
# Fails when a tracked file still carries the name this project was forked
# under. smelt must never read, write or collide with CircleCI's own tool on the
# same machine, so the old name may appear only where it is attribution or part
# of CircleCI's API contract.
#
# Run from anywhere: task check:names
set -euo pipefail

cd "$(git rev-parse --show-toplevel)"

# The old name in every shape it was used: the word, the daemon, the env
# prefix, the project folder, and the keychain/label prefix.
pattern='\bchunk\b|chunkd|CHUNK_|\.chunk|com\.circleci\.chunk'

# Files that may name it, wholesale.
allow_files=(
	# license attribution
	'^LICENSE$'
	'^packaging/deb/copyright$'
	# tests that prove smelt leaves the old tool's locations alone
	'^acceptance/coexist_test\.go$'
	'^internal/keyring/keyring_test\.go$'
	'^internal/factory/publish_test\.go$'
	# this script, which has to spell the patterns
	'^scripts/check-names\.sh$'
)
allowed_files=$(IFS='|'; echo "${allow_files[*]}")

# Lines that may name it, anywhere. Each is either the README's statement of
# the fork's origin or a CircleCI API-contract string, which carries the marker
# comment on its own line or the line above it.
contract='CircleCI API contract: do not rename'

failed=0
while IFS= read -r file; do
	[ -f "$file" ] || continue
	if [[ "$file" =~ $allowed_files ]]; then
		continue
	fi
	# -I skips binaries.
	hits=$(grep -nIE "$pattern" "$file" || true)
	[ -n "$hits" ] || continue
	while IFS= read -r hit; do
		n=${hit%%:*}
		text=${hit#*:}
		# The README names the upstream project it was forked from.
		if [ "$file" = README.md ] && [[ "$text" == *"fork of [CircleCI-Public/chunk-cli]"* ]]; then
			continue
		fi
		# An API-contract string is marked on its line or the one above.
		if sed -n "$((n > 1 ? n - 1 : 1)),${n}p" "$file" | grep -q "$contract"; then
			continue
		fi
		echo "${file}:${hit}"
		failed=1
	done <<<"$hits"
done < <(git ls-files)

if [ "$failed" -ne 0 ]; then
	echo >&2
	echo "check:names: the old name appears outside the allowlist." >&2
	echo "Rename it (internal/brand has the names), or, for a CircleCI API-contract string, mark it with: // ${contract}" >&2
	exit 1
fi
echo "check:names: ok"
