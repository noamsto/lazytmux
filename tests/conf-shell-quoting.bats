#!/usr/bin/env bats
# Regression guard for a command-injection class that has recurred twice in
# this repo's tmux.conf.nix: run-shell/if-shell format-expand their whole
# argument BEFORE handing it to `sh -c`, so a bare #{session_name} (etc.) in
# that argument is shell injection the moment the format's value contains a
# shell metacharacter (branch names, window titles, ... are all attacker/
# user-influenced). tmux's own fix is #{q:NAME}, which backslash-escapes the
# metacharacters sh -c would otherwise see. This file walks the EMITTED
# tmux.conf (a Nix store file, not the Nix source) and fails on every #{...}
# inside a shell-string argument that isn't exactly `#{q:NAME}` with a plain,
# unnested body.
#
# Scope, precisely:
#   - run-shell's command argument (after its flags) is a shell string.
#   - if-shell's FIRST argument is a shell string, UNLESS -F is present (then
#     it's a format, not shell, and is left alone).
#   - Everything else (if-shell -F's format, command-prompt -I seeds,
#     confirm-before -p text, split-window/new-window -c dir args, plain
#     status-format strings, ...) is not shell and must NOT be flagged.
#   - `{ ... }` brace blocks, and any string that is itself a nested tmux
#     command (e.g. set-hook's command argument, or an old-style if-shell
#     command/else-command string), hold further run-shell/if-shell calls
#     and are recursed into.
#
# KNOWN BLIND SPOT: tmux config macros (`name=value` at line start, used as
# `$name`). tmux substitutes those at parse time, so a macro's body can BE a
# shell argument -- but this scanner reads the emitted text, where the use site
# says only `$is_vim`. Quoting inside a macro body is therefore on the author,
# not on this guard; the one such macro in this conf is commented accordingly.

# --- tokenizer ---------------------------------------------------------
# Splits one logical tmux config line into tokens, matching tmux's own
# quoting: '...' is literal (no escapes), "..." processes backslash
# escapes, and adjacent quoted/bare fragments with no separating whitespace
# glue into one token (e.g. -I'#{@x}'). `{` and `}` are always their own
# token, since they delimit nested command lists.
# Result lands in the global TOKENS array.
TOKENS=()
tmux_tokenize() {
	TOKENS=()
	local s="$1"
	local n=${#s}
	local bs=$'\\'
	local i=0 c buf="" have=0
	while ((i < n)); do
		c="${s:i:1}"
		case "$c" in
		' ' | $'\t')
			if ((have)); then
				TOKENS+=("$buf")
				buf=""
				have=0
			fi
			i=$((i + 1))
			;;
		"'")
			have=1
			i=$((i + 1))
			while ((i < n)); do
				c="${s:i:1}"
				i=$((i + 1))
				[[ $c == "'" ]] && break
				buf+="$c"
			done
			;;
		'"')
			have=1
			i=$((i + 1))
			while ((i < n)); do
				c="${s:i:1}"
				if [[ $c == "$bs" ]] && ((i + 1 < n)); then
					buf+="${s:i+1:1}"
					i=$((i + 2))
					continue
				fi
				i=$((i + 1))
				[[ $c == '"' ]] && break
				buf+="$c"
			done
			;;
		'{' | '}')
			if ((have)); then
				TOKENS+=("$buf")
				buf=""
				have=0
			fi
			TOKENS+=("$c")
			i=$((i + 1))
			;;
		*)
			have=1
			buf+="$c"
			i=$((i + 1))
			;;
		esac
	done
	((have)) && TOKENS+=("$buf")
}

# --- violation bookkeeping ----------------------------------------------

CONF_VIOLATIONS=0

record_violation() {
	local lineno="$1" expansion="$2" excerpt="$3"
	CONF_VIOLATIONS=$((CONF_VIOLATIONS + 1))
	echo "${lineno}: ${expansion}: ${excerpt}"
}

