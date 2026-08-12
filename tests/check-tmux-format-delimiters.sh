#!/usr/bin/env bash
# Reject control-byte field delimiters in tmux -F/-p format strings.
#
# tmux rewrites non-printable bytes in command output to "_" unless the querying
# client's locale is UTF-8. A tab-delimited format therefore comes back as a
# single field, and every consumer silently parses garbage — which is how
# claude_reap_dead_panes came to delete every agent state file once per 5s
# (#373). Use "|", or any other printable byte.
#
# Two rules, because either alone is evadable:
#   (a) no raw C0 byte or DEL — catches -F '#{a}<TAB>#{b}'
#   (b) no \t / \x09 / \x1f escape text — catches -F $'#{a}\t#{b}', whose source
#       bytes are all printable
# Leading indentation is stripped before (a): this repo indents with tabs, and
# dozens of legitimate format-bearing lines start with one. A tab that survives
# the strip is one somebody put *inside* the line.
#
# Deliberately allowed: multi-byte UTF-8 (tmux-reflow-windows.sh's FMT carries
# ├─/╰─ glyphs) and \n (tmux-reflow-windows.sh reads a 3-line -p format; tmux
# splits the output buffer on LF before sanitizing, so newlines are not mangled).
#
# Scans whole files rather than -F-bearing lines, so a format assembled into a
# variable and used later as -F "$FMT" is caught at the line that spells the
# format out. A delimiter appended separately (SEP=$(printf '\t'); -F "#{a}$SEP")
# is NOT caught — the gate is the literal "#{", and that line has none.
#
# Arg is the directory to scan.
set -uo pipefail
export LC_ALL=C # byte-ordered ranges for the C0 scan below

dir=${1:?usage: check-tmux-format-delimiters.sh DIR}

status=0
scanned=0
while IFS= read -r file; do
	((++scanned))
	lineno=0
	while IFS= read -r line || [[ -n $line ]]; do
		((++lineno))
		[[ $line == *'#{'* ]] || continue

		[[ $line =~ ^[[:blank:]]*(.*)$ ]]
		body=${BASH_REMATCH[1]}
		case $body in
		*[$'\001'-$'\037'$'\177']*)
			printf "%s:%s: raw control byte in a tmux format — use '|'\n" "$file" "$lineno" >&2
			status=1
			;;
		esac

		case $line in
		*'\t'* | *'\x09'* | *'\x1f'*)
			printf "%s:%s: escaped control byte in a tmux format — use '|'\n" "$file" "$lineno" >&2
			status=1
			;;
		esac
	done <"$file"
done < <(find "$dir" -type f | sort)

# A missing or renamed dir makes find fail and the loop body never run, which
# would otherwise read as "all clean".
if ((scanned == 0)); then
	printf 'scanned no files under %s\n' "$dir" >&2
	exit 1
fi

if ((status)); then
	printf '\nA control byte is not a usable tmux field delimiter: tmux rewrites it to\n"_" for any client without a UTF-8 locale, collapsing the row to one field.\n' >&2
	exit 1
fi
