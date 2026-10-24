package endpoint

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/tavvet/TunnelaPanel/internal/protocol"
)

func TestPruneBackupsKeepsNewestAndIgnoresOtherFiles(t *testing.T) {
	dir := t.TempDir()
	var names []string
	for i := 0; i < 25; i++ {
		name := fmt.Sprintf("20260101T0000%02d.000000000Z.json", i)
		names = append(names, name)
		if err := os.WriteFile(filepath.Join(dir, name), []byte("{}"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "manual-copy.json"), []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}

	if err := pruneBackups(dir, 20, ""); err != nil {
		t.Fatal(err)
	}

	for i, name := range names {
		_, err := os.Stat(filepath.Join(dir, name))
		if i < 5 && !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("old backup %s was kept", name)
		}
		if i >= 5 && err != nil {
			t.Fatalf("recent backup %s was removed", name)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "manual-copy.json")); err != nil {
		t.Fatal("unrelated file was removed")
	}
}

func TestBackupFileNamesRoundTrip(t *testing.T) {
	name := backupFileName(time.Date(2026, 10, 24, 12, 0, 0, 5, time.UTC))
	if name != "20261024T120000.000000005Z.json" || !isBackupFileName(name) {
		t.Fatal("generated backup name is not recognized:", name)
	}
	for _, other := range []string{"manual-copy.json", "20261024T120000Z.json", "20261024T120000.000000005Z.json.bak"} {
		if isBackupFileName(other) {
			t.Fatal("non-backup file treated as a backup:", other)
		}
	}
}

func TestPruneBackupsKeepsCurrentAfterClockMovesBack(t *testing.T) {
	dir := t.TempDir()
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 3; i++ {
		if err := os.WriteFile(filepath.Join(dir, backupFileName(start.Add(time.Duration(i)*time.Hour))), []byte("{}"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	// Written after the clock was set back: it sorts first.
	current := backupFileName(start.Add(-time.Hour))
	if err := os.WriteFile(filepath.Join(dir, current), []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}

	if err := pruneBackups(dir, 2, current); err != nil {
		t.Fatal(err)
	}

	entries, _ := os.ReadDir(dir)
	if len(entries) != 2 {
		t.Fatalf("kept %d backups", len(entries))
	}
	if _, err := os.Stat(filepath.Join(dir, current)); err != nil {
		t.Fatal("the backup just written was deleted")
	}
	if _, err := os.Stat(filepath.Join(dir, backupFileName(start.Add(2*time.Hour)))); err != nil {
		t.Fatal("the newest older backup was deleted")
	}
}

func TestFailedApplyStillPrunesBackups(t *testing.T) {
	a := newTestAgent(t)
	dir := filepath.Join(a.config.StateDir, "backups")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	start := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < backupRetention+5; i++ {
		if err := os.WriteFile(filepath.Join(dir, backupFileName(start.Add(time.Duration(i)*time.Second))), []byte("{}"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.store.change("PATCH", protocol.UserChange{Username: "existing", Password: "fail-startup-123"}); err != nil {
		t.Fatal(err)
	}
	if err := a.apply(context.Background()); err == nil {
		t.Fatal("expected the apply to fail")
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != backupRetention {
		t.Fatalf("failed apply left %d backups: %v", len(entries), err)
	}
	if _, err := os.Stat(filepath.Join(dir, backupFileName(start))); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("oldest backup was kept")
	}
}

func TestCertificateFilesOutsideTheMountAreReported(t *testing.T) {
	dir := t.TempDir()
	cert := filepath.Join(dir, "fullchain.pem")
	if err := os.WriteFile(cert, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	hosts := fmt.Sprintf("[[main_hosts]]\nhostname = \"a\"\ncert_chain_path = %q\nprivate_key_path = %q\n", cert, cert)
	if err := os.WriteFile(filepath.Join(dir, "hosts.toml"), []byte(hosts), 0600); err != nil {
		t.Fatal(err)
	}
	if err := checkCertificateFiles(dir); err != nil {
		t.Fatal(err)
	}

	hosts += "\n[[ping_hosts]]\nhostname = \"b\"\ncert_chain_path = \"/etc/letsencrypt/live/other.example/fullchain.pem\"\n"
	if err := os.WriteFile(filepath.Join(dir, "hosts.toml"), []byte(hosts), 0600); err != nil {
		t.Fatal(err)
	}
	if err := checkCertificateFiles(dir); err == nil || !strings.Contains(err.Error(), "TT_CERT_NAME") {
		t.Fatalf("missing lineage not reported: %v", err)
	}
}

func TestOptionLikeUsernamesAreRejected(t *testing.T) {
	s := testStore(t)
	err := s.change("POST", protocol.UserChange{Username: "-a", Password: "valid-password-123"})
	if err == nil {
		t.Fatal("added a username that TrustTunnel would parse as an option")
	}

	// The check runs before the binary, which does not exist here.
	p := &supervisor{config: Config{Binary: "/nonexistent"}}
	if _, err := p.export(context.Background(), "--format"); err == nil {
		t.Fatal("exported an option-like username")
	}
}

func TestControlSocketIsGroupAccessibleOnly(t *testing.T) {
	a := newTestAgent(t)
	// macOS limits Unix socket paths to 104 bytes, so avoid the long TempDir.
	base, err := os.MkdirTemp("/tmp", "tp-sock-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(base) })
	a.config.Socket = filepath.Join(base, "run", "control.sock")

	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- a.Serve(ctx) }()
	defer func() {
		cancel()
		if err := <-served; err != nil {
			t.Error(err)
		}
	}()

	var info os.FileInfo
	for deadline := time.Now().Add(2 * time.Second); ; {
		info, err = os.Stat(a.config.Socket)
		if err == nil && info.Mode().Perm() == 0660 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("socket not secured: %v %v", info, err)
		}
		time.Sleep(10 * time.Millisecond)
	}

	dir, err := os.Stat(filepath.Dir(a.config.Socket))
	if err != nil || dir.Mode().Perm() != 0710 {
		t.Fatalf("socket directory mode: %v %v", dir, err)
	}
	for _, fi := range []os.FileInfo{info, dir} {
		if int(fi.Sys().(*syscall.Stat_t).Gid) != os.Getegid() {
			t.Fatal("socket access is not granted to the agent's group")
		}
	}
}
