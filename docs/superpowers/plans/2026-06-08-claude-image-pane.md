# Claude Conversation Image Pane — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A `prefix + I` keybind toggles a split pane showing a navigable gallery of every image the Claude session in the current pane has touched.

**Architecture:** A PostToolUse hook (`claude-images-update`) appends image paths to a per-pane JSONL manifest under `$CLAUDE_STATUS_DIR/images/`. A keybind launches `tmux-claude-images`, which splits a pane and runs a full-pane keyboard navigator that renders each image via `claude-image-render` — a terminal-aware ladder (kitten icat → chafa kitty → chafa sixel → chafa symbols). All shell, no fzf; mirrors the existing `claude-status-update` / `tmux-scratchpad` patterns.

**Tech Stack:** bash, jq, chafa, kitten (host-optional), tmux 3.6, Nix (`writeShellScriptBin`), bats.

**Repo:** `noamsto/lazytmux`, branch `feat/32-claude-image-pane`. Spec: `docs/superpowers/specs/2026-06-08-claude-image-pane-design.md`. Run tests from repo root in the devshell (`bats` + `jq` provided): `bats tests/<file>.bats`.

---

## File structure

| File | Responsibility | Action |
|------|----------------|--------|
| `scripts/claude-images-update.sh` | Hook-side appender; stdin JSON → manifest line | create |
| `scripts/claude-image-render.sh` | Render one image, terminal-aware backend ladder | create |
| `scripts/tmux-claude-images.sh` | Keybind: toggle viewer pane + nav loop | create |
| `claude-plugin/scripts/images.sh` | Plugin shim → `claude-images-update` | create |
| `tests/claude-images.bats` | Appender + renderer-selection unit tests | create |
| `tests/fixtures/hook-*.json` | Sample PostToolUse payloads | create |
| `scripts/claude-status-update.sh` | Extend dead-pane sweep to drop `images/<pane>.jsonl` | modify (lines 11-13, ~47) |
| `claude-plugin/hooks/hooks.json` | Register PostToolUse appender | modify (PostToolUse block) |
| `config/tmux.conf.nix` | `scriptNames` += 3 names; `chafa` dep; `prefix + I` bind | modify (172-187, 576, ~386) |
| `flake.nix` | `claude-images-tests` check | modify (checks block) |

---

## Task 1: Manifest appender + tests

**Files:**
- Create: `scripts/claude-images-update.sh`
- Create: `tests/fixtures/hook-read-image.json`, `hook-write-image.json`, `hook-screenshot.json`, `hook-non-image.json`
- Create: `tests/claude-images.bats`

- [ ] **Step 1: Write fixtures**

`tests/fixtures/hook-read-image.json`:
```json
{"tool_name":"Read","cwd":"/work","tool_input":{"file_path":"IMGPATH"},"tool_response":{}}
```
`tests/fixtures/hook-write-image.json`:
```json
{"tool_name":"Write","cwd":"/work","tool_input":{"file_path":"IMGPATH"},"tool_response":{}}
```
`tests/fixtures/hook-screenshot.json`:
```json
{"tool_name":"mcp__playwright__browser_take_screenshot","cwd":"/work","tool_input":{"filename":"shot.png"},"tool_response":{"content":[{"type":"text","text":"Saved to IMGPATH"}]}}
```
`tests/fixtures/hook-non-image.json`:
```json
{"tool_name":"Read","cwd":"/work","tool_input":{"file_path":"/work/notes.txt"},"tool_response":{}}
```
(The literal `IMGPATH` token is replaced with a real temp file path by each test.)

- [ ] **Step 2: Write the failing test file**

