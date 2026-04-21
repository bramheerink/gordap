# gordap Security Posture

This document records the threat model we design against, what gordap
defends against out of the box, and the residual risks operators need
to own.

## 1. Threat model

RDAP is a public-facing, unauthenticated-by-default protocol. Threats
considered:

1. **Information disclosure** (primary): anonymous callers receiving
   PII that should only be visible to authenticated / privileged
   principals.
2. **Resource exhaustion** (DoS): scraping and slowloris-style attacks
   inflating cost per query.
3. **Injection / smuggling**: SQL injection, log injection, header
   smuggling via user-controlled path segments.
4. **Privilege escalation**: anonymous caller getting tier-promoted
   responses through bypass of the auth middleware.
5. **Cache poisoning**: malformed inputs polluting the LRU / response
   cache.
6. **Supply-chain**: malicious or vulnerable dependency.
7. **Audit trail tampering**: attackers disabling / rewriting audit
   records to cover abusive queries.

Out of scope:
- Attacks on the registry's EPP system (not our surface).
- Attacks on PostgreSQL itself (operator's responsibility; use PG
  hardening + a read-only user for gordap).

## 2. Data handled

gordap handles full registration records in memory: names, postal
addresses, emails, phone numbers, and per-registrar `extras` JSONB.
Classified as **personal data** under GDPR when the contact is a
natural person.

Data lifetime by location:

| Location | Contents | Retention |
|---|---|---|
| PostgreSQL | Full records | Driven by upstream SoT + erasure events |
| `cache` package LRU | Full records (incl. PII) | Up to `Config.TTL` (default: 60s) |
| Request goroutine | Full record | Duration of the request |
| Access log | Request metadata only (method, path, status, ms, trace ID) | Operator-configured |
| Audit log (if enabled) | Requester identity, path, tier, status | Operator-configured; NIS2 suggests ≥6 months |
| OpenTelemetry spans | Path, status, duration | Operator's tracing backend |

**We never log:** request bodies, response bodies, contact fields,
bearer tokens.

## 3. Controls in place

### 3.1 Information disclosure
- **Tiered redaction** in `pkg/rdap/mapper`: Anonymous / Authenticated /
  Privileged access levels; full contact data is pruned before JSON
  serialisation. Tested in `redact_test.go`.
- **Default tier = Anonymous**: invalid / missing token → Anonymous,
  not an authentication error, matching RFC 7480's public profile.
- **Fully-redacted contacts emit no JSCard**: a Card with every
  field empty (because all were redacted) is actively *removed*
  rather than returned — removing one leakage signal.

### 3.2 Resource exhaustion
- **Rate limiting** (`middleware.RateLimiter`): token-bucket per key
  (default: client IP). Returns `429` + `Retry-After` per RFC 7480 §5.5.
- **Request timeout** (`middleware.RequestTimeout`): context cancellation
  propagates into the DataSource, aborting slow DB queries cleanly.
- **Request-body cap** (`middleware.MaxRequestBody`): defense in depth
  against forged `Content-Length`.
- **`http.Server.ReadHeaderTimeout`** set in `cmd/gordap` to defeat
  slowloris.
- **Bounded concurrency**: pgx connection pool caps back-end
  parallelism; configure per operator deployment.

### 3.3 Injection / smuggling
- **Input validation** (`pkg/rdap/validate`):
  - Domain names: RFC 1035 octet-length cap (253), UTS #46 syntactic
    validation via `idn.Normalize`.
  - Entity handles: length cap (64) + strict character set
    `[0-9A-Za-z._-]`, rejects path traversal payloads, spaces,
    control chars.
  - IPs: stdlib `netip.ParseAddr` (rejects malformed / embedded nuls).
  - Generic path segments: 512 octets hard upper bound.
- **Parameterised queries only**: all Postgres queries use `$1`
  placeholders. No string concatenation of user input.
- **Log injection**: user-controlled path values are emitted via
  `slog.String(...)`, which quotes values — no ANSI escape / newline
  smuggling.
- **Header splitting**: stdlib `net/http.ResponseWriter` rejects
  newlines in header values.

### 3.4 Auth middleware
- **Verifier interface** is the single mutation point for `Claims`.
  `auth.NopVerifier()` always returns Anonymous; operators supply
  their own JWKS / OIDC verifier.
- **Bearer-only** extraction: `Authorization: Basic …` headers are
  ignored rather than passed through, preventing confusion between
  schemes.
