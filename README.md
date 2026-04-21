# gordap

A modern, pluggable RDAP server in Go.

`gordap` is both a full-featured reference server and a toolkit of
independently importable packages. Use it as a drop-in binary to serve
Registration Data Access Protocol (RFC 7480/9082/9083) for your
registry, or pick the pieces you need — types, bootstrap registry,
RFC 9537 redaction engine, JWKS verifier, caching layer — into an
existing stack.

- **Status:** active development; passes 22/22 real-world RFC + ICANN
  RP2.2 conformance assertions, interoperates with
  [openrdap/rdap](https://github.com/openrdap/rdap) CLI.
- **Module:** `github.com/bramheerink/gordap`
- **Go:** 1.26
- **License:** MIT

## Quickstart — 30 seconds

```bash
go run github.com/bramheerink/gordap/cmd/gordap@latest \
  -addr=:8080 -self-link-base=http://localhost:8080 -icann-gtld \
  -tos-url=https://example.com/rdap-tos
```

Then in another terminal:

```bash
curl -s http://localhost:8080/domain/example.nl | jq '.objectClassName, .ldhName, .events[]'
curl -s http://localhost:8080/domains?name=example.* | jq '.domainSearchResults[].ldhName'
curl -s 'http://localhost:8080/domain/b%C3%BCcher.example' | jq '.redacted[].name.description'
```

The server boots in demo mode with an in-memory seed. Point
`-database-url=postgres://...` at your PostgreSQL to serve real data
(schema in [`pkg/rdap/storage/postgres/schema.sql`](pkg/rdap/storage/postgres/schema.sql)).

## Features

### RFC / standards
| Spec | Covered |
|---|---|
| RFC 7480 — HTTP usage | ✅ CORS, Content-Type, 406, 429 + Retry-After, HTTPS-ready |
| RFC 7482 / RFC 9082 — query format | ✅ domain / entity / nameserver / ip / help |
| RFC 7484 — bootstrap | ✅ IANA registry fetch + 302 redirect on NotFound |
| RFC 8977 — sorting / paging | ✅ `count` + opaque `cursor` + `paging_metadata` |
| RFC 8982 — partial response | ⚠️ via gzip (fieldSet selector: roadmap) |
| RFC 9083 — JSON responses | ✅ full object classes incl. secureDNS, variants, events |
| RFC 9536 — reverse search | ✅ `/domains`, `/entities`, `/nameservers` with wildcards |
| RFC 9537 — redaction | ✅ JSONPath + method + reason emitter (not just pruning) |
| RFC 9553 — JSContact | ✅ primary contact format; jCard deprecation-ready |
| draft-rdap-jscontact | ✅ `jscontact_level_0` conformance identifier |
| draft-rdap-versioning | ✅ `versioning_help` member on `/help` |
| draft-rdap-rir-search | ✅ `rdap-bottom`; `rdap-{top,up,down}` scoped 501 |
| draft-rdap-openid | ⚠️ JWKS verifier works; full draft flow roadmap |
| ICANN RP v2.2 | ✅ mandatory notices, events, conformance, self-links |
| ICANN TIG v2.2 | ✅ CORS, HSTS, input validation, rate limiting |

### Operational
- **10k+ QPS** on a single replica with the built-in LRU cache +
  singleflight. See [PERFORMANCE.md](PERFORMANCE.md).
- **Zero external deps** in core — only `pgx/v5`, `otel`, `x/net/idna`.
  Prometheus/Redis/OpenSearch are opt-in adapters.
- **Tiered access** (Anonymous / Authenticated / Privileged) with OIDC
  JWKS verifier (stdlib-only JWT parse) and GDPR-conservative defaults.
- **NIS2 Art. 28 audit trail** via `audit.Logger`.
- **Postgres-first, schema included**: typed columns for indexed fields,
  join tables for multi-valued channels, JSONB only for genuinely open
  data. [`schema.sql`](pkg/rdap/storage/postgres/schema.sql).

## Architecture

Every package under `pkg/rdap/` is independently importable.

```
pkg/rdap/
  types/          RFC 9083 wire types
  jscontact/      RFC 9553 Card types
  idn/            UTS #46 domain normalisation
  validate/       Length / charset input hardening
  auth/           AccessLevel + Claims + bearer middleware
    jwks/         stdlib-only OIDC verifier (RS256 / ES256)
  datasource/     DataSource contract + internal models
  ingest/         write-side contract (for your CDC / push pipeline)
  mapper/         datasource → types, tier-aware + RFC 9537 redaction
  cache/          LRU + singleflight + response cache
  bootstrap/      RFC 7484 IANA registry
  profile/        ICANN gTLD / RIR / ccTLD preset presets
  middleware/     CORS, rate-limit, gzip, body cap, security headers
  metrics/        Observer interface + stdlib expvar; Prometheus adapter
  observability/  slog factory, access log, OpenTelemetry helper
  audit/          NIS2 Art. 28 audit trail
  handlers/       HTTP handlers + router
  search/         RFC 9536 SearchIndex contract
  storage/memory/ in-memory DataSource + SearchIndex (demo / tests)
  storage/postgres/ production DataSource + SearchIndex
cmd/gordap/       reference binary (composes the above)
internal/config/  YAML loader (binary-only)
```

Full data-flow diagrams, adoption patterns (same-DB / CDC / push API),
and security boundaries in [ARCHITECTURE.md](ARCHITECTURE.md).

## How gordap compares

- **vs [openrdap/rdap](https://github.com/openrdap/rdap)** — they are a
  client/CLI, we are a server. Complementary. Our real-world test suite
  uses their CLI to verify interop.
- **vs [icann/icann-rdap](https://github.com/icann/icann-rdap)** (Rust)
  — closest peer. They are the reference-implementation authority; we
  are the Go-native toolkit for teams who want to embed RDAP into an
  existing Go stack.
- **vs [google/nomulus](https://github.com/google/nomulus)** — Nomulus
  is a full gTLD registry (EPP + billing + GCP). We are just the RDAP
  read path, neutral about infrastructure.
- **vs [DNSBelgium/rdap](https://github.com/DNSBelgium/rdap)** (Java /
  Spring Boot) — they are production-proven at .be. We trade Java
  ergonomics for a single static Go binary and a pluggable DataSource
  interface.

## Install

```bash
go install github.com/bramheerink/gordap/cmd/gordap@latest
```

Or build from source:

```bash
git clone https://github.com/bramheerink/gordap
cd gordap
make build         # produces bin/gordap
make test          # unit + e2e
make test-race     # with race detector
make test-realworld  # boots gordap + RFC assertions + openrdap interop
```

## Configure

Flags, environment variables, or YAML. Precedence: flag > env > YAML >
built-in default.

```bash
gordap \
  -addr=:443 -tls-cert=cert.pem -tls-key=key.pem \
  -database-url=postgres://rdap:pw@db/rdap \
  -self-link-base=https://rdap.example.nl \
  -icann-gtld -tos-url=https://example.nl/rdap-tos \
  -jwks-url=https://idp.example.nl/.well-known/jwks.json \
  -jwks-issuer=https://idp.example.nl -jwks-audience=rdap \
  -cache-size=50000 -cache-ttl=60s \
  -rate-limit-rps=50 -rate-limit-burst=100 \
  -bootstrap
```

Full YAML example in [`docs/config.example.yaml`](docs/config.example.yaml).

## Production checklist

Before exposing publicly:

- [ ] `make test-race` clean
- [ ] `govulncheck ./...` clean
- [ ] PostgreSQL grants: gordap's DB user has `SELECT` only
- [ ] TLS terminated by gordap (`--tls-cert/--tls-key`) or a trusted LB
      with HSTS preload
- [ ] Core dumps disabled (`ulimit -c 0`): cached records contain PII
- [ ] Rate-limit key function reads the real client IP if behind a proxy
- [ ] Audit log wired to append-only storage (NIS2 Art. 28)
- [ ] CDN in front of the anonymous tier (varnish / fastly / cloudflare)
- [ ] `Notices` include your jurisdiction-specific disclaimers

Details in [SECURITY.md](SECURITY.md).

## Real-world test suite

`make test-realworld` boots the reference binary and runs 22
assertions covering every core RFC + ICANN RP2.2 requirement plus an
interop check against openrdap/rdap. Output on a working tree:

```
--- PASS: TestRealWorld_Suite (0.57s)
    --- PASS: RFC7480_ContentType
    --- PASS: RFC7480_CORSOnEveryResponse
    --- PASS: RFC7480_OPTIONSPreflight
    --- PASS: RFC7480_406OnBadAccept
    --- PASS: RFC9083_ObjectClassNameAndConformance
    --- PASS: RFC9083_ErrorEnvelope
    --- PASS: RFC9083_SelfLinkPresent
    --- PASS: RFC9083_MandatoryEvents_IncludingDBUpdate
    --- PASS: RFC9083_IDNNameDualForm
    --- PASS: RFC9537_RedactedArrayShape
    --- PASS: ICANN_RP2.2_NoticesPresent
    --- PASS: ICANN_TIG_SecurityHeaders
    --- PASS: RFC7480_GzipCompression
    --- PASS: RFC7484_BootstrapDisabledByDefault
    --- PASS: RFC8977_PagingMetadata
    --- PASS: RFC9536_SearchPartialMatch
    --- PASS: RFC9082_UnknownPathReturnsRDAPError
    --- PASS: RIRSearch_rdap_bottom_Works
    --- PASS: RIRSearch_rdap_up_NotImplemented
    --- PASS: Versioning_HelpAdvertisesExtensions
    --- PASS: InputValidation_RejectsPathTraversal
    --- PASS: OpenRDAP_Interop
```

For the full authority treatment, `make test-conformance` runs the
ICANN RDAP Conformance Tool against a booted gordap via Docker.

## Documentation

- [ARCHITECTURE.md](ARCHITECTURE.md) — data flow, SoT, adoption
  patterns, package map, extension points
- [SECURITY.md](SECURITY.md) — threat model, PII surface, controls,
  operator-owned residual risks, deployment self-check
- [PERFORMANCE.md](PERFORMANCE.md) — 10k → 100k QPS recipes, cache
  tuning, CDN strategy, Prometheus adapter, OpenSearch decision tree

## Contributing

Bug reports and PRs welcome. Please include a test case with every
change. For non-trivial features, open an issue first to discuss scope.

## License

MIT. See [LICENSE](LICENSE).