`tests/claude-images.bats`:
```bash
#!/usr/bin/env bats

setup() {
  export CLAUDE_STATUS_DIR="$BATS_TEST_TMPDIR/state"
  export TMUX_PANE="%7"
  MANIFEST="$CLAUDE_STATUS_DIR/images/7.jsonl"
  IMG="$BATS_TEST_TMPDIR/pic.png"
  printf 'x' >"$IMG"          # a real (non-empty) file so the existence gate passes
  APP="scripts/claude-images-update.sh"
}

# Feed a fixture with IMGPATH replaced by $IMG into the appender.
run_app() { # $1 = fixture name
  sed "s#IMGPATH#$IMG#g" "tests/fixtures/$1" | bash "$APP"
}

@test "Read of an image appends one manifest line" {
  run_app hook-read-image.json
  [ -f "$MANIFEST" ]
  run wc -l <"$MANIFEST"
  [ "$output" -eq 1 ]
  run jq -r '.path' "$MANIFEST"
  [ "$output" = "$IMG" ]
  run jq -r '.source' "$MANIFEST"
  [ "$output" = "Read" ]
}

@test "Write of an image is recorded" {
  run_app hook-write-image.json
  run jq -r '.source' "$MANIFEST"
  [ "$output" = "Write" ]
}

@test "screenshot path is extracted from tool_response" {
  run_app hook-screenshot.json
  [ -f "$MANIFEST" ]
  run jq -r '.path' "$MANIFEST"
  [ "$output" = "$IMG" ]
}

@test "non-image is ignored (no manifest)" {
  run_app hook-non-image.json
  [ ! -f "$MANIFEST" ]
}

@test "missing file is ignored" {
  rm -f "$IMG"
  run_app hook-read-image.json
  [ ! -f "$MANIFEST" ]
}

@test "dedup by (path,mtime): same image twice → one line" {
  run_app hook-read-image.json
  run_app hook-read-image.json
  run wc -l <"$MANIFEST"
  [ "$output" -eq 1 ]
}

@test "no TMUX_PANE → no-op, exit 0" {
  unset TMUX_PANE
  run run_app hook-read-image.json
  [ "$status" -eq 0 ]
  [ ! -f "$MANIFEST" ]
}
```

- [ ] **Step 3: Run the tests, verify they fail**

Run: `bats tests/claude-images.bats`
Expected: FAIL — `scripts/claude-images-update.sh` does not exist.

- [ ] **Step 4: Implement the appender**

`scripts/claude-images-update.sh`:
```bash
#!/usr/bin/env bash
# Append images Claude touches (Read/Write/screenshots) to a per-pane manifest.
# PostToolUse hook: reads the hook JSON payload on stdin.
# Mirrors claude-status-update.sh — self-contained, keyed by $TMUX_PANE.
set -euo pipefail

STATE_DIR="${CLAUDE_STATUS_DIR:-/tmp/claude-status}"
IMAGES_DIR="$STATE_DIR/images"

pane_id="${TMUX_PANE:-}"
[[ -n $pane_id ]] || exit 0          # outside tmux → no-op
pane_file="${pane_id#%}"

payload="$(cat)"
[[ -n $payload ]] || exit 0

# Candidate path from common tool shapes, falling back to any image-looking
# string in the tool_response.
path="$(jq -r '
  .tool_input.file_path
  // .tool_input.path
  // .tool_input.output_path
  // .tool_input.filename
  // ([.tool_response | .. | strings | select(test("\\.(png|jpe?g|gif|webp|bmp)$"; "i"))] | first)
  // empty
' <<<"$payload" 2>/dev/null | head -n1)"
[[ -n $path ]] || exit 0

# Resolve relative paths against the hook-reported cwd.
if [[ $path != /* ]]; then
  cwd="$(jq -r '.cwd // empty' <<<"$payload" 2>/dev/null)"
  [[ -n $cwd ]] && path="$cwd/$path"
fi

shopt -s nocasematch
[[ $path =~ \.(png|jpe?g|gif|webp|bmp)$ ]] || exit 0
shopt -u nocasematch
[[ -f $path ]] || exit 0

mtime="$(stat -c %Y "$path" 2>/dev/null || echo 0)"
source_tool="$(jq -r '.tool_name // "?"' <<<"$payload" 2>/dev/null)"
printf -v now '%(%FT%T%z)T' -1

manifest="$IMAGES_DIR/$pane_file.jsonl"
mkdir -p "$IMAGES_DIR"

# Dedup by (path, mtime).
if [[ -f $manifest ]] &&
  jq -e --arg p "$path" --argjson m "$mtime" \
    'select(.path == $p and .mtime == $m)' "$manifest" >/dev/null 2>&1; then
  exit 0
fi

jq -nc --arg path "$path" --arg source "$source_tool" --arg ts "$now" --argjson mtime "$mtime" \
  '{type:"image", path:$path, source:$source, ts:$ts, mtime:$mtime}' >>"$manifest"
```

- [ ] **Step 5: Run the tests, verify they pass**

