// Package sync implements the synchronization decision engine: given the
// previously persisted state of a mapping and the currently observed local
// and remote file states, it decides what action (if any) to take. This
// file contains only pure decision logic with no network or filesystem
// access, so it can be exhaustively unit tested.
package sync

import (
	"github.com/MKwann7/db-sync-specific-files/internal/config"
)

// Action identifies what the engine should do for a mapping this cycle.
type Action string

const (
	// ActionNone means local and remote are already consistent; nothing to
	// transfer, but the current baseline should be (re-)recorded.
	ActionNone Action = "none"

	// ActionUpload means the local file should be pushed to Dropbox.
	ActionUpload Action = "upload"

	// ActionDownload means the Dropbox file should be pulled to local.
	ActionDownload Action = "download"

	// ActionConflict means both sides changed independently (including the
	// first-ever synchronization finding differing pre-existing content) in
	// a bidirectional mapping, and the configured conflict policy must be
	// applied.
	ActionConflict Action = "conflict"

	// ActionSkipDeletionIgnored means one side is missing that previously
	// existed; per the delete_policy=ignore safety rule, the deletion is
	// never propagated, and no transfer occurs.
	ActionSkipDeletionIgnored Action = "skip_deletion_ignored"

	// ActionSkipDirectionMismatch means a change was observed on the
	// non-authoritative side of a one-directional mapping (e.g. a remote
	// edit on an upload-only mapping); the change is intentionally ignored.
	ActionSkipDirectionMismatch Action = "skip_direction_mismatch"

	// ActionSkipBothMissing means neither side has a file; there is
	// nothing to do.
	ActionSkipBothMissing Action = "skip_both_missing"

	// ActionSkipRemoteAbsentForDownload / ActionSkipLocalAbsentForUpload
	// cover the initial-sync case where the authoritative side for a
	// one-directional mapping does not exist, so there is nothing to
	// propagate and the non-authoritative side must not be deleted.
	ActionSkipRemoteAbsentForDownload Action = "skip_remote_absent_for_download"
	ActionSkipLocalAbsentForUpload    Action = "skip_local_absent_for_upload"
)

// Input captures everything the decision logic needs: the mapping's
// configuration and the previously persisted vs. currently observed state
// of both sides.
type Input struct {
	Direction config.Direction

	// IsInitial is true when no prior successful/observed baseline exists
	// for this mapping (state.StatusNeverSynced).
	IsInitial bool

	PrevLocalExisted  bool
	PrevRemoteExisted bool
	PrevLocalHash     string
	PrevRemoteHash    string

	LocalExists bool
	LocalHash   string

	RemoteExists bool
	RemoteHash   string
}

// Decision is the result of evaluating an Input.
type Decision struct {
	Action Action
	Reason string
}

// Decide computes the synchronization action for a single mapping. It is a
// pure function: identical inputs always produce identical outputs, and it
// performs no I/O.
func Decide(in Input) Decision {
	switch {
	case !in.LocalExists && !in.RemoteExists:
		return Decision{ActionSkipBothMissing, "neither local nor remote file exists"}

	case in.LocalExists && !in.RemoteExists:
		return decideLocalOnly(in)

	case !in.LocalExists && in.RemoteExists:
		return decideRemoteOnly(in)

	default: // both exist
		return decideBothExist(in)
	}
}

func decideLocalOnly(in Input) Decision {
	if in.PrevRemoteExisted {
		// Remote previously existed and is now gone: either deleted or
		// temporarily inaccessible. Never infer deletion intent.
		return Decision{ActionSkipDeletionIgnored, "remote file previously existed but is now missing; deletion is not propagated (delete_policy=ignore)"}
	}
	// Remote has never existed: this is effectively an initial, one-sided
	// state. Propagate according to the configured direction.
	switch in.Direction {
	case config.DirectionUpload, config.DirectionBidirectional:
		return Decision{ActionUpload, "only local file exists; uploading per configured direction"}
	case config.DirectionDownload:
		return Decision{ActionSkipRemoteAbsentForDownload, "remote file does not exist for a download-only mapping; nothing to download, local file preserved"}
	}
	return Decision{ActionNone, "unhandled direction"}
}

func decideRemoteOnly(in Input) Decision {
	if in.PrevLocalExisted {
		return Decision{ActionSkipDeletionIgnored, "local file previously existed but is now missing; deletion is not propagated (delete_policy=ignore)"}
	}
	switch in.Direction {
	case config.DirectionDownload, config.DirectionBidirectional:
		return Decision{ActionDownload, "only remote file exists; downloading per configured direction"}
	case config.DirectionUpload:
		return Decision{ActionSkipLocalAbsentForUpload, "local file does not exist for an upload-only mapping; nothing to upload, remote file preserved"}
	}
	return Decision{ActionNone, "unhandled direction"}
}

func decideBothExist(in Input) Decision {
	if in.LocalHash != "" && in.LocalHash == in.RemoteHash {
		return Decision{ActionNone, "local and remote content hashes match"}
	}

	localChanged := in.LocalExists != in.PrevLocalExisted || in.LocalHash != in.PrevLocalHash
	remoteChanged := in.RemoteExists != in.PrevRemoteExisted || in.RemoteHash != in.PrevRemoteHash

	switch {
	case localChanged && !remoteChanged:
		switch in.Direction {
		case config.DirectionUpload, config.DirectionBidirectional:
			return Decision{ActionUpload, "local content changed since last sync"}
		case config.DirectionDownload:
			return Decision{ActionSkipDirectionMismatch, "local content changed but mapping is download-only; local change ignored"}
		}

	case remoteChanged && !localChanged:
		switch in.Direction {
		case config.DirectionDownload, config.DirectionBidirectional:
			return Decision{ActionDownload, "remote content changed since last sync"}
		case config.DirectionUpload:
			return Decision{ActionSkipDirectionMismatch, "remote content changed but mapping is upload-only; remote change ignored"}
		}

	default: // both changed (or this is the initial sync and both pre-exist with differing content)
		switch in.Direction {
		case config.DirectionUpload:
			return Decision{ActionUpload, "both sides differ; local is authoritative for an upload-only mapping"}
		case config.DirectionDownload:
			return Decision{ActionDownload, "both sides differ; remote is authoritative for a download-only mapping"}
		case config.DirectionBidirectional:
			return Decision{ActionConflict, "both local and remote content differ independently; conflict policy required"}
		}
	}
	return Decision{ActionNone, "unhandled case"}
}
