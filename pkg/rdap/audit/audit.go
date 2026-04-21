// Package audit is the NIS2 Article 28 audit-trail hook. EU TLD
// registries are required to log each RDAP access with requester
// identity (or pseudonym), declared purpose, legal basis, data scope
// released, and response time. The hook is deliberately narrow — we
// capture the metadata the regulation asks for and leave the storage /
// retention decisions to the operator.
//
// Typical deployment:
//
//	logger := audit.NewSlog(slog.New(file.NewJSONHandler(auditFile, nil)))
//	srv := &handlers.Server{ ..., Audit: logger }
//
// The slog-backed implementation writes one structured record per
// access; shipping to an append-only sink (WORM storage, syslog-ng with
// integrity, etc.) is a downstream concern.
package audit

import (
	"context"
	"log/slog"
	"time"
)

// Event describes a single RDAP access worth auditing.
type Event struct {
	Time       time.Time
	Method     string // GET / HEAD
	Path       string // /domain/example.nl
	Object     string // domain|entity|nameserver|ip
	Subject    string // auth.Claims.Subject, or "" for anonymous
	AccessTier string // anonymous|authenticated|privileged
	RemoteIP   string
	Status     int
	Duration   time.Duration
}

// Logger is the minimal sink. Implementations MUST be safe for
// concurrent use and SHOULD never block the request path — buffer +
// background flush when the sink is slow.
type Logger interface {
	Log(ctx context.Context, e Event)
}

// Noop discards events. Use when NIS2 compliance is out of scope or
// when the operator handles auditing in an HTTP proxy.
type Noop struct{}

func (Noop) Log(context.Context, Event) {}

// Slog is an audit Logger backed by a dedicated *slog.Logger. Use a
// separate logger (and sink) from the operational access log — audit
// records have different retention and integrity requirements.
type Slog struct {
	Logger *slog.Logger
}

func NewSlog(l *slog.Logger) *Slog { return &Slog{Logger: l} }

func (s *Slog) Log(ctx context.Context, e Event) {
	s.Logger.LogAttrs(ctx, slog.LevelInfo, "rdap.audit",
		slog.Time("time", e.Time),
		slog.String("method", e.Method),
		slog.String("path", e.Path),
		slog.String("object", e.Object),
		slog.String("subject", e.Subject),
		slog.String("tier", e.AccessTier),
		slog.String("remote_ip", e.RemoteIP),
		slog.Int("status", e.Status),
		slog.Duration("duration", e.Duration),
	)
}