Run: `bats tests/claude-images.bats`
Expected: PASS (7 tests). If the screenshot test fails, inspect the `jq` extraction against the fixture — the `tool_response` recursive-string scan is the fallback path.

- [ ] **Step 6: Lint**

Run: `shellcheck scripts/claude-images-update.sh`
Expected: no warnings.

- [ ] **Step 7: Commit**

```bash
git add scripts/claude-images-update.sh tests/claude-images.bats tests/fixtures/hook-*.json
git commit -m "feat: per-pane image manifest appender (#32)"
```

---

## Task 2: Renderer ladder + selection tests

**Files:**
- Create: `scripts/claude-image-render.sh`
- Modify: `tests/claude-images.bats` (append renderer-selection tests)

- [ ] **Step 1: Add failing selection tests**

Append to `tests/claude-images.bats`:
```bash
choose() { bash scripts/claude-image-render.sh --choose "$1" "$2"; }

@test "kitty terminal with kitten → kitten" {
  run choose xterm-kitty 1
  [ "$output" = "kitten" ]
}

@test "kitty terminal without kitten → chafa-kitty" {
  run choose xterm-kitty 0
  [ "$output" = "chafa-kitty" ]
}

@test "ghostty with kitten → kitten" {
  run choose xterm-ghostty 1
  [ "$output" = "kitten" ]
}

@test "foot → chafa-sixel" {
  run choose foot 0
  [ "$output" = "chafa-sixel" ]
}

@test "unknown terminal → chafa-symbols (universal floor)" {
  run choose dumb 0
  [ "$output" = "chafa-symbols" ]
}
```

- [ ] **Step 2: Run, verify failure**

Run: `bats tests/claude-images.bats`
Expected: the 5 new tests FAIL — `scripts/claude-image-render.sh` does not exist.

- [ ] **Step 3: Implement the renderer**

`scripts/claude-image-render.sh`:
```bash
#!/usr/bin/env bash
# Render one image to the current pane, choosing the best backend for the outer
# terminal. Selection is isolated in choose_renderer for testability.
# Usage: claude-image-render <path> [cols] [rows]
#        claude-image-render --choose <client_termname> <has_kitten 0|1>   (test hook)
set -euo pipefail

choose_renderer() { # $1=client_termname  $2=has_kitten(0|1) → prints backend id
  local term="$1" kitten="$2"
  case "$term" in
  xterm-kitty* | xterm-ghostty*)
    if [[ $kitten == 1 ]]; then echo kitten; else echo chafa-kitty; fi ;;
  foot* | *wezterm* | xterm* | contour* | konsole*)
    echo chafa-sixel ;;
  *)
    echo chafa-symbols ;;
  esac
}

if [[ ${1:-} == --choose ]]; then
  choose_renderer "${2:-}" "${3:-0}"
  exit 0
fi

path="${1:-}"
cols="${2:-}"
rows="${3:-}"
[[ -f $path ]] || {
  printf '[missing: %s]\n' "$path"
  exit 0
}

term="$(tmux display-message -p '#{client_termname}' 2>/dev/null || echo "${TERM:-}")"
has_kitten=0
command -v kitten >/dev/null 2>&1 && has_kitten=1
backend="$(choose_renderer "$term" "$has_kitten")"

size_kitten=()
size_chafa=()
[[ -n $cols && -n $rows ]] && {
  size_kitten=(--place "${cols}x${rows}@0x0")
  size_chafa=(--size "${cols}x${rows}")
}

case "$backend" in
kitten)
  kitten icat --clear --unicode-placeholder --transfer-mode=memory "${size_kitten[@]}" "$path" ;;
chafa-kitty)
  chafa -f kitty --passthrough tmux "${size_chafa[@]}" "$path" ;;
chafa-sixel)
  chafa -f sixel "${size_chafa[@]}" "$path" ;;
chafa-symbols)
  chafa -f symbols "${size_chafa[@]}" "$path" ;;
esac
```

- [ ] **Step 4: Run, verify pass**

Run: `bats tests/claude-images.bats`
Expected: PASS (all 12 tests).

- [ ] **Step 5: Lint**

Run: `shellcheck scripts/claude-image-render.sh`
Expected: no warnings.

- [ ] **Step 6: Commit**

