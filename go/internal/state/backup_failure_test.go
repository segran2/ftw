package state

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The home box's backup on 16 October 2026 copied on the SD card, ran into
// the live time limit and reported only "context deadline exceeded".
func TestLiveBackupPastItsLimitNamesTheLimitAndTable(t *testing.T) {
	s := freshStore(t)
	if err := s.BulkRecordHistory(pacedBackupPoints(5000)); err != nil {
		t.Fatal(err)
	}
	orig := liveBackupTimeout
	t.Cleanup(func() { liveBackupTimeout = orig })
	liveBackupTimeout = time.Second
	// Each batch waits as a commit on the SD card did; four outlast the limit.
	s.backupPause = func(ctx context.Context) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(300 * time.Millisecond):
			return nil
		}
	}
	err := s.BackupToCompressed(filepath.Join(t.TempDir(), "full.gz"))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("backup past its limit = %v", err)
	}
	for _, want := range []string{"live backup reached its time limit of 1s", "copy history_hot"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("reason %q does not say %q", err, want)
		}
	}
}

// A deadline that lands inside a batch rolls back the copy's transaction and
// closes its statement; the next row then fails with "statement is closed".
// The reason must still carry the context's error (CI run 39759536215).
func TestBackupCopyCancelledInsideABatchKeepsTheContextError(t *testing.T) {
	dest, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "dest.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer dest.Close()
	if _, err := dest.Exec(`CREATE TABLE t(a INTEGER)`); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	scan := func(_ context.Context, _ historyQueryer, _ string, visit func([]any) error) (int64, error) {
		if err := visit([]any{int64(1)}); err != nil {
			return 0, err
		}
		cancel()
		// database/sql rolls the transaction back on its own goroutine and
		// then returns the connection.
		deadline := time.Now().Add(5 * time.Second)
		for dest.Stats().InUse > 0 {
			if time.Now().After(deadline) {
				return 0, errors.New("the cancelled transaction kept its connection")
			}
			time.Sleep(time.Millisecond)
		}
		return 2, visit([]any{int64(2)})
	}
	err = copyVerifiedTable(ctx, nil, dest, "t", scan, nil)
	if !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), "copy t") {
		t.Fatalf("copy cancelled inside a batch = %v", err)
	}
}

func TestCanceledBackupIsNotBlamedOnTheLimit(t *testing.T) {
	s := freshStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err := s.BackupWithConfigurationContext(ctx, filepath.Join(t.TempDir(), "full.gz"), nil)
	if !errors.Is(err, context.Canceled) || strings.Contains(err.Error(), "time limit") {
		t.Fatalf("canceled backup = %v", err)
	}
}

func TestBackupScratchOnDiskNamesScratchLeftInShm(t *testing.T) {
	shm := t.TempDir()
	orig := backupShmDir
	t.Cleanup(func() { backupShmDir = orig })
	backupShmDir = shm
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	defer slog.SetDefault(previous)

	path, cleanup := backupScratchFile(filepath.Join(t.TempDir(), "full.gz"), 1<<20)
	cleanup()
	if !strings.HasPrefix(path, shm) || logs.Len() != 0 {
		t.Fatalf("fitting scratch = %s, logs %q", path, logs.String())
	}

	// A backup stopped by a restart leaves its scratch until reboot.
	if err := os.WriteFile(filepath.Join(shm, "ftw-backup-4218622654.raw"), make([]byte, 4096), 0o600); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), "full.gz")
	path, cleanup = backupScratchFile(dst, 1<<50)
	defer cleanup()
	if path != dst+".raw.tmp" {
		t.Fatalf("oversized scratch = %s", path)
	}
	for _, want := range []string{"copying on the data disk", "backup_scratch_in_shm_bytes=4096"} {
		if !strings.Contains(logs.String(), want) {
			t.Fatalf("log %q does not say %q", logs.String(), want)
		}
	}
}
