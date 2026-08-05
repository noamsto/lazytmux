package graphics

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestFetcherWritesBytesToCacheAndReturnsLocalPath(t *testing.T) {
	dir := t.TempDir()
	var gotArgs []string
	f := &SSHFetcher{
		Host: "g6", CtlSock: "/run/x.sock", CacheDir: dir, MaxBytes: 1 << 20,
		Run: func(ctx context.Context, args ...string) ([]byte, error) {
			gotArgs = args
			return []byte("1700000000 5\nHELLO"), nil
		},
	}
	local, err := f.Localize(context.Background(), "/tmp/a.png")
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(local)
	if err != nil || string(b) != "HELLO" {
		t.Fatalf("cached content = %q err=%v", b, err)
	}
	if filepath.Dir(local) != dir {
		t.Fatalf("wrote outside the cache dir: %s", local)
	}
	joined := strings.Join(gotArgs, " ")
	if !strings.Contains(joined, "-S /run/x.sock") || !strings.Contains(joined, "g6") {
		t.Fatalf("did not use the ControlMaster socket: %v", gotArgs)
	}
}

// The caller's context has to reach Run unchanged: NewSSHFetcher's production
// Run wraps exec.CommandContext, and only the exec itself dying on cancel (not
// a goroutine-plus-select wrapper around it) keeps a timed-out ssh from
// running forever in the background (spec D4).
func TestFetcherThreadsTheCallersContextToRun(t *testing.T) {
	dir := t.TempDir()
	type key struct{}
	ctx := context.WithValue(context.Background(), key{}, "marker")
	var gotCtx context.Context
	f := &SSHFetcher{Host: "g6", CacheDir: dir, MaxBytes: 1 << 20, Run: func(ctx context.Context, args ...string) ([]byte, error) {
		gotCtx = ctx
		return []byte("1700000000 5\nHELLO"), nil
	}}
	if _, err := f.Localize(ctx, "/tmp/a.png"); err != nil {
		t.Fatal(err)
	}
	if gotCtx.Value(key{}) != "marker" {
		t.Fatal("Localize did not pass the caller's context through to Run")
	}
}

func TestFetcherSecondCallIsAHitAndTransfersNothing(t *testing.T) {
	dir := t.TempDir()
	calls := 0
	f := &SSHFetcher{
		Host: "g6", CacheDir: dir, MaxBytes: 1 << 20,
		Run: func(ctx context.Context, args ...string) ([]byte, error) {
			calls++
			if calls == 1 {
				return []byte("1700000000 5\nHELLO"), nil
			}
			// Same mtime+size: the remote script prints the key and exits
			// without cat-ing.
			return []byte("1700000000 5\n"), nil
		},
	}
	first, err := f.Localize(context.Background(), "/tmp/a.png")
	if err != nil {
		t.Fatal(err)
	}
	second, err := f.Localize(context.Background(), "/tmp/a.png")
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("cache miss on an unchanged file: %s vs %s", first, second)
	}
}

func TestFetcherTreatsAChangedMtimeAsANewFile(t *testing.T) {
	dir := t.TempDir()
	calls := 0
	f := &SSHFetcher{
		Host: "g6", CacheDir: dir, MaxBytes: 1 << 20,
		Run: func(ctx context.Context, args ...string) ([]byte, error) {
			calls++
			if calls == 1 {
				return []byte("1700000000 1\nA"), nil
			}
			return []byte("1700000009 1\nB"), nil
		},
	}
	first, _ := f.Localize(context.Background(), "/tmp/scratch.raw")
	second, _ := f.Localize(context.Background(), "/tmp/scratch.raw")
	if first == second {
		t.Fatal("a rewritten scratch frame must not reuse the old cache entry")
	}
	b, _ := os.ReadFile(second)
	if string(b) != "B" {
		t.Fatalf("second content = %q", b)
	}
}

func TestFetcherRejectsOversizeAndBadReplies(t *testing.T) {
	dir := t.TempDir()
	// Over the cap the remote script exits 3 without cat-ing, which surfaces as
	// a non-zero ssh exit.
	over := &SSHFetcher{Host: "g6", CacheDir: dir, MaxBytes: 4, Run: func(context.Context, ...string) ([]byte, error) {
		return nil, errors.New("exit status 3")
	}}
	if _, err := over.Localize(context.Background(), "/tmp/big.raw"); err == nil {
		t.Fatal("oversize fetch must error so the store is dropped")
	}
	bad := &SSHFetcher{Host: "g6", CacheDir: dir, MaxBytes: 1 << 20, Run: func(context.Context, ...string) ([]byte, error) {
		return []byte("garbage"), nil
	}}
	if _, err := bad.Localize(context.Background(), "/tmp/a.png"); err == nil {
		t.Fatal("unparsable reply must error")
	}
}

func TestFetcherRecoversWhenTheCachedCopyIsGone(t *testing.T) {
	dir := t.TempDir()
	calls := 0
	f := &SSHFetcher{Host: "g6", CacheDir: dir, MaxBytes: 1 << 20, Run: func(context.Context, ...string) ([]byte, error) {
		calls++
		if calls == 2 {
			return []byte("1700000000 1\n"), nil // header only: "you already have it"
		}
		return []byte("1700000000 1\nA"), nil
	}}
	local, _ := f.Localize(context.Background(), "/tmp/a.png")
	os.Remove(local) // pruned, or the daemon restarted
	if _, err := f.Localize(context.Background(), "/tmp/a.png"); err == nil {
		t.Fatal("a lost cache entry must error once")
	}
	if _, err := f.Localize(context.Background(), "/tmp/a.png"); err != nil {
		t.Fatalf("and then recover by refetching, got %v", err)
	}
}

// ssh space-joins the post-host argv into ONE string that the remote login
// shell re-parses — that second parse is what shQuote has to survive, not the
// first. This drives an outer `sh -c` over the joined argv (standing in for
// the remote login shell) around an inner `sh -c "$1"` echo, exactly as
// Localize's argv shape does, so a break in either parse shows up here rather
// than only against a live remote.
func TestShQuoteSurvivesSSHsDoubleParse(t *testing.T) {
	weird := `/tmp/a "quoted" it's got spaces.png`
	args := []string{"sh", "-c", shQuote(`printf '%s' "$1"`), "_", shQuote(weird)}
	out, err := exec.Command("sh", "-c", strings.Join(args, " ")).Output()
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != weird {
		t.Fatalf("round-trip = %q, want %q", out, weird)
	}
}
