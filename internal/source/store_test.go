package source

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

// lockedBuffer is a log sink that can be read while a goroutine is still logging.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// flakySource returns its records, or err when set.
type flakySource struct {
	mu      sync.Mutex
	records []Record
	err     error
	calls   int
}

func (f *flakySource) Records(ctx context.Context) ([]Record, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return append([]Record(nil), f.records...), nil
}

func (f *flakySource) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func (f *flakySource) set(records []Record, err error) {
	f.mu.Lock()
	f.records, f.err = records, err
	f.mu.Unlock()
}

func TestStoreRefreshAndFailure(t *testing.T) {
	f, records := fixtureRecords(t)
	src := &flakySource{records: records}
	var logs bytes.Buffer
	st := NewStore(src, f.options(), slog.New(slog.NewTextHandler(&logs, nil)))

	// Before the first refresh: an empty snapshot, never nil, and not healthy.
	if s := st.Snapshot(); s == nil || s.Len() != 0 || !s.Built().IsZero() {
		t.Fatalf("initial snapshot = %+v", s)
	}
	if st.Healthy(time.Hour) || !st.LastRefresh().IsZero() || st.Age() < 100*365*24*time.Hour {
		t.Error("a store that never refreshed must not be healthy")
	}
	if _, _, err := st.Snapshot().Page("", 10); err != nil {
		t.Errorf("paging the empty snapshot: %v", err)
	}

	var swaps []*Snapshot
	st.OnSwap(func(s *Snapshot) { swaps = append(swaps, s) })
	order := ""
	st.OnSwap(func(*Snapshot) { order += "b" })

	if err := st.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	first := st.Snapshot()
	if first.Len() != 19 || len(swaps) != 1 || swaps[0] != first || order != "b" {
		t.Fatalf("after refresh: len=%d swaps=%d", first.Len(), len(swaps))
	}
	if !st.Healthy(time.Minute) || st.Age() > time.Minute || st.LastRefresh().IsZero() {
		t.Error("store should be healthy after a refresh")
	}
	if st.Healthy(0) {
		t.Error("Healthy(0) must be false")
	}
	if !strings.Contains(logs.String(), "tiles=19") || strings.Contains(logs.String(), "FIXTURE") {
		t.Errorf("refresh log: %s", logs.String())
	}

	// A failed refresh keeps the previous snapshot, does not call hooks and does not reset the age.
	last := st.LastRefresh()
	boom := errors.New("scan failed")
	src.set(nil, boom)
	if err := st.Refresh(context.Background()); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	if st.Snapshot() != first || len(swaps) != 1 || !st.LastRefresh().Equal(last) {
		t.Error("failed refresh must keep the previous snapshot")
	}

	// Recovery swaps a new snapshot in; the old one is untouched for requests still using it.
	src.set(records[:3], nil)
	if err := st.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	second := st.Snapshot()
	if second == first || len(swaps) != 2 || swaps[1] != second || first.Len() != 19 || second.Len() >= first.Len() {
		t.Errorf("after recovery: first=%d second=%d swaps=%d", first.Len(), second.Len(), len(swaps))
	}
	// Liveness for original eviction: a key that left the listing is no longer live.
	live, dead := 0, 0
	for _, k := range first.Keys() {
		if second.Has(k) {
			live++
		} else {
			dead++
		}
	}
	if live != second.Len() || dead != first.Len()-second.Len() {
		t.Errorf("live=%d dead=%d", live, dead)
	}
}

func TestStoreRun(t *testing.T) {
	// In a synctest bubble the ticker runs on virtual time: the test takes no
	// real time, and Wait returns only once Run is idle again.
	synctest.Test(t, func(t *testing.T) {
		const interval = time.Minute
		f, records := fixtureRecords(t)
		src := &flakySource{records: records}
		var logs lockedBuffer
		st := NewStore(src, f.options(), slog.New(slog.NewTextHandler(&logs, nil)))
		// The values Run's goroutine writes are still locked or atomic: Wait orders
		// the test after Run, but the race detector wants every access ordered.
		var swapCount atomic.Int32
		st.OnSwap(func(*Snapshot) { swapCount.Add(1) })
		swaps := func() int32 { return swapCount.Load() }
		// synctest.Sleep lets virtual time pass and returns once the refreshes
		// that fell due are over.

		ctx, cancel := context.WithCancel(t.Context())
		var run sync.WaitGroup
		run.Go(func() { st.Run(ctx, interval) })
		synctest.Wait()
		if swaps() != 1 {
			t.Fatalf("after the start-up refresh: %d swaps, want 1", swaps())
		}
		synctest.Sleep(interval)
		if swaps() != 2 {
			t.Fatalf("after a timed refresh: %d swaps, want 2", swaps())
		}
		if st.Snapshot().Len() != 19 {
			t.Fatalf("len = %d", st.Snapshot().Len())
		}

		// The source starts failing: Run logs it and keeps serving the old snapshot.
		src.set(nil, errors.New("ProvisionedThroughputExceededException"))
		synctest.Sleep(interval)
		if !strings.Contains(logs.String(), "keeping the previous snapshot") {
			t.Fatal("failure was not logged")
		}
		if st.Snapshot().Len() != 19 || swaps() != 2 {
			t.Error("snapshot lost after a failed refresh")
		}
		if !st.Healthy(3 * interval) {
			t.Error("store should still be healthy after 1 missed refresh")
		}
		// Health goes stale while refreshes fail.
		synctest.Sleep(2 * interval)
		if st.Healthy(3 * interval) {
			t.Error("store should be unhealthy after 3 missed refreshes")
		}
		// ...and recovers.
		src.set(records, nil)
		synctest.Sleep(interval)
		if swaps() != 3 || !st.Healthy(3*interval) {
			t.Errorf("store should be healthy again (%d swaps, want 3)", swaps())
		}

		cancel()
		run.Wait() // the bubble reports a deadlock if Run does not stop
		calls := src.callCount()
		synctest.Sleep(10 * interval)
		if swaps() != 3 || src.callCount() != calls {
			t.Error("refreshes continued after cancel")
		}
	})
}

func TestStoreConcurrentReads(t *testing.T) {
	f, records := fixtureRecords(t)
	st := NewStore(Static(records), f.options(), nil)
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			for ctx.Err() == nil {
				s := st.Snapshot()
				tiles, _, err := s.Page("", 5)
				if err != nil || (s.Len() > 0 && len(tiles) == 0) {
					t.Errorf("read during refresh: %d tiles, %v", len(tiles), err)
					return
				}
				for _, tl := range tiles {
					if _, ok := s.FileID(tl.Key); !ok {
						t.Error("tile without a file id in its own snapshot")
						return
					}
				}
			}
		})
	}
	for range 200 {
		if err := st.Refresh(ctx); err != nil {
			t.Fatal(err)
		}
	}
	cancel()
	wg.Wait()
}