# Scans one shell-command string (already stripped of its own outer quotes)
# for #{...} expansions. Every one must be exactly #{q:NAME}, with no
# further #{ nested inside NAME -- #{q:#{X}} does NOT quote, it's #{q:} of
# a body that is itself unquoted. Reports every offender, not just the first.
scan_shell_string() {
	local str="$1" lineno="$2"
	local n=${#str}
	local excerpt="${str:0:80}"
	local i=0
	while ((i < n)); do
		if [[ ${str:i:2} == '#{' ]]; then
			local start=$i depth=1 j=$((i + 2)) nested=0
			while ((j < n && depth > 0)); do
				if [[ ${str:j:2} == '#{' ]]; then
					nested=1
					depth=$((depth + 1))
					j=$((j + 2))
				elif [[ ${str:j:1} == '}' ]]; then
					depth=$((depth - 1))
					j=$((j + 1))
				else
					j=$((j + 1))
				fi
			done
			local group="${str:start:$((j - start))}"
			if [[ $group == '#{?'* ]]; then
				# A conditional emits one of its BRANCHES; the condition itself is
				# only tested and never reaches the shell. So recurse past the first
				# top-level comma and hold the branches to the same rule. This is
				# what lets `#{?client_name,--client #{q:client_name},}` pass: it is
				# the empty-safe way to emit a flag and its value together or
				# neither, which a bare #{q:} cannot do (empty = zero words).
				local body="${group:3:$((${#group} - 4))}"
				local k=0 d=0 bn=${#body}
				while ((k < bn)); do
					if [[ ${body:k:2} == '#{' ]]; then
						d=$((d + 1))
						k=$((k + 2))
					elif [[ ${body:k:1} == '}' ]]; then
						d=$((d - 1))
						k=$((k + 1))
					elif [[ ${body:k:1} == ',' ]] && ((d == 0)); then
						break
					else
						k=$((k + 1))
					fi
				done
				((k < bn)) && scan_shell_string "${body:$((k + 1))}" "$lineno"
			elif [[ $group != '#{q:'* ]] || ((nested)); then
				record_violation "$lineno" "$group" "$excerpt"
			fi
			i=$j
		else
			i=$((i + 1))
		fi
	done
}

# Walks a token array. run-shell's argument and if-shell's non-F first
# argument get scanned as shell. `{ ... }` blocks recurse. Any other token
# that is ITSELF a nested tmux command line starting with run-shell/if-shell
# (set-hook's command argument; an old-style if-shell command/else-command
# string) recurses too -- everything else is inert and left alone.
walk_tokens() {
	local lineno="$1"
	shift
	local -a _toks=("$@")
	local n=${#_toks[@]}
	local i=0
	while ((i < n)); do
		local tok="${_toks[i]}"
		case "$tok" in
		run-shell)
			i=$((i + 1))
			while ((i < n)) && [[ ${_toks[i]} == -* ]]; do
				case "${_toks[i]}" in
				-d | -t | -c) i=$((i + 2)) ;;
				*) i=$((i + 1)) ;;
				esac
			done
			if ((i < n)); then
				scan_shell_string "${_toks[i]}" "$lineno"
				i=$((i + 1))
			fi
			;;
		if-shell)
			i=$((i + 1))
			local has_f=0
			while ((i < n)) && [[ ${_toks[i]} == -* ]]; do
				case "${_toks[i]}" in
				-F)
					has_f=1
					i=$((i + 1))
					;;
				-t) i=$((i + 2)) ;;
				*) i=$((i + 1)) ;;
				esac
			done
			if ((i < n)); then
				((has_f)) || scan_shell_string "${_toks[i]}" "$lineno"
				i=$((i + 1))
			fi
			;;
		'{')
			local depth=1 j=$((i + 1))
			while ((j < n && depth > 0)); do
				case "${_toks[j]}" in
				'{') depth=$((depth + 1)) ;;
				'}') depth=$((depth - 1)) ;;
				esac
				j=$((j + 1))
			done
			local close=$((j - 1))
			walk_tokens "$lineno" "${_toks[@]:$((i + 1)):$((close - i - 1))}"
			i=$j
			;;
		*)
			# Anywhere in the string, not just at its start: a command/else-command
			# argument may chain with ';' ("set -g x y ; run-shell '...'"), and
			# anchoring to ^ would let that bypass the guard entirely.
			if [[ $tok =~ (^|[[:space:]\;{])(run-shell|if-shell)([[:space:]]|$) ]]; then
				tmux_tokenize "$tok"
				walk_tokens "$lineno" "${TOKENS[@]}"
			fi
			i=$((i + 1))
			;;
		esac
	done
}

