package source

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// Store holds the current Snapshot and refreshes it from a Source.
// All methods are safe for concurrent use.
type Store struct {
	src Source
	opt Options
	log *slog.Logger

	snap    atomic.Pointer[Snapshot]
	success atomic.Int64 // unix nanoseconds of the last successful refresh; 0 = never

	refreshMu sync.Mutex // one refresh at a time
	// buildMu guards records and the order of swaps: every snapshot is built
	// and swapped in under it, from the records read last.
	buildMu sync.Mutex
	records []Record
	read    bool // records holds a successful read
	hooksMu sync.Mutex
	hooks   []func(*Snapshot)
}

// NewStore returns a Store serving an empty snapshot until the first Refresh. log may be nil.
func NewStore(src Source, opt Options, log *slog.Logger) *Store {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	st := &Store{src: src, opt: opt, log: log}
	st.snap.Store(emptySnapshot)
	return st
}

// Snapshot returns the current snapshot. It is never nil. Take it once per
// request and use that one value throughout, so a swap cannot split a response.
func (st *Store) Snapshot() *Snapshot { return st.snap.Load() }

// OnSwap registers fn to be called with each new snapshot after it has been
// swapped in, by Refresh or by Rebuild. Hooks run on the goroutine that
// swapped, in the order registered: a hook that does real work (the warmer)
// must hand it to its own goroutine and return, and must be safe to call
// from two goroutines at once (a Rebuild during a Refresh). They are not
// called when a refresh fails.
func (st *Store) OnSwap(fn func(*Snapshot)) {
	st.hooksMu.Lock()
	st.hooks = append(st.hooks, fn)
	st.hooksMu.Unlock()
}

// Refresh reads the source, builds a snapshot and swaps it in. If reading
// fails, the previous snapshot stays in place and the error is returned.
func (st *Store) Refresh(ctx context.Context) error {
	st.refreshMu.Lock()
	defer st.refreshMu.Unlock()
	start := time.Now()
	records, err := st.src.Records(ctx)
	if err != nil {
		return err
	}
	st.buildMu.Lock()
	st.records, st.read = records, true
	snap := Build(records, st.opt)
	st.snap.Store(snap)
	st.buildMu.Unlock()
	st.success.Store(time.Now().UnixNano())
	st.log.Info("snapshot refreshed", "items", len(records), "records", snap.Records(), "tiles", snap.Len(),
		"duration", time.Since(start).Round(time.Millisecond))
	st.swapped(snap)
	return nil
}

// Rebuild builds a snapshot again from the records read last, without
// reading the source, and swaps it in. Call it after something Options.Hidden
// reports has changed: when it returns, the current snapshot reflects every
// such change made before the call, whatever refresh runs at the same time.
// It does not wait for a refresh that is reading the source, and it does not
// count as one (LastRefresh, Age and Healthy are about reading). Before the
// first successful refresh there is nothing to build from and it does nothing.
func (st *Store) Rebuild() {
	st.buildMu.Lock()
	if !st.read {
		st.buildMu.Unlock()
		return
	}
	snap := Build(st.records, st.opt)
	st.snap.Store(snap)
	st.buildMu.Unlock()
	st.swapped(snap)
}

// swapped calls the OnSwap hooks for snap.
func (st *Store) swapped(snap *Snapshot) {
	st.hooksMu.Lock()
	hooks := append([]func(*Snapshot){}, st.hooks...)
	st.hooksMu.Unlock()
	for _, fn := range hooks {
		fn(snap)
	}
}

// Run refreshes every interval until ctx is cancelled, starting with a refresh
// straight away unless one has already succeeded. A failed refresh is logged
// and the previous snapshot is kept. Each refresh is given at most
// max(interval, 30 s) to finish.
func (st *Store) Run(ctx context.Context, interval time.Duration) {
	timeout := max(interval, 30*time.Second)
	refresh := func() {
		rctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		if err := st.Refresh(rctx); err != nil && !errors.Is(ctx.Err(), context.Canceled) {
			if st.success.Load() == 0 {
				st.log.Error("reading the bot's records failed; nothing has been read yet", "error", err)
			} else {
				st.log.Error("snapshot refresh failed; keeping the previous snapshot", "error", err, "age", st.Age().Round(time.Second))
			}
		}
	}
	if st.success.Load() == 0 {
		refresh()
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			refresh()
		}
	}
}

// LastRefresh is the time of the last successful refresh; the zero time if there has been none.
func (st *Store) LastRefresh() time.Time {
	ns := st.success.Load()
	if ns == 0 {
		return time.Time{}
	}
	return time.Unix(0, ns)
}

// Age is the time since the last successful refresh; the largest Duration if there has been none.
func (st *Store) Age() time.Duration {
	ns := st.success.Load()
	if ns == 0 {
		return time.Duration(1<<63 - 1)
	}
	return time.Since(time.Unix(0, ns))
}

// Healthy reports whether a refresh has succeeded within maxAge
// (/healthz uses 3 x GALLERY_REFRESH).
func (st *Store) Healthy(maxAge time.Duration) bool {
	return st.success.Load() != 0 && st.Age() < maxAge
}
