// gordap-stress is a load + correctness tester for an RDAP server.
//
// Unlike generic HTTP load testers (k6, wrk, hey, vegeta), every
// response is *parsed and validated* against the deterministic
// expectation derived from internal/synth. A 200 with a malformed
// body counts as a failure, not as throughput.
//
// Metrics collected:
//   - Throughput per endpoint
//   - Latency p50/p90/p95/p99/p999 per endpoint and overall
//   - Status-code distribution
//   - Cache hit ratio (via X-Gordap-Cache header)
//   - Validation pass/fail counters with the first 5 mismatches printed
//
// Output is human-readable by default; --json emits a structured
// report suitable for CI assertions or trend lines.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bramheerink/gordap/internal/synth"
)

// ---------- workload ----------

type queryKind string

const (
	kDomain         queryKind = "domain"
	kDomainMissing  queryKind = "domain_missing"
	kEntity         queryKind = "entity"
	kEntityInvalid  queryKind = "entity_invalid"
	kNameserver     queryKind = "nameserver"
	kSearchDomains  queryKind = "search_domains"
	kSearchEntities queryKind = "search_entities"
	kHelp           queryKind = "help"
)

// query is a single planned request: where to go, what to expect.
// The expectation is what powers the inline correctness check —
// every response is parsed and matched against `wantStatus` plus the
// `validate` predicate.
type query struct {
	kind       queryKind
	path       string
	wantStatus int
	validate   func(body map[string]any) string // returns "" on pass, mismatch reason on fail
}

// workload mix, expressed as integer weights. The runtime weight-picker
// uses cumulative sum; tweaking these reflects realistic ratios:
//
//	70% domain (positive)  — most queries hit a name we know
//	 5% domain (negative)  — keep the 404 path in the mix
//	10% entity            — registrar lookups, abuse contacts
//	 1% entity invalid     — validate input rejection
//	 5% nameserver        — operator NS lookups
//	 7% search domains    — partial-match traffic
//	 1% search entities
//	 1% help
type weighted struct {
	kind   queryKind
	weight int
}

var defaultMix = []weighted{
	{kDomain, 70}, {kDomainMissing, 5},
	{kEntity, 10}, {kEntityInvalid, 1},
	{kNameserver, 5},
	{kSearchDomains, 7}, {kSearchEntities, 1},
	{kHelp, 1},
}

// ---------- metrics ----------

type bucket struct {
	count    atomic.Int64
	failures atomic.Int64 // validation failures (response parsed but didn't match expectation)
	lat      *latencies
}

func newBucket() *bucket { return &bucket{lat: newLatencies()} }

// latencies stores observation samples for percentile reporting.
// Reservoir-bounded so memory stays predictable on long runs.
type latencies struct {
	mu       sync.Mutex
	samples  []time.Duration
	cap      int
	seen     int64
	min, max time.Duration
}

const sampleCap = 100_000

func newLatencies() *latencies { return &latencies{cap: sampleCap, min: time.Hour} }

func (l *latencies) Add(d time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.seen++
	if d < l.min {
		l.min = d
	}
	if d > l.max {
		l.max = d
	}
	if int64(len(l.samples)) < int64(l.cap) {
		l.samples = append(l.samples, d)
		return
	}
	// Reservoir sampling: replace a random existing slot.
	idx := rand.Int64N(l.seen)
	if idx < int64(l.cap) {
		l.samples[idx] = d
	}
}

func (l *latencies) percentiles(ps ...float64) []time.Duration {
	l.mu.Lock()
	cp := make([]time.Duration, len(l.samples))
	copy(cp, l.samples)
	l.mu.Unlock()
	sort.Slice(cp, func(i, j int) bool { return cp[i] < cp[j] })
	out := make([]time.Duration, len(ps))
	for i, p := range ps {
		if len(cp) == 0 {
			continue
		}
		idx := int(float64(len(cp))*p) - 1
		if idx < 0 {
			idx = 0
		}
		if idx >= len(cp) {
			idx = len(cp) - 1
		}
		out[i] = cp[idx]
	}
	return out
}

type stats struct {
	startedAt time.Time
	overall   *bucket
	byKind    sync.Map // queryKind → *bucket
	byStatus  sync.Map // int → *atomic.Int64
	cacheHit  atomic.Int64
	cacheMiss atomic.Int64
	transErr  atomic.Int64 // network errors / timeouts (no HTTP response)

	// First N mismatch examples for human debugging.
	mismatchMu sync.Mutex
	mismatches []string
}

