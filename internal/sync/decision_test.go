package sync

import (
	"testing"

	"github.com/MKwann7/db-sync-specific-files/internal/config"
)

func TestDecide_BothMissing(t *testing.T) {
	d := Decide(Input{Direction: config.DirectionBidirectional})
	if d.Action != ActionSkipBothMissing {
		t.Errorf("got %s", d.Action)
	}
}

func TestDecide_InitialOnlyLocalExists_Upload(t *testing.T) {
	for _, dir := range []config.Direction{config.DirectionUpload, config.DirectionBidirectional} {
		d := Decide(Input{Direction: dir, IsInitial: true, LocalExists: true, LocalHash: "h1"})
		if d.Action != ActionUpload {
			t.Errorf("direction=%s: got %s, want upload", dir, d.Action)
		}
	}
}

func TestDecide_InitialOnlyLocalExists_DownloadDirection_Skipped(t *testing.T) {
	d := Decide(Input{Direction: config.DirectionDownload, IsInitial: true, LocalExists: true, LocalHash: "h1"})
	if d.Action != ActionSkipRemoteAbsentForDownload {
		t.Errorf("got %s", d.Action)
	}
}

func TestDecide_InitialOnlyRemoteExists_Download(t *testing.T) {
	for _, dir := range []config.Direction{config.DirectionDownload, config.DirectionBidirectional} {
		d := Decide(Input{Direction: dir, IsInitial: true, RemoteExists: true, RemoteHash: "h1"})
		if d.Action != ActionDownload {
			t.Errorf("direction=%s: got %s, want download", dir, d.Action)
		}
	}
}

func TestDecide_InitialOnlyRemoteExists_UploadDirection_Skipped(t *testing.T) {
	d := Decide(Input{Direction: config.DirectionUpload, IsInitial: true, RemoteExists: true, RemoteHash: "h1"})
	if d.Action != ActionSkipLocalAbsentForUpload {
		t.Errorf("got %s", d.Action)
	}
}

func TestDecide_InitialBothExistSameContent_NoAction(t *testing.T) {
	d := Decide(Input{
		Direction: config.DirectionBidirectional, IsInitial: true,
		LocalExists: true, LocalHash: "same",
		RemoteExists: true, RemoteHash: "same",
	})
	if d.Action != ActionNone {
		t.Errorf("got %s, want none", d.Action)
	}
}

func TestDecide_InitialBothExistDifferentContent_Bidirectional_Conflict(t *testing.T) {
	d := Decide(Input{
		Direction: config.DirectionBidirectional, IsInitial: true,
		LocalExists: true, LocalHash: "local-h",
		RemoteExists: true, RemoteHash: "remote-h",
	})
	if d.Action != ActionConflict {
		t.Errorf("got %s, want conflict (must not silently pick a winner)", d.Action)
	}
}

func TestDecide_InitialBothExistDifferentContent_UploadDirection_UploadWins(t *testing.T) {
	d := Decide(Input{
		Direction: config.DirectionUpload, IsInitial: true,
		LocalExists: true, LocalHash: "local-h",
		RemoteExists: true, RemoteHash: "remote-h",
	})
	if d.Action != ActionUpload {
		t.Errorf("got %s, want upload (direction is the configured authority)", d.Action)
	}
}

func TestDecide_InitialBothExistDifferentContent_DownloadDirection_DownloadWins(t *testing.T) {
	d := Decide(Input{
		Direction: config.DirectionDownload, IsInitial: true,
		LocalExists: true, LocalHash: "local-h",
		RemoteExists: true, RemoteHash: "remote-h",
	})
	if d.Action != ActionDownload {
		t.Errorf("got %s, want download (direction is the configured authority)", d.Action)
	}
}

func TestDecide_LocalChangedOnly_Bidirectional_Upload(t *testing.T) {
	d := Decide(Input{
		Direction:        config.DirectionBidirectional,
		PrevLocalExisted: true, PrevRemoteExisted: true,
		PrevLocalHash: "old", PrevRemoteHash: "same",
		LocalExists: true, LocalHash: "new",
		RemoteExists: true, RemoteHash: "same",
	})
	if d.Action != ActionUpload {
		t.Errorf("got %s", d.Action)
	}
}

