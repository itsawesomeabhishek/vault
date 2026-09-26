package membership

import (
	"testing"
	"time"
)

func TestStaticViewAndNotify(t *testing.T) {
	s := NewStatic()
	s.Upsert(Member{ID: "n2", Zone: "b", State: Alive})
	s.Upsert(Member{ID: "n1", Zone: "a", State: Alive})
	v := s.View("n1")
	if v.Self().ID != "n1" {
		t.Fatal("self")
	}
	ids := v.Members()
	if len(ids) != 2 || ids[0].ID != "n1" || ids[1].ID != "n2" {
		t.Fatalf("sorted members=%+v", ids)
	}
	ch, cancel := v.Subscribe()
	defer cancel()
	s.SetState("n2", Dead)
	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatal("no notification")
	}
	var dead Member
	for _, m := range s.View("n1").Members() {
		if m.ID == "n2" {
			dead = m
		}
	}
	if dead.ID == "" || dead.IsAlive() {
		t.Fatalf("dead member=%+v", dead)
	}
	s.Remove("n2")
	if len(s.View("n1").Members()) != 1 {
		t.Fatal("remove")
	}
}

func TestStateString(t *testing.T) {
	if Alive.String() != "alive" || State(99).String() != "unknown" {
		t.Fatal(Alive.String(), State(99).String())
	}
}
