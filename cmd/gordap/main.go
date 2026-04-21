package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/bramheerink/gordap/internal/config"
	"github.com/bramheerink/gordap/pkg/rdap/auth"
	"github.com/bramheerink/gordap/pkg/rdap/auth/jwks"
	"github.com/bramheerink/gordap/pkg/rdap/bootstrap"
	"github.com/bramheerink/gordap/pkg/rdap/cache"
	"github.com/bramheerink/gordap/pkg/rdap/datasource"
	"github.com/bramheerink/gordap/pkg/rdap/handlers"
	"github.com/bramheerink/gordap/pkg/rdap/middleware"
	"github.com/bramheerink/gordap/pkg/rdap/observability"
	"github.com/bramheerink/gordap/pkg/rdap/profile"
	"github.com/bramheerink/gordap/pkg/rdap/search"
	"github.com/bramheerink/gordap/pkg/rdap/storage/memory"
	pgstore "github.com/bramheerink/gordap/pkg/rdap/storage/postgres"
	"github.com/bramheerink/gordap/pkg/rdap/types"
	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	var (
		cfgPath = flag.String("config", "", "optional YAML config file; flag values win over file values")

		addr            = flag.String("addr", ":8080", "listen address")
		dsn             = flag.String("database-url", os.Getenv("DATABASE_URL"), "PostgreSQL connection string; empty = memory demo mode")
		demo            = flag.Bool("demo", false, "force memory demo backend")
		selfLinkBase    = flag.String("self-link-base", os.Getenv("GORDAP_SELF_LINK_BASE"), "public canonical URL for rel=self links")
		icannGTLD       = flag.Bool("icann-gtld", false, "enable ICANN RP2.2 / TIG v2.2 conformance preset")
		tosURL          = flag.String("tos-url", "", "Terms of Service URL (required when --icann-gtld)")
		jwksURL         = flag.String("jwks-url", "", "OIDC JWKS endpoint; empty = no bearer verification")
		jwksIssuer      = flag.String("jwks-issuer", "", "expected issuer (empty = don't check)")
		jwksAudience    = flag.String("jwks-audience", "", "expected audience (empty = don't check)")
		enableBootstrap = flag.Bool("bootstrap", false, "consult IANA bootstrap for redirects")
		bootstrapEvery  = flag.Duration("bootstrap-refresh", 24*time.Hour, "bootstrap refresh interval")
		readHeaderTimeo = flag.Duration("read-header-timeout", 5*time.Second, "header read timeout (slowloris guard)")
		readTimeo       = flag.Duration("read-timeout", 15*time.Second, "full request read timeout")
		writeTimeo      = flag.Duration("write-timeout", 30*time.Second, "response write timeout")
		idleTimeo       = flag.Duration("idle-timeout", 120*time.Second, "keepalive idle timeout")
		shutdownTimeo   = flag.Duration("shutdown-timeout", 15*time.Second, "graceful shutdown window")
		requestTimeout  = flag.Duration("request-timeout", 10*time.Second, "per-request context timeout")
		maxBodyBytes    = flag.Int64("max-body-bytes", 4<<10, "max request body")
		rateLimitRPS    = flag.Float64("rate-limit-rps", 50, "per-client-IP sustained rate (0 disables)")
		rateLimitBurst  = flag.Float64("rate-limit-burst", 100, "per-client-IP burst capacity")
		gzipMinSize     = flag.Int("gzip-min-size", 128, "compress responses larger than N bytes")
		cacheSize       = flag.Int("cache-size", 50_000, "record LRU capacity per object class (0 disables)")
		cacheTTL        = flag.Duration("cache-ttl", 60*time.Second, "record LRU TTL for positive results")
		cacheNegTTL     = flag.Duration("cache-neg-ttl", 5*time.Second, "record LRU TTL for NotFound results")
		respCacheSize   = flag.Int("response-cache-size", 50_000, "response cache capacity per tier (PII-safe; 0 disables)")
		respCacheTTL    = flag.Duration("response-cache-ttl", 60*time.Second, "response cache TTL")
		tlsCert         = flag.String("tls-cert", "", "TLS certificate file (enables HTTPS)")
		tlsKey          = flag.String("tls-key", "", "TLS private key file")
	)
	flag.Parse()

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		slog.Error("load config failed", slog.Any("err", err))
		os.Exit(2)
	}
	applyYAMLDefaults(cfg, addr, dsn, selfLinkBase, icannGTLD, tosURL,
		jwksURL, jwksIssuer, jwksAudience, cacheSize, cacheTTL, cacheNegTTL,
		rateLimitRPS, rateLimitBurst, gzipMinSize, maxBodyBytes,
		tlsCert, tlsKey, requestTimeout, readHeaderTimeo, shutdownTimeo,
		enableBootstrap, bootstrapEvery, demo)

	logger := observability.NewLogger()
	slog.SetDefault(logger)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	ds, closer, err := buildDataSource(ctx, logger, *demo, *dsn)
	if err != nil {
		logger.Error("datasource init failed", slog.Any("err", err))
		os.Exit(1)
	}
	defer closer()

	searchIndex, _ := ds.(search.Index)

	if *cacheSize > 0 && *cacheTTL > 0 {
		ds = cache.New(ds, cache.Config{Size: *cacheSize, TTL: *cacheTTL, NegTTL: *cacheNegTTL})
		logger.Info("cache enabled", slog.Int("size", *cacheSize), slog.Duration("ttl", *cacheTTL))
	}

	server := &handlers.Server{
		DS:           ds,
		Search:       searchIndex,
		Logger:       logger,
		SelfLinkBase: *selfLinkBase,
	}
	if *respCacheSize > 0 && *respCacheTTL > 0 {
		server.ResponseCache = cache.NewResponseCache(*respCacheSize, *respCacheTTL)
		logger.Info("response cache enabled",
			slog.Int("size", *respCacheSize), slog.Duration("ttl", *respCacheTTL))
	}
	applyProfile(server, *icannGTLD, *tosURL, cfg.Profile.ExtraNotices)
	if *icannGTLD {
		// ICANN conformance tool still expects jCard on every entity.
		// The JSContact card is emitted alongside for forward-compat.
		server.EmitJCard = true
		server.RedactionReason = "Data minimization per GDPR Art. 5(1)(c)"
	}

	if *enableBootstrap {
		reg := bootstrap.New(nil)
		if err := reg.Refresh(ctx); err != nil {
			logger.Warn("bootstrap initial refresh failed", slog.Any("err", err))
		} else {
			server.Bootstrap = reg
			go refreshLoop(ctx, reg, *bootstrapEvery, logger)
		}
	}

	verifier := buildVerifier(logger, *jwksURL, *jwksIssuer, *jwksAudience, cfg.Auth.ScopeMap)

	router := handlers.NewRouter(server, verifier)

	var handler http.Handler = router
	handler = middleware.Gzip(*gzipMinSize)(handler)
	handler = middleware.RequestTimeout(*requestTimeout)(handler)
	handler = middleware.MaxRequestBody(*maxBodyBytes)(handler)
	if *rateLimitRPS > 0 {
		rl := middleware.NewRateLimiter(*rateLimitRPS, *rateLimitBurst)
		handler = rl.Middleware(middleware.ClientIP)(handler)
		go rateLimitCleanup(ctx, rl)
	}
	handler = middleware.CORS()(handler)
	handler = middleware.SecurityHeaders()(handler)
	handler = observability.AccessLog(logger)(handler)

	srv := &http.Server{
		Addr:              *addr,
		Handler:           handler,
		ReadHeaderTimeout: *readHeaderTimeo,
		ReadTimeout:       *readTimeo,
		WriteTimeout:      *writeTimeo,
		IdleTimeout:       *idleTimeo,
	}

	go func() {
		logger.Info("rdap server starting",
			slog.String("addr", *addr), slog.Bool("tls", *tlsCert != ""),
			slog.Bool("icann_gtld", *icannGTLD), slog.Bool("jwks", *jwksURL != ""))
		var err error
		if *tlsCert != "" {
			err = srv.ListenAndServeTLS(*tlsCert, *tlsKey)
		} else {
			err = srv.ListenAndServe()
		}
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("listen failed", slog.Any("err", err))
			stop()
		}
	}()

	<-ctx.Done()
	logger.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), *shutdownTimeo)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("shutdown failed", slog.Any("err", err))
		os.Exit(1)
	}
}