```bash
git add scripts/claude-image-render.sh tests/claude-images.bats
git commit -m "feat: terminal-aware image render ladder (#32)"
```

---

## Task 3: Viewer (toggle + navigation)

The interactive loop can't be meaningfully bats-tested; it's verified manually in Task 7. Keep logic minimal and delegate rendering to Task 2.

**Files:**
- Create: `scripts/tmux-claude-images.sh`

- [ ] **Step 1: Implement the viewer**

`scripts/tmux-claude-images.sh`:
```bash
#!/usr/bin/env bash
# Toggle a split pane that browses the image manifest of the active Claude pane.
# Outer mode (keybind): toggle the viewer pane on/off.
# Inner mode (--view PANE): full-pane keyboard navigator.
set -euo pipefail

STATE_DIR="${CLAUDE_STATUS_DIR:-/tmp/claude-status}"
IMAGES_DIR="$STATE_DIR/images"
SELF="${BASH_SOURCE[0]}"

if [[ ${1:-} == --view ]]; then
  src_pane="$2"
  manifest="$IMAGES_DIR/${src_pane#%}.jsonl"
  mapfile -t lines <"$manifest"
  n=${#lines[@]}
  ((n > 0)) || {
    echo "no images"
    sleep 1
    exit 0
  }
  i=0
  while true; do
    clear
    path="$(jq -r '.path' <<<"${lines[$i]}")"
    src="$(jq -r '.source' <<<"${lines[$i]}")"
    read -r cols rows < <(tmux display-message -p '#{pane_width} #{pane_height}')
    claude-image-render "$path" "$cols" "$((rows - 1))" || true
    printf '\n[%d/%d] %s · %s   n/p · #=jump · q quit ' \
      "$((i + 1))" "$n" "$(basename "$path")" "$src"
    read -rsn1 key || break
    case "$key" in
    n) ((i = (i + 1) % n)) ;;
    p) ((i = (i - 1 + n) % n)) ;;
    q) break ;;
    [0-9])
      num=$((key - 1))
      ((num >= 0 && num < n)) && i=$num ;;
    esac
  done
  exit 0
fi

# Outer mode (keybind): toggle viewer pane for the active (Claude) pane.
src_pane="$(tmux display-message -p '#{pane_id}')"
manifest="$IMAGES_DIR/${src_pane#%}.jsonl"
[[ -s $manifest ]] || {
  tmux display-message "no images yet for this pane"
  exit 0
}

existing="$(tmux list-panes -F '#{pane_id} #{@claude_img_src}' |
  awk -v s="$src_pane" '$2 == s {print $1; exit}')"
if [[ -n $existing ]]; then
  tmux kill-pane -t "$existing"
  exit 0
fi

viewer="$(tmux split-window -h -P -F '#{pane_id}' "'$SELF' --view '$src_pane'")"
tmux set-option -p -t "$viewer" @claude_img_src "$src_pane"
```

- [ ] **Step 2: Lint**

Run: `shellcheck scripts/tmux-claude-images.sh`
Expected: no warnings. (If SC2329/SC2034 noise appears, match the disable-comment style already used in `scripts/lib-claude.sh`.)

- [ ] **Step 3: Commit**

```bash
git add scripts/tmux-claude-images.sh
git commit -m "feat: image-pane viewer (toggle + keyboard navigator) (#32)"
```

---

## Task 4: Plugin shim + hook registration

**Files:**
- Create: `claude-plugin/scripts/images.sh`
- Modify: `claude-plugin/hooks/hooks.json`

- [ ] **Step 1: Create the shim** (mirrors `claude-plugin/scripts/status.sh`)

`claude-plugin/scripts/images.sh`:
```bash
#!/usr/bin/env bash
# Degrade gracefully: lazytmux not installed, or CC outside a lazytmux pane →
# silently no-op instead of erroring on every PostToolUse event.
command -v claude-images-update >/dev/null 2>&1 || exit 0
exec claude-images-update
```

- [ ] **Step 2: Make it executable**

Run: `chmod +x claude-plugin/scripts/images.sh`

- [ ] **Step 3: Register in hooks.json**