const maxRecordedMismatches = 10

func newStats() *stats {
	return &stats{startedAt: time.Now(), overall: newBucket()}
}

func (s *stats) bucketFor(k queryKind) *bucket {
	if b, ok := s.byKind.Load(k); ok {
		return b.(*bucket)
	}
	nb := newBucket()
	actual, _ := s.byKind.LoadOrStore(k, nb)
	return actual.(*bucket)
}

func (s *stats) recordStatus(code int) {
	if v, ok := s.byStatus.Load(code); ok {
		v.(*atomic.Int64).Add(1)
		return
	}
	c := &atomic.Int64{}
	c.Store(1)
	s.byStatus.LoadOrStore(code, c)
}

func (s *stats) recordMismatch(q query, code int, reason string) {
	s.mismatchMu.Lock()
	defer s.mismatchMu.Unlock()
	if len(s.mismatches) >= maxRecordedMismatches {
		return
	}
	s.mismatches = append(s.mismatches,
		fmt.Sprintf("[%s] %s → status=%d: %s", q.kind, q.path, code, reason))
}

// ---------- main ----------

func main() {
	var (
		url         = flag.String("url", "http://localhost:8080", "target gordap URL")
		concurrency = flag.Int("c", 50, "concurrent workers")
		duration    = flag.Duration("d", 30*time.Second, "test duration")
		corpus      = flag.Int("n", 10_000, "size of the seed corpus (must match gordap-seed -n)")
		timeout     = flag.Duration("timeout", 5*time.Second, "per-request timeout")
		jsonOut     = flag.Bool("json", false, "emit machine-readable JSON report")
		warmup      = flag.Duration("warmup", 0, "warmup duration before measuring (defaults to none)")
		dist        = flag.String("dist", "uniform", "key distribution: uniform | zipf (zipf models the long-tail of real RDAP traffic)")
	)
	flag.Parse()

	if *concurrency <= 0 || *duration <= 0 || *corpus <= 0 {
		fmt.Fprintln(os.Stderr, "concurrency, duration and corpus must all be positive")
		os.Exit(2)
	}
	switch *dist {
	case "uniform", "zipf":
	default:
		fmt.Fprintln(os.Stderr, "dist must be 'uniform' or 'zipf'")
		os.Exit(2)
	}

	// Optional warmup: send traffic but don't record.
	if *warmup > 0 {
		warmCtx, cancel := context.WithTimeout(context.Background(), *warmup)
		runStress(warmCtx, *url, *concurrency, *corpus, *timeout, *dist, newStats())
		cancel()
	}

	st := newStats()
	ctx, cancel := context.WithTimeout(context.Background(), *duration)
	defer cancel()
	runStress(ctx, *url, *concurrency, *corpus, *timeout, *dist, st)

	if *jsonOut {
		emitJSON(st, *concurrency, *duration, *corpus, *url, os.Stdout)
		return
	}
	emitText(st, *concurrency, *duration, *corpus, *url, os.Stdout)
}

// pickKey picks an integer in [0, corpus) using the chosen
// distribution. "uniform" gives each key equal probability — useful
// for finding the cold-path floor. "zipf" with alpha ~2 produces a
// long-tail where the top ~1% of keys account for ~50% of queries,
// matching what RDAP operators observe in production logs (a tiny
// set of celebrity domains dominates traffic). Real cache hit ratio
// and tail-latency numbers only show up under Zipf.
func pickKey(r *rand.Rand, corpus int, dist string) int {
	if dist == "zipf" {
		// Inverse CDF: i = corpus * u^alpha, alpha=2 → strong skew.
		u := r.Float64()
		idx := int(float64(corpus) * (u * u))
		if idx < 0 {
			idx = 0
		}
		if idx >= corpus {
			idx = corpus - 1
		}
		return idx
	}
	return r.IntN(corpus)
}

