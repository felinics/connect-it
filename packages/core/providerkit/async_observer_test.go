package providerkit

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestNewAsyncObserverValidatesConfiguration(t *testing.T) {
	t.Parallel()

	if _, err := NewAsyncObserver(nil, 1); !errors.Is(
		err,
		ErrObserverSinkRequired,
	) {
		t.Fatalf("nil sink error = %v", err)
	}
	if _, err := NewAsyncObserver(ObserverFunc(nil), 1); !errors.Is(
		err,
		ErrObserverSinkRequired,
	) {
		t.Fatalf("nil function error = %v", err)
	}
	if _, err := NewAsyncObserver(
		ObserverFunc(func(context.Context, RequestEvent) {}),
		0,
	); !errors.Is(err, ErrInvalidObserverQueueCapacity) {
		t.Fatalf("zero capacity error = %v", err)
	}
}

func TestAsyncObserverQueueIsBoundedAndObserveIsNonBlocking(t *testing.T) {
	t.Parallel()

	started := make(chan struct{})
	release := make(chan struct{})
	var (
		calls atomic.Int64
		once  sync.Once
	)
	observer, err := NewAsyncObserver(
		ObserverFunc(func(context.Context, RequestEvent) {
			calls.Add(1)
			once.Do(func() {
				close(started)
				<-release
			})
		}),
		2,
	)
	if err != nil {
		t.Fatal(err)
	}

	observer.Observe(t.Context(), validRequestEvent())
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("sink did not start")
	}
	observer.Observe(t.Context(), validRequestEvent())
	observer.Observe(t.Context(), validRequestEvent())

	returned := make(chan struct{})
	go func() {
		observer.Observe(t.Context(), validRequestEvent())
		close(returned)
	}()
	select {
	case <-returned:
	case <-time.After(time.Second):
		t.Fatal("Observe blocked behind downstream sink")
	}
	if got := observer.Dropped(); got != 1 {
		t.Fatalf("Dropped = %d, want 1", got)
	}

	close(release)
	closeCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := observer.Close(closeCtx); err != nil {
		t.Fatal(err)
	}
	if got := calls.Load(); got != 3 {
		t.Fatalf("delivered calls = %d, want 3", got)
	}
}

func TestAsyncObserverSinkPanicDropsEventAndWorkerContinues(t *testing.T) {
	t.Parallel()

	var calls atomic.Int64
	observer, err := NewAsyncObserver(
		ObserverFunc(func(context.Context, RequestEvent) {
			if calls.Add(1) == 1 {
				panic("sink-secret-must-not-escape")
			}
		}),
		2,
	)
	if err != nil {
		t.Fatal(err)
	}
	observer.Observe(t.Context(), validRequestEvent())
	observer.Observe(t.Context(), validRequestEvent())

	closeCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := observer.Close(closeCtx); err != nil {
		t.Fatal(err)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("sink calls = %d, want 2", got)
	}
	if got := observer.Dropped(); got != 1 {
		t.Fatalf("Dropped = %d, want panicking event", got)
	}
}

func TestAsyncObserverRejectsInvalidAndPostCloseEvents(t *testing.T) {
	t.Parallel()

	var calls atomic.Int64
	observer, err := NewAsyncObserver(
		ObserverFunc(func(context.Context, RequestEvent) {
			calls.Add(1)
		}),
		1,
	)
	if err != nil {
		t.Fatal(err)
	}
	invalid := validRequestEvent()
	invalid.Labels.Operation = "also_set"
	observer.Observe(t.Context(), invalid)

	closeCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := observer.Close(closeCtx); err != nil {
		t.Fatal(err)
	}
	observer.Observe(t.Context(), validRequestEvent())
	if got := observer.Dropped(); got != 2 {
		t.Fatalf("Dropped = %d, want 2", got)
	}
	if got := calls.Load(); got != 0 {
		t.Fatalf("sink calls = %d, want 0", got)
	}
}

func TestAsyncObserverObserveAndCloseAreRaceSafe(t *testing.T) {
	t.Parallel()

	var delivered atomic.Int64
	observer, err := NewAsyncObserver(
		ObserverFunc(func(context.Context, RequestEvent) {
			delivered.Add(1)
		}),
		64,
	)
	if err != nil {
		t.Fatal(err)
	}

	const (
		goroutines = 32
		perWorker  = 128
		total      = goroutines * perWorker
	)
	start := make(chan struct{})
	var producers sync.WaitGroup
	producers.Add(goroutines)
	for range goroutines {
		go func() {
			defer producers.Done()
			<-start
			for range perWorker {
				observer.Observe(context.Background(), validRequestEvent())
			}
		}()
	}
	close(start)

	closeErr := make(chan error, 1)
	go func() {
		closeCtx, cancel := context.WithTimeout(
			context.Background(),
			5*time.Second,
		)
		defer cancel()
		closeErr <- observer.Close(closeCtx)
	}()
	producers.Wait()
	if err := <-closeErr; err != nil {
		t.Fatal(err)
	}
	if got := delivered.Load() + int64(observer.Dropped()); got != total {
		t.Fatalf(
			"delivered + dropped = %d, want %d",
			got,
			total,
		)
	}
}

func TestAsyncObserverCloseHonorsContextWhileSinkIsBlocked(t *testing.T) {
	t.Parallel()

	started := make(chan struct{})
	release := make(chan struct{})
	observer, err := NewAsyncObserver(
		ObserverFunc(func(context.Context, RequestEvent) {
			close(started)
			<-release
		}),
		1,
	)
	if err != nil {
		t.Fatal(err)
	}
	observer.Observe(t.Context(), validRequestEvent())
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("sink did not start")
	}

	closeCtx, cancel := context.WithTimeout(
		context.Background(),
		time.Millisecond,
	)
	if err := observer.Close(closeCtx); !errors.Is(
		err,
		context.DeadlineExceeded,
	) {
		t.Fatalf("Close error = %v", err)
	}
	cancel()
	close(release)

	drainCtx, drainCancel := context.WithTimeout(
		context.Background(),
		time.Second,
	)
	defer drainCancel()
	if err := observer.Close(drainCtx); err != nil {
		t.Fatal(err)
	}
}
