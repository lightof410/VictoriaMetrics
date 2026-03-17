package zookeeper

import (
	"sync"
	"testing"
	"time"

	"github.com/go-zookeeper/zk"
)

func newTestTreeCache() (*ZookeeperTreeCache, chan ZookeeperTreeCacheEvent) {
	events := make(chan ZookeeperTreeCacheEvent, 100)
	tc := &ZookeeperTreeCache{
		prefix: "/test",
		events: events,
		stop:   make(chan struct{}),
		wg:     &sync.WaitGroup{},
	}
	tc.head = &zookeeperTreeCacheNode{
		events:   make(chan zk.Event, 10),
		children: map[string]*zookeeperTreeCacheNode{},
		done:     make(chan struct{}, 1),
		stopped:  true,
	}
	return tc, events
}

func makeNode(data []byte, stopped bool) *zookeeperTreeCacheNode {
	d := make([]byte, len(data))
	copy(d, data)
	return &zookeeperTreeCacheNode{
		data:     &d,
		events:   make(chan zk.Event, 1),
		children: map[string]*zookeeperTreeCacheNode{},
		done:     make(chan struct{}, 1),
		stopped:  stopped,
	}
}

func drainEvents(ch chan ZookeeperTreeCacheEvent, timeout time.Duration) []ZookeeperTreeCacheEvent {
	var result []ZookeeperTreeCacheEvent
	timer := time.After(timeout)
	for {
		select {
		case ev := <-ch:
			result = append(result, ev)
		case <-timer:
			return result
		}
	}
}

func TestRecursiveDelete_Leaf(t *testing.T) {
	tc, events := newTestTreeCache()

	node := makeNode([]byte("hello"), false)
	tc.recursiveDelete("/test/child1", node)

	evs := drainEvents(events, 50*time.Millisecond)
	if len(evs) != 1 {
		t.Fatalf("expected 1 event, got %d", len(evs))
	}
	if evs[0].Path != "/test/child1" {
		t.Errorf("event path = %q, want /test/child1", evs[0].Path)
	}
	if evs[0].Data != nil {
		t.Error("expected Data=nil for delete event")
	}
	if node.data != nil {
		t.Error("expected node.data to be nil after delete")
	}
	if !node.stopped {
		t.Error("expected node.stopped to be true")
	}
}

func TestRecursiveDelete_WithChildren(t *testing.T) {
	tc, events := newTestTreeCache()

	child := makeNode([]byte("child-data"), false)
	parent := makeNode([]byte("parent-data"), false)
	parent.children["c1"] = child

	tc.recursiveDelete("/test/parent", parent)

	evs := drainEvents(events, 50*time.Millisecond)
	// Expect 2 delete events: parent + child
	if len(evs) != 2 {
		t.Fatalf("expected 2 events, got %d", len(evs))
	}

	paths := map[string]bool{}
	for _, ev := range evs {
		paths[ev.Path] = true
		if ev.Data != nil {
			t.Errorf("expected Data=nil for path %q", ev.Path)
		}
	}
	if !paths["/test/parent"] {
		t.Error("missing delete event for /test/parent")
	}
	if !paths["/test/parent/c1"] {
		t.Error("missing delete event for /test/parent/c1")
	}
}

func TestRecursiveDelete_AlreadyStopped(t *testing.T) {
	tc, events := newTestTreeCache()

	// Node already stopped, has data.
	node := makeNode([]byte("data"), true)
	tc.recursiveDelete("/test/n", node)

	evs := drainEvents(events, 50*time.Millisecond)
	if len(evs) != 1 {
		t.Fatalf("expected 1 event, got %d", len(evs))
	}
	// Should not panic — done channel not signaled for already-stopped node.
}

func TestRecursiveDelete_NilData(t *testing.T) {
	tc, events := newTestTreeCache()

	node := &zookeeperTreeCacheNode{
		data:     nil,
		children: map[string]*zookeeperTreeCacheNode{},
		done:     make(chan struct{}, 1),
		stopped:  true,
	}
	tc.recursiveDelete("/test/empty", node)

	evs := drainEvents(events, 50*time.Millisecond)
	if len(evs) != 0 {
		t.Fatalf("expected 0 events for nil-data node, got %d", len(evs))
	}
}

func TestRecursiveStop(t *testing.T) {
	tc, _ := newTestTreeCache()

	grandchild := makeNode([]byte("gc"), false)
	child := makeNode([]byte("c"), false)
	child.children["gc"] = grandchild

	root := makeNode([]byte("r"), false)
	root.children["c"] = child

	tc.recursiveStop(root)

	for name, node := range map[string]*zookeeperTreeCacheNode{
		"root":       root,
		"child":      child,
		"grandchild": grandchild,
	} {
		if !node.stopped {
			t.Errorf("%s: expected stopped=true", name)
		}
	}
}

func TestRecursiveStop_AlreadyStopped(t *testing.T) {
	tc, _ := newTestTreeCache()

	node := makeNode([]byte("x"), true)
	// Should not panic or double-signal done.
	tc.recursiveStop(node)
	if !node.stopped {
		t.Error("expected stopped=true")
	}
}

func TestResyncState_RemovesStaleNodes(t *testing.T) {
	tc, events := newTestTreeCache()

	// Previous state had child "a" and "b".
	prevA := makeNode([]byte("a-data"), false)
	prevB := makeNode([]byte("b-data"), false)
	previousState := &zookeeperTreeCacheNode{
		children: map[string]*zookeeperTreeCacheNode{
			"a": prevA,
			"b": prevB,
		},
	}

	// Current state only has "a".
	curA := makeNode([]byte("a-data-new"), false)
	currentState := &zookeeperTreeCacheNode{
		children: map[string]*zookeeperTreeCacheNode{
			"a": curA,
		},
	}

	tc.resyncState("/test", currentState, previousState)

	evs := drainEvents(events, 50*time.Millisecond)
	// "b" was removed, so we expect a delete event for it.
	if len(evs) != 1 {
		t.Fatalf("expected 1 delete event, got %d", len(evs))
	}
	if evs[0].Path != "/test/b" {
		t.Errorf("event path = %q, want /test/b", evs[0].Path)
	}
	if evs[0].Data != nil {
		t.Error("expected Data=nil for delete event")
	}
}

func TestResyncState_KeepsCommonNodes(t *testing.T) {
	tc, events := newTestTreeCache()

	prev := &zookeeperTreeCacheNode{
		children: map[string]*zookeeperTreeCacheNode{
			"a": makeNode([]byte("old"), false),
		},
	}
	cur := &zookeeperTreeCacheNode{
		children: map[string]*zookeeperTreeCacheNode{
			"a": makeNode([]byte("new"), false),
		},
	}

	tc.resyncState("/test", cur, prev)

	evs := drainEvents(events, 50*time.Millisecond)
	if len(evs) != 0 {
		t.Fatalf("expected 0 events when all nodes still exist, got %d", len(evs))
	}
}