func TestDecide_RemoteChangedOnly_Bidirectional_Download(t *testing.T) {
	d := Decide(Input{
		Direction:        config.DirectionBidirectional,
		PrevLocalExisted: true, PrevRemoteExisted: true,
		PrevLocalHash: "same", PrevRemoteHash: "old",
		LocalExists: true, LocalHash: "same",
		RemoteExists: true, RemoteHash: "new",
	})
	if d.Action != ActionDownload {
		t.Errorf("got %s", d.Action)
	}
}

func TestDecide_BothChanged_Bidirectional_Conflict(t *testing.T) {
	d := Decide(Input{
		Direction:        config.DirectionBidirectional,
		PrevLocalExisted: true, PrevRemoteExisted: true,
		PrevLocalHash: "old-local", PrevRemoteHash: "old-remote",
		LocalExists: true, LocalHash: "new-local",
		RemoteExists: true, RemoteHash: "new-remote",
	})
	if d.Action != ActionConflict {
		t.Errorf("got %s, want conflict", d.Action)
	}
}

func TestDecide_NeitherChanged_NoAction(t *testing.T) {
	d := Decide(Input{
		Direction:        config.DirectionBidirectional,
		PrevLocalExisted: true, PrevRemoteExisted: true,
		PrevLocalHash: "same", PrevRemoteHash: "same",
		LocalExists: true, LocalHash: "same",
		RemoteExists: true, RemoteHash: "same",
	})
	if d.Action != ActionNone {
		t.Errorf("got %s", d.Action)
	}
}

func TestDecide_UploadOnly_RemoteChangeIgnored(t *testing.T) {
	d := Decide(Input{
		Direction:        config.DirectionUpload,
		PrevLocalExisted: true, PrevRemoteExisted: true,
		PrevLocalHash: "same", PrevRemoteHash: "old",
		LocalExists: true, LocalHash: "same",
		RemoteExists: true, RemoteHash: "changed-remotely",
	})
	if d.Action != ActionSkipDirectionMismatch {
		t.Errorf("got %s, want skip (upload-only must not pull remote changes)", d.Action)
	}
}

func TestDecide_DownloadOnly_LocalChangeIgnored(t *testing.T) {
	d := Decide(Input{
		Direction:        config.DirectionDownload,
		PrevLocalExisted: true, PrevRemoteExisted: true,
		PrevLocalHash: "old", PrevRemoteHash: "same",
		LocalExists: true, LocalHash: "changed-locally",
		RemoteExists: true, RemoteHash: "same",
	})
	if d.Action != ActionSkipDirectionMismatch {
		t.Errorf("got %s, want skip (download-only must not push local changes)", d.Action)
	}
}

func TestDecide_LocalDeleted_NeverPropagatesDeletion(t *testing.T) {
	d := Decide(Input{
		Direction:         config.DirectionBidirectional,
		PrevLocalExisted:  true,
		PrevRemoteExisted: true,
		PrevLocalHash:     "h1", PrevRemoteHash: "h1",
		LocalExists:  false,
		RemoteExists: true, RemoteHash: "h1",
	})
	if d.Action != ActionSkipDeletionIgnored {
		t.Errorf("got %s, want skip_deletion_ignored (must never delete remote counterpart)", d.Action)
	}
}

func TestDecide_RemoteDeleted_NeverPropagatesDeletion(t *testing.T) {
	d := Decide(Input{
		Direction:         config.DirectionBidirectional,
		PrevLocalExisted:  true,
		PrevRemoteExisted: true,
		PrevLocalHash:     "h1", PrevRemoteHash: "h1",
		LocalExists: true, LocalHash: "h1",
		RemoteExists: false,
	})
	if d.Action != ActionSkipDeletionIgnored {
		t.Errorf("got %s, want skip_deletion_ignored (must never delete local counterpart)", d.Action)
	}
}
