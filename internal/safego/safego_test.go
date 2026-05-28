package safego

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestGo_PanicIsRecovered_ProcessKeepsRunning(t *testing.T) {
	start := PanicCount()
	done := make(chan struct{})
	Go("test-panic", func() {
		defer close(done)
		panic("intentional test panic")
	})
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("safego.Go did not run fn within 2s")
	}
	// If recover were missing, the test process would crash before
	// reaching this line, so just reaching here proves recovery. The
	// counter increment is also asserted so a future regression that
	// swallows the recover-side bookkeeping shows up.
	if got := PanicCount() - start; got != 1 {
		t.Errorf("PanicCount delta = %d, want 1", got)
	}
}

func TestGo_NormalReturnDoesNotIncrementCounter(t *testing.T) {
	start := PanicCount()
	var ran atomic.Bool
	done := make(chan struct{})
	Go("test-normal", func() {
		ran.Store(true)
		close(done)
	})
	<-done
	if !ran.Load() {
		t.Fatal("fn did not run")
	}
	if got := PanicCount() - start; got != 0 {
		t.Errorf("PanicCount delta = %d, want 0", got)
	}
}

// Stresses the recover path with concurrent panicking goroutines —
// the atomic counter must end equal to the number of panics, no off-by
// 1 from a race between multiple goroutines panicking at once.
func TestGo_ConcurrentPanicsCountsCorrectly(t *testing.T) {
	const n = 64
	start := PanicCount()
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		Go("test-concurrent", func() {
			defer wg.Done()
			panic("boom")
		})
	}
	wg.Wait()
	if got := PanicCount() - start; got != n {
		t.Errorf("PanicCount delta = %d, want %d", got, n)
	}
}

func TestGo_NilFnPanicsInCaller(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected synchronous panic from safego.Go(nil)")
		}
	}()
	Go("test-nil", nil)
}
