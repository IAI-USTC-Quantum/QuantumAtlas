package lazyload

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeStore is an in-test Store: an in-memory map standing in for a durable tier,
// with counters + hooks so tests can assert Load/Store call counts and inject
// errors. (The real durable tiers are PostgreSQL / object storage; this fake
// only exercises the orchestrator's control flow.)
type fakeStore struct {
	mu        sync.Mutex
	data      map[string]string
	loads     int32
	stores    int32
	loadErr   error
	storeErr  error
	loadDelay time.Duration
}

func newFakeStore() *fakeStore { return &fakeStore{data: map[string]string{}} }

func (s *fakeStore) Load(ctx context.Context, key string) (string, bool, error) {
	atomic.AddInt32(&s.loads, 1)
	if s.loadDelay > 0 {
		time.Sleep(s.loadDelay)
	}
	if s.loadErr != nil {
		return "", false, s.loadErr
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.data[key]
	return v, ok, nil
}

func (s *fakeStore) Store(ctx context.Context, key, value string) error {
	atomic.AddInt32(&s.stores, 1)
	if s.storeErr != nil {
		return s.storeErr
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[key] = value
	return nil
}

// TestGet_Hit: a value already in the store is returned without calling the loader.
func TestGet_Hit(t *testing.T) {
	store := newFakeStore()
	store.data["k"] = "cached"
	var loaderCalls int32
	m := New[string](store, func(context.Context, string) (string, error) {
		atomic.AddInt32(&loaderCalls, 1)
		return "loaded", nil
	})

	v, found, err := m.Get(context.Background(), "k")
	if err != nil || !found || v != "cached" {
		t.Fatalf("Get = (%q,%v,%v), want (cached,true,nil)", v, found, err)
	}
	if loaderCalls != 0 {
		t.Errorf("loader called %d times on a hit, want 0", loaderCalls)
	}
	if got := atomic.LoadInt32(&store.stores); got != 0 {
		t.Errorf("Store called %d times on a hit, want 0", got)
	}
}

// TestGet_MissMaterializes: a miss calls the loader once and writes it back, so
// the value is now cached for the next read.
func TestGet_MissMaterializes(t *testing.T) {
	store := newFakeStore()
	m := New[string](store, func(_ context.Context, key string) (string, error) {
		return "for:" + key, nil
	})

	v, found, err := m.Get(context.Background(), "k")
	if err != nil || !found || v != "for:k" {
		t.Fatalf("Get = (%q,%v,%v), want (for:k,true,nil)", v, found, err)
	}
	if got := atomic.LoadInt32(&store.stores); got != 1 {
		t.Errorf("Store called %d times, want 1 (write-through)", got)
	}
	store.mu.Lock()
	cached := store.data["k"]
	store.mu.Unlock()
	if cached != "for:k" {
		t.Errorf("write-back stored %q, want for:k", cached)
	}
}

// TestGet_NotFound: a Loader ErrNotFound is a genuine miss, not an error, and is
// never written back.
func TestGet_NotFound(t *testing.T) {
	store := newFakeStore()
	m := New[string](store, func(context.Context, string) (string, error) {
		return "", ErrNotFound
	})

	v, found, err := m.Get(context.Background(), "k")
	if err != nil || found || v != "" {
		t.Fatalf("Get = (%q,%v,%v), want (\"\",false,nil)", v, found, err)
	}
	if got := atomic.LoadInt32(&store.stores); got != 0 {
		t.Errorf("Store called %d times on ErrNotFound, want 0", got)
	}
}

// TestGet_LoaderError: a non-ErrNotFound loader error propagates unchanged (so a
// future fail-safe layer can classify it).
func TestGet_LoaderError(t *testing.T) {
	sentinel := errors.New("upstream boom")
	store := newFakeStore()
	m := New[string](store, func(context.Context, string) (string, error) {
		return "", sentinel
	})

	_, found, err := m.Get(context.Background(), "k")
	if !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want %v", err, sentinel)
	}
	if found {
		t.Errorf("found = true on loader error, want false")
	}
}

// TestGet_StoreLoadError: a genuine Store.Load failure propagates (not a miss).
func TestGet_StoreLoadError(t *testing.T) {
	sentinel := errors.New("db down")
	store := newFakeStore()
	store.loadErr = sentinel
	var loaderCalls int32
	m := New[string](store, func(context.Context, string) (string, error) {
		atomic.AddInt32(&loaderCalls, 1)
		return "x", nil
	})

	_, _, err := m.Get(context.Background(), "k")
	if !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want %v", err, sentinel)
	}
	if loaderCalls != 0 {
		t.Errorf("loader called %d times after Load error, want 0", loaderCalls)
	}
}

