package node

import (
	"container/heap"
	"sync"
	"time"

	"github.com/hydra-software/vault/engine/internal/store"
)

// Repair priorities: objects with fewer healthy copies are repaired first,
// which minimises the window in which a further failure could lose data.
const (
	priorityBackground = 1
	priorityDegraded   = 5
	priorityCritical   = 10
)

type repairItem struct {
	ref      store.ObjectRef
	priority int
	reason   string
	queued   time.Time
	attempts int
	index    int
}

type itemHeap []*repairItem

func (h itemHeap) Len() int { return len(h) }
func (h itemHeap) Less(i, j int) bool {
	if h[i].priority != h[j].priority {
		return h[i].priority > h[j].priority
	}
	return h[i].queued.Before(h[j].queued)
}
func (h itemHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i]; h[i].index = i; h[j].index = j }
func (h *itemHeap) Push(x any)   { it := x.(*repairItem); it.index = len(*h); *h = append(*h, it) }
func (h *itemHeap) Pop() any {
	old := *h
	it := old[len(old)-1]
	*h = old[:len(old)-1]
	return it
}

// repairQueue is a deduplicating priority queue.
type repairQueue struct {
	mu        sync.Mutex
	cond      *sync.Cond
	items     itemHeap
	byRef     map[store.ObjectRef]*repairItem
	inFlight  map[store.ObjectRef]bool
	closed    bool
	rebalance chan struct{}
}

func newRepairQueue() *repairQueue {
	q := &repairQueue{byRef: map[store.ObjectRef]*repairItem{}, inFlight: map[store.ObjectRef]bool{}, rebalance: make(chan struct{}, 1)}
	q.cond = sync.NewCond(&q.mu)
	return q
}

func (q *repairQueue) enqueue(ref store.ObjectRef, priority int, reason string) {
	q.push(&repairItem{ref: ref, priority: priority, reason: reason, queued: time.Now()})
}

func (q *repairQueue) enqueueAfter(ref store.ObjectRef, priority int, reason string, d time.Duration) {
	time.AfterFunc(d, func() { q.enqueue(ref, priority, reason) })
}

func (q *repairQueue) push(it *repairItem) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return
	}
	if cur, ok := q.byRef[it.ref]; ok {
		if it.priority > cur.priority {
			cur.priority = it.priority
			cur.reason = it.reason
			heap.Fix(&q.items, cur.index)
		}
		return
	}
	q.byRef[it.ref] = it
	heap.Push(&q.items, it)
	q.cond.Signal()
}

// pop blocks until an item is available or the queue is closed. Items for an
// object already being repaired are deferred so two workers never race.
func (q *repairQueue) pop() (*repairItem, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for {
		if q.closed {
			return nil, false
		}
		for i := 0; i < len(q.items); i++ {
			it := q.items[i]
			if !q.inFlight[it.ref] {
				heap.Remove(&q.items, it.index)
				delete(q.byRef, it.ref)
				q.inFlight[it.ref] = true
				return it, true
			}
		}
		q.cond.Wait()
	}
}

func (q *repairQueue) done(ref store.ObjectRef) {
	q.mu.Lock()
	delete(q.inFlight, ref)
	q.mu.Unlock()
	q.cond.Broadcast()
}

func (q *repairQueue) depth() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.items) + len(q.inFlight)
}

func (q *repairQueue) close() {
	q.mu.Lock()
	q.closed = true
	q.mu.Unlock()
	q.cond.Broadcast()
}

func (q *repairQueue) kickRebalance() {
	select {
	case q.rebalance <- struct{}{}:
	default:
	}
}
