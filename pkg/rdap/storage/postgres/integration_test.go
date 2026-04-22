//go:build integration

// Postgres integration tests. Spin up a real Postgres via
// testcontainers, apply schema.sql, seed a small synthetic dataset,
// and verify both happy-path queries and that the indexes the
// schema declares actually get used.
//
// Run with:
//
//	go test -tags=integration ./pkg/rdap/storage/postgres/...
//
// Skipped by `go test ./...` so contributors without Docker still get
// a clean run.
package postgres

import (
	"context"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bramheerink/gordap/internal/synth"
	"github.com/bramheerink/gordap/pkg/rdap/search"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
)

const (
	testCorpus = 1000
	testDBUser = "rdap"
	testDBName = "rdap"
	testDBPass = "stress"
)

// startPostgres boots a real Postgres 17 container, applies schema.sql,
// and returns a connected pool plus a cleanup function. Every test that
// needs PG calls this exactly once.
func startPostgres(t *testing.T) (*pgxpool.Pool, func()) {
	t.Helper()
	ctx := context.Background()

	root := repoRoot(t)
	schemaPath := filepath.Join(root, "pkg", "rdap", "storage", "postgres", "schema.sql")

	container, err := tcpostgres.Run(ctx,
		"postgres:17-alpine",
		tcpostgres.WithDatabase(testDBName),
		tcpostgres.WithUsername(testDBUser),
		tcpostgres.WithPassword(testDBPass),
		tcpostgres.WithInitScripts(schemaPath),
		tcpostgres.BasicWaitStrategies(),
	)
	if err != nil {
		t.Skipf("testcontainers Postgres unavailable (Docker missing?): %v", err)
	}

	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		_ = container.Terminate(ctx)
		t.Fatal(err)
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		_ = container.Terminate(ctx)
		t.Fatal(err)
	}
	// Confirm the wait worked.
	if err := pool.Ping(context.Background()); err != nil {
		pool.Close()
		_ = container.Terminate(ctx)
		t.Fatal(err)
	}
	cleanup := func() {
		pool.Close()
		_ = container.Terminate(context.Background())
	}
	if err := seedTestData(ctx, pool, testCorpus); err != nil {
		cleanup()
		t.Fatal(err)
	}
	return pool, cleanup
}

func repoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	// Walk up from pkg/rdap/storage/postgres → repo root.
	for dir := wd; dir != "/"; dir = filepath.Dir(dir) {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
	}
	t.Fatal("repo root not found from " + wd)
	return ""
}

