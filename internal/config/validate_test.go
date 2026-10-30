package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTempConfig(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("writing temp config: %v", err)
	}
	return path
}

const validYAML = `
sync:
  interval: 60s
  dry_run: true
  defaults:
    direction: bidirectional
    conflict_policy: keep_both
    delete_policy: ignore
  files:
    - name: Manuscript
      local: ./books/manuscript.docx
      remote: /Books/manuscript.docx
      direction: bidirectional
    - name: Cover
      local: ./artwork/cover.png
      remote: /Books/Artwork/cover.png
      direction: upload
`

func TestLoad_ParsesValidConfig(t *testing.T) {
	path := writeTempConfig(t, validYAML)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Sync.Interval.Seconds() != 60 {
		t.Errorf("interval = %v", cfg.Sync.Interval)
	}
	if !cfg.Sync.DryRun {
		t.Error("expected dry_run true")
	}
	if len(cfg.Sync.Files) != 2 {
		t.Fatalf("expected 2 files, got %d", len(cfg.Sync.Files))
	}
	if cfg.Sync.Files[1].Direction != DirectionUpload {
		t.Errorf("file[1] direction = %s", cfg.Sync.Files[1].Direction)
	}
	// Inherited default.
	if cfg.Sync.Files[1].ConflictPolicy != ConflictKeepBoth {
		t.Errorf("file[1] conflict_policy = %s, want inherited default", cfg.Sync.Files[1].ConflictPolicy)
	}
}

func TestLoad_RejectsUnknownFields(t *testing.T) {
	path := writeTempConfig(t, validYAML+"\nunknown_top_level_key: true\n")
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for unknown top-level field")
	}
}

func TestLoad_InvalidDuration(t *testing.T) {
	path := writeTempConfig(t, `
sync:
  interval: "not-a-duration"
  files:
    - name: X
      local: /tmp/x
      remote: /x
`)
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for invalid duration")
	}
}

func TestValidate_RejectsDuplicateLocalPaths(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{
		Sync: SyncSettings{
			Interval: 60_000_000_000,
			Files: []FileMapping{
				{Name: "A", Local: filepath.Join(dir, "a.txt"), Remote: "/a.txt", Direction: DirectionUpload, ConflictPolicy: ConflictKeepBoth, DeletePolicy: DeletePolicyIgnore},
				{Name: "B", Local: filepath.Join(dir, "a.txt"), Remote: "/b.txt", Direction: DirectionUpload, ConflictPolicy: ConflictKeepBoth, DeletePolicy: DeletePolicyIgnore},
			},
		},
		Security: SecuritySettings{AllowedRoots: []string{dir}},
	}
	err := cfg.Validate(ValidateOptions{RequireAllowedRoots: true})
	if err == nil || !strings.Contains(err.Error(), "duplicate local path") {
		t.Fatalf("expected duplicate local path error, got %v", err)
	}
}

func TestValidate_RejectsDuplicateRemotePaths(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{
		Sync: SyncSettings{
			Interval: 60_000_000_000,
			Files: []FileMapping{
				{Name: "A", Local: filepath.Join(dir, "a.txt"), Remote: "/same.txt", Direction: DirectionUpload, ConflictPolicy: ConflictKeepBoth, DeletePolicy: DeletePolicyIgnore},
				{Name: "B", Local: filepath.Join(dir, "b.txt"), Remote: "/Same.txt", Direction: DirectionUpload, ConflictPolicy: ConflictKeepBoth, DeletePolicy: DeletePolicyIgnore},
			},
		},
		Security: SecuritySettings{AllowedRoots: []string{dir}},
	}
	err := cfg.Validate(ValidateOptions{RequireAllowedRoots: true})
	if err == nil || !strings.Contains(err.Error(), "duplicate remote path") {
		t.Fatalf("expected duplicate remote path error (case-insensitive), got %v", err)
	}
}

func TestValidate_RejectsInvalidDirection(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{
		Sync: SyncSettings{
			Interval: 60_000_000_000,
			Files: []FileMapping{
				{Name: "A", Local: filepath.Join(dir, "a.txt"), Remote: "/a.txt", Direction: "sideways", ConflictPolicy: ConflictKeepBoth, DeletePolicy: DeletePolicyIgnore},
			},
		},
		Security: SecuritySettings{AllowedRoots: []string{dir}},
	}
	err := cfg.Validate(ValidateOptions{RequireAllowedRoots: true})
	if err == nil || !strings.Contains(err.Error(), "invalid direction") {
		t.Fatalf("expected invalid direction error, got %v", err)
	}
}

