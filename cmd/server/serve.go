package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nebulactrl/nebulactrl/internal/app"
	"github.com/nebulactrl/nebulactrl/internal/config"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/internal/secretbox/rekey"
	"github.com/nebulactrl/nebulactrl/internal/upgrade"
	ncversion "github.com/nebulactrl/nebulactrl/internal/version"
)

const shutdownTimeout = 30 * time.Second

func serve(ctx context.Context) error {
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		return fmt.Errorf("configuration: %w", err)
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))
	slog.SetDefault(logger)
	if err := hardenProcess(); err != nil {
		logger.Warn("process hardening (no core dumps, not dumpable) failed", "error", err)
	}

	pool, err := connect(ctx, cfg.DatabaseURL, logger)
	if err != nil {
		return err
	}
	defer pool.Close()

	// A changed version is an upgrade: back the database up before the
	// migrations touch it, and stop here when that failed.
	ncversion.SetCurrent(version)
	decision, err := preflightUpgrade(ctx, cfg, pool, logger)
	if err != nil {
		return err
	}
	if err := db.Migrate(ctx, pool, logger); err != nil {
		return err
	}
	if err := upgrade.Commit(ctx, pool, decision, version); err != nil {
		logger.Warn("record the upgrade", "error", err)
	}
	if _, err := app.EnsureSetupToken(ctx, cfg, pool, logger); err != nil {
		return err
	}
	a, err := app.Build(cfg, pool, logger)
	// The ring holds the keys now; do not keep a second copy of the key bytes.
	secretbox.Wipe(cfg.SecretKey)
	for _, k := range cfg.PreviousSecretKeys {
		secretbox.Wipe(k)
	}
	if err != nil {
		return err
	}
	go logKeyRing(ctx, logger, pool, a.Box)
	defer a.Close()
	if err := a.Jobs.Migrate(ctx); err != nil {
		return err
	}
	if err := a.Start(ctx); err != nil {
		return fmt.Errorf("start jobs: %w", err)
	}

	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           a.Handler,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		// No Read/WriteTimeout: SSE and websocket connections are long-lived.
	}
	errCh := make(chan error, 2)
	go func() {
		logger.Info("listening", "addr", cfg.Listen, "public_url", cfg.PublicURL)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- fmt.Errorf("http server: %w", err)
		}
	}()

	// The status listener serves published status pages by Host and nothing
	// else; the control-plane host's proxy routes their hostnames here. It is a
	// separate server so a status hostname can never reach the dashboard, the
	// API or the agent endpoints, and it stays on the internal network (the
	// compose file never publishes its port).
	var statusSrv *http.Server
	if a.Status != nil {
		statusSrv = &http.Server{
			Addr:              cfg.StatusListen,
			Handler:           a.Status,
			ReadHeaderTimeout: 5 * time.Second,
			ReadTimeout:       10 * time.Second,
			WriteTimeout:      30 * time.Second,
			IdleTimeout:       60 * time.Second,
			MaxHeaderBytes:    16 << 10,
		}
		go func() {
			logger.Info("status listener", "addr", cfg.StatusListen)
			if err := statusSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				errCh <- fmt.Errorf("status server: %w", err)
			}
		}()
	}

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}
	logger.Info("shutting down")
	shutCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
	defer cancel()
	var errs []error
	if err := srv.Shutdown(shutCtx); err != nil {
		errs = append(errs, fmt.Errorf("http shutdown: %w", err))
	}
	if statusSrv != nil {
		if err := statusSrv.Shutdown(shutCtx); err != nil {
			errs = append(errs, fmt.Errorf("status shutdown: %w", err))
		}
	}
	if err := a.Jobs.Stop(shutCtx); err != nil {
		errs = append(errs, fmt.Errorf("jobs stop: %w", err))
	}
	return errors.Join(errs...)
}

// logKeyRing logs the key ring and, when values are not on the current key,
// tells the operator to run rekey (docs/security/secrets-at-rest.md).
func logKeyRing(ctx context.Context, logger *slog.Logger, pool *pgxpool.Pool, box *secretbox.Box) {
	if box == nil {
		return
	}
	logger.InfoContext(ctx, "secret key ring", "current_key_id", box.KeyID(), "keys", len(box.KeyIDs()))
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	rep, err := rekey.Status(ctx, pool, box, rekey.Options{})
	if err != nil {
		logger.WarnContext(ctx, "could not count sealed values per key", "error", err)
		return
	}
	if n := rep.Pending(); n > 0 {
		legacy := 0
		for _, c := range rep.Columns {
			legacy += c.Legacy
		}
		logger.WarnContext(ctx, "sealed values are not on the current key or format; run `nebulactrl-server rekey`", "values", n, "legacy_v1", legacy, "current_key_id", box.KeyID())
	}
}

// connect opens the pool and waits briefly for the database to accept
// connections (compose starts postgres and the server together).
func connect(ctx context.Context, url string, logger *slog.Logger) (*pgxpool.Pool, error) {
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("database pool: %w", err)
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		pingCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		err = pool.Ping(pingCtx)
		cancel()
		if err == nil {
			return pool, nil
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			pool.Close()
			return nil, fmt.Errorf("database unreachable: %w", err)
		}
		logger.Warn("waiting for database", "error", err)
		select {
		case <-time.After(time.Second):
		case <-ctx.Done():
			pool.Close()
			return nil, ctx.Err()
		}
	}
}
