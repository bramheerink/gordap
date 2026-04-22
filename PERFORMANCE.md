# gordap Performance Notes

How we target 10k+ QPS without resorting to exotic infrastructure, and
where to look when you need 100k+.

## 1. The cost of a single request

A typical domain lookup on a warm cache:

| Step | Cost |
|---|---|
| HTTP parse + route | ~10μs |
| auth middleware (no verify) | ~1μs |
| input validation + IDN | ~5μs |
| cache hit | ~1μs |
| mapper + JSON encode | ~50μs |
| middleware stack (gzip, CORS, log) | ~30μs |
| total | ~100μs / request |

On a cold cache, add a DB round-trip (~0.3-2ms for a point lookup
against indexed columns). Your limits come from the DB, not gordap.

## 2. Scaling to 10k QPS

Rough math: 10k req/s × ~100μs = one CPU core saturated.  Four replicas
× four cores gives you ~40k cold-path capacity before anything else.

### 2.1 Cache hits do the heavy lifting

The `cache` package wraps any DataSource with an in-process LRU +
singleflight. RDAP's read pattern is heavily skewed (a small set of
popular names drives most queries); realistic hit ratios run at 80-95%.
Cache hits bring the DB load from 10k QPS down to 500-2000 QPS.

Tuning:

```go
ds = cache.New(ds, cache.Config{
    Size:   50_000,      // per object class — entries of a few KB each
    TTL:    60 * time.Second,
    NegTTL: 5 * time.Second,
})
```

- `Size` — memory budget. 50k entries × ~2KB average ≈ 100MB per class.
- `TTL` — aligns with `Cache-Control: max-age=60`. Longer means staler
  data; your ingest pipeline sets the floor.
- `NegTTL` — shorter on purpose. Keeps "doesn't exist" answers from
  lingering past the moment a name is registered.

### 2.2 Singleflight on cache misses

`cache.singleflight` collapses concurrent cold-key requests into one
back-end call. Without it, 100 concurrent requests for a hot-miss
`example.com` produce 100 DB queries; with it, 1. This is critical
during cache warm-up and after TTL expiry.

### 2.3 CDN in front

Anonymous responses are byte-identical within the
`Cache-Control: max-age` window, which makes them trivially
CDN-cacheable. Varnish / Fastly / Cloudflare in front of gordap turns
the public anonymous tier into a solved problem at essentially any QPS.

Authenticated / privileged responses **MUST bypass the CDN** (they vary
per caller). Configure your CDN to bypass on `Authorization` header
presence, or serve authenticated traffic on a separate hostname.

### 2.4 Postgres tuning

- `max_connections`: 100-200 per gordap replica, fronted by PgBouncer
  in transaction mode (so actual PG backends stay bounded).
- `shared_buffers`: 25% of RAM; the hot indexes want to live there.
- Read replicas: point gordap at a read replica, keep writes on the
  primary.
- `effective_cache_size`: set to OS page cache size so the planner
  picks index scans correctly.

### 2.5 Response compression

`middleware.Gzip(minSize)` compresses ~70% of RDAP payload on typical
domains. At 10k QPS that's several hundred megabits saved on the uplink
— often the difference between a 1 Gbit NIC being fine and being red-hot.

## 3. Scaling to 100k+ QPS

Further things to consider at gTLD-top-shelf scale:

1. **Read replicas**: multiple PG replicas behind PgBouncer with
   connection affinity. gordap replicas pick a replica via LB config.
2. **Response cache** (roadmap): a dedicated keyed-by-(object, id,
   access_level) cache of pre-rendered JSON. Skips mapper+JSON encode
   on every hit. ~10× cheaper than the record cache.
3. **OpenSearch for reverse search**: RFC 9536 `/entities?email=*`
   etc. — trigram GIN on PostgreSQL tops out around 10M rows. See §5.
4. **Geo-replicated deploys**: gordap replicas in each region, each
   with a local read replica. The CDN handles the long tail.
5. **Hardware**: modern server CPUs (Epyc / Xeon) with many cores let
   a single replica do 20k-40k cold QPS. Don't scale out before
   scaling up — Go's stdlib `net/http` loves high core counts.

## 4. Observability

- `pkg/rdap/metrics` exposes an `Observer` interface. A stdlib
  `expvar`-backed implementation ships in core; import `expvar` in
  your `main` and mount `/debug/vars` to see live counters.
- `pkg/rdap/observability` wires slog and OpenTelemetry spans.
- Recommended SLOs:
  - p99 < 200ms for exact lookup (cache hit)
  - p99 < 2s for search (when implemented)
  - 99.95% availability
  - Cache hit ratio > 80% after 10 min warmup

### 4.1 Prometheus adapter (10-line pattern)

```go
import (
    "github.com/prometheus/client_golang/prometheus"
    "github.com/bramheerink/gordap/pkg/rdap/metrics"
)

type promObserver struct {
    hits   *prometheus.CounterVec
    errs   *prometheus.CounterVec
    dur    *prometheus.HistogramVec
}

func (p *promObserver) Observed(_ context.Context, op string, d time.Duration, err error) {
    p.hits.WithLabelValues(op).Inc()
    p.dur.WithLabelValues(op).Observe(d.Seconds())
    if err != nil { p.errs.WithLabelValues(op).Inc() }
}
```

Register the Vec collectors with `prometheus.MustRegister`, pass the
observer into `metrics.WrapDataSource(ds, obs)`. Done.