// seedTestData mirrors cmd/gordap-seed at small scale: enough rows
// for queries to be meaningful, few enough that container startup
// stays sub-10s.
func seedTestData(ctx context.Context, pool *pgxpool.Pool, n int) error {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	expires := now.AddDate(1, 0, 0)

	entityRows := make([][]any, 0, n)
	for i := 0; i < n; i++ {
		entityRows = append(entityRows, []any{
			synth.EntityHandle(i), synth.Kinds(i),
			synth.EntityFullName(i), synth.EntityOrganization(i), "",
			synth.EntityCountry(i), synth.EntityLocality(i), "",
			synth.EntityPostalCode(i), []string{synth.EntityStreet(i)},
			now, now, []byte("{}"),
		})
	}
	if _, err := pool.CopyFrom(ctx, pgx.Identifier{"entities"},
		[]string{"handle", "kind", "full_name", "organization", "title",
			"country_code", "locality", "region", "postal_code", "street",
			"created_at", "updated_at", "extras"},
		pgx.CopyFromRows(entityRows)); err != nil {
		return err
	}

	emailRows := make([][]any, 0, n)
	for i := 0; i < n; i++ {
		emailRows = append(emailRows, []any{synth.EntityHandle(i), synth.EntityEmail(i)})
	}
	if _, err := pool.CopyFrom(ctx, pgx.Identifier{"entity_emails"},
		[]string{"entity_handle", "email"}, pgx.CopyFromRows(emailRows)); err != nil {
		return err
	}

	nsCount := (n + 9) / 10
	nsRows := make([][]any, 0, nsCount)
	for i := 0; i < nsCount; i++ {
		nsRows = append(nsRows, []any{
			synth.NameserverHandle(i), synth.NameserverName(i), "",
			[]string{}, []string{},
		})
	}
	if _, err := pool.CopyFrom(ctx, pgx.Identifier{"nameservers"},
		[]string{"handle", "ldh_name", "unicode_name", "ipv4", "ipv6"},
		pgx.CopyFromRows(nsRows)); err != nil {
		return err
	}

	domRows := make([][]any, 0, n)
	for i := 0; i < n; i++ {
		domRows = append(domRows, []any{
			synth.DomainHandle(i), synth.DomainName(i), synth.DomainName(i),
			[]string{"active"}, now, expires, now, now,
		})
	}
	if _, err := pool.CopyFrom(ctx, pgx.Identifier{"domains"},
		[]string{"handle", "ldh_name", "unicode_name", "status",
			"registered_at", "expires_at", "last_changed", "last_rdap_update"},
		pgx.CopyFromRows(domRows)); err != nil {
		return err
	}

	dnRows := make([][]any, 0, n)
	for i := 0; i < n; i++ {
		dnRows = append(dnRows, []any{
			synth.DomainHandle(i), synth.NameserverHandle(synth.NameserverForDomain(i)),
		})
	}
	if _, err := pool.CopyFrom(ctx, pgx.Identifier{"domain_nameservers"},
		[]string{"domain_handle", "nameserver_handle"}, pgx.CopyFromRows(dnRows)); err != nil {
		return err
	}

	dcRows := make([][]any, 0, n)
	for i := 0; i < n; i++ {
		dcRows = append(dcRows, []any{
			synth.DomainHandle(i), synth.EntityHandle(synth.EntityForDomain(i)), "registrant",
		})
	}
	if _, err := pool.CopyFrom(ctx, pgx.Identifier{"domain_contacts"},
		[]string{"domain_handle", "entity_handle", "role"}, pgx.CopyFromRows(dcRows)); err != nil {
		return err
	}

	// Real IDN entries from synth.IDNFixtures so the storage
	// round-trip for non-ASCII names is exercised.
	idnRows := make([][]any, 0, len(synth.IDNFixtures))
	for _, idn := range synth.IDNFixtures {
		idnRows = append(idnRows, []any{
			idn.Handle, idn.LDH, idn.Unicode,
			[]string{"active"}, now, expires, now, now,
		})
	}
	_, err := pool.CopyFrom(ctx, pgx.Identifier{"domains"},
		[]string{"handle", "ldh_name", "unicode_name", "status",
			"registered_at", "expires_at", "last_changed", "last_rdap_update"},
		pgx.CopyFromRows(idnRows))
	return err
}

// ---------- the tests ----------

func TestIntegration_GetDomain_HappyPath(t *testing.T) {
	pool, cleanup := startPostgres(t)
	defer cleanup()
	p := New(pool)

	d, err := p.GetDomain(context.Background(), synth.DomainName(42))
	if err != nil {
		t.Fatalf("GetDomain: %v", err)
	}
	if d.LDHName != synth.DomainName(42) {
		t.Fatalf("ldh_name: got %q want %q", d.LDHName, synth.DomainName(42))
	}
	if len(d.Nameservers) != 1 {
		t.Fatalf("expected 1 nameserver, got %d", len(d.Nameservers))
	}
	if len(d.Contacts) != 1 {
		t.Fatalf("expected 1 contact, got %d", len(d.Contacts))
	}
	c := d.Contacts[0]
	if c.Handle != synth.EntityHandle(42) {
		t.Fatalf("contact handle: %q", c.Handle)
	}
	if len(c.Emails) != 1 {
		t.Fatalf("expected 1 email folded into contact, got %d", len(c.Emails))
	}
}

func TestIntegration_GetDomain_NotFound(t *testing.T) {
	pool, cleanup := startPostgres(t)
	defer cleanup()
	p := New(pool)

	_, err := p.GetDomain(context.Background(), "no-such-domain.invalid")
	if err == nil {
		t.Fatal("expected ErrNotFound, got nil")
	}
}

func TestIntegration_IDNRoundTrip(t *testing.T) {
	pool, cleanup := startPostgres(t)
	defer cleanup()
	p := New(pool)

	for _, idn := range synth.IDNFixtures {
		d, err := p.GetDomain(context.Background(), idn.LDH)
		if err != nil {
			t.Fatalf("GetDomain(%q): %v", idn.LDH, err)
		}
		if d.LDHName != idn.LDH {
			t.Errorf("LDH round-trip: got %q want %q", d.LDHName, idn.LDH)
		}
		if d.UnicodeName != idn.Unicode {
			t.Errorf("Unicode round-trip: got %q want %q", d.UnicodeName, idn.Unicode)
		}
	}
}

