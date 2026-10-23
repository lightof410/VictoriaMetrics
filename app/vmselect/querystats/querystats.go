package querystats

import (
	"flag"
	"fmt"
	"io"
	"net"
	"sort"
	"sync"
	"time"

	"github.com/VictoriaMetrics/VictoriaMetrics/lib/flagutil"
	"github.com/VictoriaMetrics/VictoriaMetrics/lib/logger"
	"github.com/VictoriaMetrics/VictoriaMetrics/lib/stringsutil"
)

// maxClientAddresses limits the number of distinct client addresses and
// X-Forwarded-For values returned per query at /api/v1/status/top_queries.
const maxClientAddresses = 8

var (
	lastQueriesCount = flag.Int("search.queryStats.lastQueriesCount", 20000, "Query stats for /api/v1/status/top_queries is tracked on this number of last queries. "+
		"Zero value disables query stats tracking")
	minQueryDuration    = flag.Duration("search.queryStats.minQueryDuration", time.Millisecond, "The minimum duration for queries to track in query stats at /api/v1/status/top_queries. Queries with lower duration are ignored in query stats")
	minQueryMemoryUsage = flagutil.NewBytes("search.queryStats.minQueryMemoryUsage", 1024, "The minimum memory bytes consumption for queries to track in query stats at /api/v1/status/top_queries. Queries with lower memory bytes consumption are ignored in query stats")
)

var (
	qsTracker *queryStatsTracker
	initOnce  sync.Once
)

// Enabled returns true of query stats tracking is enabled.
func Enabled() bool {
	return *lastQueriesCount > 0
}

// RegisterQuery registers the query on the given timeRangeMsecs, which has been started at startTime.
//
// remoteAddr is the client address from the HTTP request, xForwardedFor is the raw X-Forwarded-For header.
//
// RegisterQuery must be called when the query is finished.
func RegisterQuery(accountID, projectID uint32, query string, timeRangeMsecs int64, startTime time.Time, memoryUsage int64, remoteAddr, xForwardedFor string) {
	initOnce.Do(initQueryStats)
	qsTracker.registerQuery(accountID, projectID, query, timeRangeMsecs, startTime, memoryUsage, remoteAddr, xForwardedFor)
}

// RegisterQueryMultiTenant registers the query on the given timeRangeMsecs, which has been started at startTime.
//
// remoteAddr is the client address from the HTTP request, xForwardedFor is the raw X-Forwarded-For header.
//
// RegisterQueryMultiTenant must be called when the query is finished.
func RegisterQueryMultiTenant(query string, timeRangeMsecs int64, startTime time.Time, remoteAddr, xForwardedFor string) {
	initOnce.Do(initQueryStats)
	qsTracker.registerQueryMultiTenant(query, timeRangeMsecs, startTime, remoteAddr, xForwardedFor)
}

// WriteJSONQueryStats writes query stats to given writer in json format.
func WriteJSONQueryStats(w io.Writer, topN int, maxLifetime time.Duration) {
	initOnce.Do(initQueryStats)
	qsTracker.writeJSONQueryStats(w, topN, nil, maxLifetime)
}

// WriteJSONQueryStatsForAccountProject writes query stats for the given (accountID, projectID) to given writer in json format.
func WriteJSONQueryStatsForAccountProject(w io.Writer, topN int, accountID, projectID uint32, maxLifetime time.Duration) {
	initOnce.Do(initQueryStats)
	apFilter := &accountProjectFilter{
		accountID: accountID,
		projectID: projectID,
	}
	qsTracker.writeJSONQueryStats(w, topN, apFilter, maxLifetime)
}

// queryStatsTracker holds statistics for queries
type queryStatsTracker struct {
	mu      sync.Mutex
	a       []queryStatRecord
	nextIdx uint
}

type queryStatRecord struct {
	accountID     uint32
	projectID     uint32
	query         string
	timeRangeSecs int64
	registerTime  time.Time
	duration      time.Duration
	multiTenant   bool
	memoryUsage   int64
	remoteAddr    string
	xForwardedFor string
}