func runStress(ctx context.Context, base string, concurrency, corpus int, timeout time.Duration, dist string, st *stats) {
	// Tuned Transport: default MaxIdleConnsPerHost=2 starves a 100-worker
	// pool. Bump it so connection setup doesn't dominate the measured
	// latency tail.
	transport := &http.Transport{
		MaxIdleConns:        concurrency * 2,
		MaxIdleConnsPerHost: concurrency * 2,
		IdleConnTimeout:     90 * time.Second,
	}

	var wg sync.WaitGroup
	for w := 0; w < concurrency; w++ {
		wg.Add(1)
		go func(seed uint64) {
			defer wg.Done()
			r := rand.New(rand.NewPCG(seed, seed^0xdeadbeef))
			client := &http.Client{Timeout: timeout, Transport: transport}
			for ctx.Err() == nil {
				q := pickQuery(r, corpus, dist)
				// Detached request context: per-request deadline is the
				// http.Client.Timeout, NOT the test-duration ctx. Otherwise
				// every in-flight request at the moment the test ends
				// fails with "context deadline exceeded" — pure tool
				// artefact, looks like server bugs in the report.
				exec(context.Background(), client, base, q, st)
			}
		}(uint64(w))
	}
	wg.Wait()
}

// pickQuery uses the cumulative-weight algorithm to choose a query
// kind and instantiates it against the deterministic corpus. The
// dist parameter controls how the integer index inside the corpus is
// drawn — uniform for cold-path measurement, zipf for production-
// like skew.
func pickQuery(r *rand.Rand, corpus int, dist string) query {
	total := 0
	for _, m := range defaultMix {
		total += m.weight
	}
	roll := r.IntN(total)
	var kind queryKind
	for _, m := range defaultMix {
		if roll < m.weight {
			kind = m.kind
			break
		}
		roll -= m.weight
	}

	switch kind {
	case kDomain:
		i := pickKey(r, corpus, dist)
		name := synth.DomainName(i)
		return query{
			kind: kind, path: "/domain/" + name, wantStatus: 200,
			validate: func(b map[string]any) string {
				if got, _ := b["objectClassName"].(string); got != "domain" {
					return fmt.Sprintf("objectClassName=%q want \"domain\"", got)
				}
				if got, _ := b["ldhName"].(string); got != name {
					return fmt.Sprintf("ldhName=%q want %q", got, name)
				}
				return ""
			},
		}
	case kDomainMissing:
		// Use index well outside the corpus → must 404.
		i := corpus + r.IntN(1_000_000)
		name := synth.DomainName(i)
		return query{
			kind: kind, path: "/domain/" + name, wantStatus: 404,
			validate: func(b map[string]any) string {
				if got, _ := b["objectClassName"].(string); got != "error" {
					return fmt.Sprintf("expected error envelope, got %q", got)
				}
				return ""
			},
		}
	case kEntity:
		i := pickKey(r, corpus, dist)
		h := synth.EntityHandle(i)
		return query{
			kind: kind, path: "/entity/" + h, wantStatus: 200,
			validate: func(b map[string]any) string {
				if got, _ := b["objectClassName"].(string); got != "entity" {
					return fmt.Sprintf("objectClassName=%q want \"entity\"", got)
				}
				if got, _ := b["handle"].(string); got != h {
					return fmt.Sprintf("handle=%q want %q", got, h)
				}
				return ""
			},
		}
	case kEntityInvalid:
		// Path traversal payload — input validation must reject with 400.
		return query{
			kind: kind, path: "/entity/..%2Fetc%2Fpasswd", wantStatus: 400,
			validate: func(b map[string]any) string {
				if got, _ := b["errorCode"].(float64); got != 400 {
					return fmt.Sprintf("errorCode=%v want 400", got)
				}
				return ""
			},
		}
	case kNameserver:
		i := r.IntN(corpus / 10)
		if i < 1 {
			i = 1
		}
		name := synth.NameserverName(i)
		return query{
			kind: kind, path: "/nameserver/" + name, wantStatus: 200,
			validate: func(b map[string]any) string {
				if got, _ := b["objectClassName"].(string); got != "nameserver" {
					return fmt.Sprintf("objectClassName=%q want \"nameserver\"", got)
				}
				return ""
			},
		}
	case kSearchDomains:
		// "syn-12*.nl" — matches all syn-12.., syn-120.., etc.
		bucket := r.IntN(10) * 10
		pattern := fmt.Sprintf("syn-%d*", bucket)
		return query{
			kind: kind, path: "/domains?name=" + pattern, wantStatus: 200,
			validate: func(b map[string]any) string {
				if got, _ := b["objectClassName"].(string); got != "domainSearchResults" {
					return fmt.Sprintf("objectClassName=%q want \"domainSearchResults\"", got)
				}
				return ""
			},
		}
	case kSearchEntities:
		country := synth.Countries[r.IntN(len(synth.Countries))]
		return query{
			kind: kind, path: "/entities?countryCode=" + country, wantStatus: 200,
			validate: func(b map[string]any) string {
				if got, _ := b["objectClassName"].(string); got != "entitySearchResults" {
					return fmt.Sprintf("objectClassName=%q want \"entitySearchResults\"", got)
				}
				return ""
			},
		}
	case kHelp:
		return query{
			kind: kind, path: "/help", wantStatus: 200,
			validate: func(b map[string]any) string {
				if got, _ := b["objectClassName"].(string); got != "help" {
					return fmt.Sprintf("objectClassName=%q want \"help\"", got)
				}
				return ""
			},
		}
	}
	return query{}
}

