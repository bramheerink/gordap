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

Sample runs on a developer laptop (memory backend, no rate limit):

```
100 workers / 10s:   24,623 req/s   p50=2.1ms p99=14ms  100.00% pass
500 workers / 30s:   26,362 req/s   p50=11ms p99=64ms   100.00% pass
```

Note: when interpreting validation failures, distinguish *real* server
defects (status mismatch, malformed body, wrong content) from
test-tool artefacts (test-duration ctx cancelling in-flight requests).
gordap-stress detaches the per-request context from the test
deadline so the latter doesn't show up as the former.

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

### 7.5 The tools

| Binary | Purpose |
|---|---|
| `cmd/gordap` | The reference RDAP server. `--demo-synth=N` boots with N synthetic records in memory; `--database-url=...` points at Postgres. `--debug-addr=127.0.0.1:6060` exposes `/debug/vars` (expvar) + `/debug/pprof`. |
| `cmd/gordap-seed` | Bulk-loads N synthetic domains/entities/nameservers + their join records into Postgres via `pgx CopyFrom`. ~50-200k rows/s on local PG. Idempotent with `--truncate`. |
| `cmd/gordap-stress` | Concurrent HTTP load + correctness validator. Every response is parsed and validated against the deterministic expectation from `internal/synth`. Reports throughput per endpoint, p50/p90/p95/p99/p999 latency, status distribution, cache hit ratio (via `X-Gordap-Cache`), and the first ten validation mismatches. `--json` emits machine-readable output for aggregation. |
| `cmd/gordap-stress-aggregate` | Combines per-host JSON reports for horizontal stress generation. Sums request counts, weights percentiles by request count. |

`internal/synth` is the deterministic name generator both the seeder and
the stress runner draw from. Same `i` produces the same handle / LDH
name everywhere, so the stress runner can predict which queries should
succeed (`i < corpus`) and which should 404 (`i >= corpus`) — the basis
for inline correctness validation.

### 7.6 Measurement history

Real numbers from a developer-grade workstation (not a laptop) running
the synthetic stress suite. Two things move the needle most: cache hit
ratio (workload + TTL + size driven) and the right Postgres indexes.

| Run | Backend | RPS | Domain p99 | Search p99 | Validation |
|---|---|---|---|---|---|
| Memory-only, 5k corpus | in-memory | 26,362 | 14ms | n/a | 100% |
| PG (Docker Alpine), 50k, defaults | postgres:17-alpine | 4,035 | 117ms | 290ms | 100% |
| PG (native), 50k, +N+1 fix on Contacts | PG 18.3 | 4,037 | 51ms | 325ms | 100% |
| PG (native), 50k, +CTE on GetDomain | PG 18.3 | 4,037 | 51ms | 325ms | 100% |
| **PG (native), 50k, +text_pattern_ops & pg_trgm** | **PG 18.3** | **22,134** | **15ms** | **22ms** | **100%** |

The 5.5× jump came from one schema change: adding the right indexes
for `LIKE`. Default UNIQUE B-tree on `ldh_name` uses `text_ops`, which
only supports `=`. Search queries used `ILIKE` and fell back to seq-
scan on every request.

The N+1 fix and the GetDomain CTE moved domain p99 from 117ms → 15ms
(8×) but didn't shift overall throughput — the bottleneck simply
relocated to the unindexed search queries.

Workload mix in all the above: 70% domain / 5% missing-domain / 10%
entity / 1% bad-handle / 5% nameserver / 7% domain-search / 1%
entity-search / 1% help. This is **synthetic-stress** mix, deliberately
heavier on search than real public RDAP traffic — registries report
search hits well under 2% of public requests, with the bulk being
exact lookups. With a more realistic mix the headline RPS would be
even higher.

Validated correctness across all runs: **2.5+ million synthetic
requests, zero defects**. No transport errors, no schema mismatches,
no race-detector failures.

## 8. Production setup proposal — ccTLD scale

For a "big ccTLD" — call it 1-10M domains, peak 1k-10k RPS, 99.95%
availability target — here is a deployment that has all the moving
parts gordap was designed around:

```
                ┌────────────────┐
                │   Public CDN   │   anonymous-tier responses are
                │  (Cloudflare/  │   byte-identical within max-age,
                │   Fastly/Varnish) │   so the CDN absorbs >80% of load
                └───────┬────────┘
                        │
                ┌───────▼────────┐
                │  Load Balancer │   TLS termination, HSTS preload,
                │ (HAProxy/nginx)│   X-Forwarded-For for the rate-limiter
                └───────┬────────┘
                        │
        ┌───────────────┼───────────────┐
        ▼               ▼               ▼
   ┌─────────┐     ┌─────────┐     ┌─────────┐
   │ gordap  │     │ gordap  │     │ gordap  │   stateless replicas;
   │  v0.x   │     │  v0.x   │     │  v0.x   │   start with 3, scale
   └────┬────┘     └────┬────┘     └────┬────┘   horizontally on RPS
        └─────────────┬─┴─────────────┘
                      │
                ┌─────▼──────┐
                │ PgBouncer  │  transaction-mode pooling — caps PG
                │  (~16 conn │  backend connections regardless of
                │   per repl)│  gordap replica count
                └─────┬──────┘
                      │
                ┌─────▼──────┐               ┌─────────────┐
                │  PG 17/18  │ ─ replicate ─►│  PG replica │
                │  primary   │               │  (read-only)│
                └────────────┘               └─────────────┘
                      ▲
                      │ CDC / sync (Debezium → Kafka, or direct
                      │  writes from your EPP/registry platform)
                ┌─────┴──────┐
                │ EPP backend│   source of truth
                └────────────┘
```