func TestIntegration_GetEntity_FoldsChannels(t *testing.T) {
	pool, cleanup := startPostgres(t)
	defer cleanup()
	p := New(pool)

	c, err := p.GetEntity(context.Background(), synth.EntityHandle(7))
	if err != nil {
		t.Fatalf("GetEntity: %v", err)
	}
	if c.Handle != synth.EntityHandle(7) {
		t.Fatalf("handle: %q", c.Handle)
	}
	if len(c.Emails) == 0 {
		t.Fatal("emails should be folded into entity scan; got 0")
	}
}

func TestIntegration_GetIPNetwork_NotFoundOnEmptyTable(t *testing.T) {
	pool, cleanup := startPostgres(t)
	defer cleanup()
	p := New(pool)

	_, err := p.GetIPNetwork(context.Background(), netip.MustParseAddr("192.0.2.1"))
	if err == nil {
		t.Fatal("expected ErrNotFound on empty ip_networks table")
	}
}

func TestIntegration_SearchDomains_Prefix(t *testing.T) {
	pool, cleanup := startPostgres(t)
	defer cleanup()
	p := New(pool)

	res, err := p.SearchDomains(context.Background(),
		search.Query{Terms: map[string]string{"name": "syn-1*"}, Limit: 50})
	if err != nil {
		t.Fatalf("SearchDomains: %v", err)
	}
	if res.Total == 0 {
		t.Fatal("expected matches for syn-1*, got 0")
	}
}

func TestIntegration_SearchDomains_Infix(t *testing.T) {
	pool, cleanup := startPostgres(t)
	defer cleanup()
	p := New(pool)

	res, err := p.SearchDomains(context.Background(),
		search.Query{Terms: map[string]string{"name": "*syn-1*"}, Limit: 50})
	if err != nil {
		t.Fatalf("SearchDomains infix: %v", err)
	}
	if res.Total == 0 {
		t.Fatal("expected matches for *syn-1*, got 0")
	}
}

func TestIntegration_SearchEntities_ByCountryCode(t *testing.T) {
	pool, cleanup := startPostgres(t)
	defer cleanup()
	p := New(pool)

	res, err := p.SearchEntities(context.Background(),
		search.Query{Terms: map[string]string{"countryCode": "NL"}, Limit: 50})
	if err != nil {
		t.Fatalf("SearchEntities: %v", err)
	}
	if res.Total == 0 {
		t.Fatal("expected NL entities, got 0")
	}
}

// TestIntegration_IndexesUsed asserts that the schema's indexes
// actually get used for the queries that depend on them. A regression
// here means a schema change accidentally broke a hot path.
func TestIntegration_IndexesUsed(t *testing.T) {
	pool, cleanup := startPostgres(t)
	defer cleanup()
	ctx := context.Background()

	cases := []struct {
		name     string
		query    string
		args     []any
		mustHave string
	}{
		{
			"domain exact uses unique index",
			`EXPLAIN (FORMAT TEXT) SELECT * FROM domains WHERE ldh_name = 'syn-42.de'`,
			nil,
			"Index Scan",
		},
		{
			"domain prefix uses text_pattern_ops",
			`EXPLAIN (FORMAT TEXT) SELECT * FROM domains WHERE ldh_name LIKE 'syn-1%'`,
			nil,
			"domains_ldh_pattern",
		},
		{
			"domain infix uses trigram GIN",
			`EXPLAIN (FORMAT TEXT) SELECT * FROM domains WHERE ldh_name LIKE '%syn-1%'`,
			nil,
			"domains_ldh_trgm",
		},
		{
			"reverse contact lookup uses entity_handle index",
			`EXPLAIN (FORMAT TEXT) SELECT * FROM domain_contacts WHERE entity_handle = 'SYN-ENT-1'`,
			nil,
			"domain_contacts_entity_idx",
		},
	}
	// Force the planner to consider the indexes even on a tiny test
	// dataset — at 1k rows PG's cost model rightly prefers seq-scan
	// over GIN/B-tree, but we want to verify the indexes EXIST and
	// would be used at production scale.
	if _, err := pool.Exec(ctx, "SET enable_seqscan = off"); err != nil {
		t.Fatalf("disable seqscan: %v", err)
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rows, err := pool.Query(ctx, c.query, c.args...)
			if err != nil {
				t.Fatalf("EXPLAIN: %v", err)
			}
			defer rows.Close()
			var plan strings.Builder
			for rows.Next() {
				var line string
				if err := rows.Scan(&line); err != nil {
					t.Fatal(err)
				}
				plan.WriteString(line)
				plan.WriteByte('\n')
			}
			if !strings.Contains(plan.String(), c.mustHave) {
				t.Fatalf("plan does not contain %q:\n%s", c.mustHave, plan.String())
			}
		})
	}
}
