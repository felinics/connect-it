package providerkit

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
)

const (
	// DefaultObserverQueueCapacity bounds production observation memory while
	// leaving enough room for short log/metrics sink stalls.
	DefaultObserverQueueCapacity = 1024
)

var (
	// ErrObserverSinkRequired is returned when an asynchronous observer has no
	// downstream sink.
	ErrObserverSinkRequired = errors.New("provider observer sink is required")
	// ErrInvalidObserverQueueCapacity is returned for a non-positive fixed
	// queue capacity.
	ErrInvalidObserverQueueCapacity = errors.New(
		"provider observer queue capacity must be positive",
	)
)

type queuedObservation struct {
	ctx   context.Context
	event RequestEvent
}

// AsyncObserver decouples Provider calls from a log/metrics sink through a
// fixed-capacity queue. Observe never waits for the downstream sink. Events
// rejected because they are invalid, the queue is full or closed, or the sink
// panics are counted by Dropped.
//
// Close does not close the queue channel. A small state lock prevents new
// sends while the worker drains already accepted events, avoiding a
// send-on-closed race with concurrent Observe calls.
type AsyncObserver struct {
	sink  Observer
	queue chan queuedObservation
	stop  chan struct{}
	done  chan struct{}

	stateMu sync.RWMutex
	closed  bool

	closeOnce sync.Once
	dropped   atomic.Uint64
}

// NewAsyncObserver creates and starts a fixed-capacity asynchronous observer.
func NewAsyncObserver(
	sink Observer,
	queueCapacity int,
) (*AsyncObserver, error) {
	if sink == nil {
		return nil, ErrObserverSinkRequired
	}
	if function, ok := sink.(ObserverFunc); ok && function == nil {
		return nil, ErrObserverSinkRequired
	}
	if queueCapacity <= 0 {
		return nil, ErrInvalidObserverQueueCapacity
	}
	observer := &AsyncObserver{
		sink:  sink,
		queue: make(chan queuedObservation, queueCapacity),
		stop:  make(chan struct{}),
		done:  make(chan struct{}),
	}
	go observer.run()
	return observer, nil
}

// Observe validates and attempts to enqueue an event without waiting for the
// downstream sink. A context without cancellation is retained so an accepted
// event can still carry trace values after the Provider request completes.
func (observer *AsyncObserver) Observe(
	ctx context.Context,
	event RequestEvent,
) {
	if observer == nil {
		return
	}
	if event.Validate() != nil {
		observer.dropped.Add(1)
		return
	}
	if ctx == nil {
		ctx = context.Background()
	} else {
		ctx = context.WithoutCancel(ctx)
	}

	observation := queuedObservation{ctx: ctx, event: event}
	observer.stateMu.RLock()
	if observer.closed {
		observer.stateMu.RUnlock()
		observer.dropped.Add(1)
		return
	}
	select {
	case observer.queue <- observation:
	default:
		observer.dropped.Add(1)
	}
	observer.stateMu.RUnlock()
}

// Dropped returns the number of events that were not delivered. It is safe to
// call concurrently with Observe and Close.
func (observer *AsyncObserver) Dropped() uint64 {
	if observer == nil {
		return 0
	}
	return observer.dropped.Load()
}

// Close stops accepting events and drains the fixed queue. A caller should
// pass a bounded context because a downstream sink is allowed to block.
func (observer *AsyncObserver) Close(ctx context.Context) error {
	if observer == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	observer.closeOnce.Do(func() {
		observer.stateMu.Lock()
		observer.closed = true
		close(observer.stop)
		observer.stateMu.Unlock()
	})

	select {
	case <-observer.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (observer *AsyncObserver) run() {
	defer close(observer.done)
	for {
		select {
		case observation := <-observer.queue:
			observer.deliver(observation)
		case <-observer.stop:
			for {
				select {
				case observation := <-observer.queue:
					observer.deliver(observation)
				default:
					return
				}
			}
		}
	}
}

func (observer *AsyncObserver) deliver(observation queuedObservation) {
	if !observeSafely(
		observation.ctx,
		observer.sink,
		observation.event,
	) {
		observer.dropped.Add(1)
	}
}