- **Verifier errors fail closed to Anonymous**, not to 401 — the
  redaction layer is the single source of truth for what a caller
  can see.

### 3.5 Cache safety
- LRU stores raw records (contains PII). Documented in
  `pkg/rdap/cache/cache.go` package comment. Invariant: only
  `mapper.*` may serialise a cached record; no code path exposes
  the cache value directly.
- Negative cache has a *shorter* TTL (default: TTL/2) to avoid
  long-lived "this domain doesn't exist" poisoning.
- **Roadmap**: a response-cache keyed by `(object, id, access_level)`
  that stores already-rendered JSON. Removes PII from the cache's
  working set entirely. Tracked for the next milestone.

### 3.6 TLS & transport
- `cmd/gordap --tls-cert --tls-key` serves HTTPS directly.
  Recommended deployment: TLS terminated at the LB, HSTS preload.
- **`Strict-Transport-Security`** header set by
  `middleware.SecurityHeaders()`.
- Current TLS version is whatever stdlib defaults to at the Go
  version compiled against — pin `tls.Config{MinVersion: tls.VersionTLS13}`
  for strict deployments.

### 3.7 Validation answers — "deze tekens mogen niet in deze TLD"
- We validate **syntactically** (UTS #46 + RFC 1035 length).
- We do **not** validate per-TLD character policies (`.de` requires
  ≥3 chars, `.中国` only accepts specific scripts, etc). Reason:
  those are registry rules, enforced by the authoritative system.
  Rejecting at RDAP would be wrong for anyone serving a different
  policy than the one we hardcoded.
- We do **not** validate TLD existence. An unknown TLD yields a
  clean 404 after lookup — better UX than a blanket 400, and it
  preserves the ability to serve private/test TLDs.

### 3.8 Supply chain
- Core dependencies: `pgx/v5`, `otel`, `x/net/idna`. All pinned in
  `go.mod` / `go.sum`.
- Go 1.26 toolchain; `go vet` clean.
- **Recommended**: run `govulncheck ./...` in CI. Not baked in
  because we don't ship a CI harness.

### 3.9 Audit trail
- `pkg/rdap/audit.Logger` interface: a `Noop` default, a `Slog`-backed
  default you can point at a dedicated log file. Recorded fields match
  NIS2 Art. 28: requester subject, tier, path, status, duration, remote
  IP, timestamp.
- Audit records **contain no contact data** — only query metadata.

## 4. Residual risks (operator-owned)

1. **TLS config**: gordap ships with stdlib defaults. Enforce TLS 1.3
   and modern cipher lists via your LB config or by setting
   `http.Server.TLSConfig` in a custom `cmd/`.
2. **Core dumps**: production processes SHOULD have `ulimit -c 0` /
   `core_pattern=/dev/null`. A core from a caching gordap contains PII.
3. **PostgreSQL user**: gordap only reads. Grant it `SELECT` on the
   relevant tables and nothing else. Separate account for ingest.
4. **Client-IP in rate limiter**: deployments behind a proxy must
   supply a key function that reads the verified forwarded IP. The
   default `middleware.ClientIP` uses `r.RemoteAddr`.
5. **CORS wildcard**: RDAP is strictly public; we emit
   `Access-Control-Allow-Origin: *` intentionally. Do NOT enable
   credentialed CORS — the combination is insecure.
6. **Audit log retention**: operator picks storage, rotation,
   integrity. Ship audit records to append-only storage for NIS2.
7. **Bootstrap registry URLs**: gordap pulls from
   `https://data.iana.org/rdap/*.json`. An attacker with control over
   your DNS / TLS root store could substitute redirect targets.
   Consider pinning the IANA cert in high-assurance deployments.

## 5. Self-check before deploying

- [ ] `govulncheck ./...` clean
- [ ] `go test -race -count=1 ./...` clean
- [ ] PostgreSQL user is read-only; ingest uses a separate account
- [ ] Core dumps disabled
- [ ] TLS terminated either by gordap with `--tls-cert/--tls-key` or
      by a trusted LB; HSTS set
- [ ] Rate-limit key function reads the real client IP (not LB IP)
- [ ] Audit log wired to append-only storage if serving EU TLD
- [ ] `handlers.Server.SelfLinkBase` set to the public canonical URL
- [ ] `handlers.Server.Notices` set to
      `profile.ICANNgTLDNotices(tosURL)` for gTLD deployments
- [ ] CDN in front of the server for anonymous traffic