// queryStatClients holds the distinct client addresses and X-Forwarded-For
// values observed for a single query stats entry.
type queryStatClients struct {
	remoteAddrs   []string
	xForwardedFor []string
	truncated     bool
}

func (cs *queryStatClients) add(remoteAddr, xForwardedFor string) {
	if remoteAddr != "" && !containsString(cs.remoteAddrs, remoteAddr) {
		if len(cs.remoteAddrs) < maxClientAddresses {
			cs.remoteAddrs = append(cs.remoteAddrs, remoteAddr)
		} else {
			cs.truncated = true
		}
	}
	if xForwardedFor != "" && !containsString(cs.xForwardedFor, xForwardedFor) {
		if len(cs.xForwardedFor) < maxClientAddresses {
			cs.xForwardedFor = append(cs.xForwardedFor, xForwardedFor)
		} else {
			cs.truncated = true
		}
	}
}

func containsString(a []string, s string) bool {
	for _, v := range a {
		if v == s {
			return true
		}
	}
	return false
}

// normalizeRemoteAddr drops the port from remoteAddr, so queries from the same
// client are aggregated regardless of the ephemeral port used by its connections.
func normalizeRemoteAddr(remoteAddr string) string {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		return remoteAddr
	}
	return host
}

type queryStatKey struct {
	accountID     uint32
	projectID     uint32
	query         string
	timeRangeSecs int64
	multiTenant   bool
}

type accountProjectFilter struct {
	accountID uint32
	projectID uint32
}

func initQueryStats() {
	recordsCount := *lastQueriesCount
	if recordsCount <= 0 {
		recordsCount = 1
	} else {
		logger.Infof("enabled query stats tracking at `/api/v1/status/top_queries` with -search.queryStats.lastQueriesCount=%d, -search.queryStats.minQueryDuration=%s, -search.queryStats.minQueryMemoryUsage=%s",
			*lastQueriesCount, *minQueryDuration, minQueryMemoryUsage)
	}
	qsTracker = &queryStatsTracker{
		a: make([]queryStatRecord, recordsCount),
	}
}

func (qst *queryStatsTracker) writeJSONQueryStats(w io.Writer, topN int, apiFilter *accountProjectFilter, maxLifetime time.Duration) {
	fmt.Fprintf(w, `{"topN":"%d","maxLifetime":"%s",`, topN, maxLifetime)
	fmt.Fprintf(w, `"search.queryStats.lastQueriesCount":%d,`, *lastQueriesCount)
	fmt.Fprintf(w, `"search.queryStats.minQueryDuration":"%s",`, *minQueryDuration)
	fmt.Fprintf(w, `"search.queryStats.minQueryMemoryUsage":"%s",`, minQueryMemoryUsage)
	fmt.Fprintf(w, `"topByCount":[`)
	topByCount := qst.getTopByCount(topN, apiFilter, maxLifetime)
	for i, r := range topByCount {
		fmt.Fprintf(w, `{"accountID":%d,"projectID":%d,"query":%s,"timeRangeSeconds":%d,"count":%d,"multiTenant":%v`,
			r.accountID, r.projectID, stringsutil.JSONString(r.query), r.timeRangeSecs, r.count, r.multiTenant)
		writeClientsJSON(w, r.clients)
		fmt.Fprintf(w, `}`)
		if i+1 < len(topByCount) {
			fmt.Fprintf(w, `,`)
		}
	}
	fmt.Fprintf(w, `],"topByAvgDuration":[`)
	topByAvgDuration := qst.getTopByAvgDuration(topN, apiFilter, maxLifetime)
	for i, r := range topByAvgDuration {
		fmt.Fprintf(w, `{"accountID":%d,"projectID":%d,"query":%s,"timeRangeSeconds":%d,"avgDurationSeconds":%.3f,"count":%d,"multiTenant":%v`,
			r.accountID, r.projectID, stringsutil.JSONString(r.query), r.timeRangeSecs, r.duration.Seconds(), r.count, r.multiTenant)
		writeClientsJSON(w, r.clients)
		fmt.Fprintf(w, `}`)
		if i+1 < len(topByAvgDuration) {
			fmt.Fprintf(w, `,`)
		}
	}
	fmt.Fprintf(w, `],"topBySumDuration":[`)
	topBySumDuration := qst.getTopBySumDuration(topN, apiFilter, maxLifetime)
	for i, r := range topBySumDuration {
		fmt.Fprintf(w, `{"accountID":%d,"projectID":%d,"query":%s,"timeRangeSeconds":%d,"sumDurationSeconds":%.3f,"count":%d,"multiTenant":%v`,
			r.accountID, r.projectID, stringsutil.JSONString(r.query), r.timeRangeSecs, r.duration.Seconds(), r.count, r.multiTenant)
		writeClientsJSON(w, r.clients)
		fmt.Fprintf(w, `}`)
		if i+1 < len(topBySumDuration) {
			fmt.Fprintf(w, `,`)
		}
	}

	fmt.Fprintf(w, `],"topByAvgMemoryUsage":[`)
	topByAvgMemoryConsumption := qst.getTopByAvgMemoryUsage(topN, apiFilter, maxLifetime)
	for i, r := range topByAvgMemoryConsumption {
		fmt.Fprintf(w, `{"query":%s,"timeRangeSeconds":%d,"avgMemoryBytes":%d,"count":%d`,
			stringsutil.JSONString(r.query), r.timeRangeSecs, r.memoryUsage, r.count)
		writeClientsJSON(w, r.clients)
		fmt.Fprintf(w, `}`)
		if i+1 < len(topByAvgMemoryConsumption) {
			fmt.Fprintf(w, `,`)
		}
	}

	fmt.Fprintf(w, `]}`)
}

