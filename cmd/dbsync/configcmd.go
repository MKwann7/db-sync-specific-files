package main

import (
	"fmt"

	"github.com/MKwann7/db-sync-specific-files/internal/config"
)

func runConfigCommand(args []string) error {
	if len(args) == 0 || args[0] != "validate" {
		return fmt.Errorf("usage: dbsync config validate")
	}
	return runConfigValidate()
}

// runConfigValidate validates the configuration file without making any
// network requests, as required by `dbsync config validate`.
func runConfigValidate() error {
	path := configPath()
	cfg, err := config.Load(path)
	if err != nil {
		return fmt.Errorf("loading %s: %w", path, err)
	}
	cfg.ConfigPath = path

	if err := cfg.Validate(config.ValidateOptions{RequireAllowedRoots: true}); err != nil {
		return err
	}

	fmt.Printf("Configuration %s is valid: %d file mapping(s) configured.\n", path, len(cfg.Sync.Files))
	for _, f := range cfg.Sync.Files {
		fmt.Printf("  - %-20s %-14s local=%-40s remote=%s\n", f.Name, f.Direction, f.Local, f.Remote)
	}
	return nil
}
