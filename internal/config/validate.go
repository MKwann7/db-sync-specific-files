package config

import (
	"fmt"
	"path/filepath"
	"strings"
)

// ValidateOptions controls validation behavior.
type ValidateOptions struct {
	// RequireAllowedRoots, when true (the default), requires at least one
	// security.allowed_roots entry and rejects any mapping whose local path
	// resolves outside all configured roots.
	RequireAllowedRoots bool
}

// Validate performs semantic validation of the configuration without any
// network access. It resolves local paths (including symlinks) and rejects
// unsafe or ambiguous configurations.
func (c *Config) Validate(opts ValidateOptions) error {
	var errs []string

	if c.Sync.Interval <= 0 {
		errs = append(errs, "sync.interval must be a positive duration")
	}

	if len(c.Sync.Files) == 0 {
		errs = append(errs, "sync.files must contain at least one mapping")
	}

	if opts.RequireAllowedRoots && len(c.Security.AllowedRoots) == 0 {
		errs = append(errs, "security.allowed_roots must specify at least one permitted local directory root")
	}

	resolvedRoots := make([]string, 0, len(c.Security.AllowedRoots))
	for _, r := range c.Security.AllowedRoots {
		abs, err := resolveRoot(r)
		if err != nil {
			errs = append(errs, fmt.Sprintf("security.allowed_roots entry %q: %v", r, err))
			continue
		}
		resolvedRoots = append(resolvedRoots, abs)
	}

	seenLocal := map[string]string{}
	seenRemote := map[string]string{}

	for i := range c.Sync.Files {
		f := &c.Sync.Files[i]
		label := f.Name
		if label == "" {
			label = fmt.Sprintf("files[%d]", i)
		}

		if f.Name == "" {
			errs = append(errs, fmt.Sprintf("%s: name is required", label))
		}
		if f.Local == "" {
			errs = append(errs, fmt.Sprintf("%s: local path is required", label))
		}
		if f.Remote == "" {
			errs = append(errs, fmt.Sprintf("%s: remote path is required", label))
		}
		if !strings.HasPrefix(f.Remote, "/") {
			errs = append(errs, fmt.Sprintf("%s: remote path must be absolute (start with /)", label))
		}

		switch f.Direction {
		case DirectionUpload, DirectionDownload, DirectionBidirectional:
		default:
			errs = append(errs, fmt.Sprintf("%s: invalid direction %q", label, f.Direction))
		}

		switch f.ConflictPolicy {
		case ConflictKeepBoth, ConflictPreferLocal, ConflictPreferRemote, ConflictSkip:
		default:
			errs = append(errs, fmt.Sprintf("%s: invalid conflict_policy %q", label, f.ConflictPolicy))
		}

		switch f.DeletePolicy {
		case DeletePolicyIgnore:
		default:
			errs = append(errs, fmt.Sprintf("%s: invalid delete_policy %q (only 'ignore' is supported)", label, f.DeletePolicy))
		}

		if f.Local != "" {
			resolved, err := resolveLocalPath(f.Local)
			if err != nil {
				errs = append(errs, fmt.Sprintf("%s: local path %q: %v", label, f.Local, err))
			} else {
				f.ResolvedLocal = resolved

				if len(resolvedRoots) > 0 {
					if !isWithinAnyRoot(resolved, resolvedRoots) {
						errs = append(errs, fmt.Sprintf("%s: local path %q resolves outside permitted allowed_roots", label, f.Local))
					}
				}

				if other, ok := seenLocal[resolved]; ok {
					errs = append(errs, fmt.Sprintf("%s: duplicate local path %q also used by %q", label, f.Local, other))
				}
				seenLocal[resolved] = label

				if c.StatePath != "" {
					if statePath, err := resolveLocalPath(c.StatePath); err == nil && statePath == resolved {
						errs = append(errs, fmt.Sprintf("%s: local path must not point at the state database (%q)", label, c.StatePath))
					}
				}
				if c.ConfigPath != "" {
					if cfgAbs, err := resolveLocalPath(c.ConfigPath); err == nil && cfgAbs == resolved {
						errs = append(errs, fmt.Sprintf("%s: local path must not point at the configuration file (%q)", label, c.ConfigPath))
					}
				}
			}
		}

		if f.Remote != "" {
			cleanRemote := cleanRemotePath(f.Remote)
			if other, ok := seenRemote[cleanRemote]; ok {
				errs = append(errs, fmt.Sprintf("%s: duplicate remote path %q also used by %q", label, f.Remote, other))
			}
			seenRemote[cleanRemote] = label
		}
	}

	// Detect ambiguous/overlapping mappings: one mapping's remote path must
	// not be a path-prefix of another mapping's remote path (which would
	// make folder-level reconciliation ambiguous), and likewise for local
	// paths.
	remotePaths := make([]string, 0, len(seenRemote))
	for p := range seenRemote {
		remotePaths = append(remotePaths, p)
	}
	for i := range remotePaths {
		for j := range remotePaths {
			if i == j {
				continue
			}
			if isPathPrefix(remotePaths[i], remotePaths[j]) {
				errs = append(errs, fmt.Sprintf("remote path %q overlaps with %q (ambiguous mapping)", remotePaths[i], remotePaths[j]))
			}
		}
	}

	localPaths := make([]string, 0, len(seenLocal))
	for p := range seenLocal {
		localPaths = append(localPaths, p)
	}
	for i := range localPaths {
		for j := range localPaths {
			if i == j {
				continue
			}
			if isPathPrefix(localPaths[i], localPaths[j]) {
				errs = append(errs, fmt.Sprintf("local path %q overlaps with %q (ambiguous mapping)", localPaths[i], localPaths[j]))
			}
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("configuration validation failed:\n  - %s", strings.Join(errs, "\n  - "))
	}
	return nil
}

func resolveRoot(root string) (string, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	// Roots are not required to exist (they may be created later), but if
	// they do exist we resolve symlinks for consistent comparisons.
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		return filepath.Clean(real), nil
	}
	return filepath.Clean(abs), nil
}

