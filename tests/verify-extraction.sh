#!/usr/bin/env bash
# Second half of § The gate in
# docs/superpowers/specs/2026-09-10-og-generate-extract-config-generation-design.md:
# the half a flake check structurally cannot cover, because it needs two
# revisions of the tree.
#
# checks.<system>.tmux-conf-extraction-assertions renders both ways inside ONE
# checkout, so both sides interpolate the same store paths and it is blind to
# store-path drift. This builds .#default at the base revision and at HEAD and
# diffs the two generated tmux.conf files, which sees it.
#
# BASE is pinned to a SHA rather than to `main` so the comparison does not
# silently change meaning when `main` moves.
#
# The -f scrape is deliberately NOT tests/test-display.sh:130's
# `-f /nix/store/[a-z0-9]*-tmux[.]conf` pattern: this script exists to detect a
# change in the generated output's shape, so its own instrument must not be
# broken by one. Any non-space token after -f.
set -euo pipefail

BASE="${BASE:-e79e925}"

REPO="$(git rev-parse --show-toplevel)"
TMP="$(mktemp -d)"
cleanup() {
	rm -rf "$TMP"
}
trap cleanup EXIT

base_sha="$(git -C "$REPO" rev-parse --short "$BASE")"
head_sha="$(git -C "$REPO" rev-parse --short HEAD)"

if [[ $base_sha == "$head_sha" ]]; then
	echo "base and HEAD are the same commit ($head_sha) — nothing to compare" >&2
	exit 1
fi

# A lock bump produces a non-empty diff for a reason unrelated to the
# extraction, which would discredit the one instrument covering store-path
# drift. Abort before spending two builds on it.
if ! git -C "$REPO" diff --quiet "$BASE" HEAD -- flake.lock; then
	echo "flake.lock differs between $base_sha and $head_sha — a lock bump makes this comparison meaningless" >&2
	echo "rebase onto the base's lock, or re-pin BASE, before trusting the diff" >&2
	exit 1
fi

if [[ -n $(git -C "$REPO" status --porcelain) ]]; then
	echo "note: working tree is dirty; the HEAD side builds the committed tree, not what is on disk" >&2
fi

# Build from an exported tree rather than the checkout: `path:` keeps nix off
# the git filter, and neither side can be perturbed by the other's build.
conf_at() {
	local rev="$1" dir="$TMP/$2" out
	mkdir -p "$dir"
	git -C "$REPO" archive "$rev" | tar -x -C "$dir"
	out="$(nix build --no-link --print-out-paths "path:$dir#default")"
	# -m1 rather than a `| head -1`, which can SIGPIPE grep under pipefail.
	grep -m1 -o -- '-f [^[:space:]]*' "$out/bin/tmux" | cut -d' ' -f2
}

echo "building $base_sha (base)..." >&2
base_conf="$(conf_at "$BASE" base)"
echo "building $head_sha (HEAD)..." >&2
head_conf="$(conf_at HEAD head)"

echo "base: $base_conf"
echo "head: $head_conf"

if diff -u "$base_conf" "$head_conf"; then
	echo "tmux.conf is byte-identical between $base_sha and $head_sha"
else
	echo "tmux.conf differs between $base_sha and $head_sha (diff above)" >&2
	exit 1
fi
