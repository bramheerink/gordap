# Gordap — Critical Audit & Production-Readiness Review

_Red-team review, 2026-04-21._

---

## Status after fixes (2026-04-21)

| Area | Review score | After fixes |
|---|---|---|
| Code quality | 8 / 10 | **8 / 10** |
| Architecture | 9 / 10 | **9 / 10** |
| Security hardening | 6 / 10 | **8 / 10** |
| RFC completeness | 7 / 10 | **8 / 10** |

**Resolved in this push:**

| Finding | File | Approach |
|---|---|---|
| 🔴 PII in record cache | [handlers/response_cache.go](../pkg/rdap/handlers/response_cache.go) | Response-cache middleware keyed on `(object, id, tier)`. Rendered JSON is already redacted → no PII in the working set. |
| 🔴 Rate-limit default → DoS | [middleware/ratelimit.go](../pkg/rdap/middleware/ratelimit.go) | `Middleware(nil)` now panics with a concrete error message. |
| 🟠 JWT clock skew + replay | [auth/jwks/jwks.go](../pkg/rdap/auth/jwks/jwks.go) + [replay.go](../pkg/rdap/auth/jwks/replay.go) | 30s default skew; `jti` cache with TTL = token exp. Single-replica fully covered; multi-replica best-effort (documented). |
| 🟠 `http.Server` timeouts | [cmd/gordap/main.go](../cmd/gordap/main.go) | Full quartet: `ReadHeaderTimeout` 5s / `ReadTimeout` 15s / `WriteTimeout` 30s / `IdleTimeout` 120s, each exposed as a flag. |
| ⚠️ RFC 9536 prefix-only | [search/search.go](../pkg/rdap/search/search.go) | `MatchPattern` now supports prefix (`foo*`), suffix (`*foo`), and infix (`*foo*`). Postgres ILIKE already did all three. |
| ❓ jCard fallback | [jscontact/jcard.go](../pkg/rdap/jscontact/jcard.go) | Full RFC 7095 marshaller. `EmitJCard` option in `mapper.Options`; auto-enabled by `--icann-gtld`. |

**Deliberately skipped:**

- 🟡 `==` on issuer/audience — signature verification runs first, so the timing-attack surface is theoretical. `subtle.ConstantTimeCompare` would be cargo-cult. The audit itself calls the risk "negligible" and still recommends the fix — that is contradictory.

**Nuance on some audit findings** (after a critical re-read):

- **PII in record cache as HIGH** — diagnosis correct, severity overstated. The cache exposes only the `datasource.DataSource` interface; to leak PII you'd need to bypass the interface and serialise raw types yourself. That invariant is enforced by the type system, not merely by code review. Call it documented contract discipline (LOW/MEDIUM), not an "incident waiting to happen." ResponseCache is an upgrade, not an urgent fix.
- **jti without shared store as MEDIUM** — in a multi-replica deployment, per-replica replay cache is partly theatre (an attacker plays the token against replica 2). Our implementation fully covers single-replica; real cross-replica revocation needs Redis + IdP webhook (out of scope for v1.0 without a concrete operator request).
- **SWR as MEDIUM** — latency optimisation, not a correctness issue. Singleflight already serialises thundering-herd. Should be LOW/P3. Built anyway because it was cheap.
- **"Without jCard fallback you can't run a gTLD registry"** — true today for ICANN-contracted parties, not universal. For ccTLDs/RIRs, JSContact-only is fine. Per-request negotiation via `?jscard=false` or `Accept: application/rdap+json; profile=jcard` has been added so legacy clients can explicitly ask for jCard without affecting the modern path.
- **"OpenRDAP runs at multiple real TLDs"** — **factually wrong**. OpenRDAP is a client library + CLI + web portal, not an authoritative server. Likely confused with APNIC rdapd or rdap.org (bootstrap).

---

The code is **clean** — no point in sugar-coating that — but "clean" ≠ "production-ready for a TLD registry." Below are five real weaknesses that will break under pressure.

---

## 1. Security

### 🔴 HIGH — PII leakage through record cache

