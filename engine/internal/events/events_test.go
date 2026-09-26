package events

import (
	"testing"
	"time"
)

func TestBusHistoryAndSubscribe(t *testing.T) {
	b := NewBus("n1", 2)
	b.Publish("put", Info, "scans", "a", "one")
	b.Publish("put", Info, "scans", "b", "two")
	b.Publish("put", Warning, "scans", "c", "three")
	got := b.Recent(10)
	if len(got) != 2 || got[0].Key != "b" || got[1].Key != "c" {
		t.Fatalf("history=%+v", got)
	}
	ch, cancel := b.Subscribe()
	defer cancel()
	b.Publish("repair", Error, "scans", "c", "fixed")
	select {
	case e := <-ch:
		if e.Node != "n1" || e.Message != "fixed" || e.Severity != Error {
			t.Fatalf("event=%+v", e)
		}
	case <-time.After(time.Second):
		t.Fatal("subscriber missed the event")
	}
}
