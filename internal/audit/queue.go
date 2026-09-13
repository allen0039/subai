package audit

import (
	"context"
	"errors"
	"sync"
	"time"
)

// Queue is the fairness-bounded admission gate for moderation calls (§6.3,
// review P2-12): three separate bounds —
//   - globalWaiting: max requests waiting across all keys (default 50),
//   - perKey: max concurrent waiters per key (default 10, fairness),
//   - maxInFlight: max moderation calls executing at once.
//
// Auditing never occupies upstream concurrency slots (§3), and per-Key caps
// prevent one large job from starving other devices (§6.3).
type Queue struct {
	mu        sync.Mutex
	perKey    map[string]int
	perKeyMax int
	waitMax   int
	waiting   int
	inflight  chan struct{}
}

var (
	ErrQueueFull   = errors.New("audit_queue_full")
	ErrQueueTimout = errors.New("audit queue wait timeout")
)

func NewQueue(globalWaitMax, perKeyMax, maxInFlight int) *Queue {
	if maxInFlight <= 0 {
		maxInFlight = 1
	}
	return &Queue{
		perKey:    map[string]int{},
		perKeyMax: perKeyMax,
		waitMax:   globalWaitMax,
		inflight:  make(chan struct{}, maxInFlight),
	}
}

// Acquire reserves a moderation slot or fails with ErrQueueFull when the
// global wait cap, per-Key cap, or executor capacity semantics reject the
// request, or ErrQueueTimout when the wait deadline expires. Client
// cancellation removes the waiter (§6.3).
//
// Counting semantics (review R2-10): `waiting` counts requests QUEUED for an
// executor only — entering the executor slot TRANSFERS the request out of
// waiting. The per-Key cap bounds total in-system requests per key (waiting +
// executing). Release is idempotent (sync.Once).
func (q *Queue) Acquire(ctx context.Context, keyID string, waitDeadline time.Duration) (release func(), err error) {
	q.mu.Lock()
	if q.perKeyMax > 0 && q.perKey[keyID] >= q.perKeyMax {
		q.mu.Unlock()
		return nil, ErrQueueFull
	}
	if q.waitMax > 0 && q.waiting >= q.waitMax {
		// independent global WAIT bound: waiters beyond it are rejected even
		// though executors are free (review P2-12)
		q.mu.Unlock()
		return nil, ErrQueueFull
	}
	q.perKey[keyID]++
	q.waiting++
	q.mu.Unlock()
	defer func() {
		if err != nil {
			q.mu.Lock()
			q.perKey[keyID]--
			if q.perKey[keyID] <= 0 {
				delete(q.perKey, keyID)
			}
			q.waiting--
			q.mu.Unlock()
		}
	}()

	timer := time.NewTimer(waitDeadline)
	defer timer.Stop()
	select {
	case q.inflight <- struct{}{}:
		// Transfer out of the waiting count the moment execution starts, so
		// globalWaiting genuinely means "queued, not yet executing".
		q.mu.Lock()
		q.waiting--
		q.mu.Unlock()
		var once sync.Once
		return func() {
			once.Do(func() {
				q.mu.Lock()
				q.perKey[keyID]--
				if q.perKey[keyID] <= 0 {
					delete(q.perKey, keyID)
				}
				q.mu.Unlock()
				<-q.inflight
			})
		}, nil
	case <-timer.C:
		return nil, ErrQueueTimout
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// InFlight reports current executing moderation calls (metrics/testing).
func (q *Queue) InFlight() int { return len(q.inflight) }

// Waiting reports total waiters across keys (metrics/testing).
func (q *Queue) Waiting() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.waiting
}
