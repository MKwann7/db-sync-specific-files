package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"time"
)

// runRunCommand starts the continuous synchronization service: it performs
// an initial reconciliation cycle immediately, then runs further cycles on
// the configured interval until SIGINT/SIGTERM is received, at which point
// it finishes any in-flight cycle and exits cleanly.
func runRunCommand(args []string) error {
	cfg, logger, engine, db, err := setup()
	if err != nil {
		return err
	}
	defer db.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	logger.Info("dbsync starting", "version", version, "mappings", len(cfg.Sync.Files), "interval", cfg.Sync.Interval, "dry_run", cfg.Sync.DryRun)

	runCycle := func() {
		report := engine.RunOnce(ctx)
		if report.HasErrors() {
			for _, res := range report.Results {
				if res.Err != nil {
					logger.Error("mapping synchronization failed", "mapping", res.Mapping.Name, "error", res.Err)
				}
			}
		}
		logger.Info("synchronization cycle complete", "mappings", len(report.Results))
	}

	runCycle()

	ticker := time.NewTicker(cfg.Sync.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			logger.Info("shutdown signal received, exiting gracefully")
			return nil
		case <-ticker.C:
			runCycle()
		}
	}
}