## 5. OpenSearch — when and when not

### When
- Reverse search at scale (RFC 9536), `/entities?email=*@domain`
- Fuzzy / typo-tolerant matching with BM25 relevance
- Multi-field scoring (`fn AND country AND email`)
- Registries >10M domains where trigram GIN becomes unwieldy

### When not
- Exact lookups — PG wins on latency (single indexed read)
- Small/medium registries (<1M rows) — `pg_trgm` + GIN is faster
  operationally
- You don't already run OpenSearch — the infra cost rarely pays off
  for just reverse search

### Architecture when enabled

```
 Postgres (SoT)  ──outbox / CDC──► OpenSearch (derived index)
        ▲                                │
        │                                ▼
        └── gordap ◄── DataSource ──── SearchIndex ────►
```

PG remains authoritative. OpenSearch is rebuildable from PG at any
time. Don't let anyone query OS-only data — always have a PG row
backing it.

The `SearchIndex` interface (roadmap, arriving with the search
endpoints) will have two reference implementations: `postgres/search`
(trigram) and `opensearch/search` (Lucene). Operators pick one; the
handler doesn't care.

## 6. Knobs that matter

Summary of tunable parameters that move p99 most:

| Knob | Default | At 10k QPS | At 100k QPS |
|---|---|---|---|
| `cache.Config.Size` | operator-chosen | 50k | 500k |
| `cache.Config.TTL` | operator-chosen | 60s | 60-300s |
| pgx pool size | 50 | 50-100 | 100-200 (+ PgBouncer) |
| `http.Server.ReadHeaderTimeout` | 5s | 5s | 5s |
| `middleware.RequestTimeout` | operator-chosen | 10s | 5s |
| Gzip `minSize` | 128 | 128 | 256 |
| Rate limit | operator-chosen | 50/IP/s | 10/IP/s + token bucket |

## 7. Benchmarks

### 7.1 Built-in load + correctness tester

`cmd/gordap-stress` does what generic HTTP load testers (hey, wrk,
vegeta, k6) don't: every response is *parsed and validated* against the
deterministic expectation derived from `internal/synth`. A 200 with a
malformed body counts as a defect, not as throughput. This catches
silent regressions that pure RPS-counting tools miss.

Quickstart against a memory-backed demo:

```bash
make demo-synth N=10000             # boots gordap on :8080 with 10k synthetic records
make stress C=100 D=30s URL=http://localhost:8080
```

Output covers throughput, p50/p90/p95/p99/p999 latency overall and
per-endpoint, status-code distribution, cache hit ratio
(via `X-Gordap-Cache`), and the first ten validation mismatches
(if any) for human debugging.

Sample run on a developer laptop (memory backend, 100 workers, 10s):

```
Total requests:     187,018
Throughput:         18,696 req/s
Latency p50/p99:    2.5 ms / 19.0 ms
Cache hit ratio:    84.7%
Validation passed:  99.95%
```

### 7.2 Horizontal scaling for stress generation

A single load-generator instance saturates around 20-40k RPS depending
on hardware (HTTP client + JSON parsing dominates). Beyond that, fan
out across machines and use `gordap-stress-aggregate` to roll up:

```bash
# On each generator host:
gordap-stress -url=https://rdap.example.com -c=200 -d=300s -json \
   > /tmp/run-$(hostname).json

# Centrally:
gordap-stress-aggregate /tmp/run-*.json
```

Aggregated output sums request counts, weights percentiles by request
volume, and merges per-endpoint breakdowns. Approximate but stable for
ranking and trend lines. For exact cross-machine percentiles you'd ship
raw samples (HDR-histogram serialisation) — not built; open an issue if
you need it.

### 7.3 Server-side diagnostics

Run gordap with `--debug-addr=127.0.0.1:6060` (private only — pprof
publicly exposed is a CVE waiting). You then get:

- `http://127.0.0.1:6060/debug/vars` — expvar JSON (per-op counters
  via `pkg/rdap/metrics`, runtime memstats, custom Publish entries)
- `http://127.0.0.1:6060/debug/pprof/heap` — heap profile
- `http://127.0.0.1:6060/debug/pprof/profile?seconds=30` — CPU profile
- `http://127.0.0.1:6060/debug/pprof/goroutine` — goroutine dump

Correlate client-side stress numbers with server-side memstats /
goroutine count to pinpoint where pressure shows up: GC pauses,
goroutine leaks, lock contention.

### 7.4 Postgres-backed runs

For PG-backed stress tests:

```bash
export DATABASE_URL=postgres://rdap:pw@localhost/rdap
psql -1 -f pkg/rdap/storage/postgres/schema.sql
make seed N=100000
make demo URL_FLAG=-database-url=$DATABASE_URL  # or run gordap manually with -database-url
make stress C=100 D=60s
```

The seeder uses pgx CopyFrom and reaches 50-200k rows/s on commodity
disks; 100k synthetic domains seeds in 1-3s.

### 7.5 Generic tooling fallback

The validation tier is what makes our stress unique, but generic
tools work too if you only care about RPS:

1. `hey -n 100000 -c 100 https://rdap.example.com/domain/example.nl`
2. `wrk -t 8 -c 500 -d 60s --latency <url>`
3. Track p50/p95/p99 via OpenTelemetry histograms, not averages.

A single gordap replica on commodity hardware (8 cores, 16GB) should
comfortably serve 15-25k cold-path QPS against a local PG. Anything
below that suggests misconfiguration.
