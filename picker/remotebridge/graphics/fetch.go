package graphics

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// remoteFetch runs on the remote host. It prints "<mtime> <size>" first, then
// the bytes — but only when the caller's cached key differs and the file is
// within the cap. One round trip covers stat, cache validation and transfer,
// and a cache hit transfers nothing but the header.
//
// $1 path, $2 the caller's cached "<mtime> <size>" (empty if none), $3 max bytes.
const remoteFetch = `p=$1; k=$2; m=$3
s=$(stat -c '%Y %s' -- "$p" 2>/dev/null || stat -f '%m %z' -- "$p") || exit 1
printf '%s\n' "$s"
[ "$s" = "$k" ] && exit 0
sz=${s#* }
[ "$sz" -gt "$m" ] && exit 3
exec cat -- "$p"`

// cachePrune is the total-bytes ceiling for the fetch cache, and how often
// (in fetches) it is enforced.
const (
	cacheCap      = 256 << 20
	pruneInterval = 64
)

// SSHFetcher localises a remote path by copying it into a local cache over the
// daemon's ssh ControlMaster socket, so no fetch pays a new handshake and image
// bytes never share the control stream with live terminal output.
type SSHFetcher struct {
	Host     string
	CtlSock  string
	CacheDir string
	MaxBytes int64
	// Run executes ssh; injected so tests never touch the network. It takes the
	// context so production can use exec.CommandContext — cancellation has to
	// kill the ssh process, not merely stop waiting on it.
	Run func(ctx context.Context, args ...string) ([]byte, error)

	mu      sync.Mutex
	keys    map[string]string // remote path -> last seen "<mtime> <size>"
	locals  map[string]string // "<path>\x00<key>" -> local file
	fetches int
}

// NewSSHFetcher builds the production fetcher.
func NewSSHFetcher(host, ctlSock, cacheDir string, maxBytes int64) *SSHFetcher {
	f := &SSHFetcher{Host: host, CtlSock: ctlSock, CacheDir: cacheDir, MaxBytes: maxBytes}
	f.Run = func(ctx context.Context, args ...string) ([]byte, error) {
		return exec.CommandContext(ctx, "ssh", args...).Output()
	}
	_ = os.MkdirAll(cacheDir, 0o700)
	f.prune()
	return f
}

func (f *SSHFetcher) Localize(ctx context.Context, remote string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.keys == nil {
		f.keys, f.locals = map[string]string{}, map[string]string{}
	}
	if err := os.MkdirAll(f.CacheDir, 0o700); err != nil {
		return "", err
	}

	args := []string{}
	if f.CtlSock != "" {
		args = append(args, "-S", f.CtlSock)
	}
	args = append(args, "-T", f.Host, "--", "sh", "-c", shQuote(remoteFetch), "_",
		shQuote(remote), shQuote(f.keys[remote]), strconv.FormatInt(f.MaxBytes, 10))

	out, err := f.Run(ctx, args...)
	if err != nil {
		return "", fmt.Errorf("fetch %s: %w", remote, err)
	}
	nl := strings.IndexByte(string(out), '\n')
	if nl < 0 {
		return "", fmt.Errorf("fetch %s: no header in reply", remote)
	}
	key := strings.TrimSpace(string(out[:nl]))
	if len(strings.Fields(key)) != 2 {
		return "", fmt.Errorf("fetch %s: bad header %q", remote, key)
	}
	body := out[nl+1:]

	ck := remote + "\x00" + key
	if len(body) == 0 {
		// The remote skipped the transfer because our cached key matched.
		if local, ok := f.locals[ck]; ok {
			if _, statErr := os.Stat(local); statErr == nil {
				f.keys[remote] = key
				return local, nil
			}
		}
		// We claimed a copy we no longer have (daemon restart, pruned entry).
		// Forget the key, or every later call would keep asking for the same
		// no-op transfer and this path would never recover.
		delete(f.keys, remote)
		return "", fmt.Errorf("fetch %s: cached copy is gone, refetching next time", remote)
	}
	f.keys[remote] = key

	sum := sha256.Sum256([]byte(ck))
	local := filepath.Join(f.CacheDir, hex.EncodeToString(sum[:])[:32]+".bin")
	if err := os.WriteFile(local, body, 0o600); err != nil {
		return "", err
	}
	f.locals[ck] = local
	if f.fetches++; f.fetches%pruneInterval == 0 {
		f.prune()
	}
	return local, nil
}

// prune drops the oldest cache entries until the directory is under cacheCap.
func (f *SSHFetcher) prune() {
	ents, err := os.ReadDir(f.CacheDir)
	if err != nil {
		return
	}
	type ent struct {
		path string
		size int64
		mod  int64
	}
	var all []ent
	var total int64
	for _, e := range ents {
		info, ierr := e.Info()
		if ierr != nil {
			continue
		}
		all = append(all, ent{filepath.Join(f.CacheDir, e.Name()), info.Size(), info.ModTime().UnixNano()})
		total += info.Size()
	}
	sort.Slice(all, func(i, j int) bool { return all[i].mod < all[j].mod })
	for _, e := range all {
		if total <= cacheCap {
			return
		}
		if os.Remove(e.path) == nil {
			total -= e.size
		}
	}
}

// shQuote single-quotes s for the remote login shell: ssh space-joins the
// post-host argv into one string the remote shell re-parses, so every argument
// has to survive that second parse intact.
func shQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
