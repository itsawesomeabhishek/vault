package membership

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/hashicorp/memberlist"
)

// GossipConfig configures SWIM-based membership (hashicorp/memberlist).
type GossipConfig struct {
	Self        Member
	BindAddr    string
	BindPort    int
	Advertise   string
	SecretKey   []byte        // 32 bytes: AES-256 encryption of all gossip traffic
	DeadTimeout time.Duration // how long a failed node keeps its ring slots before rebalancing
}

// Gossip is a Membership backed by memberlist.
type Gossip struct {
	ml          *memberlist.Memberlist
	self        Member
	deadTimeout time.Duration

	mu      sync.RWMutex
	members map[string]Member
	n       notifier
	stop    chan struct{}
}

type nodeMeta struct {
	Zone string `json:"z"`
	RPC  string `json:"r"`
	API  string `json:"a,omitempty"`
}

// NewGossip starts memberlist on the configured address.
func NewGossip(cfg GossipConfig) (*Gossip, error) {
	g := &Gossip{self: cfg.Self, deadTimeout: cfg.DeadTimeout, members: map[string]Member{}, stop: make(chan struct{})}
	if g.deadTimeout <= 0 {
		g.deadTimeout = 10 * time.Minute
	}
	mc := memberlist.DefaultLANConfig()
	mc.Name = cfg.Self.ID
	mc.BindAddr = cfg.BindAddr
	mc.BindPort = cfg.BindPort
	if cfg.Advertise != "" {
		mc.AdvertiseAddr = cfg.Advertise
		mc.AdvertisePort = cfg.BindPort
	}
	mc.SecretKey = cfg.SecretKey
	mc.Delegate = g
	mc.Events = g
	mc.Logger = log.New(io.Discard, "", 0)
	ml, err := memberlist.Create(mc)
	if err != nil {
		return nil, fmt.Errorf("membership: start gossip: %w", err)
	}
	g.ml = ml
	self := cfg.Self
	self.State = Alive
	self.Since = time.Now()
	self.GossipAddr = net.JoinHostPort(ml.LocalNode().Addr.String(), strconv.Itoa(int(ml.LocalNode().Port)))
	g.self = self
	g.members[self.ID] = self
	go g.reap()
	return g, nil
}

// Join contacts seed gossip addresses. It succeeds if any seed answers.
func (g *Gossip) Join(seeds []string) (int, error) {
	if len(seeds) == 0 {
		return 0, nil
	}
	return g.ml.Join(seeds)
}

// Leave announces a graceful departure and stops gossiping.
func (g *Gossip) Leave(timeout time.Duration) error {
	close(g.stop)
	if err := g.ml.Leave(timeout); err != nil {
		return err
	}
	return g.ml.Shutdown()
}

// Self implements Membership.
func (g *Gossip) Self() Member { return g.self }

// Members implements Membership.
func (g *Gossip) Members() []Member {
	g.mu.RLock()
	defer g.mu.RUnlock()
	out := make([]Member, 0, len(g.members))
	for _, m := range g.members {
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Subscribe implements Membership.
func (g *Gossip) Subscribe() (<-chan struct{}, func()) { return g.n.subscribe() }

// GossipAddrs returns the gossip addresses of alive members (join seeds).
func (g *Gossip) GossipAddrs() []string {
	var out []string
	for _, m := range g.Members() {
		if m.IsAlive() && m.GossipAddr != "" {
			out = append(out, m.GossipAddr)
		}
	}
	return out
}

// reap removes nodes that stayed dead longer than DeadTimeout so their
// ring slots are reassigned (triggering rebalancing).
func (g *Gossip) reap() {
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-g.stop:
			return
		case <-t.C:
			changed := false
			g.mu.Lock()
			for id, m := range g.members {
				if m.State == Dead && time.Since(m.Since) > g.deadTimeout {
					delete(g.members, id)
					changed = true
				}
			}
			g.mu.Unlock()
			if changed {
				g.n.notify()
			}
		}
	}
}

func (g *Gossip) toMember(n *memberlist.Node, st State) (Member, bool) {
	var meta nodeMeta
	if err := json.Unmarshal(n.Meta, &meta); err != nil {
		return Member{}, false
	}
	return Member{
		ID: n.Name, Zone: meta.Zone, RPCAddr: meta.RPC, APIAddr: meta.API,
		GossipAddr: net.JoinHostPort(n.Addr.String(), strconv.Itoa(int(n.Port))), State: st, Since: time.Now(),
	}, true
}

func (g *Gossip) set(n *memberlist.Node, st State) {
	m, ok := g.toMember(n, st)
	if !ok {
		return
	}
	g.mu.Lock()
	if st == Left {
		delete(g.members, m.ID)
	} else {
		g.members[m.ID] = m
	}
	g.mu.Unlock()
	g.n.notify()
}

// NotifyJoin implements memberlist.EventDelegate.
func (g *Gossip) NotifyJoin(n *memberlist.Node) { g.set(n, Alive) }

// NotifyUpdate implements memberlist.EventDelegate.
func (g *Gossip) NotifyUpdate(n *memberlist.Node) { g.set(n, Alive) }

// NotifyLeave implements memberlist.EventDelegate. memberlist reports both
// crashes and graceful leaves here; only graceful leaves free ring slots
// immediately.
func (g *Gossip) NotifyLeave(n *memberlist.Node) {
	if n.State == memberlist.StateLeft {
		g.set(n, Left)
		return
	}
	g.set(n, Dead)
}

// NodeMeta implements memberlist.Delegate.
func (g *Gossip) NodeMeta(limit int) []byte {
	b, _ := json.Marshal(nodeMeta{Zone: g.self.Zone, RPC: g.self.RPCAddr, API: g.self.APIAddr})
	if len(b) > limit {
		return nil
	}
	return b
}

// NotifyMsg implements memberlist.Delegate (unused).
func (g *Gossip) NotifyMsg([]byte) {}

// GetBroadcasts implements memberlist.Delegate (unused).
func (g *Gossip) GetBroadcasts(int, int) [][]byte { return nil }

// LocalState implements memberlist.Delegate (unused).
func (g *Gossip) LocalState(bool) []byte { return nil }

// MergeRemoteState implements memberlist.Delegate (unused).
func (g *Gossip) MergeRemoteState([]byte, bool) {}
