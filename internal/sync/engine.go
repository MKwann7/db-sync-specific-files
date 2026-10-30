package sync

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/MKwann7/db-sync-specific-files/internal/config"
	"github.com/MKwann7/db-sync-specific-files/internal/dropbox"
	"github.com/MKwann7/db-sync-specific-files/internal/filesystem"
	"github.com/MKwann7/db-sync-specific-files/internal/state"
)

// Engine executes synchronization cycles for a set of configured file
// mappings, using an injected Dropbox client and state database so it can
// be tested without any real network access.
type Engine struct {
	cfg    *config.Config
	client dropbox.Client
	db     *state.DB
	logger *slog.Logger

	// randSuffix generates entropy for conflict-copy filenames; overridable
	// in tests for determinism.
	randSuffix func() (string, error)
	// nowFunc returns the current time; overridable in tests.
	nowFunc func() time.Time
}

// New constructs an Engine.
func New(cfg *config.Config, client dropbox.Client, db *state.DB, logger *slog.Logger) *Engine {
	if logger == nil {
		logger = slog.Default()
	}
	return &Engine{
		cfg:    cfg,
		client: client,
		db:     db,
		logger: logger,
		randSuffix: func() (string, error) {
			buf := make([]byte, 6)
			if _, err := rand.Read(buf); err != nil {
				return "", err
			}
			return hex.EncodeToString(buf), nil
		},
		nowFunc: time.Now,
	}
}

// MappingResult records the outcome of synchronizing a single mapping
// during one cycle.
type MappingResult struct {
	Mapping config.FileMapping
	Action  Action
	Reason  string
	Err     error
}

// Report summarizes one full reconciliation cycle.
type Report struct {
	Results []MappingResult
}

// HasErrors reports whether any mapping failed during the cycle.
func (r Report) HasErrors() bool {
	for _, res := range r.Results {
		if res.Err != nil {
			return true
		}
	}
	return false
}

// RunOnce performs a single reconciliation pass over every configured
// mapping and returns a report. Mappings are processed independently: a
// failure in one mapping does not prevent others from being processed.
func (e *Engine) RunOnce(ctx context.Context) Report {
	var report Report
	for _, m := range e.cfg.Sync.Files {
		if ctx.Err() != nil {
			break
		}
		res := e.syncMapping(ctx, m)
		report.Results = append(report.Results, res)
	}
	return report
}

func (e *Engine) syncMapping(ctx context.Context, m config.FileMapping) MappingResult {
	logger := e.logger.With("mapping", m.Name, "local", m.Local, "remote", m.Remote)

	prev, err := e.db.Get(ctx, m.ID())
	if err != nil {
		logger.Error("failed to read prior synchronization state", "error", err)
		return MappingResult{Mapping: m, Err: fmt.Errorf("reading prior state: %w", err)}
	}
	if prev.Status == state.StatusInProgress {
		logger.Warn("previous synchronization attempt was interrupted (process restarted mid-sync); re-evaluating safely from last confirmed state")
	}

	localExists, localHash, err := e.localFileState(m.ResolvedLocal)
	if err != nil {
		logger.Error("failed to inspect local file", "error", err)
		return MappingResult{Mapping: m, Err: fmt.Errorf("inspecting local file: %w", err)}
	}

	remoteMD, err := e.client.GetMetadata(ctx, m.Remote)
	if err != nil {
		logger.Error("failed to fetch remote metadata", "error", err)
		return MappingResult{Mapping: m, Err: fmt.Errorf("fetching remote metadata: %w", err)}
	}
	remoteExists := !remoteMD.IsDeleted

	isInitial := prev.Status == state.StatusNeverSynced

	decision := Decide(Input{
		Direction:         m.Direction,
		IsInitial:         isInitial,
		PrevLocalExisted:  prev.LocalExisted,
		PrevRemoteExisted: prev.RemoteExisted,
		PrevLocalHash:     prev.LocalHash,
		PrevRemoteHash:    prev.RemoteHash,
		LocalExists:       localExists,
		LocalHash:         localHash,
		RemoteExists:      remoteExists,
		RemoteHash:        remoteMD.ContentHash,
	})

	result := MappingResult{Mapping: m, Action: decision.Action, Reason: decision.Reason}

	if e.cfg.Sync.DryRun {
		logger.Info("dry-run: would take action", "action", decision.Action, "reason", decision.Reason)
		return result
	}

	switch decision.Action {
	case ActionNone:
		logger.Debug("no action needed", "reason", decision.Reason)
		if err := e.db.CommitObserved(ctx, state.MappingState{
			MappingID: m.ID(), LocalHash: localHash, RemoteHash: remoteMD.ContentHash, RemoteRev: remoteMD.Rev,
			LocalExisted: localExists, RemoteExisted: remoteExists,
		}, state.StatusSucceeded); err != nil {
			logger.Error("failed to persist synchronization state", "error", err)
		}

	case ActionSkipBothMissing, ActionSkipDeletionIgnored, ActionSkipDirectionMismatch,
		ActionSkipRemoteAbsentForDownload, ActionSkipLocalAbsentForUpload:
		logger.Info("skipping mapping this cycle", "action", decision.Action, "reason", decision.Reason)
		// Intentionally do not advance the persisted baseline here: doing so
		// could mask a genuine deletion/mismatch on the next cycle. The
		// mapping will be safely re-evaluated from the same baseline again.

	case ActionUpload:
		if err := e.upload(ctx, logger, m, localHash, remoteMD); err != nil {
			logger.Error("upload failed", "error", err)
			result.Err = err
			_ = e.db.CommitFailure(ctx, m.ID(), state.StatusFailed, err.Error())
		}

	case ActionDownload:
		if err := e.download(ctx, logger, m, remoteMD); err != nil {
			logger.Error("download failed", "error", err)
			result.Err = err
			_ = e.db.CommitFailure(ctx, m.ID(), state.StatusFailed, err.Error())
		}

	case ActionConflict:
		if err := e.resolveConflict(ctx, logger, m, localHash, remoteMD); err != nil {
			logger.Error("conflict resolution failed", "error", err)
			result.Err = err
			_ = e.db.CommitFailure(ctx, m.ID(), state.StatusFailed, err.Error())
		}
	}

	return result
}

