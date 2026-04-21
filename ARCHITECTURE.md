# gordap Architecture

**Audience:** operators (registries, registrars, RIRs) evaluating gordap for
production deployment, and engineers wiring it into an existing registry
stack.

## 1. What gordap is (and isn't)

**Is:** a read-only RDAP (RFC 7480/9082/9083) server + importable Go
toolkit. Serves `/domain`, `/entity`, `/nameserver`, `/ip`, `/help` per
STD 95, with JSContact (RFC 9553) contact data and tiered-access
redaction.

**Is not:** the authoritative source of your registration data. gordap
is a **read-optimized projection** of data that lives in your
EPP/registry platform. RDAP by design has no write semantics; writes
happen upstream and land in the gordap PostgreSQL via one of three
patterns (§3).

## 2. Package layout

Each package under `pkg/rdap/` is independently importable. You can
pick the pieces you need into an existing stack; the reference binary
is just one composition.

| Package | Purpose |
|---|---|
| `types` | RFC 9083 wire types (Domain, Entity, Nameserver, IPNetwork, Error) |
| `jscontact` | RFC 9553 Card types |
| `idn` | UTS #46 domain normalisation |
| `validate` | Length / charset input hardening |
| `auth` | AccessLevel, Claims, bearer middleware, Verifier interface |
| `datasource` | Read-side contract: `DataSource` interface + internal models |
| `ingest` | Write-side contract: `Ingester` interface (for your sync pipeline) |
| `mapper` | `datasource → types` with tiered-access redaction |
| `redaction` *(reserved)* | Future RFC 9537 policy engine |
| `cache` | In-memory LRU + singleflight decorator for any DataSource |
| `bootstrap` | RFC 7484 IANA registry — 302 redirects on NotFound |
| `profile` | ICANN gTLD / RIR / ccTLD preset notices + conformance strings |
| `middleware` | CORS, rate-limit, gzip, request-body cap, security headers |
| `metrics` | Observer interface + stdlib `expvar` impl; Prometheus via adapter |
| `observability` | slog factory, access-log middleware, OpenTelemetry span helper |
| `audit` | NIS2 Art. 28 audit-trail hook |
| `handlers` | HTTP handlers, router, error responses |
| `storage/memory` | In-memory `DataSource`, used for demo mode and tests |
| `storage/postgres` | First-class PostgreSQL backend (see `schema.sql`) |

## 3. Data flow

### 3.1 Read path (query → response)

```
HTTP GET /domain/example.nl
      │
      ▼
[ auth middleware ]         → decodes Bearer token → Claims on ctx
      │
      ▼
[ validate + idn.Normalize ] → UTS #46 + RFC 1035 length cap
      │
      ▼
[ cache layer ]             → LRU hit? return. singleflight on miss.
      │  miss
      ▼
[ DataSource.GetDomain ]    → Postgres (or memory for demo)
      │
      ▼
[ mapper.Domain(opts) ]     → RedactContact → JSContact Card → types.Domain
      │
      ▼
[ middleware stack: gzip, CORS, cache-control, access-log, audit ]
      │
      ▼
application/rdap+json  →  client (or CDN)
```

### 3.2 Write path (your registry → gordap PG)

gordap ships **no public write path**. The operator picks one of:

#### Pattern A — Same-DB
```
 EPP registry  ──writes──►  PostgreSQL  ◄──reads── gordap
```
Simplest. gordap's `pgx` pool points at the same PG cluster the registry
platform writes to. Zero sync lag, zero extra infrastructure, but the
RDAP read load competes with EPP writes unless you use read-replicas.

#### Pattern B — CDC
```
 EPP DB ──Debezium──► Kafka ──worker──► gordap PG ◄── gordap
```
The worker implements `ingest.Ingester`. Decouples schemas — your
registry DB can evolve without breaking RDAP reads. Handles large
registries (millions of domains) cleanly and isolates failure domains.
Eventual consistency on the order of seconds.

