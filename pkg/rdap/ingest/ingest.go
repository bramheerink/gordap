// Package ingest declares the contract operators use to *write* data
// into gordap. The public HTTP surface is strictly read-only — RDAP
// (RFC 7480) has no write semantics — so writes must come from a
// dedicated ingestion path owned by the operator.
//
// Big deployments typically fall into one of three patterns:
//
//	A. Same-DB: gordap points pgx at the same PostgreSQL the operator's
//	   EPP/registry platform already writes to. No ingest code runs on
//	   the gordap side. Simplest, tightest coupling.
//
//	B. CDC: the operator streams changes (Debezium, Kafka, Pub/Sub) into
//	   a gordap-owned PG. A worker in their pipeline calls Ingester to
//	   apply each event.
//
//	C. Push API: the operator POSTs upserts/deletes to a private HTTP
//	   endpoint (mTLS-gated or bound to localhost) that dispatches to
//	   this Ingester. Good fit for smaller operators.
//
// The core ship stays read-only. Anyone enabling pattern B or C mounts
// their own handler wired to an Ingester implementation.
package ingest

import (
	"context"

	"github.com/bramheerink/gordap/pkg/rdap/datasource"
)

// Ingester mutates the authoritative record set. Methods are idempotent
// — an Upsert of the same object twice leaves the store unchanged, a
// Delete of an absent handle is not an error. This matches what CDC
// pipelines expect.
type Ingester interface {
	UpsertDomain(ctx context.Context, d *datasource.Domain) error
	DeleteDomain(ctx context.Context, handle string) error

	UpsertEntity(ctx context.Context, c *datasource.Contact) error
	DeleteEntity(ctx context.Context, handle string) error

	UpsertNameserver(ctx context.Context, n *datasource.Nameserver) error
	DeleteNameserver(ctx context.Context, handle string) error

	UpsertIPNetwork(ctx context.Context, n *datasource.IPNetwork) error
	DeleteIPNetwork(ctx context.Context, handle string) error
}
