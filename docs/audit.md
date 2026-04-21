# Gordap — Kritische Audit & Production-Readiness Review

_Red-team review, 2026-04-21._

---

## Status na fixes (2026-04-21)

| Onderdeel | Review-score | Na fixes |
|---|---|---|
| Code-kwaliteit | 8 / 10 | **8 / 10** |
| Architectuur | 9 / 10 | **9 / 10** |
| Security-hardening | 6 / 10 | **8 / 10** |
| RFC-volledigheid | 7 / 10 | **8 / 10** |

**Opgelost in deze push:**

| Finding | Bestand | Aanpak |
|---|---|---|
| 🔴 PII in record-cache | [handlers/response_cache.go](../pkg/rdap/handlers/response_cache.go) | Response-cache middleware gekeyd op `(object, id, tier)`. Rendered-JSON is al geredigeerd → geen PII meer in working set. |
| 🔴 Rate-limit default → DoS | [middleware/ratelimit.go](../pkg/rdap/middleware/ratelimit.go) | `Middleware(nil)` panikt nu met concrete foutmelding. |
| 🟠 JWT clock-skew + replay | [auth/jwks/jwks.go](../pkg/rdap/auth/jwks/jwks.go) + [replay.go](../pkg/rdap/auth/jwks/replay.go) | 30s default skew; `jti`-cache met TTL = token exp. Single-replica effectief, multi-replica best-effort (gedocumenteerd). |
| 🟠 `http.Server` timeouts | [cmd/gordap/main.go](../cmd/gordap/main.go) | Volledige quartet: `ReadHeaderTimeout` 5s / `ReadTimeout` 15s / `WriteTimeout` 30s / `IdleTimeout` 120s, allemaal als flag. |
| ⚠️ RFC 9536 prefix-only | [search/search.go](../pkg/rdap/search/search.go) | `MatchPattern` ondersteunt nu prefix (`foo*`), suffix (`*foo`) en infix (`*foo*`). Postgres ILIKE deed al alle drie. |
| ❓ jCard fallback | [jscontact/jcard.go](../pkg/rdap/jscontact/jcard.go) | Volledige RFC 7095 marshaller. `EmitJCard` optie in `mapper.Options`; auto-enabled door `--icann-gtld`. |

**Bewust overgeslagen:**

- 🟡 `==` op issuer/audience — signatuur-verificatie loopt eerst, timing-oppervlak is theoretisch. `subtle.ConstantTimeCompare` zou cargo-cult zijn. De audit zegt zelf "verwaarloosbaar" en adviseert de fix toch — dat is tegenstrijdig.

**Nuance op enkele audit-findings** (ná eigen kritische herlezing):

- **PII in record-cache als HIGH** — diagnose klopt, severity is opgeschroefd. De cache exposet alleen de `datasource.DataSource`-interface; om te lekken moet je die interface omzeilen en raw types zelf serialiseren. De invariant is door het type-systeem afgedwongen, niet enkel door code-review. Noem dat gedocumenteerde contract-discipline (LOW/MEDIUM), geen "aspirant-incident". ResponseCache is een upgrade, geen urgent fix.
- **jti zonder shared store als MEDIUM** — in een multi-replica deployment is de per-replica replay-cache deels theater (attacker speelt token tegen replica-2). Onze implementatie dekt single-replica volledig; echte cross-replica revocation vereist Redis + IdP-webhook (buiten scope voor v1.0 zonder concrete operator-vraag).
- **SWR als MEDIUM** — latency-optimalisatie, geen correctheidsprobleem. Singleflight serialiseert thundering herd al. Hoort op LOW/P3. Wel gebouwd omdat 't goedkoop was.
- **"Zonder jCard-fallback kun je geen gTLD-registry draaien"** — correct voor ICANN-contracted parties vandaag, niet universeel. Voor ccTLD's/RIR's is JSContact-only prima. Per-request negotiation via `?jscard=false` of `Accept: application/rdap+json; profile=jcard` is toegevoegd zodat legacy-clients expliciet om jCard kunnen vragen zonder de moderne pad te beïnvloeden.
- **"OpenRDAP draait bij meerdere echte TLD's"** — **feitelijk onjuist**. OpenRDAP is een Go client-library + CLI + web-portal. Draait nergens authoritative. Mogelijk verward met APNIC rdapd of rdap.org (bootstrap).

