package querystats

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestNormalizeRemoteAddr(t *testing.T) {
	f := func(remoteAddr, want string) {
		t.Helper()
		if got := normalizeRemoteAddr(remoteAddr); got != want {
			t.Fatalf("unexpected result for normalizeRemoteAddr(%q); got %q; want %q", remoteAddr, got, want)
		}
	}
	f("1.2.3.4:5678", "1.2.3.4")
	f("[2001:db8::1]:443", "2001:db8::1")
	f("1.2.3.4", "1.2.3.4")
	f("", "")
}

func TestQueryStatClientsAdd(t *testing.T) {
	var cs queryStatClients
	cs.add("1.2.3.4", "9.9.9.9, 10.0.0.1")
	cs.add("1.2.3.4", "9.9.9.9, 10.0.0.1")
	cs.add("", "")
	if len(cs.remoteAddrs) != 1 || cs.remoteAddrs[0] != "1.2.3.4" {
		t.Fatalf("unexpected remoteAddrs; got %q", cs.remoteAddrs)
	}
	if len(cs.xForwardedFor) != 1 || cs.xForwardedFor[0] != "9.9.9.9, 10.0.0.1" {
		t.Fatalf("unexpected xForwardedFor; got %q", cs.xForwardedFor)
	}
	if cs.truncated {
		t.Fatalf("unexpected truncated flag")
	}

	cs = queryStatClients{}
	for i := 0; i < maxClientAddresses+2; i++ {
		cs.add(fmt.Sprintf("10.0.0.%d", i), fmt.Sprintf("xff-%d", i))
	}
	if len(cs.remoteAddrs) != maxClientAddresses {
		t.Fatalf("unexpected number of remoteAddrs; got %d; want %d", len(cs.remoteAddrs), maxClientAddresses)
	}
	if len(cs.xForwardedFor) != maxClientAddresses {
		t.Fatalf("unexpected number of xForwardedFor; got %d; want %d", len(cs.xForwardedFor), maxClientAddresses)
	}
	if !cs.truncated {
		t.Fatalf("expecting truncated flag to be set")
	}
}

func TestCollectClients(t *testing.T) {
	now := time.Now()
	qst := &queryStatsTracker{
		a: make([]queryStatRecord, 3),
	}
	qst.a[0] = queryStatRecord{
		accountID:     1,
		projectID:     2,
		query:         "up",
		registerTime:  now,
		duration:      time.Second,
		remoteAddr:    "1.2.3.4",
		xForwardedFor: "9.9.9.9",
	}
	qst.a[1] = queryStatRecord{
		accountID:     1,
		projectID:     2,
		query:         "up",
		registerTime:  now,
		duration:      time.Second,
		remoteAddr:    "1.2.3.4",
		xForwardedFor: "9.9.9.9",
	}
	qst.a[2] = queryStatRecord{
		accountID:    1,
		projectID:    2,
		query:        "foo",
		registerTime: now,
		duration:     time.Second,
		remoteAddr:   "5.6.7.8",
	}

	k := qst.a[0].key()
	clients := qst.collectClients([]queryStatKey{k}, nil, time.Minute)
	cs := clients[k]
	if cs == nil {
		t.Fatalf("missing clients for key %v", k)
	}
	if len(cs.remoteAddrs) != 1 || cs.remoteAddrs[0] != "1.2.3.4" {
		t.Fatalf("unexpected remoteAddrs; got %q", cs.remoteAddrs)
	}
	if len(cs.xForwardedFor) != 1 || cs.xForwardedFor[0] != "9.9.9.9" {
		t.Fatalf("unexpected xForwardedFor; got %q", cs.xForwardedFor)
	}

	// Clients of queries outside of the requested key must be skipped.
	if _, ok := clients[qst.a[2].key()]; ok {
		t.Fatalf("unexpected clients for a query which wasn't requested")
	}
}

func TestWriteJSONQueryStatsClients(t *testing.T) {
	now := time.Now()
	qst := &queryStatsTracker{
		a: make([]queryStatRecord, 1),
	}
	qst.a[0] = queryStatRecord{
		accountID:     1,
		projectID:     2,
		query:         "up",
		registerTime:  now,
		duration:      2 * time.Second,
		memoryUsage:   1 << 20,
		remoteAddr:    "1.2.3.4",
		xForwardedFor: "9.9.9.9, 10.0.0.1",
	}

	var buf bytes.Buffer
	qst.writeJSONQueryStats(&buf, 10, nil, time.Minute)
	s := buf.String()

	for _, want := range []string{
		`"remoteAddrs":["1.2.3.4"]`,
		`"xForwardedFor":["9.9.9.9, 10.0.0.1"]`,
		`"clientsTruncated":false`,
	} {
		if n := strings.Count(s, want); n != 4 {
			t.Fatalf("unexpected number of %q in JSON; got %d; want 4; json=%s", want, n, s)
		}
	}
}
