// gordap-seed populates a PostgreSQL database (matching schema.sql)
// with synthetic data drawn from internal/synth. Bulk inserts via
// pgx CopyFrom for throughput — typically 50k–200k rows/s on a local
// PG depending on disk.
//
// Usage:
//
//	gordap-seed -database-url=postgres://... -n=100000 -truncate
//
// The --truncate flag is required for re-runs; otherwise the inserts
// fail on the primary-key constraint. Truncation cascades to
// domain_contacts / domain_nameservers.
package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"time"

	"github.com/bramheerink/gordap/internal/synth"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	var (
		dsn      = flag.String("database-url", os.Getenv("DATABASE_URL"), "Postgres DSN (or env DATABASE_URL)")
		n        = flag.Int("n", 10_000, "number of domains and entities to generate")
		truncate = flag.Bool("truncate", false, "TRUNCATE target tables before inserting")
		batch    = flag.Int("batch", 5_000, "CopyFrom batch size")
	)
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	if *dsn == "" {
		logger.Error("missing --database-url")
		os.Exit(2)
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, *dsn)
	if err != nil {
		logger.Error("postgres connect", slog.Any("err", err))
		os.Exit(1)
	}
	defer pool.Close()

	if *truncate {
		if err := truncateTables(ctx, pool); err != nil {
			logger.Error("truncate", slog.Any("err", err))
			os.Exit(1)
		}
		logger.Info("truncated existing tables")
	}

	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	start := time.Now()

	if err := seedEntities(ctx, pool, *n, *batch, now); err != nil {
		logger.Error("seed entities", slog.Any("err", err))
		os.Exit(1)
	}
	logger.Info("entities done", slog.Int("n", *n), slog.Duration("elapsed", time.Since(start)))

	nsCount := (*n + 9) / 10
	if err := seedNameservers(ctx, pool, nsCount, *batch); err != nil {
		logger.Error("seed nameservers", slog.Any("err", err))
		os.Exit(1)
	}
	logger.Info("nameservers done", slog.Int("n", nsCount))

	if err := seedEmails(ctx, pool, *n, *batch); err != nil {
		logger.Error("seed emails", slog.Any("err", err))
		os.Exit(1)
	}
	if err := seedPhones(ctx, pool, *n, *batch); err != nil {
		logger.Error("seed phones", slog.Any("err", err))
		os.Exit(1)
	}

	if err := seedDomains(ctx, pool, *n, *batch, now); err != nil {
		logger.Error("seed domains", slog.Any("err", err))
		os.Exit(1)
	}
	if err := seedDomainNameservers(ctx, pool, *n, *batch); err != nil {
		logger.Error("seed domain_nameservers", slog.Any("err", err))
		os.Exit(1)
	}
	if err := seedDomainContacts(ctx, pool, *n, *batch); err != nil {
		logger.Error("seed domain_contacts", slog.Any("err", err))
		os.Exit(1)
	}

	logger.Info("seed complete",
		slog.Int("domains", *n),
		slog.Int("entities", *n),
		slog.Int("nameservers", nsCount),
		slog.Duration("total", time.Since(start)))
}

func truncateTables(ctx context.Context, pool *pgxpool.Pool) error {
	tables := []string{
		"domain_contacts", "domain_nameservers",
		"entity_emails", "entity_phones",
		"domains", "nameservers", "entities",
	}
	for _, t := range tables {
		if _, err := pool.Exec(ctx, "TRUNCATE TABLE "+t+" CASCADE"); err != nil {
			return err
		}
	}
	return nil
}

func seedEntities(ctx context.Context, pool *pgxpool.Pool, n, batch int, now time.Time) error {
	cols := []string{"handle", "kind", "full_name", "organization", "title",
		"country_code", "locality", "region", "postal_code", "street",
		"created_at", "updated_at", "extras"}
	rows := make([][]any, 0, batch)
	for i := 0; i < n; i++ {
		rows = append(rows, []any{
			synth.EntityHandle(i),
			synth.Kinds(i),
			synth.EntityFullName(i),
			synth.EntityOrganization(i),
			"",
			synth.EntityCountry(i),
			synth.EntityLocality(i),
			"",
			synth.EntityPostalCode(i),
			[]string{synth.EntityStreet(i)},
			now,
			now,
			[]byte("{}"),
		})
		if len(rows) >= batch {
			if err := flush(ctx, pool, "entities", cols, rows); err != nil {
				return err
			}
			rows = rows[:0]
		}
	}
	return flush(ctx, pool, "entities", cols, rows)
}