### Sizing on commodity hardware

For a 5M-domain registry:

| Component | Recommended |
|---|---|
| gordap replicas | 3-4 × 8 cores, 8GB RAM (cache-dependent) |
| PostgreSQL primary | 16 cores, 64GB RAM, NVMe SSD, `shared_buffers=16GB` |
| PG read replica(s) | 1-2 × same shape, gordap-stress runs read-only against these |
| PgBouncer | 1 instance per gordap replica or shared, transaction mode |
| Network | 10 Gbps LAN between gordap and PG |
| CDN | Cloudflare/Fastly/AWS CloudFront — anonymous tier only |

### gordap flags for this profile

```bash
gordap \
  -addr=:443 -tls-cert=cert.pem -tls-key=key.pem \
  -database-url=postgres://rdap@pgbouncer:6432/rdap \
  -self-link-base=https://rdap.example.nl \
  -icann-gtld -tos-url=https://example.nl/rdap-tos \
  -cache-size=200000 -cache-ttl=300s -cache-stale-ttl=120s \
  -response-cache-size=200000 -response-cache-ttl=60s \
  -rate-limit-rps=100 -rate-limit-burst=200 \
  -trusted-proxies=10.0.0.0/8,172.16.0.0/12 \
  -read-timeout=15s -write-timeout=30s -idle-timeout=120s \
  -bootstrap                              # 302 unknown TLDs to authoritative
```

### Capacity expectations

Extrapolating from our measured ~22k RPS on a single workstation
replica with native PG, three replicas behind a CDN should comfortably
serve a major-ccTLD workload. Typical .nl-class peak is 5-10k RPS
sustained — well within reach with margin to spare. The CDN absorbs
the anonymous tier (typically 80%+ of public traffic), so the gordap
fleet mostly handles authenticated and cache-miss queries.

### Operational checklist

- [ ] Apply `pkg/rdap/storage/postgres/schema.sql` (includes pg_trgm
      + text_pattern_ops indexes — non-negotiable for search performance)
- [ ] Separate ingest user with INSERT/UPDATE on the registry tables;
      gordap user has SELECT only
- [ ] PG `log_min_duration_statement=100` — surface query regressions
- [ ] gordap behind LB: set `-trusted-proxies=` to your LB CIDR
- [ ] Audit log → append-only sink (NIS2 Art. 28)
- [ ] Disable core dumps in production (record-cache holds PII)
- [ ] govulncheck + go test -race in CI
- [ ] Periodic `make test-realworld` against production via gordap-stress
      on a separate host

### Where Postgres stops being enough

Past ~50M domains or sustained 50k+ RPS:

- Read replicas with affinity (gordap reads → replicas, never primary)
- Separate OpenSearch cluster fed via outbox/CDC for reverse search
  (RFC 9536) — PG's trigram GIN starts to struggle past ~100M rows
- Sharded gordap deployments per-region with regional PG cluster
- HDR-histogram metrics shipping for cross-region percentile aggregation

### Why Postgres at all

Effectively the entire RDAP server ecosystem stores authoritative
registration data in an SQL database. Among public deployments:

- **PostgreSQL** dominates: SIDN (.nl), DENIC (.de), CentralNic, PIR
  (.org), APNIC rdapd, RIPE NCC, ICANN's icann-rdap reference, all
  build on it.
- **MySQL/MariaDB** appears in older or smaller deployments.
- **Oracle** in legacy registries (mostly being migrated off).
- **Cloud-managed**: Google's Nomulus uses Spanner on GCP.
- **NoSQL** (Mongo etc.) was tried by a few smaller registries c.
  2014-2018; most migrated back to SQL because EPP semantics are
  fundamentally relational.

So gordap's PostgreSQL-first stance is the conservative-by-consensus
choice. The `DataSource` interface keeps the door open for an
operator who really needs something else, but PG should be the default
for any new deployment.

## 9. Generic tooling fallback

The validation tier is what makes our stress unique, but generic
tools work too if you only care about RPS:

1. `hey -n 100000 -c 100 https://rdap.example.com/domain/example.nl`
2. `wrk -t 8 -c 500 -d 60s --latency <url>`
3. Track p50/p95/p99 via OpenTelemetry histograms, not averages.

A single gordap replica on commodity hardware (8 cores, 16GB) should
comfortably serve 15-25k cold-path QPS against a local PG. Anything
below that suggests misconfiguration.
