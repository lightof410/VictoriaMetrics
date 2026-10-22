package main

import (
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/VictoriaMetrics/metrics"

	"github.com/VictoriaMetrics/VictoriaMetrics/lib/auth"
	"github.com/VictoriaMetrics/VictoriaMetrics/lib/httpserver"
)

// queryExecutionRateLimiter limits the number of executions of the same query
// within a sliding time window.
//
// The limiter is keyed by tenant and query string, so the same PromQL expression
// executed by different tenants is limited independently.
type queryExecutionRateLimiter struct {
	maxExecutions int
	window        time.Duration

	mu              sync.Mutex
	executionsByKey map[string][]time.Time
	lastCleanup     time.Time
}

func newQueryExecutionRateLimiter(maxExecutions int, window time.Duration) *queryExecutionRateLimiter {
	return &queryExecutionRateLimiter{
		maxExecutions:   maxExecutions,
		window:          window,
		executionsByKey: make(map[string][]time.Time),
	}
}

// allow registers an execution of the query identified by key at the given time.
//
// It returns false if the query has already been executed maxExecutions times
// during the last window. In this case it also returns the duration after which
// the query can be executed again.
func (l *queryExecutionRateLimiter) allow(key string, now time.Time) (bool, time.Duration) {
	if l == nil || l.maxExecutions <= 0 || l.window <= 0 {
		return true, 0
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	// Periodically drop entries for queries, which haven't been executed during the last window.
	// This prevents unlimited growth of executionsByKey for queries, which are executed only once.
	if now.Sub(l.lastCleanup) >= l.window {
		l.cleanupLocked(now)
		l.lastCleanup = now
	}

	cutoff := now.Add(-l.window)
	executions := pruneExecutions(l.executionsByKey[key], cutoff)
	if len(executions) >= l.maxExecutions {
		l.executionsByKey[key] = executions
		return false, executions[0].Add(l.window).Sub(now)
	}
	l.executionsByKey[key] = append(executions, now)
	return true, 0
}

// cleanupLocked removes executions, which are outside the sliding window, and drops empty entries.
//
// l.mu must be held by the caller.
func (l *queryExecutionRateLimiter) cleanupLocked(now time.Time) {
	cutoff := now.Add(-l.window)
	for key, executions := range l.executionsByKey {
		executions = pruneExecutions(executions, cutoff)
		if len(executions) == 0 {
			delete(l.executionsByKey, key)
			continue
		}
		l.executionsByKey[key] = executions
	}
}

// pruneExecutions returns executions newer than cutoff.
//
// executions contains timestamps in ascending order.
func pruneExecutions(executions []time.Time, cutoff time.Time) []time.Time {
	i := 0
	for i < len(executions) && !executions[i].After(cutoff) {
		i++
	}
	if i == 0 {
		return executions
	}
	// Reuse the underlying array in order to avoid growing it on every execution.
	return append(executions[:0], executions[i:]...)
}

var queryRateLimiter *queryExecutionRateLimiter

var queryRateLimitRejected = metrics.NewCounter(`vm_query_rate_limit_rejected_total`)

// checkQueryRateLimit rejects the query if it has been executed too many times
// during -search.queryExecutionsWindow.
func checkQueryRateLimit(w http.ResponseWriter, at *auth.Token, r *http.Request) error {
	if queryRateLimiter == nil {
		return nil
	}
	query := r.FormValue("query")
	if query == "" {
		// Let the request handler return the proper error for an empty query.
		return nil
	}
	ok, retryAfter := queryRateLimiter.allow(queryRateLimitKey(at, query), time.Now())
	if ok {
		return nil
	}
	queryRateLimitRejected.Inc()
	retryAfterSeconds := int((retryAfter + time.Second - 1) / time.Second)
	if retryAfterSeconds < 1 {
		retryAfterSeconds = 1
	}
	w.Header().Set("Retry-After", strconv.Itoa(retryAfterSeconds))
	return &httpserver.ErrorWithStatusCode{
		Err: fmt.Errorf("cannot execute the same query more than %d times per %s; please retry in %d seconds. "+
			"The limit can be tuned via -search.maxQueryExecutions and -search.queryExecutionsWindow",
			queryRateLimiter.maxExecutions, queryRateLimiter.window, retryAfterSeconds),
		StatusCode: http.StatusTooManyRequests,
	}
}

// queryRateLimitKey returns the rate limiting key for the given tenant and query.
func queryRateLimitKey(at *auth.Token, query string) string {
	return at.String() + "\x00" + query
}