// applyYAMLDefaults merges YAML values into flag pointers where the
// corresponding flag was not explicitly set on the command line. This
// lets operators ship a full YAML file and still override any single
// value via a flag.
func applyYAMLDefaults(cfg *config.Config, addr, dsn, self *string,
	icann *bool, tos, jwksURL, jwksIssuer, jwksAud *string,
	cSize *int, cTTL *time.Duration, cNegTTL *time.Duration,
	rRPS, rBurst *float64, gz *int, maxBody *int64,
	cert, key *string, reqTimo, hdrTimo, shutTimo *time.Duration,
	boot *bool, bootEvery *time.Duration, demo *bool) {
	set := map[string]bool{}
	flag.Visit(func(f *flag.Flag) { set[f.Name] = true })

	if !set["addr"] && cfg.Addr != "" {
		*addr = cfg.Addr
	}
	if !set["database-url"] && cfg.DatabaseURL != "" {
		*dsn = cfg.DatabaseURL
	}
	if !set["self-link-base"] && cfg.SelfLinkBase != "" {
		*self = cfg.SelfLinkBase
	}
	if !set["icann-gtld"] && cfg.Profile.ICANNgTLD {
		*icann = true
	}
	if !set["tos-url"] && cfg.Profile.ToSURL != "" {
		*tos = cfg.Profile.ToSURL
	}
	if !set["jwks-url"] && cfg.Auth.JWKSURL != "" {
		*jwksURL = cfg.Auth.JWKSURL
	}
	if !set["jwks-issuer"] && cfg.Auth.Issuer != "" {
		*jwksIssuer = cfg.Auth.Issuer
	}
	if !set["jwks-audience"] && cfg.Auth.Audience != "" {
		*jwksAud = cfg.Auth.Audience
	}
	if !set["cache-size"] && cfg.CacheSize != nil {
		*cSize = *cfg.CacheSize
	}
	if !set["cache-ttl"] && cfg.CacheTTL > 0 {
		*cTTL = cfg.CacheTTL
	}
	if !set["cache-neg-ttl"] && cfg.CacheNegTTL > 0 {
		*cNegTTL = cfg.CacheNegTTL
	}
	if !set["rate-limit-rps"] && cfg.RateLimitRPS != nil {
		*rRPS = *cfg.RateLimitRPS
	}
	if !set["rate-limit-burst"] && cfg.RateLimitBurst != nil {
		*rBurst = *cfg.RateLimitBurst
	}
	if !set["gzip-min-size"] && cfg.GzipMinSize != nil {
		*gz = *cfg.GzipMinSize
	}
	if !set["max-body-bytes"] && cfg.MaxBodyBytes != nil {
		*maxBody = *cfg.MaxBodyBytes
	}
	if !set["tls-cert"] && cfg.TLSCert != "" {
		*cert = cfg.TLSCert
	}
	if !set["tls-key"] && cfg.TLSKey != "" {
		*key = cfg.TLSKey
	}
	if !set["request-timeout"] && cfg.RequestTimeout > 0 {
		*reqTimo = cfg.RequestTimeout
	}
	if !set["read-header-timeout"] && cfg.ReadHeaderTimo > 0 {
		*hdrTimo = cfg.ReadHeaderTimo
	}
	if !set["shutdown-timeout"] && cfg.ShutdownTimo > 0 {
		*shutTimo = cfg.ShutdownTimo
	}
	if !set["bootstrap"] && cfg.EnableBootstrap != nil {
		*boot = *cfg.EnableBootstrap
	}
	if !set["bootstrap-refresh"] && cfg.BootstrapEvery > 0 {
		*bootEvery = cfg.BootstrapEvery
	}
	if !set["demo"] && cfg.Demo != nil {
		*demo = *cfg.Demo
	}
}