---

De code is **netjes** — daar ga ik niet omheen draaien — maar "netjes" ≠ "production-ready voor een TLD registry". Hieronder vijf reële zwakheden die onder druk zullen breken.

---

## 1. Veiligheid

### 🔴 HIGH — PII-lekkage door record-cache

[pkg/rdap/cache/cache.go:8-26](../pkg/rdap/cache/cache.go#L8-L26) geeft zélf toe: *"Any code path that serialises a cached value without going through `mapper.Redact` WILL leak PII."* De enige controle is **code review**. Dat is geen control, dat is een aspirant-incident. Een registrar die onder NIS2 / GDPR Art. 32 audit komt, zal hier direct op vallen.

**Fix:** Schakel om naar response-cache gekeyd op `(object, id, access_level)`:

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

Dan zit er **geen enkel** ongefilterd PII-object meer in de hot set. De huidige roadmap-TODO is onvoldoende — dit moet vóór v1.0.

### 🟠 MEDIUM — JWT: geen `jti` replay-protectie, geen clock skew

[pkg/rdap/auth/jwks/jwks.go:144-149](../pkg/rdap/auth/jwks/jwks.go#L144-L149):

```go
now := time.Now().Unix()
if claims.Exp > 0 && now >= claims.Exp { ... }
if claims.Nbf > 0 && now < claims.Nbf { ... }
```

Geen `jti` cache → tokens zijn **replay-baar tot aan exp**. Geen leeway → NTP-skew van enkele seconden geeft false rejects bij federatieve IdP's (RIPE NCC SSO zit regelmatig 2–3s uit de pas). Voeg toe:

```go
const clockSkew = 30 * time.Second
if claims.Exp > 0 && now > claims.Exp+int64(clockSkew.Seconds()) { ... }
if claims.Nbf > 0 && now+int64(clockSkew.Seconds()) < claims.Nbf { ... }
// + jti LRU van laatste 5 min
```

### 🟡 LOW — Issuer/Audience compare via `==`

[jwks.go:151](../pkg/rdap/auth/jwks/jwks.go#L151) + [jwks.go:168](../pkg/rdap/auth/jwks/jwks.go#L168) gebruiken `==`. Omdat signatuur eerst wordt geverifieerd is timing-attack-oppervlak verwaarloosbaar, maar een strikte pentest-checker zal `subtle.ConstantTimeCompare` eisen. Goedkope fix.

### 🟢 ReDoS

Geen risico — [pkg/rdap/validate/validate.go](../pkg/rdap/validate/validate.go) gebruikt **geen regex**, alleen byte-whitelist en lengte-cap (253 octets, RFC 1035). Dit is het sterkste punt van het pakket.

---

## 2. Schaalbaarheid & Performance

### 🔴 HIGH — Rate limit faalt achter proxy (DoS door default)

[pkg/rdap/middleware/ratelimit.go:82-84](../pkg/rdap/middleware/ratelimit.go#L82-L84):

```go
if key == nil { key = ClientIP }   // ← RemoteAddr!
```

Achter een L7 load balancer is `r.RemoteAddr` altijd de LB-IP. **Één (eerlijke) client triggert 429 voor álle tenants op dezelfde replica.** De documentatie zegt "operator moet dit overriden" — een default die productie sloopt is een bug, geen feature. Maak het fail-loud:

```go
func (rl *RateLimiter) Middleware(key func(*http.Request) string) ... {
    if key == nil {
        panic("ratelimit: key function is required; use middleware.ClientIP or a trusted-proxy extractor")
    }
    ...
}
```

### 🟠 MEDIUM — Geen stale-while-revalidate

LRU + singleflight zijn correct ([cache.go:229-262](../pkg/rdap/cache/cache.go#L229-L262)), maar TTL-expiry is **hard**. Bij een DB-hiccup verdwijnt je warme set ineens, singleflight serialiseert dan duizenden verzoeken per populair domein = latency-cliff. SWR is 40 regels extra:

```go
if entry.expiresAt.Before(now) && entry.expiresAt.Add(staleFor).After(now) {
    go c.refreshAsync(key)   // geserialiseerd via singleflight
    return entry.value, nil  // serveer stale
}
```

### 🟢 Concurrency

Muteren onder `sync.Mutex`, double-checked locking correct, geen goroutine-leaks in handlers. Prima.

---

## 3. Robuustheid

### 🟠 MEDIUM — `http.Server` timeouts niet zichtbaar in cmd/

Ik zie `RequestTimeout` middleware ([hardening.go:29-37](../pkg/rdap/middleware/hardening.go#L29-L37)), maar geen `ReadHeaderTimeout` / `WriteTimeout` op de server zelf. Slow-loris is dan gewoon mogelijk. Verplicht in [cmd/gordapd](../cmd/):

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

[handlers/errors.go](../pkg/rdap/handlers/errors.go) volgt RFC 9083 §6 correct, geen stack traces naar buiten, `application/rdap+json` content-type gezet.

---

## 4. Feature-volledigheid

| Feature | Status | Opmerking |
|---|---|---|
| RFC 9083 object classes | ✅ | domain/entity/nameserver/ip/autnum |
| RFC 9082 URL routing | ✅ | |
| RFC 9536 search | ⚠️ | Prefix-only (`*` suffix). SIDN/RIPE vragen vaak substring. |
| RFC 9537 redaction markers | ✅ | Driestaps-tiering |
| RFC 9553 JSContact | ✅ | Native, **dit is je USP** |
| jCard fallback | ❓ | Niet gezien — verplicht voor ICANN compliance pre-2027 |
| Bootstrap (RFC 9224) | ✅ | pkg/rdap/bootstrap aanwezig |
| TLS client cert auth | ❌ | Geen mTLS-verifier |

**Kritiek:** zonder jCard-fallback kun je vandaag **geen gTLD-registry** draaien.

---

## 5. Marktvergelijking

| Pakket | Type | Kracht | Zwakte t.o.v. gordap |
|---|---|---|---|
| **OpenRDAP** (github.com/openrdap/rdap) | Primair **client** + demo server in Go | Mature parser, bekend in ecosysteem | Geen pluggable data source, geen redaction-tiering, server-kant is demo-grade |
| **APNIC rdapd** (Perl) | RIR-server | Draait productie bij APNIC | Legacy Perl, geen GDPR-tiering, geen JSContact |
| **ARIN Whowas / rdap** (Java) | RIR-server, intern | Battle-tested bij ARIN | Niet open, niet pluggable |
| **rdap-pilot / ICANN ref impl** | Ref-implementatie | Normatief, volgt RFC's strak | Geen productie-focus (geen cache, geen rate-limit) |

**Waarom gordap kiezen?**

1. **Enige** Go-server met RFC 9553 JSContact first-class (niet bolted-on).
2. Pluggable `DataSource` → je kunt Postgres, OpenSearch, of REST-upstream pluggen zonder fork.
3. RFC 9537 redaction-tiering ingebouwd — niemand anders doet dit zo netjes.

**Drie grootste zwaktes t.o.v. OpenRDAP:**

1. Geen battle-test bij echte TLD (OpenRDAP draait bij meerdere).
2. Geen jCard-fallback → niet ICANN-contract-ready.
3. Geen IANA-bootstrap auto-refresh zichtbaar in scheduler.

---

## Eindoordeel

| | Score |
|---|---|
| Code-kwaliteit | 8 / 10 |
| Architectuur | 9 / 10 (pluggable design is echt goed) |
| Security-hardening | 6 / 10 (cache-design + rate-limit default zijn showstoppers) |
| RFC-volledigheid | 7 / 10 (mist jCard fallback) |
| Production-ready voor een TLD registry | **❌ Nee, nog niet** |
| Production-ready voor een intern registrar-tool of demo | **✅ Ja** |

**Minimale blockers voor v1.0:**

1. Response-cache i.p.v. record-cache (fix HIGH PII-risk).
2. Rate-limit default moet `panic` zonder expliciete key-function.
3. `http.Server` timeouts verplicht in reference binary.
4. JWT clock-skew + `jti` replay cache.
5. jCard fallback.

Verder: voeg `govulncheck` en `gosec` toe aan CI (README noemt ze, `.github/` moet ze draaien).
