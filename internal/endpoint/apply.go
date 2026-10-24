package endpoint

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	// Each backup holds every password at that time, including those of users
	// deleted later, so only the most recent ones are kept.
	backupRetention = 20

	// backupLayout names backups by UTC time. It has a fixed width, so names
	// sort chronologically.
	backupLayout = "20060102T150405.000000000Z"
)

func backupFileName(t time.Time) string {
	return t.UTC().Format(backupLayout) + ".json"
}

func isBackupFileName(name string) bool {
	stamp, ok := strings.CutSuffix(name, ".json")
	if !ok {
		return false
	}
	t, err := time.Parse(backupLayout, stamp)
	// Round-tripping rejects names that parse but are not in canonical form.
	return err == nil && backupFileName(t) == name
}

// pruneBackups deletes the oldest automatic backups so that keep remain,
// counting current. current, the backup just written, is never deleted: after
// the clock moves backwards its name can sort before older backups. Other
// files are left untouched.
func pruneBackups(dir string, keep int, current string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	var names []string
	for _, e := range entries {
		if !e.Type().IsRegular() || !isBackupFileName(e.Name()) {
			continue
		}
		if e.Name() == current {
			keep--
			continue
		}
		names = append(names, e.Name())
	}
	keep = max(keep, 0)
	if len(names) <= keep {
		return nil
	}
	sort.Strings(names)
	for _, name := range names[:len(names)-keep] {
		if err := os.Remove(filepath.Join(dir, name)); err != nil {
			return err
		}
	}
	return nil
}

// transaction captures the exact pre-apply files for backups and crash recovery.
// Both payloads contain credentials and must remain private.
type transaction struct {
	Credentials []byte `json:"credentials"`
	State       []byte `json:"state"`
}

// recoverTransaction runs before state is loaded or the endpoint is started.
// Keeping the journal until both writes complete makes recovery repeatable.
func recoverTransaction(stateDir, credentials string) error {
	path := filepath.Join(stateDir, "apply.json")
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return errors.New("cannot inspect interrupted apply")
	}
	var tx transaction
	if json.Unmarshal(b, &tx) != nil || len(tx.Credentials) == 0 || len(tx.State) == 0 {
		return errors.New("invalid interrupted apply journal")
	}
	if atomicWrite(credentials, tx.Credentials) != nil ||
		atomicWrite(filepath.Join(stateDir, "state.json"), tx.State) != nil {
		return errors.New("cannot recover interrupted apply")
	}
	if durableRemove(path) != nil {
		return errors.New("cannot finish apply recovery")
	}
	return nil
}

// apply replaces active credentials and restarts TrustTunnel as one recoverable
// operation. The caller must hold a.mu throughout, including any rollback.
func (a *Agent) apply(ctx context.Context) error {
	if err := a.store.drift(); err != nil {
		return err
	}
	if !a.store.pending() {
		return nil
	}

	oldBytes, err := os.ReadFile(a.store.path)
	if err != nil {
		return errors.New("cannot read active credentials")
	}
	oldState, err := os.ReadFile(a.store.statePath)
	if err != nil {
		return errors.New("cannot read saved state")
	}
	newBytes, err := a.store.draft.encode(true)
	if err != nil {
		return err
	}

	backupDir := filepath.Join(a.config.StateDir, "backups")
	if os.MkdirAll(backupDir, 0700) != nil {
		return errors.New("cannot create backup directory")
	}
	backupName := backupFileName(time.Now())
	backup := filepath.Join(backupDir, backupName)
	tx, err := json.Marshal(transaction{Credentials: oldBytes, State: oldState})
	if err != nil {
		return errors.New("cannot prepare backup")
	}
	if atomicWrite(backup, tx) != nil {
		return errors.New("cannot save credentials backup")
	}
	// Prune on every attempt, including ones that fail later: each attempt
	// adds a backup. The one just written is always kept.
	if pruneBackups(backupDir, backupRetention, backupName) != nil {
		_, _ = a.logs.Write([]byte("[agent] Cannot remove old credentials backups\n"))
	}
	// Write the recovery journal before touching active credentials. A journal
	// left on disk tells the next startup to restore the original file pair.
	journal := filepath.Join(a.config.StateDir, "apply.json")
	if atomicWrite(journal, tx) != nil {
		return errors.New("cannot create apply journal")
	}

	// Rollback restores the applied state while retaining the pending draft so
	// the administrator can correct it and retry.
	oldApplied, oldHash := a.store.applied, a.store.hash
	rollback := func(cause error) error {
		_ = a.process.stop()
		if atomicWrite(a.store.path, oldBytes) != nil {
			return errors.New("apply failed and credentials rollback failed; restore the saved backup")
		}
		a.store.applied = oldApplied
		a.store.hash = oldHash
		if atomicWrite(a.store.statePath, oldState) != nil {
			return errors.New("apply failed and state rollback failed; restore the saved backup")
		}
		// A disconnected HTTP client must not cancel recovery. Restart still
		// bounds process shutdown and readiness with the configured timeouts.
		if err := a.process.restart(context.Background()); err != nil {
			return errors.New("apply failed; previous settings restored but endpoint did not restart")
		}
		_ = durableRemove(journal)
		return errors.New(cause.Error() + "; previous settings restored")
	}

	if atomicWrite(a.store.path, newBytes) != nil {
		return rollback(errors.New("cannot write new credentials"))
	}
	if err := a.process.restart(ctx); err != nil {
		return rollback(err)
	}

	// Publish the new applied snapshot only after the replacement process has
	// passed readiness. Keep the journal until that snapshot is durable.
	a.store.applied = clone(a.store.draft)
	a.store.hash = digest(newBytes)
	if err := a.store.persist(); err != nil {
		return rollback(err)
	}
	// Durable journal removal is the commit boundary: startup will now retain
	// the new credentials and state instead of recovering the previous pair.
	if durableRemove(journal) != nil {
		return rollback(errors.New("cannot finish apply transaction"))
	}
	return nil
}
