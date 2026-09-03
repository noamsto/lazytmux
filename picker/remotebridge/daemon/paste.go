package daemon

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/noamsto/lazytmux/picker/remotebridge/controlmode"
)

// Clipboard image paste (#361). A local ctrl+v reaches an agent as the single
// byte 0x16; the agent then reads the system clipboard ITSELF. In a mirror
// pane that read happens on the remote, where the local image does not exist
// — so the daemon (the only component that is both local and holding the ssh
// ControlMaster) intercepts the byte here: read the local clipboard image,
// ship it over the bridge's own socket, and send-keys the remote temp path
// into the pane. Claude Code resolves an existing image path in the prompt
// into an attachment at submit (verified live, see the design spec), so the
// agent receives the image exactly as if the paste had been local.
//
// Everything here runs off pumpInput's goroutine except the cheap gate and
// clipboard probe; the upload+inject runs async so a slow link never freezes
// the pane's input.

const (
	// pasteMaxBytes mirrors the graphics fetcher's gfx-max-bytes default.
	pasteMaxBytes = 8 << 20
	// pasteTimeout bounds the whole upload. It is async, so it can be more
	// generous than the graphics fetcher's 2s without freezing anything, but
	// it stays bounded so a hung socket leaks no goroutine.
	pasteTimeout = 5 * time.Second
	// clipTimeout bounds one clipboard probe/extract: xclip asks the X
	// selection's owner for the data, and a wedged owner would otherwise
	// block the input pump's goroutine.
	clipTimeout = 2 * time.Second
)

// pasteAgentProcs is the gate set: remote foreground commands whose ctrl+v
// means "attach the clipboard image". Only claude is verified (codex has no
// clipboard read at all; cursor-agent is unmeasured). A pane running anything
// else gets the byte forwarded, preserving readline quoted-insert.
var pasteAgentProcs = map[string]bool{"claude": true}

// pastePathRe validates the path the remote store script prints before it is
// interpolated anywhere. The script is ours, but the reply crosses ssh and a
// remote shell, so it is treated as untrusted.
var pastePathRe = regexp.MustCompile(`^/tmp/lazytmux-paste/img-[A-Za-z0-9]+\.(png|jpe?g|gif|webp)$`)

// pasteExtRe gates the extension before it is interpolated into the remote
// mktemp template.
var pasteExtRe = regexp.MustCompile(`^(png|jpe?g|gif|webp)$`)

// pasteHandler intercepts ctrl+v on agent panes. Every side effect is an
// injected field so tests never touch ssh, tmux, or a clipboard.
type pasteHandler struct {
	upload func(ctx context.Context, ext string, data []byte) (string, error)
	// readClipboard returns the local clipboard's image bytes and extension;
	// ok is false when the clipboard holds no image (or no clipboard tool
	// exists), err only when an image was seen but could not be extracted.
	readClipboard func() (data []byte, ext string, ok bool, err error)
	// procFor reports the remote pane's foreground command (@bridge_proc).
	procFor func(remotePane string) string
	// notify shows a message on the mirror session's client.
	notify func(msg string)
}

// paster builds the handler for one pumpInput, or nil when the Config cannot
// ship files (tests, --test-local): a nil handler forwards input verbatim.
func (c Config) paster() *pasteHandler {
	if c.PasteUpload == nil {
		return nil
	}
	return &pasteHandler{
		upload:        c.PasteUpload,
		readClipboard: readClipboardImage,
		procFor:       func(remotePane string) string { return bridgeProc(c, remotePane) },
		notify:        func(msg string) { notifyLocal(c, msg) },
	}
}

// handle returns the bytes to forward to the remote pane. A 0x16 outside a
// bracketed paste on an agent pane with an image on the clipboard is
// swallowed and replaced by an async upload+inject; every other case forwards
// the payload untouched, so text paste, quoted-insert and empty clipboards
// behave exactly as they did before this interception existed.
func (h *pasteHandler) handle(remotePane string, payload []byte, send func(string)) []byte {
	kept, drops := splitPasteDrops(payload)
	if drops == 0 {
		return payload
	}
	if !pasteAgentProcs[h.procFor(remotePane)] {
		return payload
	}
	data, ext, ok, err := h.readClipboard()
	switch {
	case err != nil:
		h.notify("lazytmux: clipboard image read failed: " + err.Error())
		return kept
	case !ok:
		return payload
	default:
		go h.paste(remotePane, data, ext, send)
		return kept
	}
}

// paste ships one clipboard image to the remote and injects the resulting
// path into the pane's prompt. Every failure is a visible no-op: the byte was
// already swallowed, so nothing reaching for the remote's (empty) clipboard
// runs in its place and lies about what happened.
func (h *pasteHandler) paste(remotePane string, data []byte, ext string, send func(string)) {
	// Claude Code's path-inlining regex excludes bmp, and no converter is
	// guaranteed on either host — report rather than ship a dead path.
	if !pasteExtRe.MatchString(ext) {
		h.notify("lazytmux: clipboard image format not pasteable (." + ext + "; copy as png)")
		return
	}
	if int64(len(data)) > pasteMaxBytes {
		h.notify(fmt.Sprintf("lazytmux: clipboard image too large (%.1f MiB, cap %d MiB)",
			float64(len(data))/(1<<20), pasteMaxBytes>>20))
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), pasteTimeout)
	defer cancel()
	path, err := h.upload(ctx, ext, data)
	if err != nil {
		h.notify("lazytmux: image upload to remote failed: " + err.Error())
		return
	}
	if !pastePathRe.MatchString(path) {
		h.notify("lazytmux: remote returned an unexpected paste path")
		return
	}
	// The trailing space keeps whatever the user types next from merging into
	// the path token, which would silently stop the agent recognising it.
	// Hex send-keys sidesteps every quoting layer between here and the pane.
	for _, args := range controlmode.SendKeysArgs(remotePane, append([]byte(path), ' '), controlmode.InputChunkBytes) {
		send(strings.Join(args, " "))
	}
}

