// Package membership tracks cluster nodes and their liveness.
package membership

import (
	"sort"
	"sync"
	"time"
)

// State is a node's liveness as seen locally.
type State int

// Node states.
const (
	Alive State = iota
	Suspect
	Dead
	Left
)

func (s State) String() string {
	switch s {
	case Alive:
		return "alive"
	case Suspect:
		return "suspect"
	case Dead:
		return "dead"
	case Left:
		return "left"
	default:
		return "unknown"
	}
}

// Member describes a node.
type Member struct {
	ID         string    `json:"id"`
	Zone       string    `json:"zone"`
	RPCAddr    string    `json:"rpcAddr"`
	APIAddr    string    `json:"apiAddr,omitempty"`
	GossipAddr string    `json:"gossipAddr,omitempty"`
	State      State     `json:"-"`
	Since      time.Time `json:"since"`
}

// IsAlive reports whether requests may be sent to the member.
func (m Member) IsAlive() bool { return m.State == Alive || m.State == Suspect }

// Membership is a node's view of the cluster.
type Membership interface {
	// Self returns the local node.
	Self() Member
	// Members returns every node that belongs to the ring: alive nodes plus
	// failed nodes still within their grace period. Sorted by ID.
	Members() []Member
	// Subscribe returns a channel that receives a value after any change.
	Subscribe() (<-chan struct{}, func())
}

// notifier fans out change notifications.
type notifier struct {
	mu   sync.Mutex
	subs map[chan struct{}]struct{}
}

func (n *notifier) subscribe() (<-chan struct{}, func()) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.subs == nil {
		n.subs = map[chan struct{}]struct{}{}
	}
	ch := make(chan struct{}, 1)
	n.subs[ch] = struct{}{}
	return ch, func() {
		n.mu.Lock()
		defer n.mu.Unlock()
		delete(n.subs, ch)
	}
}

func (n *notifier) notify() {
	n.mu.Lock()
	defer n.mu.Unlock()
	for ch := range n.subs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// Static is an in-memory membership shared by nodes in tests and
// single-process clusters. The harness controls liveness explicitly.
type Static struct {
	mu      sync.RWMutex
	members map[string]Member
	n       notifier
}

// NewStatic returns an empty static membership registry.
func NewStatic() *Static { return &Static{members: map[string]Member{}} }

// Upsert adds or updates a member.
func (s *Static) Upsert(m Member) {
	s.mu.Lock()
	if m.Since.IsZero() {
		m.Since = time.Now()
	}
	s.members[m.ID] = m
	s.mu.Unlock()
	s.n.notify()
}

// SetState changes a member's liveness.
func (s *Static) SetState(id string, st State) {
	s.mu.Lock()
	m, ok := s.members[id]
	if ok {
		m.State = st
		m.Since = time.Now()
		s.members[id] = m
	}
	s.mu.Unlock()
	if ok {
		s.n.notify()
	}
}

// Remove deletes a member (graceful leave).
func (s *Static) Remove(id string) {
	s.mu.Lock()
	delete(s.members, id)
	s.mu.Unlock()
	s.n.notify()
}

// View returns a Membership bound to one node.
func (s *Static) View(self string) Membership { return &staticView{s: s, self: self} }

func (s *Static) list() []Member {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Member, 0, len(s.members))
	for _, m := range s.members {
		if m.State != Left {
			out = append(out, m)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

type staticView struct {
	s    *Static
	self string
}

func (v *staticView) Self() Member {
	v.s.mu.RLock()
	defer v.s.mu.RUnlock()
	return v.s.members[v.self]
}

func (v *staticView) Members() []Member                  { return v.s.list() }
func (v *staticView) Subscribe() (<-chan struct{}, func()) { return v.s.n.subscribe() }
