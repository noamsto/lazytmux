#!/usr/bin/env bash
# Reject Linux-only binaries that don't exist on a nix-darwin PATH (the
# util-linux family). They pass silently on the Linux CI/dev box and only break
# at runtime on macOS — exactly how flock/setsid slipped in before lib-log.sh
# grew acquire_lock/detach.
#
# Scope is deliberately the zero-false-positive set: binaries nixpkgs CANNOT
# provide on darwin. GNU-vs-BSD *flag* differences (stat -c, date -d, sed -i,
# readlink -f, grep -P) are judgment calls — a verified GNU-first/BSD fallback
# is fine — so they're left to the shell-reviewer agent, not this hard gate.
#
# Escape hatch: append `# portable-ok: <reason>` to a line with a real fallback.
#
# Args are the shell files to scan (passed by the pre-commit hook).
set -uo pipefail

# cmd|suggestion — every entry is Linux-only in nixpkgs (meta.platforms = linux).
denylist=(
	"flock|use acquire_lock (scripts/lib-log.sh)"
	"setsid|use detach (scripts/lib-log.sh)"
	"taskset|no darwin equivalent — drop it or gate by OS"
	"ionice|no darwin equivalent — drop it or gate by OS"
	"chrt|no darwin equivalent — drop it or gate by OS"
	"nsenter|no darwin equivalent — drop it or gate by OS"
	"unshare|no darwin equivalent — drop it or gate by OS"
)

status=0
for file in "$@"; do
	[[ -f $file ]] || continue
	lineno=0
	while IFS= read -r line || [[ -n $line ]]; do
		((++lineno))
		# Skip full-line comments and explicitly-waived lines.
		[[ $line =~ ^[[:space:]]*# ]] && continue
		[[ $line == *"portable-ok"* ]] && continue
		for entry in "${denylist[@]}"; do
			cmd=${entry%%|*}
			hint=${entry#*|}
			# Match the binary only as a standalone word (not unflock, not a
			# substring), anywhere on a non-comment line.
			if [[ $line =~ (^|[^[:alnum:]_])${cmd}([^[:alnum:]_]|$) ]]; then
				printf "%s:%s: Linux-only '%s' — %s\n" "$file" "$lineno" "$cmd" "$hint" >&2
				status=1
			fi
		done
	done <"$file"
done

if ((status)); then
	printf '\nLinux-only binaries break at runtime on macOS. Fix them, or waive a\nline with a verified fallback using a portable-ok comment.\n' >&2
	exit 1
fi
