package main

import "testing"

func TestRequireLoopbackAPI(t *testing.T) {
	if err := requireLoopbackAPI("127.0.0.1:18080"); err != nil {
		t.Fatal(err)
	}
	if err := requireLoopbackAPI("localhost:18080"); err != nil {
		t.Fatal(err)
	}
	if err := requireLoopbackAPI("0.0.0.0:18080"); err == nil {
		t.Fatal("non-loopback API must be rejected")
	}
	if err := requireLoopbackAPI("192.168.1.9:18080"); err == nil {
		t.Fatal("LAN API must be rejected")
	}
}
