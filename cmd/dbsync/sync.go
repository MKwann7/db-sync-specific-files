package main

import (
	"context"
	"flag"
	"fmt"
)

func runSyncCommand(args []string) error {
	fs := flag.NewFlagSet("sync", flag.ExitOnError)
	once := fs.Bool("once", false, "run a single reconciliation cycle and exit")
	dryRun := fs.Bool("dry-run", false, "inspect pending actions without modifying local files, remote files, or synchronization baselines")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if !*once && !*dryRun {
		// Default `dbsync sync` with no flags behaves like --once, the most
		// predictable one-shot behavior for scripting/cron usage.
		*once = true
	}

	cfg, logger, engine, db, err := setup()
	if err != nil {
		return err
	}
	defer db.Close()

	if *dryRun {
		cfg.Sync.DryRun = true
	}

	logger.Info("starting synchronization cycle", "dry_run", cfg.Sync.DryRun, "mappings", len(cfg.Sync.Files))
	report := engine.RunOnce(context.Background())

	for _, res := range report.Results {
		if res.Err != nil {
			fmt.Printf("FAIL    %-20s %s: %v\n", res.Mapping.Name, res.Action, res.Err)
			continue
		}
		fmt.Printf("%-7s %-20s %s\n", labelFor(string(res.Action), cfg.Sync.DryRun), res.Mapping.Name, res.Reason)
	}

	logger.Info("synchronization cycle complete", "errors", report.HasErrors())
	if report.HasErrors() {
		return fmt.Errorf("one or more mappings failed to synchronize; see output above")
	}
	return nil
}

func labelFor(action string, dryRun bool) string {
	if dryRun {
		return "DRYRUN"
	}
	switch action {
	case "none":
		return "OK"
	case "upload":
		return "UPLOAD"
	case "download":
		return "DOWNLOAD"
	case "conflict":
		return "CONFLICT"
	default:
		return "SKIP"
	}
}
