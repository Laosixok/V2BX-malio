package core

import "testing"

type trafficCleanerTestCore struct {
	Core
	tag   string
	users []string
}

func (c *trafficCleanerTestCore) DeleteUserTraffic(tag string, users []string) {
	c.tag = tag
	c.users = append(c.users, users...)
}

func TestSelectorDeleteUserTraffic(t *testing.T) {
	selected := &trafficCleanerTestCore{}
	other := &trafficCleanerTestCore{}
	selector := &Selector{}
	selector.nodes.Store("selected", selected)
	selector.nodes.Store("other", other)
	selector.nodes.Store("unsupported", struct{ Core }{})

	selector.DeleteUserTraffic("selected", []string{"removed"})
	selector.DeleteUserTraffic("missing", []string{"removed"})
	selector.DeleteUserTraffic("unsupported", []string{"removed"})
	if selected.tag != "selected" || len(selected.users) != 1 || selected.users[0] != "removed" {
		t.Fatalf("cleanup was not routed to selected core: %+v", selected)
	}
	if len(other.users) != 0 {
		t.Fatal("cleanup affected another node")
	}
}