// localFileState reports whether the local file exists and, if so, its
// Dropbox-compatible content hash.
func (e *Engine) localFileState(path string) (exists bool, hash string, err error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, "", nil
		}
		return false, "", err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return false, "", err
	}
	if info.IsDir() {
		return false, "", fmt.Errorf("local path %q is a directory, not a file", path)
	}

	h, err := dropbox.ContentHash(f)
	if err != nil {
		return false, "", fmt.Errorf("hashing local file: %w", err)
	}
	return true, h, nil
}

// upload pushes the local file to Dropbox, using a conditional update when
// the remote already exists so a concurrent remote modification cannot be
// silently clobbered.
func (e *Engine) upload(ctx context.Context, logger *slog.Logger, m config.FileMapping, localHash string, remoteMD dropbox.Metadata) error {
	f, err := os.Open(m.ResolvedLocal)
	if err != nil {
		return fmt.Errorf("opening local file for upload: %w", err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return fmt.Errorf("stat local file: %w", err)
	}

	mode := dropbox.UploadModeAdd
	ifMatch := ""
	if !remoteMD.IsDeleted {
		mode = dropbox.UploadModeUpdate
		ifMatch = remoteMD.Rev
	}

	logger.Info("uploading file", "mode", mode, "size", info.Size())
	result, err := e.client.Upload(ctx, dropbox.UploadRequest{
		RemotePath: m.Remote,
		Mode:       mode,
		IfMatchRev: ifMatch,
		Reader:     f,
		Size:       info.Size(),
	})
	if err != nil {
		return fmt.Errorf("uploading to %q: %w", m.Remote, err)
	}

	logger.Info("upload complete", "rev", result.Rev)
	return e.db.CommitSuccess(ctx, state.MappingState{
		MappingID: m.ID(), LocalHash: localHash, RemoteHash: result.ContentHash, RemoteRev: result.Rev,
		LocalExisted: true, RemoteExisted: true,
	})
}

// download pulls the remote file to local, verifying its content hash
// before atomically replacing the destination so a corrupted or partial
// transfer never damages the existing local file.
func (e *Engine) download(ctx context.Context, logger *slog.Logger, m config.FileMapping, remoteMD dropbox.Metadata) error {
	md, body, err := e.client.Download(ctx, m.Remote)
	if err != nil {
		return fmt.Errorf("downloading %q: %w", m.Remote, err)
	}
	defer body.Close()

	logger.Info("downloading file", "rev", md.Rev, "size", md.Size)
	gotHash, err := filesystem.AtomicWriteVerified(m.ResolvedLocal, body, md.ContentHash, dropbox.ContentHash, 0o644)
	if err != nil {
		return fmt.Errorf("writing downloaded content to %q: %w", m.Local, err)
	}

	logger.Info("download complete and verified", "rev", md.Rev)
	return e.db.CommitSuccess(ctx, state.MappingState{
		MappingID: m.ID(), LocalHash: gotHash, RemoteHash: md.ContentHash, RemoteRev: md.Rev,
		LocalExisted: true, RemoteExisted: true,
	})
}

// resolveConflict applies the mapping's configured conflict policy when
// both sides have independently changed since the last successful sync.
func (e *Engine) resolveConflict(ctx context.Context, logger *slog.Logger, m config.FileMapping, localHash string, remoteMD dropbox.Metadata) error {
	logger.Warn("conflict detected: local and remote changed independently", "policy", m.ConflictPolicy)

	switch m.ConflictPolicy {
	case config.ConflictSkip:
		logger.Info("conflict policy is 'skip': leaving both versions untouched")
		_ = e.db.RecordConflict(ctx, m.ID(), string(m.ConflictPolicy), "", "", "both versions left untouched per skip policy")
		return e.db.CommitObserved(ctx, state.MappingState{
			MappingID: m.ID(), LocalHash: localHash, RemoteHash: remoteMD.ContentHash, RemoteRev: remoteMD.Rev,
			LocalExisted: true, RemoteExisted: true,
		}, state.StatusConflict)

	case config.ConflictPreferLocal:
		// Back up the remote's current content locally before overwriting
		// it, then upload local, overwriting remote.
		backupPath, err := e.backupRemoteContent(ctx, m)
		if err != nil {
			return err
		}
		logger.Info("conflict policy is 'prefer_local': backed up remote version, uploading local version", "backup", backupPath)
		_ = e.db.RecordConflict(ctx, m.ID(), string(m.ConflictPolicy), "", backupPath, "remote version backed up before being overwritten by local")
		return e.upload(ctx, logger, m, localHash, remoteMD)

	case config.ConflictPreferRemote:
		backupPath, err := e.backupLocalFile(m.ResolvedLocal)
		if err != nil {
			return err
		}
		logger.Info("conflict policy is 'prefer_remote': backed up local version, downloading remote version", "backup", backupPath)
		_ = e.db.RecordConflict(ctx, m.ID(), string(m.ConflictPolicy), backupPath, "", "local version backed up before being overwritten by remote")
		return e.download(ctx, logger, m, remoteMD)

	default: // keep_both (default policy)
		return e.keepBoth(ctx, logger, m, localHash, remoteMD)
	}
}

// keepBoth preserves the local file untouched at its normal path and saves
// the remote version alongside it under a unique conflict filename, leaving
// the remote file itself unmodified. Both original versions survive.
func (e *Engine) keepBoth(ctx context.Context, logger *slog.Logger, m config.FileMapping, localHash string, remoteMD dropbox.Metadata) error {
	conflictPath, err := filesystem.UniqueConflictPath(m.ResolvedLocal, e.timestamp, e.randSuffix)
	if err != nil {
		return fmt.Errorf("allocating conflict copy filename: %w", err)
	}

	_, body, err := e.client.Download(ctx, m.Remote)
	if err != nil {
		return fmt.Errorf("downloading remote version for conflict copy: %w", err)
	}
	defer body.Close()

	if _, err := filesystem.AtomicWriteVerified(conflictPath, body, remoteMD.ContentHash, dropbox.ContentHash, 0o644); err != nil {
		return fmt.Errorf("writing conflict copy %q: %w", conflictPath, err)
	}

	logger.Info("conflict policy is 'keep_both': local version preserved, remote version saved as conflict copy", "conflict_copy", conflictPath)
	_ = e.db.RecordConflict(ctx, m.ID(), string(m.ConflictPolicy), m.ResolvedLocal, conflictPath, "both versions preserved; remote saved as conflict copy")

	// Record the conflict as observed, without pretending resolution
	// occurred: local file is untouched (localHash unchanged) and remote is
	// untouched (remoteMD unchanged), so storing both current hashes as the
	// baseline prevents re-flagging the same conflict every cycle while the
	// user decides how to reconcile manually.
	return e.db.CommitObserved(ctx, state.MappingState{
		MappingID: m.ID(), LocalHash: localHash, RemoteHash: remoteMD.ContentHash, RemoteRev: remoteMD.Rev,
		LocalExisted: true, RemoteExisted: true,
	}, state.StatusConflict)
}

func (e *Engine) backupLocalFile(path string) (string, error) {
	backupPath, err := filesystem.UniqueConflictPath(path, e.timestamp, e.randSuffix)
	if err != nil {
		return "", fmt.Errorf("allocating backup filename: %w", err)
	}
	src, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("opening local file for backup: %w", err)
	}
	defer src.Close()
	if err := filesystem.AtomicWriteFromReader(backupPath, src, 0o644); err != nil {
		return "", fmt.Errorf("writing local backup copy: %w", err)
	}
	return backupPath, nil
}

func (e *Engine) backupRemoteContent(ctx context.Context, m config.FileMapping) (string, error) {
	backupPath, err := filesystem.UniqueConflictPath(m.ResolvedLocal, e.timestamp, e.randSuffix)
	if err != nil {
		return "", fmt.Errorf("allocating backup filename: %w", err)
	}
	_, body, err := e.client.Download(ctx, m.Remote)
	if err != nil {
		return "", fmt.Errorf("downloading remote version for backup: %w", err)
	}
	defer body.Close()
	if err := filesystem.AtomicWriteFromReader(backupPath, body, 0o644); err != nil {
		return "", fmt.Errorf("writing remote backup copy: %w", err)
	}
	return backupPath, nil
}

func (e *Engine) timestamp() string {
	return e.nowFunc().UTC().Format("20060102T150405.000000000Z")
}