# --- conf walker ---------------------------------------------------------
# Joins backslash line-continuations, skips comment/blank lines, and walks
# each logical line's tokens.
scan_conf() {
	local conf="$1"
	local lineno=0 logical="" logical_start=0
	local line
	while IFS= read -r line || [[ -n $line ]]; do
		lineno=$((lineno + 1))
		if [[ -z $logical ]]; then
			local trimmed="${line#"${line%%[![:space:]]*}"}"
			[[ -z $trimmed || $trimmed == '#'* ]] && continue
			logical_start=$lineno
		fi
		if [[ $line == *\\ ]]; then
			logical+="${line%\\}"
			continue
		fi
		logical+="$line"
		tmux_tokenize "$logical"
		walk_tokens "$logical_start" "${TOKENS[@]}"
		logical=""
	done <"$conf"
}

# Entry point for tests: prints every violation, returns 1 if any were found.
check_conf_quoting() {
	CONF_VIOLATIONS=0
	scan_conf "$1"
	((CONF_VIOLATIONS == 0))
}

# --- #(...) formats -------------------------------------------------------
# `#(cmd)` in a status/window format also runs through a shell, and is
# format-expanded first, so it is the same injection mechanism as run-shell.
# The blanket rule above is deliberately NOT applied here: these formats also
# carry presentation-only options (@thm_*, @icon_*, start_time) that are
# generated locally and still sit in shell single quotes, and unquoting those
# would change word counts on the 1s status path for no security gain.
#
# What must never appear here unquoted is the identity set below — the values
# an untrusted REMOTE host can steer, which is the trust boundary this whole
# guard exists for (a bridged session is named from the remote's session list).
IDENTITY_FORMATS=(session_name hook_session_name window_name pane_title)