[pkg/rdap/cache/cache.go:8-26](../pkg/rdap/cache/cache.go#L8-L26) admits it itself: *"Any code path that serialises a cached value without going through `mapper.Redact` WILL leak PII."* The only control is **code review**. That is not a control, that is an incident waiting to happen. A registrar under NIS2 / GDPR Art. 32 audit will flag this immediately.

**Fix:** Switch to a response cache keyed on `(object, id, access_level)`:

```go
type ResponseCache struct {
    lru *lru[responseKey, []byte] // pre-rendered JSON
}
type responseKey struct {
    kind string    // "domain"|"entity"|...
    id   string
    tier auth.Tier // Anonymous/Authenticated/Privileged
}
```

Then **no** unredacted PII object lives in the hot set. The current roadmap TODO is not enough — this has to land before v1.0.

### 🟠 MEDIUM — JWT: no `jti` replay protection, no clock skew

[pkg/rdap/auth/jwks/jwks.go:144-149](../pkg/rdap/auth/jwks/jwks.go#L144-L149):

```go
now := time.Now().Unix()
if claims.Exp > 0 && now >= claims.Exp { ... }
if claims.Nbf > 0 && now < claims.Nbf { ... }
```

No `jti` cache → tokens are **replayable until exp**. No leeway → an NTP skew of a few seconds gives false rejects at federated IdPs (RIPE NCC SSO routinely drifts 2–3s). Add:

```go
const clockSkew = 30 * time.Second
if claims.Exp > 0 && now > claims.Exp+int64(clockSkew.Seconds()) { ... }
if claims.Nbf > 0 && now+int64(clockSkew.Seconds()) < claims.Nbf { ... }
// + jti LRU for the last 5 minutes
```

### 🟡 LOW — Issuer/Audience compared with `==`

[jwks.go:151](../pkg/rdap/auth/jwks/jwks.go#L151) + [jwks.go:168](../pkg/rdap/auth/jwks/jwks.go#L168) use `==`. Because the signature is verified first, the timing-attack surface is negligible, but a strict pentest checklist will demand `subtle.ConstantTimeCompare`. Cheap fix.

### 🟢 ReDoS

No risk — [pkg/rdap/validate/validate.go](../pkg/rdap/validate/validate.go) uses **no regex**, only a byte whitelist and a length cap (253 octets, RFC 1035). This is the strongest part of the package.

---

## 2. Scalability & Performance

### 🔴 HIGH — Rate limit fails behind a proxy (DoS by default)

[pkg/rdap/middleware/ratelimit.go:82-84](../pkg/rdap/middleware/ratelimit.go#L82-L84):

```go
if key == nil { key = ClientIP }   // ← RemoteAddr!
```

Behind an L7 load balancer, `r.RemoteAddr` is always the LB IP. **One (honest) client triggers 429 for every tenant on the same replica.** The docs say "operator must override this" — a default that breaks production is a bug, not a feature. Make it fail-loud:

```go
func (rl *RateLimiter) Middleware(key func(*http.Request) string) ... {
    if key == nil {
        panic("ratelimit: key function is required; use middleware.ClientIP or a trusted-proxy extractor")
    }
    ...
}
```

### 🟠 MEDIUM — No stale-while-revalidate

LRU + singleflight are correct ([cache.go:229-262](../pkg/rdap/cache/cache.go#L229-L262)), but TTL expiry is **hard**. On a DB hiccup, the warm set vanishes at once, singleflight then serialises thousands of requests per popular domain = latency cliff. SWR is 40 extra lines:

```go
if entry.expiresAt.Before(now) && entry.expiresAt.Add(staleFor).After(now) {
    go c.refreshAsync(key)   // serialised via singleflight
    return entry.value, nil  // serve stale
}
```

### 🟢 Concurrency

Mutations under `sync.Mutex`, double-checked locking is correct, no goroutine leaks in handlers. Fine.

---

## 3. Robustness

### 🟠 MEDIUM — `http.Server` timeouts not visible in cmd/

I see `RequestTimeout` middleware ([hardening.go:29-37](../pkg/rdap/middleware/hardening.go#L29-L37)), but no `ReadHeaderTimeout` / `WriteTimeout` on the server itself. Slow-loris is then wide open. Required in [cmd/gordapd](../cmd/):

```go
srv := &http.Server{
    Addr: ":8080", Handler: router,
    ReadHeaderTimeout: 5 * time.Second,
    ReadTimeout:       10 * time.Second,
    WriteTimeout:      15 * time.Second,
    IdleTimeout:       60 * time.Second,
}
```

### 🟢 Error envelope

[handlers/errors.go](../pkg/rdap/handlers/errors.go) follows RFC 9083 §6 correctly, no stack traces leak out, `application/rdap+json` content-type is set.

---

## 4. Feature completeness

| Feature | Status | Note |
|---|---|---|
| RFC 9083 object classes | ✅ | domain/entity/nameserver/ip/autnum |
| RFC 9082 URL routing | ✅ | |
| RFC 9536 search | ⚠️ | Prefix-only (`*` suffix). SIDN/RIPE often want substring. |
| RFC 9537 redaction markers | ✅ | Three-tier model |
| RFC 9553 JSContact | ✅ | Native, **this is your USP** |
| jCard fallback | ❓ | Not found — required for ICANN compliance pre-2027 |
| Bootstrap (RFC 9224) | ✅ | pkg/rdap/bootstrap present |
| TLS client-cert auth | ❌ | No mTLS verifier |

**Critical:** without jCard fallback you cannot run a **gTLD registry** today.

---

## 5. Market comparison

**Correction vs. the first version:** OpenRDAP ([github.com/openrdap/rdap](https://github.com/openrdap/rdap)) is **not a server**. It is a client library + CLI to query existing RDAP services. Earlier framing as "demo server" / benchmark target was wrong and has been removed.

Language does not matter — if a Perl, Java, or PHP implementation is better on features, it wins. Feature comparison follows, no language framing.

### Publicly available RDAP server implementations

| Package | Language | Public status |
|---|---|---|
| **APNIC rdapd** ([github.com/APNIC-net/rdapd](https://github.com/APNIC-net/rdapd)) | Perl | Production at APNIC, open source |
| **Viagénie rdapd** ([github.com/viagenie/rdapd](https://github.com/viagenie/rdapd)) | Java | Maintained, smaller adoption |
| **ICANN RDAP Pilot / reference server** | Java | Historical reference, no longer actively maintained |
| **RIPE NCC / ARIN / Verisign / SIDN internal stacks** | Various | **Not public** — can't compare |
| **rdap.org** | — | Bootstrap redirect service, not an authoritative server |

### Feature matrix

Disclaimer: APNIC rdapd and Viagénie rdapd columns are based on what is visible in their public repos. RFC 9537 (2024) and RFC 9553 (2023) are recent standards; older servers usually don't have them, and when they do it's bolted-on. Verify before citing externally.

| Feature | gordap | APNIC rdapd | Viagénie rdapd | ICANN ref |
|---|:---:|:---:|:---:|:---:|
| RFC 9083 object classes | ✅ | ✅ | ✅ | ✅ |
| RFC 9082 URL routing | ✅ | ✅ | ✅ | ✅ |
| RFC 9536 search (prefix + infix) | ✅ | ⚠️ prefix-only | ⚠️ prefix-only | ⚠️ |
| RFC 9537 redaction markers | ✅ | ❌ | ❌ | ❌ |
| GDPR three-tier redaction | ✅ | ❌ | ❌ | ❌ |
| RFC 9553 JSContact | ✅ native | ❌ | ❌ | ❌ |
| jCard output | ✅ (fallback via negotiation) | ✅ | ✅ | ✅ |
| Pluggable DataSource | ✅ design principle | ❌ APNIC-whois coupled | ❌ | ❌ |
| Response cache with tier keying | ✅ | ❌ | ❌ | ❌ |
| JWT/OIDC + jti replay protection | ✅ | ❌ | ⚠️ basic | ❌ |
| Prometheus / OTEL observability | ✅ | ⚠️ custom | ⚠️ basic | ❌ |
| Rate limiting (token bucket) | ✅ | ⚠️ external (front) | ⚠️ external | ❌ |
| Production deployment at RIR/TLD | ❌ | ✅ APNIC | ⚠️ unknown | ❌ |
| Battle-tested at scale | ❌ | ✅ | ⚠️ | ❌ |

### Honest per-criterion read

- **Feature breadth:** gordap wins. RFC 9537 + RFC 9553 + pluggable storage in one package is not present on any public competitor.
- **Battle-testing:** APNIC rdapd wins by a wide margin. Years of production. Gordap has zero public deployments.
- **Performance at proven scale:** APNIC rdapd wins. Gordap has only synthetic benchmarks.
- **Modern standards (auth, observability, 9537/9553):** gordap wins. APNIC rdapd is Perl with older patterns; JSContact/redaction markers are absent.
- **Ergonomics for a new operator:** gordap wins. Pluggable design lets you wire in your own database/auth. APNIC rdapd is coupled to APNIC's schema.

### Who picks what?

- **You are a RIR and need to be live tomorrow:** APNIC rdapd. Proven, works, boring.
- **You are a registry that needs to pass GDPR/NIS2 audit with RFC 9537:** gordap (or build your own on a custom stack).
- **You are a ccTLD with your own database schema and want a modern RDAP layer:** gordap.
- **You are a PHP shop with a legacy whois:** there is **no serious public PHP RDAP server** that I know of. That's a gap, not competition.

### Three biggest real weaknesses of gordap

1. **No battle-testing.** APNIC rdapd runs in production at RIR scale; gordap has zero references.
2. **No testcontainers integration** for Postgres (see [audit2.md](audit2.md)) — query bugs only surface in production.
3. **No Zipfian benchmark** — PERFORMANCE.md numbers come from uniform-random load, not realistic RDAP traffic patterns.

---

## Final verdict

| | Score |
|---|---|
| Code quality | 8 / 10 |
| Architecture | 9 / 10 (pluggable design is genuinely good) |
| Security hardening | 6 / 10 (cache design + rate-limit default are showstoppers) |
| RFC completeness | 7 / 10 (missing jCard fallback) |
| Production-ready for a TLD registry | **❌ Not yet** |
| Production-ready for an internal registrar tool or demo | **✅ Yes** |

**Minimum blockers for v1.0:**

1. Response cache instead of record cache (fixes HIGH PII risk).
2. Rate-limit default must `panic` without an explicit key function.
3. `http.Server` timeouts required in the reference binary.
4. JWT clock skew + `jti` replay cache.
5. jCard fallback.

Also: add `govulncheck` and `gosec` to CI (README mentions them, `.github/` should actually run them).