func seedEmails(ctx context.Context, pool *pgxpool.Pool, n, batch int) error {
	cols := []string{"entity_handle", "email"}
	rows := make([][]any, 0, batch)
	for i := 0; i < n; i++ {
		rows = append(rows, []any{synth.EntityHandle(i), synth.EntityEmail(i)})
		if len(rows) >= batch {
			if err := flush(ctx, pool, "entity_emails", cols, rows); err != nil {
				return err
			}
			rows = rows[:0]
		}
	}
	return flush(ctx, pool, "entity_emails", cols, rows)
}

func seedPhones(ctx context.Context, pool *pgxpool.Pool, n, batch int) error {
	cols := []string{"entity_handle", "number", "kinds"}
	rows := make([][]any, 0, batch)
	for i := 0; i < n; i++ {
		rows = append(rows, []any{synth.EntityHandle(i), synth.EntityPhone(i), []string{"voice"}})
		if len(rows) >= batch {
			if err := flush(ctx, pool, "entity_phones", cols, rows); err != nil {
				return err
			}
			rows = rows[:0]
		}
	}
	return flush(ctx, pool, "entity_phones", cols, rows)
}

func seedNameservers(ctx context.Context, pool *pgxpool.Pool, n, batch int) error {
	cols := []string{"handle", "ldh_name", "unicode_name", "ipv4", "ipv6"}
	rows := make([][]any, 0, batch)
	for i := 0; i < n; i++ {
		rows = append(rows, []any{
			synth.NameserverHandle(i),
			synth.NameserverName(i),
			"",
			[]string{},
			[]string{},
		})
		if len(rows) >= batch {
			if err := flush(ctx, pool, "nameservers", cols, rows); err != nil {
				return err
			}
			rows = rows[:0]
		}
	}
	return flush(ctx, pool, "nameservers", cols, rows)
}

func seedDomains(ctx context.Context, pool *pgxpool.Pool, n, batch int, now time.Time) error {
	cols := []string{"handle", "ldh_name", "unicode_name", "status",
		"registered_at", "expires_at", "last_changed", "last_rdap_update"}
	rows := make([][]any, 0, batch)
	expires := now.AddDate(1, 0, 0)
	for i := 0; i < n; i++ {
		rows = append(rows, []any{
			synth.DomainHandle(i),
			synth.DomainName(i),
			synth.DomainName(i),
			[]string{"active"},
			now,
			expires,
			now,
			now,
		})
		if len(rows) >= batch {
			if err := flush(ctx, pool, "domains", cols, rows); err != nil {
				return err
			}
			rows = rows[:0]
		}
	}
	if err := flush(ctx, pool, "domains", cols, rows); err != nil {
		return err
	}
	// Real IDN fixtures: exercises the storage round-trip for non-
	// ASCII names. Without them, an IDN scan bug in the provider
	// would not surface in either tests or stress runs.
	idnRows := make([][]any, 0, len(synth.IDNFixtures))
	for _, idn := range synth.IDNFixtures {
		idnRows = append(idnRows, []any{
			idn.Handle, idn.LDH, idn.Unicode,
			[]string{"active"}, now, expires, now, now,
		})
	}
	return flush(ctx, pool, "domains", cols, idnRows)
}

func seedDomainNameservers(ctx context.Context, pool *pgxpool.Pool, n, batch int) error {
	cols := []string{"domain_handle", "nameserver_handle"}
	rows := make([][]any, 0, batch)
	for i := 0; i < n; i++ {
		rows = append(rows, []any{
			synth.DomainHandle(i),
			synth.NameserverHandle(synth.NameserverForDomain(i)),
		})
		if len(rows) >= batch {
			if err := flush(ctx, pool, "domain_nameservers", cols, rows); err != nil {
				return err
			}
			rows = rows[:0]
		}
	}
	return flush(ctx, pool, "domain_nameservers", cols, rows)
}

func seedDomainContacts(ctx context.Context, pool *pgxpool.Pool, n, batch int) error {
	cols := []string{"domain_handle", "entity_handle", "role"}
	rows := make([][]any, 0, batch)
	for i := 0; i < n; i++ {
		rows = append(rows, []any{
			synth.DomainHandle(i),
			synth.EntityHandle(synth.EntityForDomain(i)),
			"registrant",
		})
		if len(rows) >= batch {
			if err := flush(ctx, pool, "domain_contacts", cols, rows); err != nil {
				return err
			}
			rows = rows[:0]
		}
	}
	return flush(ctx, pool, "domain_contacts", cols, rows)
}

func flush(ctx context.Context, pool *pgxpool.Pool, table string, cols []string, rows [][]any) error {
	if len(rows) == 0 {
		return nil
	}
	_, err := pool.CopyFrom(ctx, pgx.Identifier{table}, cols, pgx.CopyFromRows(rows))
	return err
}