// resolveLocalPath resolves a configured local path to an absolute,
// symlink-evaluated path. The file itself need not exist yet, but its
// deepest existing ancestor directory is resolved for symlinks so that
// configured allowed_roots cannot be escaped via a symlinked ancestor.
func resolveLocalPath(p string) (string, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	abs = filepath.Clean(abs)

	// Walk up until we find an existing ancestor, resolve symlinks there,
	// then re-append the remaining (not-yet-existing) path components.
	dir := abs
	var suffix []string
	for {
		real, err := filepath.EvalSymlinks(dir)
		if err == nil {
			full := real
			for i := len(suffix) - 1; i >= 0; i-- {
				full = filepath.Join(full, suffix[i])
			}
			return filepath.Clean(full), nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			// Reached filesystem root without finding an existing ancestor.
			return abs, nil
		}
		suffix = append(suffix, filepath.Base(dir))
		dir = parent
	}
}

func isWithinAnyRoot(resolved string, roots []string) bool {
	for _, root := range roots {
		if isPathPrefix(root, resolved) || root == resolved {
			return true
		}
	}
	return false
}

// isPathPrefix reports whether child is prefix-contained within parent using
// path-segment boundaries (so "/data/book" is not considered inside
// "/data/bo").
func isPathPrefix(parent, child string) bool {
	if parent == child {
		return false
	}
	sep := string(filepath.Separator)
	if !strings.HasSuffix(parent, sep) {
		parent += sep
	}
	return strings.HasPrefix(child+sep, parent)
}

func cleanRemotePath(p string) string {
	cleaned := filepath.ToSlash(filepath.Clean(p))
	return strings.ToLower(cleaned)
}
