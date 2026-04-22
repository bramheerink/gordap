# Gordap — Second Audit: "Are we by far the best?"

_Red-team review after fixes, 2026-04-22._

Short answer: **no, "by far the best" doesn't hold.** "Best public, pluggable, JSContact-native RDAP server" — probably yes, but that's a weak bench because competition is mostly non-public (RIRs run internal stacks). And the test tooling has real gaps a serious registry will find.

---

## Honest positioning

| Claim | Holds up? |
|---|---|
| "Only server with RFC 9553 JSContact + jCard negotiation" | ✅ Yes |
| "Only open server with RFC 9537 redaction tiering first-class" | ✅ Yes, as far as publicly known |
| "Pluggable DataSource beats alternatives" | ✅ Yes — APNIC rdapd is coupled to APNIC's schema |
| "By far the best" | ❌ No. Battle-testing is absent. APNIC rdapd has years of production. |
| "Production-ready for a TLD registry" | ⚠️ Almost, but not on the testing side |

The fixes (ResponseCache, panic on rate-limit default, http.Server timeouts, jti cache, jCard) closed three of the five v1.0 blockers. That is real. The audit score upgrade (6→8 security, 7→8 RFC) is justified. But the gap between "good code" and "software you'd hand to RIPE NCC blind" lives in the test layer, and that is where the weaknesses are.

---

## Test tooling — critical

### 🔴 Postgres storage has **no integration tests**

[pkg/rdap/storage/postgres/postgres_test.go](../pkg/rdap/storage/postgres/postgres_test.go) tests `scanContactRow` against a **fake scanner interface**. [search_test.go](../pkg/rdap/storage/postgres/search_test.go) tests the escape function `ilikePattern` — but **never runs a real query** against a real Postgres.

Consequence: a broken WHERE clause, a case-sensitivity bug, an index that isn't used, a trigram GIN that accidentally triggers a seq-scan — **you only see it in production**. For a server whose selling point is "pluggable storage," this is a gap with a name.

**Fix:** `testcontainers-go` with a real Postgres that loads `schema.sql`, seeds 1000 rows, and uses `EXPLAIN ANALYZE` to verify indexes are used. Not hard, but mandatory.

### 🟠 Stress tool is missing Zipf distribution

[cmd/gordap-stress/main.go](../cmd/gordap-stress/main.go) picks uniform-random inside each category. Real RDAP traffic is Zipfian (top 1% of domains = 50%+ of queries). That means:

- Your cache hit ratio in benchmarks is artificially **lower** than in production (uniform distribution stresses the cache less well).
- Your tail latency (p99/p999) is artificially **cleaner** than production (no hot keys).

Implementing Zipf is ~20 lines (`rand.Zipf`). Without it, your PERFORMANCE.md numbers are not comparable to operator reality.

### 🟠 "realworld" test seed is synthetic

[test/realworld/realworld_test.go](../test/realworld/realworld_test.go) boots a real binary — good — but uses in-memory demo data. No IDNs in the seed (`synth.go` only generates `syn-N.{nl,com,de,test}`), no variety in registrars, no DNSSEC edge cases, no auth-tier differentiation at the HTTP layer.

The IDN dual-form test at line 306 only passes because the handler does it on the fly, not because the stored data contains IDNs. A real IDN bug in the storage layer would not be caught.

### 🟠 CI is missing three standard security checks

[.github/workflows/ci.yml](../.github/workflows/ci.yml) runs `go vet`, `go test -race`, `govulncheck` — fine. But **not**:

- `go test -cover` with a threshold (you have no visibility into which paths are dark)
- `go test -fuzz` on validator/IDN/search pattern parsing
- `gosec` for hardcoded creds + SQL-injection patterns
- ICANN conformance tool (it's in the Makefile lines 43–54, but **opt-in**)

Each of these has caught this class of bug in production RDAP servers at some point. Enabling them is cheap.

### 🟢 What's right

- **Stress tool validates response bodies**, not just status codes ([main.go:437-449](../cmd/gordap-stress/main.go#L437-L449)) — many stress tools don't do this.
- **Aggregator knows you can't average percentiles** ([main.go:15](../cmd/gordap-stress-aggregate/main.go#L15)) — rare honesty.
- **JWKS test uses real RSA crypto**, not a mock signer ([jwks_test.go:66-85](../pkg/rdap/auth/jwks/jwks_test.go#L66-L85)).
- **Race detector mandatory in CI** — gold standard.
- **rdap-validator + openrdap CLI as a second-opinion check** in realworld ([realworld_test.go:441-487](../test/realworld/realworld_test.go#L441-L487)) — genuinely strong. (OpenRDAP used correctly here: as a client/validator, not a comparable server.)
- **Response-cache e2e test** verifies DataSource is called exactly once ([cache_e2e_test.go:50-77](../pkg/rdap/handlers/cache_e2e_test.go#L50-L77)).

### 🟢 Schema is thoughtful

[schema.sql](../pkg/rdap/storage/postgres/schema.sql) has trigram GIN (lines 82, 107–109), prefix `text_pattern_ops` (line 79), citext for emails (line 46), JSONB for extras, FKs with CASCADE. This is better than what you see in many in-house registry implementations. **But:** no partitioning for 10M+ zones, no index on `entity_handle` in `domain_contacts` (seq-scan for "all domains of registrar X"), no index on `last_rdap_update`. For SIDN scale (~6.3M domains) it still works; for .com scale (~160M) it doesn't.

---

## To actually be "by far"

What's missing is **proof**, not code:

1. **Testcontainers + real Postgres** in CI — biggest gap.
2. **Zipfian load** in the stress tool + published benchmarks against APNIC rdapd on identical hardware (the only publicly comparable RDAP server).
3. **A real deployment case study** — someone who runs it for a real (even small) zone, even just a hobby ccTLD or an IRR-like registry.
4. **Fuzz + gosec + coverage gate** — trivial, major credibility boost.
5. **Partitioning example** in schema.sql for zones >5M — shows you thought about scale.
6. **Shared jti cache** (Redis) for multi-replica — without this, replay protection is half-done.

---

## Final verdict

Gordap has a real shot at being **the best publicly available RDAP server for modern standards (RFC 9537 + RFC 9553 + pluggable storage)**. That is not the same as "by far the best RDAP server." The gap isn't in the code — the code is up to standard — the gap is in **proven operation at scale** and **integration testing against real infrastructure**. Close the testcontainers gap and publish one serious benchmark, and then you can make the claim.
