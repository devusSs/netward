// Package app builds netward's components from its configuration and runs
// them: every enabled feature on its own interval, the cleanup of old
// snapshots and the web interface.
package app

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/devusSs/netward/internal/adapter/gateway"
	"github.com/devusSs/netward/internal/adapter/iplookup"
	"github.com/devusSs/netward/internal/adapter/netif"
	"github.com/devusSs/netward/internal/adapter/ping"
	"github.com/devusSs/netward/internal/adapter/resolver"
	"github.com/devusSs/netward/internal/adapter/sqlite"
	"github.com/devusSs/netward/internal/adapter/tlscert"
	"github.com/devusSs/netward/internal/config"
	"github.com/devusSs/netward/internal/service/domainip"
	"github.com/devusSs/netward/internal/service/history"
	"github.com/devusSs/netward/internal/service/privatenet"
	"github.com/devusSs/netward/internal/service/publicnet"
	"github.com/devusSs/netward/internal/web"
)

// cleanupInterval is how often snapshots older than the retention are
// deleted.
const cleanupInterval = time.Hour

// job is work that runs right away and then every interval.
type job struct {
	name     string
	interval time.Duration
	run      func(ctx context.Context) error
}

// Run opens the database, starts all enabled components and blocks until ctx
// ends or a component fails to start, for example because the web
// interface's port is taken. It returns nil after a clean shutdown.
func Run(ctx context.Context, cfg config.Config, logger *slog.Logger) error {
	store, err := sqlite.Open(ctx, cfg.Database, logger)
	if err != nil {
		return fmt.Errorf("database: %w", err)
	}

	err = run(ctx, cfg, store, logger)

	return errors.Join(err, store.Close())
}

func run(ctx context.Context, cfg config.Config, store *sqlite.Store, logger *slog.Logger) error {
	jobs, err := newJobs(cfg, store, logger)
	if err != nil {
		return err
	}

	var srv *web.Server

	if cfg.HTTP.Enabled {
		srv, err = newWeb(cfg, store, logger)
		if err != nil {
			return err
		}
	}

	logger.InfoContext(ctx, "netward started",
		slog.Bool("publicnet", cfg.PublicNet.Enabled),
		slog.Bool("privatenet", cfg.PrivateNet.Enabled),
		slog.Bool("domainip", cfg.DomainIP.Enabled),
		slog.Bool("web", cfg.HTTP.Enabled),
		slog.String("database", cfg.Database),
	)

	group, groupCtx := errgroup.WithContext(ctx)

	for _, j := range jobs {
		group.Go(func() error {
			runEvery(groupCtx, logger, j)

			return nil
		})
	}

	if srv != nil {
		group.Go(func() error { return srv.Run(groupCtx) })
	}

	err = group.Wait()

	logger.InfoContext(context.WithoutCancel(ctx), "netward stopped")

	return err //nolint:wrapcheck // The web server already wraps its errors.
}

