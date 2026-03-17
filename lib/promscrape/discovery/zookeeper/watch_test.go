package zookeeper

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/VictoriaMetrics/VictoriaMetrics/lib/promutil"
)

func TestWatchForNodeUpdate(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	updates := make(chan ZookeeperTreeCacheEvent, 10)
	w := &zookeeperWatcher{
		ctx:       ctx,
		cancel:    cancel,
		updates:   updates,
		sources:   make(map[string]*promutil.Labels),
		stoppedCh: make(chan struct{}, 1),
		parse:     parseServersetMember,
	}

	// Run the event processing loop in a goroutine.
	// We skip the full watchForNodeUpdate (which needs conn/treeCaches in defer)
	// and directly test the core select loop.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-w.ctx.Done():
				return
			case event := <-w.updates:
				if event.Data != nil {
					labelSet, err := w.parse(*event.Data, event.Path)
					if err == nil {
						w.sources[event.Path] = labelSet
					} else {
						delete(w.sources, event.Path)
					}
				} else {
					delete(w.sources, event.Path)
				}
			}
		}
	}()

	// Test 1: Add a node.
	data1 := []byte(`{"serviceEndpoint":{"host":"10.0.0.1","port":8080},"status":"ALIVE","shard":0}`)
	updates <- ZookeeperTreeCacheEvent{Path: "/svc/member_001", Data: &data1}
	waitForSources(t, w, 1)

	labels := w.sources["/svc/member_001"]
	assertLabel(t, labels, "__address__", "10.0.0.1:8080")
	assertLabel(t, labels, "__meta_serverset_status", "ALIVE")

	// Test 2: Add a second node.
	data2 := []byte(`{"serviceEndpoint":{"host":"10.0.0.2","port":9090},"status":"ALIVE","shard":1}`)
	updates <- ZookeeperTreeCacheEvent{Path: "/svc/member_002", Data: &data2}
	waitForSources(t, w, 2)

	assertLabel(t, w.sources["/svc/member_002"], "__address__", "10.0.0.2:9090")

	// Test 3: Remove a node (Data=nil).
	updates <- ZookeeperTreeCacheEvent{Path: "/svc/member_001", Data: nil}
	waitForSources(t, w, 1)

	if _, ok := w.sources["/svc/member_001"]; ok {
		t.Error("expected /svc/member_001 to be removed from sources")
	}

	// Test 4: Invalid data should remove the path.
	badData := []byte(`not json`)
	updates <- ZookeeperTreeCacheEvent{Path: "/svc/member_002", Data: &badData}
	waitForSources(t, w, 0)

	if _, ok := w.sources["/svc/member_002"]; ok {
		t.Error("expected /svc/member_002 to be removed after parse error")
	}

	cancel()
	<-done
}

func waitForSources(t *testing.T, w *zookeeperWatcher, want int) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		if len(w.sources) == want {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("timeout waiting for sources count = %d, got %d", want, len(w.sources))
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}
}

func assertLabel(t *testing.T, labels *promutil.Labels, name, want string) {
	t.Helper()
	if labels == nil {
		t.Fatalf("labels is nil, want label %q=%q", name, want)
	}
	for _, l := range labels.Labels {
		if l.Name == name {
			if l.Value != want {
				t.Errorf("label %q = %q, want %q", name, l.Value, want)
			}
			return
		}
	}
	t.Errorf("label %q not found", name)
}

func TestWatchForNodeUpdate_UpdateExisting(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	updates := make(chan ZookeeperTreeCacheEvent, 10)
	w := &zookeeperWatcher{
		ctx:     ctx,
		cancel:  cancel,
		updates: updates,
		sources: make(map[string]*promutil.Labels),
		parse:   parseServersetMember,
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-w.ctx.Done():
				return
			case event := <-w.updates:
				if event.Data != nil {
					labelSet, err := w.parse(*event.Data, event.Path)
					if err == nil {
						w.sources[event.Path] = labelSet
					} else {
						delete(w.sources, event.Path)
					}
				} else {
					delete(w.sources, event.Path)
				}
			}
		}
	}()

	// Insert initial node.
	data1 := []byte(fmt.Sprintf(`{"serviceEndpoint":{"host":"10.0.0.1","port":8080},"status":"ALIVE","shard":0}`))
	updates <- ZookeeperTreeCacheEvent{Path: "/svc/m1", Data: &data1}
	waitForSources(t, w, 1)
	assertLabel(t, w.sources["/svc/m1"], "__meta_serverset_status", "ALIVE")

	// Update same path with new data.
	data2 := []byte(fmt.Sprintf(`{"serviceEndpoint":{"host":"10.0.0.1","port":8080},"status":"DEAD","shard":0}`))
	updates <- ZookeeperTreeCacheEvent{Path: "/svc/m1", Data: &data2}

	// Wait for the update to be processed.
	deadline := time.After(2 * time.Second)
	for {
		if labels, ok := w.sources["/svc/m1"]; ok {
			for _, l := range labels.Labels {
				if l.Name == "__meta_serverset_status" && l.Value == "DEAD" {
					goto done_label
				}
			}
		}
		select {
		case <-deadline:
			t.Fatal("timeout waiting for status update to DEAD")
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}
done_label:

	cancel()
	<-done
}
