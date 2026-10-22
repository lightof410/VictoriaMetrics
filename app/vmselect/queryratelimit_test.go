package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/VictoriaMetrics/VictoriaMetrics/lib/auth"
	"github.com/VictoriaMetrics/VictoriaMetrics/lib/httpserver"
)

func TestQueryExecutionRateLimiterSlidingWindow(t *testing.T) {
	l := newQueryExecutionRateLimiter(10, 10*time.Second)
	now := time.Unix(1700000000, 0)

	for i := 0; i < 10; i++ {
		if ok, _ := l.allow("q", now); !ok {
			t.Fatalf("unexpected rejection at execution %d", i)
		}
	}

	ok, retryAfter := l.allow("q", now.Add(time.Second))
	if ok {
		t.Fatalf("expected the 11th execution to be rejected")
	}
	if retryAfter != 9*time.Second {
		t.Fatalf("unexpected retryAfter; got %s; want %s", retryAfter, 9*time.Second)
	}

	// The execution is allowed again after the sliding window is over.
	if ok, _ := l.allow("q", now.Add(10*time.Second)); !ok {
		t.Fatalf("expected the execution to be allowed after the window")
	}
}

func TestQueryExecutionRateLimiterIsolationPerKey(t *testing.T) {
	l := newQueryExecutionRateLimiter(1, 10*time.Second)
	now := time.Unix(1700000000, 0)

	if ok, _ := l.allow("q1", now); !ok {
		t.Fatalf("unexpected rejection for q1")
	}
	if ok, _ := l.allow("q2", now); !ok {
		t.Fatalf("unexpected rejection for q2")
	}
	if ok, _ := l.allow("q1", now); ok {
		t.Fatalf("expected the second execution of q1 to be rejected")
	}
}

func TestQueryExecutionRateLimiterDisabled(t *testing.T) {
	var nilLimiter *queryExecutionRateLimiter
	if ok, _ := nilLimiter.allow("q", time.Now()); !ok {
		t.Fatalf("expected nil limiter to allow the execution")
	}

	l := newQueryExecutionRateLimiter(0, 10*time.Second)
	now := time.Unix(1700000000, 0)
	for i := 0; i < 100; i++ {
		if ok, _ := l.allow("q", now); !ok {
			t.Fatalf("unexpected rejection at execution %d", i)
		}
	}
	if n := len(l.executionsByKey); n != 0 {
		t.Fatalf("unexpected number of tracked queries; got %d; want 0", n)
	}
}

func TestQueryExecutionRateLimiterCleanup(t *testing.T) {
	l := newQueryExecutionRateLimiter(10, 10*time.Second)
	now := time.Unix(1700000000, 0)

	if ok, _ := l.allow("q", now); !ok {
		t.Fatalf("unexpected rejection")
	}
	if ok, _ := l.allow("q2", now); !ok {
		t.Fatalf("unexpected rejection")
	}

	// Stale entries must be dropped by the periodic cleanup.
	l.cleanupLocked(now.Add(10*time.Second + time.Nanosecond))
	if n := len(l.executionsByKey); n != 0 {
		t.Fatalf("unexpected number of tracked queries after cleanup; got %d; want 0", n)
	}
}

func TestCheckQueryRateLimitHTTP(t *testing.T) {
	oldLimiter := queryRateLimiter
	defer func() {
		queryRateLimiter = oldLimiter
	}()
	queryRateLimiter = newQueryExecutionRateLimiter(1, 10*time.Second)

	at := &auth.Token{AccountID: 1}
	r := httptest.NewRequest(http.MethodGet, "/select/1/prometheus/api/v1/query?query=up", nil)
	if err := checkQueryRateLimit(httptest.NewRecorder(), at, r); err != nil {
		t.Fatalf("unexpected error on the first execution: %s", err)
	}

	w := httptest.NewRecorder()
	err := checkQueryRateLimit(w, at, r)
	if err == nil {
		t.Fatalf("expected the second execution of the same query to be rejected")
	}
	var esc *httpserver.ErrorWithStatusCode
	if !errors.As(err, &esc) {
		t.Fatalf("unexpected error type: %T", err)
	}
	if esc.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("unexpected status code; got %d; want %d", esc.StatusCode, http.StatusTooManyRequests)
	}
	retryAfter := w.Header().Get("Retry-After")
	n, parseErr := strconv.Atoi(retryAfter)
	if parseErr != nil || n < 1 {
		t.Fatalf("unexpected Retry-After header; got %q", retryAfter)
	}

	// The same query executed by another tenant must not be limited.
	at2 := &auth.Token{AccountID: 2}
	if err := checkQueryRateLimit(httptest.NewRecorder(), at2, r); err != nil {
		t.Fatalf("unexpected error for another tenant: %s", err)
	}

	// Another query must not be limited.
	r2 := httptest.NewRequest(http.MethodGet, `/select/1/prometheus/api/v1/query?query=up{job="vmagent"}`, nil)
	if err := checkQueryRateLimit(httptest.NewRecorder(), at, r2); err != nil {
		t.Fatalf("unexpected error for another query: %s", err)
	}

	// Requests without a query must be passed to the request handler as-is.
	r3 := httptest.NewRequest(http.MethodGet, "/select/1/prometheus/api/v1/query", nil)
	for i := 0; i < 2; i++ {
		if err := checkQueryRateLimit(httptest.NewRecorder(), at, r3); err != nil {
			t.Fatalf("unexpected error for an empty query: %s", err)
		}
	}
}

func TestQueryRateLimitKey(t *testing.T) {
	query := "up"
	if queryRateLimitKey(nil, query) == queryRateLimitKey(&auth.Token{AccountID: 1}, query) {
		t.Fatalf("multitenant and tenant-specific keys must differ")
	}
	if queryRateLimitKey(&auth.Token{AccountID: 1}, query) == queryRateLimitKey(&auth.Token{AccountID: 2}, query) {
		t.Fatalf("keys for different tenants must differ")
	}
	if queryRateLimitKey(&auth.Token{AccountID: 1}, "a\x00b") == queryRateLimitKey(&auth.Token{AccountID: 1}, "a") {
		t.Fatalf("keys for different queries must differ")
	}
}
