package graphics

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
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

// The #556 regression net: Localize must not hold the fetcher lock across Run,
// or LocalizeBatch's goroutines would serialize behind the slowest fetch and
// the batch would cost N round-trips again.
func TestFetcherLocalizeRunsConcurrently(t *testing.T) {
	dir := t.TempDir()
	started := make(chan string, 2)
	release := make(chan struct{})
	f := &SSHFetcher{Host: "g6", CacheDir: dir, MaxBytes: 1 << 20, Run: func(ctx context.Context, args ...string) ([]byte, error) {
		// The remote path is the third-to-last argument (then key, max-bytes).
		started <- args[len(args)-3]
		select {
		case <-release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return []byte("1700000000 5\nHELLO"), nil
	}}
	var wg sync.WaitGroup
	locals := make([]string, 2)
	errs := make([]error, 2)
	for i, p := range []string{"/tmp/a.png", "/tmp/b.png"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			locals[i], errs[i] = f.Localize(context.Background(), p)
		}()
	}
	// Both fetches must be in flight before either completes.
	seen := map[string]bool{<-started: true, <-started: true}
	close(release)
	wg.Wait()
	if !seen["'/tmp/a.png'"] || !seen["'/tmp/b.png'"] {
		t.Fatalf("fetches serialized or lost: %v", seen)
	}
	for i := range errs {
		if errs[i] != nil {
			t.Fatal(errs[i])
		}
	}
	if locals[0] == locals[1] {
		t.Fatalf("distinct paths cached to the same file: %s", locals[0])
	}
}

// Two concurrent fetches of the SAME path share one ssh round-trip: the
// second waits on the first's outcome rather than double-transferring.
func TestFetcherDedupsConcurrentFetchesOfOnePath(t *testing.T) {
	dir := t.TempDir()
	var runs atomic.Int32
	release := make(chan struct{})
	f := &SSHFetcher{Host: "g6", CacheDir: dir, MaxBytes: 1 << 20, Run: func(ctx context.Context, args ...string) ([]byte, error) {
		runs.Add(1)
		<-release
		return []byte("1700000000 5\nHELLO"), nil
	}}
	var wg sync.WaitGroup
	locals := make([]string, 2)
	errs := make([]error, 2)
	for i := range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			locals[i], errs[i] = f.Localize(context.Background(), "/tmp/a.png")
		}()
	}
	// Let the first fetch reach Run and the second park on the inflight call.
	for runs.Load() == 0 {
		runtime.Gosched()
	}
	time.Sleep(20 * time.Millisecond)
	close(release)
	wg.Wait()
	if n := runs.Load(); n != 1 {
		t.Fatalf("Run called %d times for one path, want 1", n)
	}
	if errs[0] != nil || errs[1] != nil {
		t.Fatalf("errs = %v", errs)
	}
	if locals[0] != locals[1] {
		t.Fatalf("waiters got different results: %q vs %q", locals[0], locals[1])
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