func TestValidate_RejectsPathOutsideAllowedRoots(t *testing.T) {
	allowedRoot := t.TempDir()
	outside := t.TempDir()
	cfg := &Config{
		Sync: SyncSettings{
			Interval: 60_000_000_000,
			Files: []FileMapping{
				{Name: "A", Local: filepath.Join(outside, "a.txt"), Remote: "/a.txt", Direction: DirectionUpload, ConflictPolicy: ConflictKeepBoth, DeletePolicy: DeletePolicyIgnore},
			},
		},
		Security: SecuritySettings{AllowedRoots: []string{allowedRoot}},
	}
	err := cfg.Validate(ValidateOptions{RequireAllowedRoots: true})
	if err == nil || !strings.Contains(err.Error(), "outside permitted allowed_roots") {
		t.Fatalf("expected escape error, got %v", err)
	}
}

func TestValidate_RejectsSymlinkEscape(t *testing.T) {
	allowedRoot := t.TempDir()
	outside := t.TempDir()
	link := filepath.Join(allowedRoot, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}

	cfg := &Config{
		Sync: SyncSettings{
			Interval: 60_000_000_000,
			Files: []FileMapping{
				{Name: "A", Local: filepath.Join(link, "a.txt"), Remote: "/a.txt", Direction: DirectionUpload, ConflictPolicy: ConflictKeepBoth, DeletePolicy: DeletePolicyIgnore},
			},
		},
		Security: SecuritySettings{AllowedRoots: []string{allowedRoot}},
	}
	err := cfg.Validate(ValidateOptions{RequireAllowedRoots: true})
	if err == nil || !strings.Contains(err.Error(), "outside permitted allowed_roots") {
		t.Fatalf("expected symlink escape to be rejected, got %v", err)
	}
}

func TestValidate_RejectsOverwritingStatePath(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.db")
	cfg := &Config{
		StatePath: statePath,
		Sync: SyncSettings{
			Interval: 60_000_000_000,
			Files: []FileMapping{
				{Name: "A", Local: statePath, Remote: "/a.txt", Direction: DirectionUpload, ConflictPolicy: ConflictKeepBoth, DeletePolicy: DeletePolicyIgnore},
			},
		},
		Security: SecuritySettings{AllowedRoots: []string{dir}},
	}
	err := cfg.Validate(ValidateOptions{RequireAllowedRoots: true})
	if err == nil || !strings.Contains(err.Error(), "state database") {
		t.Fatalf("expected state-path-overwrite error, got %v", err)
	}
}

func TestValidate_RejectsAmbiguousOverlappingRemotePaths(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{
		Sync: SyncSettings{
			Interval: 60_000_000_000,
			Files: []FileMapping{
				{Name: "A", Local: filepath.Join(dir, "a.txt"), Remote: "/Books", Direction: DirectionUpload, ConflictPolicy: ConflictKeepBoth, DeletePolicy: DeletePolicyIgnore},
				{Name: "B", Local: filepath.Join(dir, "b.txt"), Remote: "/Books/sub.txt", Direction: DirectionUpload, ConflictPolicy: ConflictKeepBoth, DeletePolicy: DeletePolicyIgnore},
			},
		},
		Security: SecuritySettings{AllowedRoots: []string{dir}},
	}
	err := cfg.Validate(ValidateOptions{RequireAllowedRoots: true})
	if err == nil || !strings.Contains(err.Error(), "overlaps") {
		t.Fatalf("expected overlap error, got %v", err)
	}
}

func TestValidate_RequiresAllowedRootsWhenRequested(t *testing.T) {
	cfg := &Config{Sync: SyncSettings{Interval: 60_000_000_000, Files: []FileMapping{
		{Name: "A", Local: "/tmp/a.txt", Remote: "/a.txt", Direction: DirectionUpload, ConflictPolicy: ConflictKeepBoth, DeletePolicy: DeletePolicyIgnore},
	}}}
	err := cfg.Validate(ValidateOptions{RequireAllowedRoots: true})
	if err == nil || !strings.Contains(err.Error(), "allowed_roots") {
		t.Fatalf("expected missing-allowed-roots error, got %v", err)
	}
}

func TestValidate_WorksWithoutNetworkAccess(t *testing.T) {
	// This test's very existence demonstrates config.Validate performs no
	// network calls: it runs fully offline in the test sandbox.
	dir := t.TempDir()
	cfg := &Config{
		Sync: SyncSettings{Interval: 60_000_000_000, Files: []FileMapping{
			{Name: "A", Local: filepath.Join(dir, "a.txt"), Remote: "/a.txt", Direction: DirectionUpload, ConflictPolicy: ConflictKeepBoth, DeletePolicy: DeletePolicyIgnore},
		}},
		Security: SecuritySettings{AllowedRoots: []string{dir}},
	}
	if err := cfg.Validate(ValidateOptions{RequireAllowedRoots: true}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