check_hashparen_identity() {
	local conf="$1"
	CONF_VIOLATIONS=0
	local lineno=0 line body name
	# #( up to the first ) — tmux's own #() bodies here never contain a literal
	# unescaped ')', and #{q:} escapes one in a VALUE, so first-')' is enough.
	local re='#\(([^)]*)\)'
	while IFS= read -r line || [[ -n $line ]]; do
		lineno=$((lineno + 1))
		[[ ${line#"${line%%[![:space:]]*}"} == '#'* ]] && continue
		while [[ $line =~ $re ]]; do
			body="${BASH_REMATCH[1]}"
			line="${line#*"${BASH_REMATCH[0]}"}"
			for name in "${IDENTITY_FORMATS[@]}"; do
				[[ $body == *"#{$name}"* ]] &&
					record_violation "$lineno" "#{$name}" "${body:0:80}"
			done
		done
	done <"$conf"
	((CONF_VIOLATIONS == 0))
}

# --- fixtures --------------------------------------------------------

@test "flags every known-bad bare/unquoted #{} pattern" {
	cat >"$BATS_TEST_TMPDIR/bad.conf" <<'EOF'
set-hook -g after-new-window 'run-shell -b "/bin/x #{session_name}"'
bind S run-shell '/bin/x "#{session_name}"'
bind Z if-shell '/bin/x #{pane_current_command}' foo bar
bind Q if-shell -F '#{@gate}' { run-shell "/bin/x #{session_name}" }
run-shell "/bin/x #{q:#{session_name}}"
bind V if-shell '/bin/gate' 'set -g @x y ; run-shell "/bin/x #{window_name}"' 'display-message no'
bind U run-shell '/bin/x #{?client_name,--client #{client_name},}'
EOF
	run check_conf_quoting "$BATS_TEST_TMPDIR/bad.conf"
	[ "$status" -eq 1 ]
	[[ $output == *'#{session_name}'* ]]
	[[ $output == *'#{pane_current_command}'* ]]
	[[ $output == *'#{q:#{session_name}}'* ]]
	local count
	count=$(echo "$output" | grep -c .)
	# the last fixture is a run-shell chained after a ';' inside an old-style
	# if-shell command argument -- it must not slip past the recursion
	[[ $output == *'#{window_name}'* ]]
	# a conditional's BRANCH is emitted into the shell, so it is held to the
	# same rule even though the condition itself never reaches sh
	[[ $output == *'#{client_name}'* ]]
	[ "$count" -eq 7 ]
}

@test "does not flag non-shell contexts" {
	cat >"$BATS_TEST_TMPDIR/good.conf" <<'EOF'
bind Q if-shell -F '#{&&:#{@bridge_win},#{@bridge_pane}}' { run-shell "/bin/x #{q:@bridge_pane}" }
bind , command-prompt -I'#{@window_bridge_name}' { run-shell "/bin/x #{q:@bridge_pane}" }
bind x confirm-before -p "kill #{@bridge_pane}? (y/n)" { run-shell "/bin/x #{q:@bridge_pane}" }
bind | if-shell -F '#{@gate}' { run-shell "/bin/x #{q:@bridge_pane}" } { split-window -h -c "#{pane_current_path}" }
set -g status-format[0] "#{session_name}"
bind U run-shell '/bin/x #{?client_name,--client #{q:client_name},}'
EOF
	run check_conf_quoting "$BATS_TEST_TMPDIR/good.conf"
	[ "$status" -eq 0 ]
	[ -z "$output" ]
}

@test "the emitted tmux.conf has zero shell-quoting violations" {
	# Hard failure, never skip: a skip reports `ok`, so a broken CONF binding
	# would retire this guard silently — the one outcome it exists to prevent.
	[ -n "${CONF:-}" ] || {
		echo "CONF is unset; the check derivation must bind it to tmuxConfig.tmuxConf" >&2
		false
	}
	[ -f "$CONF" ] || {
		echo "CONF does not exist: $CONF" >&2
		false
	}
	run check_conf_quoting "$CONF"
	if [ "$status" -ne 0 ]; then
		echo "$output" >&2
	fi
	[ "$status" -eq 0 ]
}

@test "flags a bare identity format inside a #(...) shell format" {
	cat >"$BATS_TEST_TMPDIR/hp-bad.conf" <<'EOF'
set -g status-format[0] "#(/bin/statusline --session '#{session_name}' --thm-bg '#{@thm_bg}')"
EOF
	run check_hashparen_identity "$BATS_TEST_TMPDIR/hp-bad.conf"
	[ "$status" -eq 1 ]
	# exactly one violation, and it names session_name. The presentation-only
	# @thm_bg is deliberately not flagged -- assert on the reported expansion
	# (field 2), not on the whole line: the excerpt quotes the body verbatim
	# and so mentions @thm_bg either way.
	[ "$(echo "$output" | grep -c .)" -eq 1 ]
	[ "$(echo "$output" | cut -d' ' -f2)" = '#{session_name}:' ]
}

@test "accepts a quoted identity format inside a #(...) shell format" {
	cat >"$BATS_TEST_TMPDIR/hp-good.conf" <<'EOF'
set -g status-format[0] "#(/bin/statusline --session #{q:session_name} --thm-bg '#{@thm_bg}')"
EOF
	run check_hashparen_identity "$BATS_TEST_TMPDIR/hp-good.conf"
	[ "$status" -eq 0 ]
	[ -z "$output" ]
}

@test "the emitted tmux.conf never puts an identity format bare in #(...)" {
	[ -n "${CONF:-}" ] || {
		echo "CONF is unset; the check derivation must bind it to tmuxConfig.tmuxConf" >&2
		false
	}
	run check_hashparen_identity "$CONF"
	if [ "$status" -ne 0 ]; then
		echo "$output" >&2
	fi
	[ "$status" -eq 0 ]
}
