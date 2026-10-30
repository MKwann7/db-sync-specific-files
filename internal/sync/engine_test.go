package sync

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/MKwann7/db-sync-specific-files/internal/config"
	"github.com/MKwann7/db-sync-specific-files/internal/dropbox/dropboxmock"
	"github.com/MKwann7/db-sync-specific-files/internal/state"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

func newTestEngine(t *testing.T, cfg *config.Config, client *dropboxmock.Client) (*Engine, *state.DB) {
	t.Helper()
	dir := t.TempDir()
	db, err := state.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatalf("opening state db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return New(cfg, client, db, testLogger()), db
}

func mapping(name, local, remote string, dir config.Direction, policy config.ConflictPolicy) config.FileMapping {
	return config.FileMapping{
		Name: name, Local: local, Remote: remote, ResolvedLocal: local,
		Direction: dir, ConflictPolicy: policy, DeletePolicy: config.DeletePolicyIgnore,
	}
}

func cfgWith(interval time.Duration, dryRun bool, files ...config.FileMapping) *config.Config {
	return &config.Config{Sync: config.SyncSettings{Interval: interval, DryRun: dryRun, Files: files}}
}

func TestEngine_UploadOnly_InitialPush(t *testing.T) {
	dir := t.TempDir()
	localPath := filepath.Join(dir, "manuscript.docx")
	os.WriteFile(localPath, []byte("local content v1"), 0o644)

	client := dropboxmock.NewClient()
	m := mapping("Manuscript", localPath, "/Books/manuscript.docx", config.DirectionUpload, config.ConflictKeepBoth)
	cfg := cfgWith(time.Minute, false, m)
	engine, _ := newTestEngine(t, cfg, client)

	report := engine.RunOnce(context.Background())
	if report.HasErrors() {
		t.Fatalf("unexpected errors: %+v", report.Results)
	}
	if report.Results[0].Action != ActionUpload {
		t.Fatalf("expected upload action, got %s", report.Results[0].Action)
	}
	got, ok := client.Get("/Books/manuscript.docx")
	if !ok || string(got) != "local content v1" {
		t.Fatalf("remote content = %q, ok=%v", got, ok)
	}
}

func TestEngine_DownloadOnly_InitialPull(t *testing.T) {
	dir := t.TempDir()
	localPath := filepath.Join(dir, "notes.pdf")

	client := dropboxmock.NewClient()
	client.Seed("/References/notes.pdf", []byte("remote notes v1"))

	m := mapping("Notes", localPath, "/References/notes.pdf", config.DirectionDownload, config.ConflictKeepBoth)
	cfg := cfgWith(time.Minute, false, m)
	engine, _ := newTestEngine(t, cfg, client)

	report := engine.RunOnce(context.Background())
	if report.HasErrors() {
		t.Fatalf("unexpected errors: %+v", report.Results)
	}
	if report.Results[0].Action != ActionDownload {
		t.Fatalf("expected download action, got %s", report.Results[0].Action)
	}
	got, err := os.ReadFile(localPath)
	if err != nil || string(got) != "remote notes v1" {
		t.Fatalf("local content = %q, err=%v", got, err)
	}
}

func TestEngine_Bidirectional_LocalChangePropagates(t *testing.T) {
	dir := t.TempDir()
	localPath := filepath.Join(dir, "doc.txt")
	os.WriteFile(localPath, []byte("v1"), 0o644)

	client := dropboxmock.NewClient()
	client.Seed("/doc.txt", []byte("v1"))

	m := mapping("Doc", localPath, "/doc.txt", config.DirectionBidirectional, config.ConflictKeepBoth)
	cfg := cfgWith(time.Minute, false, m)
	engine, _ := newTestEngine(t, cfg, client)

	// First cycle: identical content, establishes baseline, no transfer.
	r1 := engine.RunOnce(context.Background())
	if r1.HasErrors() || r1.Results[0].Action != ActionNone {
		t.Fatalf("expected none action on identical initial sync, got %+v", r1.Results[0])
	}

	// Modify local only.
	os.WriteFile(localPath, []byte("v2-local-edit"), 0o644)
	r2 := engine.RunOnce(context.Background())
	if r2.HasErrors() {
		t.Fatalf("unexpected errors: %+v", r2.Results)
	}
	if r2.Results[0].Action != ActionUpload {
		t.Fatalf("expected upload, got %s (%s)", r2.Results[0].Action, r2.Results[0].Reason)
	}
	got, _ := client.Get("/doc.txt")
	if string(got) != "v2-local-edit" {
		t.Fatalf("remote not updated: %q", got)
	}
}

func TestEngine_Bidirectional_RemoteChangePropagates(t *testing.T) {
	dir := t.TempDir()
	localPath := filepath.Join(dir, "doc.txt")
	os.WriteFile(localPath, []byte("v1"), 0o644)

	client := dropboxmock.NewClient()
	client.Seed("/doc.txt", []byte("v1"))

	m := mapping("Doc", localPath, "/doc.txt", config.DirectionBidirectional, config.ConflictKeepBoth)
	cfg := cfgWith(time.Minute, false, m)
	engine, _ := newTestEngine(t, cfg, client)

	engine.RunOnce(context.Background()) // establish baseline

	client.Seed("/doc.txt", []byte("v2-remote-edit"))
	r := engine.RunOnce(context.Background())
	if r.HasErrors() || r.Results[0].Action != ActionDownload {
		t.Fatalf("expected download, got %+v", r.Results[0])
	}
	got, err := os.ReadFile(localPath)
	if err != nil || string(got) != "v2-remote-edit" {
		t.Fatalf("local not updated: %q, err=%v", got, err)
	}
}

func TestEngine_Bidirectional_SimultaneousChanges_KeepBoth(t *testing.T) {
	dir := t.TempDir()
	localPath := filepath.Join(dir, "doc.txt")
	os.WriteFile(localPath, []byte("v1"), 0o644)

	client := dropboxmock.NewClient()
	client.Seed("/doc.txt", []byte("v1"))

	m := mapping("Doc", localPath, "/doc.txt", config.DirectionBidirectional, config.ConflictKeepBoth)
	cfg := cfgWith(time.Minute, false, m)
	engine, db := newTestEngine(t, cfg, client)

	engine.RunOnce(context.Background()) // baseline

	os.WriteFile(localPath, []byte("local-change"), 0o644)
	client.Seed("/doc.txt", []byte("remote-change"))

	r := engine.RunOnce(context.Background())
	if r.HasErrors() || r.Results[0].Action != ActionConflict {
		t.Fatalf("expected conflict, got %+v", r.Results[0])
	}

	// Local file must remain untouched.
	gotLocal, _ := os.ReadFile(localPath)
	if string(gotLocal) != "local-change" {
		t.Fatalf("local file was modified during keep_both conflict: %q", gotLocal)
	}
	// Remote file must remain untouched.
	gotRemote, _ := client.Get("/doc.txt")
	if string(gotRemote) != "remote-change" {
		t.Fatalf("remote file was modified during keep_both conflict: %q", gotRemote)
	}
	// A conflict copy of the remote version must exist alongside local.
	entries, _ := os.ReadDir(dir)
	foundConflictCopy := false
	for _, e := range entries {
		if e.Name() != "doc.txt" {
			foundConflictCopy = true
			data, _ := os.ReadFile(filepath.Join(dir, e.Name()))
			if string(data) != "remote-change" {
				t.Errorf("conflict copy content = %q, want remote-change", data)
			}
		}
	}
	if !foundConflictCopy {
		t.Fatal("expected a conflict copy file to be created")
	}

	ms, err := db.Get(context.Background(), "/doc.txt")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if ms.Status != state.StatusConflict {
		t.Errorf("status = %q, want conflict", ms.Status)
	}
}

func TestEngine_InitialConflict_BidirectionalDifferingContent(t *testing.T) {
	dir := t.TempDir()
	localPath := filepath.Join(dir, "doc.txt")
	os.WriteFile(localPath, []byte("local-original"), 0o644)

	client := dropboxmock.NewClient()
	client.Seed("/doc.txt", []byte("remote-original"))

	m := mapping("Doc", localPath, "/doc.txt", config.DirectionBidirectional, config.ConflictKeepBoth)
	cfg := cfgWith(time.Minute, false, m)
	engine, _ := newTestEngine(t, cfg, client)

	r := engine.RunOnce(context.Background())
	if r.HasErrors() || r.Results[0].Action != ActionConflict {
		t.Fatalf("expected initial conflict to be reported rather than silently resolved, got %+v", r.Results[0])
	}
	// Neither side should have been silently overwritten.
	gotLocal, _ := os.ReadFile(localPath)
	if string(gotLocal) != "local-original" {
		t.Fatalf("local overwritten: %q", gotLocal)
	}
	gotRemote, _ := client.Get("/doc.txt")
	if string(gotRemote) != "remote-original" {
		t.Fatalf("remote overwritten: %q", gotRemote)
	}
}

func TestEngine_MissingLocalFile_DoesNotDeleteRemote(t *testing.T) {
	dir := t.TempDir()
	localPath := filepath.Join(dir, "doc.txt")
	os.WriteFile(localPath, []byte("v1"), 0o644)

	client := dropboxmock.NewClient()
	client.Seed("/doc.txt", []byte("v1"))

	m := mapping("Doc", localPath, "/doc.txt", config.DirectionBidirectional, config.ConflictKeepBoth)
	cfg := cfgWith(time.Minute, false, m)
	engine, _ := newTestEngine(t, cfg, client)

	engine.RunOnce(context.Background()) // baseline both exist

	os.Remove(localPath)
	r := engine.RunOnce(context.Background())
	if r.HasErrors() {
		t.Fatalf("unexpected errors: %+v", r.Results)
	}
	if r.Results[0].Action != ActionSkipDeletionIgnored {
		t.Fatalf("expected skip_deletion_ignored, got %s", r.Results[0].Action)
	}
	if _, ok := client.Get("/doc.txt"); !ok {
		t.Fatal("remote file must not be deleted when local file disappears")
	}
}

func TestEngine_MissingRemoteFile_DoesNotDeleteLocal(t *testing.T) {
	dir := t.TempDir()
	localPath := filepath.Join(dir, "doc.txt")
	os.WriteFile(localPath, []byte("v1"), 0o644)

	client := dropboxmock.NewClient()
	client.Seed("/doc.txt", []byte("v1"))

	m := mapping("Doc", localPath, "/doc.txt", config.DirectionBidirectional, config.ConflictKeepBoth)
	cfg := cfgWith(time.Minute, false, m)
	engine, _ := newTestEngine(t, cfg, client)

	engine.RunOnce(context.Background()) // baseline

	// Simulate remote deletion by seeding an empty mock without the file:
	// easiest is to create a fresh mock without the path and reuse the same
	// engine (metadata lookup will report IsDeleted).
	freshClient := dropboxmock.NewClient()
	engine.client = freshClient

	r := engine.RunOnce(context.Background())
	if r.HasErrors() {
		t.Fatalf("unexpected errors: %+v", r.Results)
	}
	if r.Results[0].Action != ActionSkipDeletionIgnored {
		t.Fatalf("expected skip_deletion_ignored, got %s", r.Results[0].Action)
	}
	if _, err := os.Stat(localPath); err != nil {
		t.Fatalf("local file must not be deleted when remote file disappears: %v", err)
	}
}

func TestEngine_DryRun_NeverModifiesAnything(t *testing.T) {
	dir := t.TempDir()
	localPath := filepath.Join(dir, "doc.txt")
	os.WriteFile(localPath, []byte("local-only"), 0o644)

	client := dropboxmock.NewClient()
	m := mapping("Doc", localPath, "/doc.txt", config.DirectionBidirectional, config.ConflictKeepBoth)
	cfg := cfgWith(time.Minute, true /* dry run */, m)
	engine, db := newTestEngine(t, cfg, client)

	r := engine.RunOnce(context.Background())
	if r.HasErrors() {
		t.Fatalf("unexpected errors: %+v", r.Results)
	}
	if r.Results[0].Action != ActionUpload {
		t.Fatalf("expected the decision to be 'upload', got %s", r.Results[0].Action)
	}

	if _, ok := client.Get("/doc.txt"); ok {
		t.Fatal("dry-run must not upload to remote")
	}
	ms, err := db.Get(context.Background(), "/doc.txt")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if ms.Status != state.StatusNeverSynced {
		t.Fatalf("dry-run must not advance synchronization baseline, got status=%s", ms.Status)
	}
}

func TestEngine_InterruptedDownload_PreservesExistingLocalFile(t *testing.T) {
	dir := t.TempDir()
	localPath := filepath.Join(dir, "doc.txt")
	os.WriteFile(localPath, []byte("good-local-content"), 0o644)

	client := dropboxmock.NewClient()
	client.Seed("/doc.txt", []byte("remote-v1"))

	m := mapping("Doc", localPath, "/doc.txt", config.DirectionDownload, config.ConflictKeepBoth)
	cfg := cfgWith(time.Minute, false, m)
	engine, _ := newTestEngine(t, cfg, client)

	// Simulate an API failure mid-cycle (e.g. connection drop during
	// download) by making the mock client return an error.
	client.Err = assertErr{}
	r := engine.RunOnce(context.Background())
	if !r.HasErrors() {
		t.Fatal("expected a failure to be reported")
	}

	got, err := os.ReadFile(localPath)
	if err != nil || string(got) != "good-local-content" {
		t.Fatalf("existing local file must survive an interrupted download: %q, err=%v", got, err)
	}
}

type assertErr struct{}

func (assertErr) Error() string { return "simulated transient dropbox api failure" }