// runEvery runs j right away and then every j.interval until ctx ends.
// Failures are logged; the next run happens as scheduled. A run that takes
// longer than the interval delays the next one instead of overlapping it.
func runEvery(ctx context.Context, logger *slog.Logger, j job) {
	ticker := time.NewTicker(j.interval)
	defer ticker.Stop()

	for {
		err := j.run(ctx)
		if err != nil && ctx.Err() == nil {
			logger.ErrorContext(ctx, "job failed", slog.String("job", j.name), slog.Any("error", err))
		}

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func newJobs(cfg config.Config, store *sqlite.Store, logger *slog.Logger) ([]job, error) {
	var (
		jobs   []job
		pinger *ping.Pinger
		err    error
	)

	needsPing := cfg.PublicNet.Enabled &&
		(cfg.PublicNet.Targets.IPv4.IsValid() || cfg.PublicNet.Targets.IPv6.IsValid()) ||
		cfg.PrivateNet.Enabled && cfg.PrivateNet.Gateways
	if needsPing {
		pinger, err = ping.New(ping.Config{
			Count:      cfg.Ping.Count,
			Interval:   cfg.Ping.Interval,
			Timeout:    cfg.Ping.Timeout,
			Privileged: cfg.Ping.Privileged,
		})
		if err != nil {
			return nil, fmt.Errorf("ping: %w", err)
		}
	}

	if cfg.PublicNet.Enabled {
		svc, svcErr := newPublicNet(cfg, store, pinger, logger)
		if svcErr != nil {
			return nil, svcErr
		}

		jobs = append(jobs, snapshotJob("publicnet", cfg.PublicNet.Interval, svc.TakeSnapshot))
	}

	if cfg.PrivateNet.Enabled {
		svc, svcErr := newPrivateNet(cfg, store, pinger, logger)
		if svcErr != nil {
			return nil, svcErr
		}

		jobs = append(jobs, snapshotJob("privatenet", cfg.PrivateNet.Interval, svc.TakeSnapshot))
	}

	if cfg.DomainIP.Enabled {
		svc, svcErr := newDomainIP(cfg, store, logger)
		if svcErr != nil {
			return nil, svcErr
		}

		jobs = append(jobs, snapshotJob("domainip", cfg.DomainIP.Interval, svc.TakeSnapshot))
	}

	if cfg.Retention > 0 {
		jobs = append(jobs, cleanupJob(cfg.Retention, store, logger))
	}

	return jobs, nil
}

func snapshotJob[S any](name string, interval time.Duration, take func(context.Context) (S, error)) job {
	return job{
		name:     name,
		interval: interval,
		run: func(ctx context.Context) error {
			_, err := take(ctx)

			return err
		},
	}
}

func cleanupJob(retention time.Duration, store *sqlite.Store, logger *slog.Logger) job {
	return job{
		name:     "cleanup",
		interval: cleanupInterval,
		run: func(ctx context.Context) error {
			before := time.Now().Add(-retention)

			deleted, err := store.DeleteSnapshotsBefore(ctx, before)
			if err != nil {
				return fmt.Errorf("delete old snapshots: %w", err)
			}

			if deleted > 0 {
				logger.InfoContext(ctx, "old snapshots deleted",
					slog.Int64("count", deleted),
					slog.Time("before", before),
				)
			}

			return nil
		},
	}
}

func newPublicNet(
	cfg config.Config,
	store *sqlite.Store,
	pinger *ping.Pinger,
	logger *slog.Logger,
) (*publicnet.Service, error) {
	svcCfg := publicnet.Config{
		IPv4Lookup: nil,
		IPv6Lookup: nil,
		Pinger:     nil,
		IPv4Target: cfg.PublicNet.Targets.IPv4,
		IPv6Target: cfg.PublicNet.Targets.IPv6,
		Repository: store,
		Logger:     logger,
		Now:        nil,
	}

	// Lookups and the pinger are only set when configured: a nil pointer in
	// the interface would not count as disabled.
	if endpoint := cfg.PublicNet.Lookup.IPv4Endpoint; endpoint != "" {
		lookup, err := iplookup.New(iplookup.FamilyIPv4, endpoint, cfg.PublicNet.Lookup.Timeout)
		if err != nil {
			return nil, fmt.Errorf("publicnet.lookup.ipv4_endpoint: %w", err)
		}

		svcCfg.IPv4Lookup = lookup
	}

	if endpoint := cfg.PublicNet.Lookup.IPv6Endpoint; endpoint != "" {
		lookup, err := iplookup.New(iplookup.FamilyIPv6, endpoint, cfg.PublicNet.Lookup.Timeout)
		if err != nil {
			return nil, fmt.Errorf("publicnet.lookup.ipv6_endpoint: %w", err)
		}

		svcCfg.IPv6Lookup = lookup
	}

	if pinger != nil {
		svcCfg.Pinger = pinger
	}

	svc, err := publicnet.New(svcCfg)
	if err != nil {
		return nil, fmt.Errorf("publicnet: %w", err)
	}

	return svc, nil
}

func newPrivateNet(
	cfg config.Config,
	store *sqlite.Store,
	pinger *ping.Pinger,
	logger *slog.Logger,
) (*privatenet.Service, error) {
	addrs, err := netif.New(cfg.PrivateNet.Interfaces.Include, cfg.PrivateNet.Interfaces.Exclude)
	if err != nil {
		return nil, fmt.Errorf("privatenet.interfaces: %w", err)
	}

	svcCfg := privatenet.Config{
		Addrs:      addrs,
		Gateways:   nil,
		Pinger:     nil,
		Repository: store,
		Logger:     logger,
		Now:        nil,
	}

	if cfg.PrivateNet.Gateways && pinger != nil {
		svcCfg.Gateways = gateway.New()
		svcCfg.Pinger = pinger
	}

	svc, err := privatenet.New(svcCfg)
	if err != nil {
		return nil, fmt.Errorf("privatenet: %w", err)
	}

	return svc, nil
}

func newDomainIP(cfg config.Config, store *sqlite.Store, logger *slog.Logger) (*domainip.Service, error) {
	r, err := resolver.New(cfg.DomainIP.Resolver, cfg.DomainIP.Timeout)
	if err != nil {
		return nil, fmt.Errorf("domainip.resolver: %w", err)
	}

	svc, err := domainip.New(domainip.Config{
		Resolver:   r,
		Domains:    cfg.DomainIP.Domains,
		Repository: store,
		Logger:     logger,
		Now:        nil,
	})
	if err != nil {
		return nil, fmt.Errorf("domainip: %w", err)
	}

	return svc, nil
}

func newWeb(cfg config.Config, store *sqlite.Store, logger *slog.Logger) (*web.Server, error) {
	var tlsConfig *tls.Config

	if cfg.HTTP.TLS.Enabled {
		certs, err := tlscert.New(tlscert.Config{
			CertFile: cfg.HTTP.TLS.Cert,
			KeyFile:  cfg.HTTP.TLS.Key,
			Hosts:    cfg.HTTP.TLS.Hosts,
			Logger:   logger,
		})
		if err != nil {
			return nil, fmt.Errorf("http.tls: %w", err)
		}

		tlsConfig = &tls.Config{GetCertificate: certs.GetCertificate, MinVersion: tls.VersionTLS12}
	}

	changes, err := history.New(store)
	if err != nil {
		return nil, fmt.Errorf("history: %w", err)
	}

	srv, err := web.New(web.Config{
		Address:   cfg.HTTP.Address,
		TLS:       tlsConfig,
		Snapshots: store,
		Changes:   changes,
		Features: web.Features{
			PublicNet:  cfg.PublicNet.Enabled,
			PrivateNet: cfg.PrivateNet.Enabled,
			DomainIP:   cfg.DomainIP.Enabled,
		},
		Logger:   logger,
		Now:      nil,
		Location: nil,
	})
	if err != nil {
		return nil, fmt.Errorf("web: %w", err)
	}

	return srv, nil
}