// splitPasteDrops copies payload without its ctrl+v bytes (0x16), counting
// the drops. A 0x16 inside a bracketed-paste bracket (ESC[200~ … ESC[201~)
// is pasted content, not the gesture, and is kept. The in-paste state is per
// payload: tmux delivers a bracketed paste in one read in practice, and a
// marker split across two reads is pathological enough to ignore.
func splitPasteDrops(payload []byte) (kept []byte, drops int) {
	const (
		begin = "\x1b[200~"
		end   = "\x1b[201~"
	)
	kept = make([]byte, 0, len(payload))
	inPaste := false
	for i := 0; i < len(payload); {
		switch {
		case !inPaste && strings.HasPrefix(string(payload[i:]), begin):
			inPaste = true
			kept = append(kept, begin...)
			i += len(begin)
		case inPaste && strings.HasPrefix(string(payload[i:]), end):
			inPaste = false
			kept = append(kept, end...)
			i += len(end)
		default:
			if payload[i] == 0x16 && !inPaste {
				drops++
			} else {
				kept = append(kept, payload[i])
			}
			i++
		}
	}
	return kept, drops
}

// imageTarget picks the best image MIME target offered by a clipboard and
// maps it to the file extension the remote temp file will carry. Preference
// order matches what an agent can inline (png first); bmp is last because it
// is reported unsupported rather than shipped.
func imageTarget(targets []string) (target, ext string) {
	pref := []struct{ mime, ext string }{
		{"image/png", "png"},
		{"image/jpeg", "jpg"},
		{"image/jpg", "jpg"},
		{"image/webp", "webp"},
		{"image/gif", "gif"},
		{"image/bmp", "bmp"},
	}
	offered := map[string]bool{}
	for _, t := range targets {
		offered[strings.TrimSpace(t)] = true
	}
	for _, p := range pref {
		if offered[p.mime] {
			return p.mime, p.ext
		}
	}
	return "", ""
}

// readClipboardImage probes the local clipboard with the same tools Claude
// Code itself uses (xclip, then wl-paste) and extracts the image with the
// tool that listed it. A missing tool, a missing display, or a clipboard
// with no image target is ok=false — the caller forwards the keypress and
// nothing about today's behaviour changes.
func readClipboardImage() (data []byte, ext string, ok bool, err error) {
	tools := []struct {
		name       string
		listArgs   []string
		extractArg func(target string) []string
	}{
		{"xclip",
			[]string{"-selection", "clipboard", "-t", "TARGETS", "-o"},
			func(t string) []string { return []string{"-selection", "clipboard", "-t", t, "-o"} }},
		{"wl-paste",
			[]string{"-l"},
			func(t string) []string { return []string{"--type", t} }},
	}
	for _, tool := range tools {
		if _, lookErr := exec.LookPath(tool.name); lookErr != nil {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), clipTimeout)
		out, listErr := exec.CommandContext(ctx, tool.name, tool.listArgs...).Output()
		cancel()
		if listErr != nil {
			continue
		}
		target, ext := imageTarget(strings.Split(string(out), "\n"))
		if target == "" {
			continue
		}
		ctx, cancel = context.WithTimeout(context.Background(), clipTimeout)
		data, err := exec.CommandContext(ctx, tool.name, tool.extractArg(target)...).Output()
		cancel()
		if err != nil {
			return nil, "", false, fmt.Errorf("%s extract %s: %w", tool.name, target, err)
		}
		return data, ext, true, nil
	}
	return nil, "", false, nil
}

// bridgeProc reads the mirror pane's @bridge_proc — the REMOTE pane's
// foreground command, stamped by the agent shipper because the local pane
// only ever runs a renderer. One fork per ctrl+v: the gesture is rare, so
// freshness beats caching.
func bridgeProc(cfg Config, remotePane string) string {
	out, err := cfg.LocalTmuxOut("list-panes", "-s", "-t", cfg.LocalSess, "-F", "#{@bridge_pane}|#{@bridge_proc}")
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		pane, proc, found := strings.Cut(line, "|")
		if found && pane == remotePane {
			return proc
		}
	}
	return ""
}

// notifyLocal shows msg on the client viewing the mirror session. A detached
// session has no client to show it on, and the paste's async context has no
// better channel — the message is best-effort by construction.
func notifyLocal(cfg Config, msg string) {
	out, err := cfg.LocalTmuxOut("list-clients", "-t", cfg.LocalSess, "-F", "#{client_name}")
	if err != nil {
		return
	}
	client, _, _ := strings.Cut(string(out), "\n")
	if client == "" {
		return
	}
	cfg.LocalTmux("display-message", "-c", client, "-d", "5000", msg)
}
