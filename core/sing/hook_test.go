package sing

import (
	"fmt"
	"sync"
	"testing"

	"github.com/InazumaV/V2bX/common/counter"
)

func TestTrafficCounterConcurrentConnections(t *testing.T) {
	hook := NewHookServer()
	const connections = 128
	start := make(chan struct{})
	observed := make(chan *counter.TrafficCounter, connections)
	var workers sync.WaitGroup
	for range connections {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			traffic := hook.getTrafficCounter("node")
			traffic.Tx("user", 1)
			observed <- traffic
		}()
	}
	close(start)
	workers.Wait()
	close(observed)

	traffic := hook.getTrafficCounter("node")
	for value := range observed {
		if value != traffic {
			t.Fatal("concurrent connections received different node counters")
		}
	}
	if got := traffic.GetUpCount("user"); got != connections {
		t.Fatalf("lost concurrent traffic: got %d, want %d", got, connections)
	}
}

func TestDeleteUserTrafficDoesNotRetainPreviousUsers(t *testing.T) {
	hook := NewHookServer()
	traffic := hook.getTrafficCounter("node")
	traffic.Tx("remaining", 7)
	other := hook.getTrafficCounter("other-node")
	other.Tx("remaining", 9)

	for i := range 1000 {
		user := fmt.Sprintf("removed-%d", i)
		storage := traffic.GetCounter(user)
		storage.UpCounter.Add(1)
		hook.deleteUserTraffic("node", []string{user})
		// A connection that is finishing may still hold its storage. Its final
		// accounting must not restore the deleted user in the node's cache.
		storage.UpCounter.Add(1)
		if got := traffic.Len(); got != 1 {
			t.Fatalf("iteration %d retained removed users: got %d counters", i, got)
		}
		if got := traffic.GetUpCount(user); got != 0 {
			t.Fatalf("removed user %q still has cached traffic: %d", user, got)
		}
	}
	if got := traffic.GetUpCount("remaining"); got != 7 {
		t.Fatalf("remaining user's traffic changed: got %d, want 7", got)
	}
	if got := other.GetUpCount("remaining"); got != 9 {
		t.Fatalf("other node's traffic changed: got %d, want 9", got)
	}
	hook.deleteUserTraffic("missing-node", []string{"missing-user"})
	if _, found := hook.counter.Load("missing-node"); found {
		t.Fatal("deleting unknown users created a node counter")
	}
}

func TestDeleteNodeTrafficDoesNotRetainPreviousNodes(t *testing.T) {
	hook := NewHookServer()
	other := hook.getTrafficCounter("other-node")
	other.Tx("user", 11)

	for i := range 1000 {
		tag := fmt.Sprintf("removed-node-%d", i)
		hook.getTrafficCounter(tag).Tx("user", 1)
		hook.deleteNodeTraffic(tag)
		if _, found := hook.counter.Load(tag); found {
			t.Fatalf("deleted node %q still has a traffic counter", tag)
		}
	}
	count := 0
	hook.counter.Range(func(_, _ any) bool {
		count++
		return true
	})
	if count != 1 {
		t.Fatalf("retained deleted nodes: got %d counters, want 1", count)
	}
	if hook.getTrafficCounter("other-node") != other || other.GetUpCount("user") != 11 {
		t.Fatal("deleting nodes changed another node's counter")
	}

	previous := hook.getTrafficCounter("reloaded-node")
	previous.Tx("old-user", 1)
	hook.deleteNodeTraffic("reloaded-node")
	replacement := hook.getTrafficCounter("reloaded-node")
	if replacement == previous || replacement.Len() != 0 {
		t.Fatal("node reload reused its previous users' traffic cache")
	}
}
