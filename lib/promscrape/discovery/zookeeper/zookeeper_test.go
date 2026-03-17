package zookeeper

import (
	"context"
	"testing"

	"github.com/VictoriaMetrics/VictoriaMetrics/lib/prompb"
	"github.com/VictoriaMetrics/VictoriaMetrics/lib/promutil"
)

func TestGetLabels_FromSources(t *testing.T) {
	sdc := &ServersetSDConfig{
		Servers: []string{"127.0.0.1:2181"},
		Paths:   []string{"/svc"},
	}

	// Pre-populate configMap with a fake apiConfig so GetLabels doesn't connect to ZK.
	w := &zookeeperWatcher{
		sources: map[string]*promutil.Labels{
			"/svc/member_001": {Labels: []prompb.Label{
				{Name: "__address__", Value: "10.0.0.1:8080"},
			}},
			"/svc/member_002": {Labels: []prompb.Label{
				{Name: "__address__", Value: "10.0.0.2:9090"},
			}},
		},
		ctx:    context.Background(),
		cancel: func() {},
	}
	cfg := &apiConfig{zkWatcher: w}
	configMap.Get(sdc, func() (any, error) { return cfg, nil })
	defer configMap.Delete(sdc)

	labels, err := sdc.GetLabels("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(labels) != 2 {
		t.Fatalf("expected 2 label sets, got %d", len(labels))
	}

	addrs := map[string]bool{}
	for _, ls := range labels {
		for _, l := range ls.Labels {
			if l.Name == "__address__" {
				addrs[l.Value] = true
			}
		}
	}
	if !addrs["10.0.0.1:8080"] || !addrs["10.0.0.2:9090"] {
		t.Errorf("unexpected addresses: %v", addrs)
	}
}

func TestGetLabels_EmptySources(t *testing.T) {
	sdc := &ServersetSDConfig{
		Servers: []string{"127.0.0.1:2181"},
		Paths:   []string{"/empty"},
	}

	w := &zookeeperWatcher{
		sources: map[string]*promutil.Labels{},
		ctx:     context.Background(),
		cancel:  func() {},
	}
	cfg := &apiConfig{zkWatcher: w}
	configMap.Get(sdc, func() (any, error) { return cfg, nil })
	defer configMap.Delete(sdc)

	labels, err := sdc.GetLabels("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(labels) != 0 {
		t.Fatalf("expected 0 label sets, got %d", len(labels))
	}
}

func TestMustStop_NoEntry(t *testing.T) {
	sdc := &ServersetSDConfig{
		Servers: []string{"127.0.0.1:2181"},
		Paths:   []string{"/nonexistent"},
	}
	// Should not panic when configMap has no entry for this config.
	sdc.MustStop()
}