func exec(ctx context.Context, client *http.Client, base string, q query, st *stats) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, base+q.path, nil)
	t0 := time.Now()
	resp, err := client.Do(req)
	d := time.Since(t0)

	b := st.bucketFor(q.kind)
	st.overall.lat.Add(d)
	b.lat.Add(d)
	st.overall.count.Add(1)
	b.count.Add(1)

	if err != nil {
		st.transErr.Add(1)
		st.overall.failures.Add(1)
		b.failures.Add(1)
		st.recordMismatch(q, 0, err.Error())
		return
	}

	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()

	switch resp.Header.Get("X-Gordap-Cache") {
	case "HIT":
		st.cacheHit.Add(1)
	default:
		st.cacheMiss.Add(1)
	}
	st.recordStatus(resp.StatusCode)

	if resp.StatusCode != q.wantStatus {
		b.failures.Add(1)
		st.overall.failures.Add(1)
		st.recordMismatch(q, resp.StatusCode,
			fmt.Sprintf("status=%d want=%d body=%s", resp.StatusCode, q.wantStatus, truncate(body)))
		return
	}

	// Parse body and run the kind-specific validator.
	var parsed map[string]any
	if err := json.Unmarshal(body, &parsed); err != nil {
		b.failures.Add(1)
		st.overall.failures.Add(1)
		st.recordMismatch(q, resp.StatusCode, "body not JSON: "+err.Error())
		return
	}
	if reason := q.validate(parsed); reason != "" {
		b.failures.Add(1)
		st.overall.failures.Add(1)
		st.recordMismatch(q, resp.StatusCode, reason)
	}
}

func truncate(b []byte) string {
	const max = 200
	if len(b) <= max {
		return string(b)
	}
	return string(b[:max]) + "...[truncated]"
}

// ---------- reporting ----------

