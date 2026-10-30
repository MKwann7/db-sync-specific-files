// Package config implements loading and validation of the YAML
// synchronization configuration used by dbsync.
package config

import (
	"bytes"
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// Direction describes which side of a mapping is authoritative.
type Direction string

const (
	DirectionUpload        Direction = "upload"
	DirectionDownload      Direction = "download"
	DirectionBidirectional Direction = "bidirectional"
)

// ConflictPolicy describes how simultaneous independent changes are resolved.
type ConflictPolicy string

const (
	ConflictKeepBoth     ConflictPolicy = "keep_both"
	ConflictPreferLocal  ConflictPolicy = "prefer_local"
	ConflictPreferRemote ConflictPolicy = "prefer_remote"
	ConflictSkip         ConflictPolicy = "skip"
)

// DeletePolicy describes how deletions are (not) propagated.
type DeletePolicy string

const (
	DeletePolicyIgnore DeletePolicy = "ignore"
)

// Defaults holds mapping-level defaults that individual files may override.
type Defaults struct {
	Direction      Direction      `yaml:"direction"`
	ConflictPolicy ConflictPolicy `yaml:"conflict_policy"`
	DeletePolicy   DeletePolicy   `yaml:"delete_policy"`
}

// FileMapping describes a single local<->remote file synchronization pair.
type FileMapping struct {
	Name           string         `yaml:"name"`
	Local          string         `yaml:"local"`
	Remote         string         `yaml:"remote"`
	Direction      Direction      `yaml:"direction"`
	ConflictPolicy ConflictPolicy `yaml:"conflict_policy"`
	DeletePolicy   DeletePolicy   `yaml:"delete_policy"`

	// ResolvedLocal is the absolute, symlink-evaluated local path. Populated
	// during validation.
	ResolvedLocal string `yaml:"-"`
}

// ID returns a stable identifier for a mapping, derived from its remote path
// (which is guaranteed unique after validation).
func (f FileMapping) ID() string {
	return f.Remote
}

// SyncSettings holds the top level synchronization engine configuration.
type SyncSettings struct {
	Interval time.Duration `yaml:"interval"`
	DryRun   bool          `yaml:"dry_run"`
	Defaults Defaults      `yaml:"defaults"`
	Files    []FileMapping `yaml:"files"`
}

// LoggingSettings configures structured logging output.
type LoggingSettings struct {
	Level  string `yaml:"level"`
	Format string `yaml:"format"`
}

// SecuritySettings restricts which local directories mappings may use.
type SecuritySettings struct {
	// AllowedRoots is a list of local directory prefixes that all mapping
	// "local" paths must resolve within. If empty, the parent directories of
	// the state/config files are used as implicit denies only (no allow-list
	// enforcement is skipped - at least one root must be supplied for
	// validation to pass unless explicitly disabled).
	AllowedRoots []string `yaml:"allowed_roots"`
}

// Config is the root YAML configuration document.
type Config struct {
	Sync     SyncSettings     `yaml:"sync"`
	Logging  LoggingSettings  `yaml:"logging"`
	Security SecuritySettings `yaml:"security"`

	// StatePath is the filesystem location of the SQLite state database.
	// Defaults to /app/state/dbsync.db but may be overridden.
	StatePath string `yaml:"state_path"`

	// ConfigPath records where this configuration was loaded from, used for
	// self-overwrite protection. Not read from YAML.
	ConfigPath string `yaml:"-"`
}

// rawDuration lets us accept either YAML duration strings (e.g. "60s") or
// bare integers (seconds) for the sync interval.
type rawSync struct {
	Interval string        `yaml:"interval"`
	DryRun   bool          `yaml:"dry_run"`
	Defaults Defaults      `yaml:"defaults"`
	Files    []FileMapping `yaml:"files"`
}

type rawConfig struct {
	Sync      rawSync          `yaml:"sync"`
	Logging   LoggingSettings  `yaml:"logging"`
	Security  SecuritySettings `yaml:"security"`
	StatePath string           `yaml:"state_path"`
}

// Load reads and parses a YAML configuration file from path. It applies
// defaults but does not perform semantic validation; call Validate
// separately.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config file %q: %w", path, err)
	}
	return Parse(data, path)
}

// Parse parses YAML bytes into a Config. sourcePath is recorded for
// diagnostic / self-overwrite checks and may be empty.
func Parse(data []byte, sourcePath string) (*Config, error) {
	var raw rawConfig
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&raw); err != nil {
		return nil, fmt.Errorf("parsing YAML: %w", err)
	}

	cfg := &Config{
		Logging:    raw.Logging,
		Security:   raw.Security,
		StatePath:  raw.StatePath,
		ConfigPath: sourcePath,
	}

	interval := 60 * time.Second
	if raw.Sync.Interval != "" {
		d, err := time.ParseDuration(raw.Sync.Interval)
		if err != nil {
			return nil, fmt.Errorf("invalid sync.interval %q: %w", raw.Sync.Interval, err)
		}
		interval = d
	}

	cfg.Sync = SyncSettings{
		Interval: interval,
		DryRun:   raw.Sync.DryRun,
		Defaults: raw.Sync.Defaults,
		Files:    raw.Sync.Files,
	}

	// Apply defaults.
	if cfg.Sync.Defaults.Direction == "" {
		cfg.Sync.Defaults.Direction = DirectionBidirectional
	}
	if cfg.Sync.Defaults.ConflictPolicy == "" {
		cfg.Sync.Defaults.ConflictPolicy = ConflictKeepBoth
	}
	if cfg.Sync.Defaults.DeletePolicy == "" {
		cfg.Sync.Defaults.DeletePolicy = DeletePolicyIgnore
	}
	for i := range cfg.Sync.Files {
		f := &cfg.Sync.Files[i]
		if f.Direction == "" {
			f.Direction = cfg.Sync.Defaults.Direction
		}
		if f.ConflictPolicy == "" {
			f.ConflictPolicy = cfg.Sync.Defaults.ConflictPolicy
		}
		if f.DeletePolicy == "" {
			f.DeletePolicy = cfg.Sync.Defaults.DeletePolicy
		}
	}

	if cfg.Logging.Level == "" {
		cfg.Logging.Level = "info"
	}
	if cfg.Logging.Format == "" {
		cfg.Logging.Format = "json"
	}
	if cfg.StatePath == "" {
		cfg.StatePath = "/app/state/dbsync.db"
	}

	return cfg, nil
}