func writeClientsJSON(w io.Writer, clients *queryStatClients) {
	fmt.Fprintf(w, `,"remoteAddrs":[`)
	if clients != nil {
		for i, addr := range clients.remoteAddrs {
			if i > 0 {
				fmt.Fprintf(w, `,`)
			}
			fmt.Fprintf(w, `%s`, stringsutil.JSONString(addr))
		}
	}
	fmt.Fprintf(w, `],"xForwardedFor":[`)
	if clients != nil {
		for i, xff := range clients.xForwardedFor {
			if i > 0 {
				fmt.Fprintf(w, `,`)
			}
			fmt.Fprintf(w, `%s`, stringsutil.JSONString(xff))
		}
	}
	fmt.Fprintf(w, `],"clientsTruncated":%v`, clients != nil && clients.truncated)
}

func (qst *queryStatsTracker) registerQuery(accountID, projectID uint32, query string, timeRangeMsecs int64, startTime time.Time, memoryUsage int64, remoteAddr, xForwardedFor string) {
	registerTime := time.Now()
	duration := registerTime.Sub(startTime)
	if duration < *minQueryDuration {
		return
	}
	if memoryUsage < int64(minQueryMemoryUsage.IntN()) {
		return
	}

	qst.mu.Lock()
	defer qst.mu.Unlock()

	a := qst.a
	idx := qst.nextIdx
	if idx >= uint(len(a)) {
		idx = 0
	}
	qst.nextIdx = idx + 1
	r := &a[idx]
	r.accountID = accountID
	r.projectID = projectID
	r.query = query
	r.timeRangeSecs = timeRangeMsecs / 1000
	r.registerTime = registerTime
	r.duration = duration
	r.memoryUsage = memoryUsage
	r.remoteAddr = normalizeRemoteAddr(remoteAddr)
	r.xForwardedFor = xForwardedFor
}

func (qst *queryStatsTracker) registerQueryMultiTenant(query string, timeRangeMsecs int64, startTime time.Time, remoteAddr, xForwardedFor string) {
	registerTime := time.Now()
	duration := registerTime.Sub(startTime)
	if duration < *minQueryDuration {
		return
	}

	qst.mu.Lock()
	defer qst.mu.Unlock()

	a := qst.a
	idx := qst.nextIdx
	if idx >= uint(len(a)) {
		idx = 0
	}
	qst.nextIdx = idx + 1
	r := &a[idx]
	r.multiTenant = true
	r.query = query
	r.timeRangeSecs = timeRangeMsecs / 1000
	r.registerTime = registerTime
	r.duration = duration
	r.remoteAddr = normalizeRemoteAddr(remoteAddr)
	r.xForwardedFor = xForwardedFor
}

