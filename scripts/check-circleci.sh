#!/usr/bin/env bash
# Fails when a tracked file outside the allowlist references something
# CircleCI owns: its GitHub repositories and packages, its API and sites, or the
# Segment write key the upstream project used to ship with. CircleCI is an opt-in backend, so
# these references belong only in the packages that implement it, in docs about
# it, and in the license attribution. The README credits the upstream project on
# one line.
#
# Run from anywhere: task check:circleci
set -euo pipefail

cd "$(git rev-parse --show-toplevel)"

# What counts as a reference. CircleCI-Public as a repo or owner (a URL or an
# owner/name slug), never as the Go import path.
patterns=(
	'CircleCI-Public/[A-Za-z0-9._-]+'
	'circleci\.com'
	'homebrew-circleci'
	'nur-packages'
	'winget-pkgs'
	'Abgkrg'
)
pattern=$(IFS='|'; echo "${patterns[*]}")

# Docs may link to CircleCI's own sites (circleci.com) when they describe the
# CircleCI backend, but never to its repositories or packages. README.md also
# credits the upstream project, on one line.
docs_pattern=$(IFS='|'; echo "${patterns[*]:0:1}|${patterns[*]:2}" | sed 's/ /|/g')
docs=(
	'^docs/'
	'^README\.md$'
)
docs_files=$(IFS='|'; echo "${docs[*]}")

# Where they are allowed to appear. Anchored regexes over repo-relative paths.
allow=(
	# the CircleCI backend and the explicit CircleCI commands
	'^internal/circleci/'
	'^internal/sidecar/'
	'^internal/authprompt/'
	'^internal/oauth/'
	'^internal/keyring/'
	'^internal/config/'
	'^internal/smeltd/server/'
	'^internal/cmd/(auth|authhelper|validate|org|task|sidecar|prune|mutate|root)\.go$'
	'^internal/testing/'
	# the CircleCI-specific skills and their copies
	'^(skills|\.agents/skills|\.claude/skills)/smelt-sidecar'
	'^skills/debug-ci-failures/'
	# license attribution
	'^LICENSE$'
	'^packaging/deb/copyright$'
	# this script and the lint config that names the allowed importers
	'^scripts/check-circleci\.sh$'
	'^\.golangci\.yml$'
	# harness targets that are just repo names
	'^harness/'
	# test fixtures
	'_test\.go$'
	'^acceptance/'
	# the project's own Smarter Testing config
	'^\.circleci/test-suites\.yml$'
)
allowed=$(IFS='|'; echo "${allow[*]}")

failed=0
while IFS= read -r file; do
	[ -f "$file" ] || continue
	if [[ "$file" =~ $allowed ]]; then
		continue
	fi
	active=$pattern
	if [[ "$file" =~ $docs_files ]]; then
		active=$docs_pattern
	fi
	# -I skips binaries. The Go module path is stripped before matching.
	hits=$(grep -nIE "$active" "$file" \
		| grep -v 'fork of \[CircleCI-Public' || true)
	if [ -n "$hits" ]; then
		echo "$hits" | sed "s|^|${file}:|"
		failed=1
	fi
done < <(git ls-files)

if [ "$failed" -ne 0 ]; then
	echo >&2
	echo "check:circleci: references to CircleCI-owned endpoints or packages found outside the allowlist." >&2
	echo "Remove them, or (for the CircleCI backend itself) extend the allowlist in scripts/check-circleci.sh." >&2
	exit 1
fi
echo "check:circleci: ok"