func emitText(st *stats, concurrency int, dur time.Duration, corpus int, url string, w io.Writer) {
	elapsed := time.Since(st.startedAt)
	total := st.overall.count.Load()
	rps := float64(total) / elapsed.Seconds()
	failed := st.overall.failures.Load()

	fmt.Fprintln(w, "=== gordap stress test ===")
	fmt.Fprintf(w, "Target:        %s\n", url)
	fmt.Fprintf(w, "Concurrency:   %d\n", concurrency)
	fmt.Fprintf(w, "Duration:      %s\n", dur)
	fmt.Fprintf(w, "Seed corpus:   %d\n", corpus)
	fmt.Fprintf(w, "Mix:           %s\n", mixSummary())

	fmt.Fprintln(w, "\n=== Performance ===")
	fmt.Fprintf(w, "Total requests:     %d\n", total)
	fmt.Fprintf(w, "Throughput:         %.0f req/s\n", rps)
	fmt.Fprintf(w, "Transport errors:   %d\n", st.transErr.Load())

	overall := st.overall.lat.percentiles(0.50, 0.90, 0.95, 0.99, 0.999)
	fmt.Fprintln(w, "\nLatency (overall)")
	fmt.Fprintf(w, "  p50:  %-9s  p99:   %s\n", overall[0], overall[3])
	fmt.Fprintf(w, "  p90:  %-9s  p999:  %s\n", overall[1], overall[4])
	fmt.Fprintf(w, "  p95:  %-9s  max:   %s\n", overall[2], st.overall.lat.max)
	fmt.Fprintf(w, "  min:  %s\n", st.overall.lat.min)

	fmt.Fprintln(w, "\nBy endpoint")
	fmt.Fprintf(w, "  %-22s %10s %8s %10s %10s\n", "kind", "count", "rps", "p50", "p99")
	st.byKind.Range(func(k, v any) bool {
		kk := k.(queryKind)
		bb := v.(*bucket)
		ps := bb.lat.percentiles(0.50, 0.99)
		fmt.Fprintf(w, "  %-22s %10d %8.0f %10s %10s\n",
			kk, bb.count.Load(),
			float64(bb.count.Load())/elapsed.Seconds(),
			ps[0], ps[1])
		return true
	})

	fmt.Fprintln(w, "\nStatus codes")
	st.byStatus.Range(func(k, v any) bool {
		c := v.(*atomic.Int64).Load()
		fmt.Fprintf(w, "  %3d:  %10d  (%5.2f%%)\n", k.(int), c, 100*float64(c)/float64(total))
		return true
	})

	fmt.Fprintln(w, "\n=== Cache ===")
	hit, miss := st.cacheHit.Load(), st.cacheMiss.Load()
	if hit+miss == 0 {
		fmt.Fprintln(w, "  No X-Gordap-Cache header observed (response cache disabled?)")
	} else {
		fmt.Fprintf(w, "  HIT:   %d (%.1f%%)\n", hit, 100*float64(hit)/float64(hit+miss))
		fmt.Fprintf(w, "  MISS:  %d (%.1f%%)\n", miss, 100*float64(miss)/float64(hit+miss))
	}

	fmt.Fprintln(w, "\n=== Correctness ===")
	if failed == 0 {
		fmt.Fprintf(w, "  Validation passed:  %d / %d (100.00%%)\n", total, total)
	} else {
		fmt.Fprintf(w, "  Validation failed:  %d / %d (%.2f%%)\n", failed, total, 100*float64(failed)/float64(total))
		fmt.Fprintln(w, "  First mismatches:")
		for _, m := range st.mismatches {
			fmt.Fprintf(w, "    - %s\n", m)
		}
	}
}

func emitJSON(st *stats, concurrency int, dur time.Duration, corpus int, url string, w io.Writer) {
	elapsed := time.Since(st.startedAt)
	total := st.overall.count.Load()
	overall := st.overall.lat.percentiles(0.50, 0.90, 0.95, 0.99, 0.999)
	report := map[string]any{
		"target":           url,
		"concurrency":      concurrency,
		"duration_seconds": dur.Seconds(),
		"seed_corpus":      corpus,
		"total_requests":   total,
		"throughput_rps":   float64(total) / elapsed.Seconds(),
		"transport_errors": st.transErr.Load(),
		"validation_failures": st.overall.failures.Load(),
		"latency_ms": map[string]float64{
			"p50":  overall[0].Seconds() * 1000,
			"p90":  overall[1].Seconds() * 1000,
			"p95":  overall[2].Seconds() * 1000,
			"p99":  overall[3].Seconds() * 1000,
			"p999": overall[4].Seconds() * 1000,
			"max":  st.overall.lat.max.Seconds() * 1000,
		},
		"cache": map[string]int64{
			"hits":   st.cacheHit.Load(),
			"misses": st.cacheMiss.Load(),
		},
	}

	byKind := map[string]any{}
	st.byKind.Range(func(k, v any) bool {
		bb := v.(*bucket)
		ps := bb.lat.percentiles(0.50, 0.95, 0.99)
		byKind[string(k.(queryKind))] = map[string]any{
			"count":    bb.count.Load(),
			"failures": bb.failures.Load(),
			"p50_ms":   ps[0].Seconds() * 1000,
			"p95_ms":   ps[1].Seconds() * 1000,
			"p99_ms":   ps[2].Seconds() * 1000,
		}
		return true
	})
	report["by_endpoint"] = byKind

	statuses := map[int]int64{}
	st.byStatus.Range(func(k, v any) bool {
		statuses[k.(int)] = v.(*atomic.Int64).Load()
		return true
	})
	report["status_codes"] = statuses

	if st.overall.failures.Load() > 0 {
		report["mismatches"] = st.mismatches
	}

	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(report)
}

func mixSummary() string {
	parts := make([]string, 0, len(defaultMix))
	total := 0
	for _, m := range defaultMix {
		total += m.weight
	}
	for _, m := range defaultMix {
		parts = append(parts, fmt.Sprintf("%s=%d%%", m.kind, m.weight*100/total))
	}
	return strings.Join(parts, " ")
}