In `claude-plugin/hooks/hooks.json`, the `PostToolUse` array currently holds one object. Add a second command to that same hooks array so it becomes:
```json
    "PostToolUse": [
      {
        "hooks": [
          {"type": "command", "command": "\"${CLAUDE_PLUGIN_ROOT}\"/scripts/status.sh processing"},
          {"type": "command", "command": "\"${CLAUDE_PLUGIN_ROOT}\"/scripts/images.sh"}
        ]
      }
    ],
```

- [ ] **Step 4: Validate JSON**

Run: `jq . claude-plugin/hooks/hooks.json >/dev/null`
Expected: no output, exit 0 (valid JSON).

- [ ] **Step 5: Commit**

```bash
git add claude-plugin/scripts/images.sh claude-plugin/hooks/hooks.json
git commit -m "feat: register image-manifest PostToolUse hook (#32)"
```

---

## Task 5: Nix wiring (package scripts, chafa dep, keybind)

**Files:**
- Modify: `config/tmux.conf.nix` — `scriptNames` (line ~172), `tmux-wrapped` PATH (line ~576), keybind (line ~386)

- [ ] **Step 1: Add the three scripts to `scriptNames`**

In `config/tmux.conf.nix`, the `scriptNames` list (starts line 172) — add three entries (they take the default `mkScript` branch in the `genAttrs` dispatch, so no other change needed):
```nix
  scriptNames = [
    "claude-status"
    "claude-status-update"
    "claude-images-update"
    "claude-image-render"
    "tmux-claude-images"
    "tmux-reflow-windows"
    "tmux-session-picker"
    "tmux-window-picker"
    "tmux-update-icons"
    "tmux-branch-display"
    "tmux-dir-display"
    "tmux-apply-theme-colors"
    "tmux-scratchpad"
    "tmux-issue-stamp"
    "tmux-issue-stamp-linear"
    "tmux-issue-stamp-github"
    "tmux-pr-enrich"
  ];
```

- [ ] **Step 2: Add `chafa` to the wrapped-tmux PATH**

In the `tmux-wrapped` `wrapProgram` call (line ~576), add `pkgs.chafa` to the `makeBinPath` list:
```nix
        --prefix PATH : ${lib.makeBinPath ([pkgs.tmux] ++ scripts ++ [pkgs.lazygit pkgs.yazi pkgs.btop pkgs.zoxide pkgs.jq pkgs.util-linux pkgs.coreutils pkgs.xdg-utils pkgs.chafa])}
```

- [ ] **Step 3: Bind `prefix + I`**

In the keybind section, after the scratchpad bind (line 386, `bind p run-shell ... tmux-scratchpad ...`), add:
```nix
    bind I run-shell '${script.tmux-claude-images}/bin/tmux-claude-images'
```

- [ ] **Step 4: Confirm `I` is unbound elsewhere**

Run: `rg -n 'bind(-key)? +"?I"?\b' config/tmux.conf.nix`
Expected: only the line you just added. (Enrich uses lowercase `i` at line 411 — different binding.) If `I` is taken, choose another free key and update both here and the spec.

- [ ] **Step 5: Build the package**

Run: `nix build .#default 2>&1 | tail -20`
Expected: builds successfully; `result` symlink updates. (Pre-commit will reformat `.nix` on commit — see Step 7.)

- [ ] **Step 6: Verify the scripts landed on the wrapped PATH**

