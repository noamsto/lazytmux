#!/usr/bin/env bash
# Launch gh-dash with a Catppuccin theme matching the active light/dark flavor.
# gh-dash reads its config once at launch, so picking the flavor here keeps it
# in step with the theme toggle — same theme-state.json the status bar reads.
# Any --config the user passes wins (we skip the overlay and defer to gh-dash).
set -euo pipefail

GH_DASH="@gh_dash@"
YQ="@yq@"

# Honor an explicit --config: don't second-guess a caller who picked their own.
for arg in "$@"; do
	if [[ $arg == "--config" || $arg == --config=* ]]; then
		exec "$GH_DASH" "$@"
	fi
done

theme_file="${XDG_STATE_HOME:-$HOME/.local/state}/theme-state.json"
theme="dark"
if [[ -f $theme_file ]]; then
	theme=$(grep -o '"theme"[[:space:]]*:[[:space:]]*"[^"]*"' "$theme_file" 2>/dev/null | cut -d'"' -f4) || true
fi

# Catppuccin palette → gh-dash semantic theme keys (Latte for light, Mocha else).
if [[ $theme == "light" ]]; then
	text="#4c4f69" subtext="#6c6f85" base="#eff1f5" overlay="#9ca0b0"
	peach="#fe640b" green="#40a02b" red="#d20f39" mauve="#8839ef"
	surface0="#ccd0da" surface1="#bcc0cc"
else
	text="#cdd6f4" subtext="#a6adc8" base="#1e1e2e" overlay="#6c7086"
	peach="#fab387" green="#a6e3a1" red="#f38ba8" mauve="#cba6f7"
	surface0="#313244" surface1="#45475a"
fi

user_config="${XDG_CONFIG_HOME:-$HOME/.config}/gh-dash/config.yml"
cache_dir="${XDG_CACHE_HOME:-$HOME/.cache}/lazytmux"
composed="$cache_dir/gh-dash-config.yml"
mkdir -p "$cache_dir"

theme_overlay=$(
	cat <<EOF
theme:
  colors:
    text:
      primary: "$text"
      secondary: "$subtext"
      inverted: "$base"
      faint: "$overlay"
      warning: "$peach"
      success: "$green"
      error: "$red"
      actor: "$mauve"
    background:
      selected: "$surface0"
    border:
      primary: "$mauve"
      secondary: "$surface1"
      faint: "$surface0"
EOF
)

if [[ -f $user_config ]]; then
	# Deep-merge our theme over the user's config (their sections/defaults stay;
	# our colors win the overlap, of which there is normally none).
	# shellcheck disable=SC2016 # $i is a yq variable, must stay single-quoted
	"$YQ" eval-all '. as $i ireduce ({}; . * $i)' "$user_config" <(printf '%s\n' "$theme_overlay") >"$composed"
else
	printf '%s\n' "$theme_overlay" >"$composed"
fi

exec "$GH_DASH" --config "$composed" "$@"