func (r *queryStatRecord) matches(apFilter *accountProjectFilter, currentTime time.Time, maxLifetime time.Duration) bool {
	if r.query == "" || currentTime.Sub(r.registerTime) > maxLifetime {
		return false
	}
	if apFilter != nil && (apFilter.accountID != r.accountID || apFilter.projectID != r.projectID) {
		return false
	}
	return true
}

func (r *queryStatRecord) key() queryStatKey {
	return queryStatKey{
		accountID:     r.accountID,
		projectID:     r.projectID,
		query:         r.query,
		timeRangeSecs: r.timeRangeSecs,
		multiTenant:   r.multiTenant,
	}
}

func (qst *queryStatsTracker) getTopByCount(topN int, apFilter *accountProjectFilter, maxLifetime time.Duration) []queryStatByCount {
	currentTime := time.Now()
	qst.mu.Lock()
	m := make(map[queryStatKey]int)
	for _, r := range qst.a {
		if r.matches(apFilter, currentTime, maxLifetime) {
			k := r.key()
			m[k] = m[k] + 1
		}
	}
	qst.mu.Unlock()

	var a []queryStatByCount
	for k, count := range m {
		a = append(a, queryStatByCount{
			accountID:     k.accountID,
			projectID:     k.projectID,
			query:         k.query,
			timeRangeSecs: k.timeRangeSecs,
			count:         count,
			multiTenant:   k.multiTenant,
			key:           k,
		})
	}
	sort.Slice(a, func(i, j int) bool {
		return a[i].count > a[j].count
	})
	if len(a) > topN {
		a = a[:topN]
	}
	keys := make([]queryStatKey, 0, len(a))
	for i := range a {
		keys = append(keys, a[i].key)
	}
	clients := qst.collectClients(keys, apFilter, maxLifetime)
	for i := range a {
		a[i].clients = clients[a[i].key]
	}
	return a
}

type queryStatByCount struct {
	accountID     uint32
	projectID     uint32
	query         string
	timeRangeSecs int64
	count         int
	multiTenant   bool
	key           queryStatKey
	clients       *queryStatClients
}

func (qst *queryStatsTracker) getTopByAvgDuration(topN int, apFilter *accountProjectFilter, maxLifetime time.Duration) []queryStatByDuration {
	currentTime := time.Now()
	qst.mu.Lock()
	type countSum struct {
		count int
		sum   time.Duration
	}
	m := make(map[queryStatKey]countSum)
	for _, r := range qst.a {
		if r.matches(apFilter, currentTime, maxLifetime) {
			k := r.key()
			ks := m[k]
			ks.count++
			ks.sum += r.duration
			m[k] = ks
		}
	}
	qst.mu.Unlock()

	var a []queryStatByDuration
	for k, ks := range m {
		a = append(a, queryStatByDuration{
			accountID:     k.accountID,
			projectID:     k.projectID,
			query:         k.query,
			timeRangeSecs: k.timeRangeSecs,
			duration:      ks.sum / time.Duration(ks.count),
			count:         ks.count,
			multiTenant:   k.multiTenant,
			key:           k,
		})
	}
	sort.Slice(a, func(i, j int) bool {
		return a[i].duration > a[j].duration
	})
	if len(a) > topN {
		a = a[:topN]
	}
	keys := make([]queryStatKey, 0, len(a))
	for i := range a {
		keys = append(keys, a[i].key)
	}
	clients := qst.collectClients(keys, apFilter, maxLifetime)
	for i := range a {
		a[i].clients = clients[a[i].key]
	}
	return a
}

type queryStatByDuration struct {
	accountID     uint32
	projectID     uint32
	query         string
	timeRangeSecs int64
	duration      time.Duration
	count         int
	multiTenant   bool
	key           queryStatKey
	clients       *queryStatClients
}

