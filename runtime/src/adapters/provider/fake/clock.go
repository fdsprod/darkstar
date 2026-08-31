package fake

import (
	"sync"
	"time"
)

// Clock supplies deterministic timestamps and delays to scripted streams.
type Clock interface {
	Now() time.Time
	After(time.Duration) <-chan time.Time
}

// LogicalClock is the default clock. Delays advance logical time immediately,
// so a scenario never sleeps in wall-clock time.
type LogicalClock struct {
	mu  sync.Mutex
	now time.Time
}

// NewLogicalClock creates a clock at a stable caller-selected instant.
func NewLogicalClock(start time.Time) *LogicalClock {
	return &LogicalClock{now: start}
}

func (clock *LogicalClock) Now() time.Time {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	return clock.now
}

func (clock *LogicalClock) After(delay time.Duration) <-chan time.Time {
	clock.mu.Lock()
	clock.now = clock.now.Add(delay)
	now := clock.now
	clock.mu.Unlock()

	ready := make(chan time.Time, 1)
	ready <- now
	close(ready)
	return ready
}

// ManualClock holds delays until Advance reaches their deadline. It lets tests
// assert blocking, cancellation, and timeout behavior without real sleeps.
type ManualClock struct {
	mu      sync.Mutex
	now     time.Time
	waiters []manualWaiter
}

type manualWaiter struct {
	deadline time.Time
	ready    chan time.Time
}

// NewManualClock creates a manually advanced clock.
func NewManualClock(start time.Time) *ManualClock {
	return &ManualClock{now: start}
}

func (clock *ManualClock) Now() time.Time {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	return clock.now
}

func (clock *ManualClock) After(delay time.Duration) <-chan time.Time {
	clock.mu.Lock()
	defer clock.mu.Unlock()

	ready := make(chan time.Time, 1)
	deadline := clock.now.Add(delay)
	if delay <= 0 {
		ready <- clock.now
		close(ready)
		return ready
	}
	clock.waiters = append(clock.waiters, manualWaiter{deadline: deadline, ready: ready})
	return ready
}

// Advance moves time forward and releases every satisfied delay.
func (clock *ManualClock) Advance(delay time.Duration) {
	if delay < 0 {
		panic("fake provider manual clock cannot advance backwards")
	}

	clock.mu.Lock()
	clock.now = clock.now.Add(delay)
	now := clock.now
	pending := clock.waiters[:0]
	for _, waiter := range clock.waiters {
		if waiter.deadline.After(now) {
			pending = append(pending, waiter)
			continue
		}
		waiter.ready <- now
		close(waiter.ready)
	}
	clock.waiters = pending
	clock.mu.Unlock()
}

// Pending returns the number of delays currently waiting for Advance.
func (clock *ManualClock) Pending() int {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	return len(clock.waiters)
}