// applyProfile wires the deployment profile into the server: ICANN
// notices + conformance identifiers when --icann-gtld, plus any extra
// notices declared in YAML.
func applyProfile(s *handlers.Server, icann bool, tosURL string, extraNotices []config.NoticeConfig) {
	if icann {
		s.ExtraConformance = profile.ICANNgTLDConformance()
		s.Notices = profile.ICANNgTLDNotices(tosURL)
	}
	for _, n := range extraNotices {
		s.Notices = append(s.Notices, types.Notice{Title: n.Title, Description: n.Description})
	}
}

func buildVerifier(logger *slog.Logger, jwksURL, iss, aud string, scopeMap map[string]string) auth.Verifier {
	if jwksURL == "" {
		return auth.NopVerifier()
	}
	tierMap := map[string]auth.AccessLevel{}
	for scope, tier := range scopeMap {
		switch tier {
		case "privileged":
			tierMap[scope] = auth.AccessPrivileged
		case "authenticated":
			tierMap[scope] = auth.AccessAuthenticated
		}
	}
	logger.Info("OIDC verifier enabled",
		slog.String("jwks_url", jwksURL),
		slog.String("issuer", iss),
		slog.Int("scope_mappings", len(tierMap)))
	return jwks.New(jwks.Config{
		JWKSURL: jwksURL, Issuer: iss, Audience: aud, ScopeMap: tierMap,
	})
}

func buildDataSource(ctx context.Context, logger *slog.Logger, demo bool, dsn string) (datasource.DataSource, func(), error) {
	if demo || dsn == "" {
		store := memory.New()
		memory.Seed(store)
		logger.Warn("demo mode: in-memory seed data (do not use in production)",
			slog.String("seed", "example.nl, bücher.example"))
		return store, func() {}, nil
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, func() {}, err
	}
	return pgstore.New(pool), pool.Close, nil
}

func refreshLoop(ctx context.Context, r *bootstrap.Registry, every time.Duration, log *slog.Logger) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := r.Refresh(ctx); err != nil {
				log.Warn("bootstrap refresh failed", slog.Any("err", err))
			}
		}
	}
}

func rateLimitCleanup(ctx context.Context, rl *middleware.RateLimiter) {
	t := time.NewTicker(2 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			rl.CleanupIdle()
		}
	}
}