func (qst *queryStatsTracker) getTopBySumDuration(topN int, apFilter *accountProjectFilter, maxLifetime time.Duration) []queryStatByDuration {
	currentTime := time.Now()
	qst.mu.Lock()
	type countDuration struct {
		count int
		sum   time.Duration
	}
	m := make(map[queryStatKey]countDuration)
	for _, r := range qst.a {
		if r.matches(apFilter, currentTime, maxLifetime) {
			k := r.key()
			kd := m[k]
			kd.count++
			kd.sum += r.duration
			m[k] = kd
		}
	}
	qst.mu.Unlock()

	var a []queryStatByDuration
	for k, kd := range m {
		a = append(a, queryStatByDuration{
			accountID:     k.accountID,
			projectID:     k.projectID,
			query:         k.query,
			timeRangeSecs: k.timeRangeSecs,
			duration:      kd.sum,
			count:         kd.count,
			multiTenant:   k.multiTenant,
			key:           k,
		})
	}
	sort.Slice(a, func(i, j int) bool {
		return a[i].duration > a[j].duration
	})
	if len(a) > topN {
		a = a[:topN]
	}
	keys := make([]queryStatKey, 0, len(a))
	for i := range a {
		keys = append(keys, a[i].key)
	}
	clients := qst.collectClients(keys, apFilter, maxLifetime)
	for i := range a {
		a[i].clients = clients[a[i].key]
	}
	return a
}

type queryStatByMemory struct {
	query         string
	timeRangeSecs int64
	memoryUsage   int64
	count         int
	key           queryStatKey
	clients       *queryStatClients
}

func (qst *queryStatsTracker) getTopByAvgMemoryUsage(topN int, apFilter *accountProjectFilter, maxLifetime time.Duration) []queryStatByMemory {
	currentTime := time.Now()
	qst.mu.Lock()
	type countSum struct {
		count int
		sum   int64
	}
	m := make(map[queryStatKey]countSum)
	for _, r := range qst.a {
		if r.matches(apFilter, currentTime, maxLifetime) {
			k := r.key()
			ks := m[k]
			ks.count++
			ks.sum += r.memoryUsage
			m[k] = ks
		}
	}
	qst.mu.Unlock()

	var a []queryStatByMemory
	for k, ks := range m {
		a = append(a, queryStatByMemory{
			query:         k.query,
			timeRangeSecs: k.timeRangeSecs,
			memoryUsage:   ks.sum / int64(ks.count),
			count:         ks.count,
			key:           k,
		})
	}
	sort.Slice(a, func(i, j int) bool {
		return a[i].memoryUsage > a[j].memoryUsage
	})
	if len(a) > topN {
		a = a[:topN]
	}
	keys := make([]queryStatKey, 0, len(a))
	for i := range a {
		keys = append(keys, a[i].key)
	}
	clients := qst.collectClients(keys, apFilter, maxLifetime)
	for i := range a {
		a[i].clients = clients[a[i].key]
	}
	return a
}

// collectClients returns the distinct client addresses tracked for the given query keys.
func (qst *queryStatsTracker) collectClients(keys []queryStatKey, apFilter *accountProjectFilter, maxLifetime time.Duration) map[queryStatKey]*queryStatClients {
	wanted := make(map[queryStatKey]struct{}, len(keys))
	for _, k := range keys {
		wanted[k] = struct{}{}
	}
	clients := make(map[queryStatKey]*queryStatClients, len(keys))
	currentTime := time.Now()
	qst.mu.Lock()
	for _, r := range qst.a {
		if !r.matches(apFilter, currentTime, maxLifetime) {
			continue
		}
		k := r.key()
		if _, ok := wanted[k]; !ok {
			continue
		}
		cs := clients[k]
		if cs == nil {
			cs = &queryStatClients{}
			clients[k] = cs
		}
		cs.add(r.remoteAddr, r.xForwardedFor)
	}
	qst.mu.Unlock()
	return clients
}