#### Pattern C — Push API
```
 EPP registry ──HTTP POST──► gordap /internal/upsert ──► gordap PG
```
Good for smaller registries with no CDC infrastructure. The operator
mounts a private HTTP handler against `ingest.Ingester` (bound to
localhost or mTLS'd). Not provided by gordap core — write a 50-line
handler or use the reference adapter (roadmap).

### 3.3 Source of truth

**The registry's authoritative system is the SoT.** gordap's PostgreSQL
is a derived projection. Consequences:

- **Right to erasure (GDPR Art. 17)**: the operator deletes upstream,
  the CDC/sync pipeline propagates the delete, `ingest.DeleteEntity`
  removes it from gordap's PG.
- **Disaster recovery**: gordap's PG is re-buildable from the SoT.
- **Schema authority**: gordap's schema is a public contract; upstream
  DB can have any shape.

## 4. Data that exists inside gordap

### 4.1 At rest (PostgreSQL)

See `pkg/rdap/storage/postgres/schema.sql`. The hybrid model:

- **Typed columns** for everything RFC 9083 mandates or queries hit:
  `handle`, `ldh_name`, `status`, `full_name`, `country_code`,
  `postal_code`, etc. Indexed, constraint-checked.
- **Join tables** for multi-valued channels (`entity_emails`,
  `entity_phones`) so RFC 9536 reverse search remains an indexed lookup.
- **JSONB only** for genuinely open data: `domains.secure_dns` (DS/key
  variants) and `entities.extras` (registrar-specific metadata).

### 4.2 In flight

- **Request handler goroutine**: full datasource record in a local
  variable for microseconds, then mapped+redacted.
- **Cache**: full datasource records for up to `TTL`. Contains PII.
  Disable core dumps in production.
- **Logs**: request method/path/status/duration + trace ID. Never
  request bodies, never response bodies, never PII.
- **Audit trail** (if enabled): requester, path, access tier. No
  contact data — only metadata.

### 4.3 In flight — response caching (recommended for ≥10k QPS)

Put a CDN / Varnish / Fastly in front of gordap. Anonymous responses
are byte-identical within the `Cache-Control: max-age` window, so the
CDN absorbs the overwhelming majority of traffic. See PERFORMANCE.md.

## 5. Deployment topology

```
              ┌──────────────┐
              │    CDN       │   public, edge-cached anonymous reads
              └──────┬───────┘
                     │
              ┌──────▼───────┐
              │ Load balancer│   HTTPS termination + HSTS + HTTP→HTTPS
              └──────┬───────┘
                     │
        ┌────────────┼─────────────┐
        ▼            ▼             ▼
   ┌─────────┐  ┌─────────┐  ┌─────────┐
   │ gordap  │  │ gordap  │  │ gordap  │   stateless; scale horizontally
   └────┬────┘  └────┬────┘  └────┬────┘
        └────────────┼─────────────┘
                     │
              ┌──────▼───────┐
              │  PgBouncer   │   transaction-mode pooling
              └──────┬───────┘
                     │
              ┌──────▼───────┐
              │ PostgreSQL   │   primary + read replicas
              └──────────────┘
                     ▲
                     │ CDC / sync
              ┌──────┴───────┐
              │ EPP registry │   source of truth
              └──────────────┘
```

gordap processes are stateless — any replica can serve any request. The
LRU cache is process-local; that's intentional (shared caches would need
Redis and a distributed invalidation story, which isn't worth the
complexity for CDN-cacheable responses).

## 6. Security boundaries

| Boundary | Controls |
|---|---|
| Public internet → LB | TLS, rate limiting at LB, WAF optional |
| LB → gordap | HTTP, optional mTLS; trust the LB's client-IP header |
| gordap → PG | TLS + password auth; read-only user recommended |
| gordap → IANA (bootstrap refresh) | HTTPS, hardcoded URLs, fail-open on error |
| Admin → gordap | No admin API in the core — ops via pod recreation / restart |

See SECURITY.md for the full threat model and mitigations.

## 7. Extension points

Every interface-based seam is an extension point:

- Swap **`DataSource`** for an alternative backend (OpenSearch, in-memory,
  sharded PG, remote API).
- Swap **`auth.Verifier`** for your OIDC/JWKS provider.
- Swap **`audit.Logger`** to ship audit records to SIEM / WORM storage.
- Swap **`metrics.Observer`** for Prometheus / StatsD / DataDog.
- Wrap **`handlers.NewRouter`** output with your own middleware stack
  before mounting on an `http.Server`.

The goal: no interface in the core forces a particular infrastructure
choice. If you find one that does, that's a bug — please file an issue.