Run: `./result/bin/tmux -V` then `ls result/bin/ | rg 'claude-image|tmux-claude-images|chafa'`
Expected: `claude-images-update`, `claude-image-render`, `tmux-claude-images` present (chafa is on the wrapped PATH, not necessarily a top-level bin symlink — that's fine).

- [ ] **Step 7: Commit** (alejandra/statix run as pre-commit; if the first commit reports reformatting, `git add -u` and re-run the same commit)

```bash
git add config/tmux.conf.nix
git commit -m "feat: wire image-pane scripts, chafa dep, prefix+I bind (#32)"
```

---

## Task 6: Extend the dead-pane sweep

**Files:**
- Modify: `scripts/claude-status-update.sh` (lines 11-13 and ~47)

- [ ] **Step 1: Define the images dir**

In `scripts/claude-status-update.sh`, after line 13 (`ISSUES_DIR="$STATE_DIR/issues"`), add:
```bash
IMAGES_DIR="$STATE_DIR/images"
```

- [ ] **Step 2: Drop the manifest when a pane is swept**

In `cleanup_stale_panes`, the pane-removal line (currently line ~47):
```bash
			rm -f "$pf" "$ISSUES_DIR/${pf##*/}"
```
becomes:
```bash
			rm -f "$pf" "$ISSUES_DIR/${pf##*/}" "$IMAGES_DIR/${pf##*/}.jsonl"
```

- [ ] **Step 3: Lint**

Run: `shellcheck scripts/claude-status-update.sh`
Expected: no new warnings versus the pre-edit baseline. (`IMAGES_DIR` is now used, so no SC2034.)

- [ ] **Step 4: Commit**

```bash
git add scripts/claude-status-update.sh
git commit -m "feat: sweep image manifests with their dead panes (#32)"
```

---

## Task 7: CI check + end-to-end verification

**Files:**
- Modify: `flake.nix` (checks block, after `claude-issues-tests` ~line 95)

- [ ] **Step 1: Add a flake check for the new bats file**

In `flake.nix`, inside `checks = { ... }`, after the `claude-issues-tests` attr, add:
```nix
          claude-images-tests =
            pkgs.runCommand "claude-images-tests" {
              nativeBuildInputs = [pkgs.bats pkgs.jq pkgs.coreutils];
            } ''
              cp -r ${./scripts} scripts
              cp -r ${./tests} tests
              bats tests/claude-images.bats
              touch $out
            '';
```

- [ ] **Step 2: Run the check in isolation**

Run: `nix build .#checks.x86_64-linux.claude-images-tests 2>&1 | tail -20`
Expected: builds (all 12 bats tests pass in the sandbox).

- [ ] **Step 3: Commit**

```bash
git add flake.nix
git commit -m "test: add claude-images bats check to flake (#32)"
```

- [ ] **Step 4: Manual end-to-end — kitty (graphics rung)**

In a kitty terminal, run the wrapped tmux: `./result/bin/tmux`. Inside it, start `claude`, have it `Read` an image (e.g. `/run/current-system/sw/share/hypr/wall0.png`), then press `prefix I`.
Expected: a split pane opens showing the wallpaper crisply; `n`/`p` cycle if multiple images; `q` then `prefix I` again toggles it closed.

- [ ] **Step 5: Manual end-to-end — foot (sixel rung)**

Repeat Step 4 in a `foot` terminal.
Expected: image renders via sixel (tmux composites it). Slightly different fidelity than kitty; must be visible, not blank.

- [ ] **Step 6: Manual end-to-end — symbols floor**

Force the floor: `TERM=dumb` is not enough (detection uses `client_termname`); instead temporarily rename/hide `kitten` and run inside a non-kitty/non-sixel client, OR add a one-off `--choose` sanity check: `./result/bin/... claude-image-render --choose vt100 0` → `chafa-symbols`. Then render: `claude-image-render /path/to.png 80 24` in such a context.
Expected: block-art image (no blank pane). Confirms the universal floor.

- [ ] **Step 7: Push & open PR**

```bash
git push -u origin feat/32-claude-image-pane
gh pr create --assignee @me --title "feat: Claude conversation image pane (#32)" --body "Closes #32. Toggle (prefix+I) a split pane gallery of images the Claude session touched. See docs/superpowers/specs/2026-06-08-claude-image-pane-design.md. Renderer ladder: kitten icat → chafa kitty → chafa sixel → chafa symbols. Verified in kitty + foot + symbols floor."
```

---

## Self-review (completed)

- **Spec coverage:** manifest (T1), appender + `lib`-equivalent self-contained pattern (T1), renderer ladder all 4 rungs (T2), split-pane toggle + navigator (T3), hook registration (T4), nix wiring + `prefix+I` + chafa dep (T5), dead-pane sweep cleanup (T6), CI + kitty/foot/symbols e2e (T7). Non-goals (no fzf, images-only, no live-while-open) honored.
- **Placeholders:** none — every code step has complete content; manual e2e steps give exact commands and expected observations.
- **Type/name consistency:** `claude-images-update`, `claude-image-render` (with `--choose` test hook and `kitten`/`chafa-kitty`/`chafa-sixel`/`chafa-symbols` backend ids), `tmux-claude-images` (`--view` mode, `@claude_img_src` pane option), `$IMAGES_DIR`, manifest `images/<pane>.jsonl`, line shape `{type,path,source,ts,mtime}` — used identically across appender, viewer, sweep, and tests.