// TestGet_WriteBackErrorTolerated: a write-back failure does NOT fail the read —
// the loaded value is still returned, and the hook observes the error.
func TestGet_WriteBackErrorTolerated(t *testing.T) {
	store := newFakeStore()
	store.storeErr = errors.New("write failed")
	var hookKey string
	var hookErr error
	m := New[string](store,
		func(context.Context, string) (string, error) { return "loaded", nil },
		WithWriteErrorHandler[string](func(key string, err error) { hookKey, hookErr = key, err }),
	)

	v, found, err := m.Get(context.Background(), "k")
	if err != nil || !found || v != "loaded" {
		t.Fatalf("Get = (%q,%v,%v), want (loaded,true,nil) despite write error", v, found, err)
	}
	if hookKey != "k" || hookErr == nil {
		t.Errorf("write-error hook got (%q,%v), want (k,non-nil)", hookKey, hookErr)
	}
}

// TestGet_Coalesces: N concurrent misses for the same key collapse to ONE
// Load→Loader→Store cycle (singleflight), and all callers get the same value.
func TestGet_Coalesces(t *testing.T) {
	const n = 25
	store := newFakeStore()
	store.loadDelay = 60 * time.Millisecond // hold the leader in Load while dups pile up
	var loaderCalls int32
	m := New[string](store, func(_ context.Context, key string) (string, error) {
		atomic.AddInt32(&loaderCalls, 1)
		return "for:" + key, nil
	})

	var wg sync.WaitGroup
	vals := make([]string, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			v, _, _ := m.Get(context.Background(), "hot")
			vals[i] = v
		}(i)
	}
	wg.Wait()

	if got := atomic.LoadInt32(&loaderCalls); got != 1 {
		t.Errorf("loader called %d times, want 1 (coalesced)", got)
	}
	if got := atomic.LoadInt32(&store.stores); got != 1 {
		t.Errorf("Store called %d times, want 1 (coalesced write-back)", got)
	}
	for i, v := range vals {
		if v != "for:hot" {
			t.Errorf("caller %d got %q, want for:hot", i, v)
		}
	}
}

// TestGet_CancelDoesNotPoisonWaiters: the shared execution is detached from the
// caller context, so a cancelled caller's context must not abort the load for
// the concurrent waiters.
func TestGet_CancelDoesNotPoisonWaiters(t *testing.T) {
	store := newFakeStore()
	store.loadDelay = 60 * time.Millisecond
	m := New[string](store, func(_ context.Context, key string) (string, error) {
		return "for:" + key, nil
	})

	cancelledCtx, cancel := context.WithCancel(context.Background())

	var wg sync.WaitGroup
	// Leader with a context we cancel almost immediately.
	var leaderVal string
	wg.Add(1)
	go func() {
		defer wg.Done()
		leaderVal, _, _ = m.Get(cancelledCtx, "hot")
	}()
	// A live waiter that must still get the value.
	var waiterVal string
	var waiterErr error
	wg.Add(1)
	go func() {
		defer wg.Done()
		time.Sleep(10 * time.Millisecond) // join while the leader is mid-Load
		waiterVal, _, waiterErr = m.Get(context.Background(), "hot")
	}()

	time.Sleep(15 * time.Millisecond)
	cancel() // cancel the leader's context mid-flight
	wg.Wait()

	if waiterErr != nil || waiterVal != "for:hot" {
		t.Errorf("waiter = (%q,%v), want (for:hot,nil) — cancellation poisoned the shared load", waiterVal, waiterErr)
	}
	if leaderVal != "for:hot" {
		t.Errorf("leader = %q, want for:hot (detached ctx survives its own cancel)", leaderVal)
	}
}
