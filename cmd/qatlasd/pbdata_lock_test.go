package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gofrs/flock"
)

// ---------------------------------------------------------------------------
// pb_data flock — single-process safety on PocketBase's SQLite store
// ---------------------------------------------------------------------------
//
// PocketBase + SQLite don't serialize multi-writer correctly across
// processes; acquirePBDataLock is our last line of defence against an
// operator accidentally pointing two qatlasd instances at the same
// pb_data. See pbdata_lock.go for the full design rationale.

func TestAcquirePBDataLock_CreatesLockFileAndHoldsExclusive(t *testing.T) {
	dir := t.TempDir()

	lock, err := acquirePBDataLock(dir)
	if err != nil {
		t.Fatalf("acquirePBDataLock: %v", err)
	}
	defer lock.Unlock()

	lockFile := filepath.Join(dir, pbDataLockFilename)
	if _, err := os.Stat(lockFile); err != nil {
		t.Errorf("lock file %s not created: %v", lockFile, err)
	}
	if !lock.Locked() {
		t.Error("returned *flock.Flock claims not locked, but acquirePBDataLock said success")
	}
}

func TestAcquirePBDataLock_SecondCallerRejectedWithTypedError(t *testing.T) {
	dir := t.TempDir()

	first, err := acquirePBDataLock(dir)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	defer first.Unlock()

	// In-process second TryLock must also fail (gofrs/flock honours
	// the in-process state in addition to the kernel flock).
	second, err := acquirePBDataLock(dir)
	if err == nil {
		if second != nil {
			_ = second.Unlock()
		}
		t.Fatal("second acquire unexpectedly succeeded while first still held the lock")
	}

	var locked *pbDataLockedError
	if !errors.As(err, &locked) {
		t.Errorf("expected *pbDataLockedError, got %T: %v", err, err)
	}
	if locked != nil && locked.dir != dir {
		t.Errorf("typed error dir = %q, want %q", locked.dir, dir)
	}
	// Error message should give the operator everything they need to
	// diagnose without consulting docs: pb_data path, lock path,
	// escape hatch (env var name).
	if !strings.Contains(err.Error(), dir) {
		t.Errorf("error %q should mention pb_data dir %q", err.Error(), dir)
	}
	if !strings.Contains(err.Error(), "QATLAS_SKIP_PB_DATA_LOCK") {
		t.Errorf("error %q should document the bypass env var", err.Error())
	}
}

func TestAcquirePBDataLock_ReleasingAllowsReacquire(t *testing.T) {
	dir := t.TempDir()

	first, err := acquirePBDataLock(dir)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	if err := first.Unlock(); err != nil {
		t.Fatalf("unlock: %v", err)
	}

	// Now re-acquire: should succeed since the prior holder released.
	second, err := acquirePBDataLock(dir)
	if err != nil {
		t.Fatalf("re-acquire after unlock: %v", err)
	}
	defer second.Unlock()
}

func TestAcquirePBDataLock_CreatesPBDataDirIfMissing(t *testing.T) {
	// Fresh-install path: $PBDataDir doesn't exist yet. The lock
	// helper must mkdir -p so we don't fail on operators who let the
	// XDG default land on a previously-empty machine.
	tmp := t.TempDir()
	missing := filepath.Join(tmp, "does-not-exist-yet", "pb_data")

	lock, err := acquirePBDataLock(missing)
	if err != nil {
		t.Fatalf("acquire on missing dir: %v", err)
	}
	defer lock.Unlock()

	info, err := os.Stat(missing)
	if err != nil || !info.IsDir() {
		t.Errorf("pb_data dir %s should have been created, stat err=%v", missing, err)
	}
}

func TestAcquirePBDataLock_RejectsEmptyDir(t *testing.T) {
	if _, err := acquirePBDataLock(""); err == nil {
		t.Error("empty dir should be a typed error, not a silent success")
	}
}

func TestPBDataLockSkipRequested_RecognisesTruthyValues(t *testing.T) {
	cases := map[string]bool{
		"1":     true,
		"true":  true,
		"True":  true, // case-insensitive
		"YES":   true,
		"on":    true,
		"y":     true,
		"t":     true,
		"":      false,
		"0":     false,
		"false": false,
		"no":    false,
		"off":   false,
		// Unrecognised non-empty strings default to false — never
		// silently treat ambiguous values as opt-in.
		"please-skip": false,
	}
	for input, want := range cases {
		t.Setenv("QATLAS_SKIP_PB_DATA_LOCK", input)
		if got := pbDataLockSkipRequested(); got != want {
			t.Errorf("pbDataLockSkipRequested() with %q = %v, want %v", input, got, want)
		}
	}
}

func TestPBDataLockedError_MessageSurfacesEscapeHatch(t *testing.T) {
	// Defensive guard: the error message is the operator's primary
	// debugging surface. If a future refactor strips the escape-hatch
	// hint, the diagnostic becomes useless.
	err := &pbDataLockedError{path: "/tmp/x/qatlasd.lock", dir: "/tmp/x"}
	msg := err.Error()
	for _, expected := range []string{
		"/tmp/x",                       // pb_data dir
		"/tmp/x/qatlasd.lock",          // lock file path
		"QATLAS_SKIP_PB_DATA_LOCK",     // escape hatch
		"QATLAS_PB_DATA_DIR",           // "use a different dir" hint
		"NOT for production",           // explicit warning on escape hatch
	} {
		if !strings.Contains(msg, expected) {
			t.Errorf("error message missing %q; got: %s", expected, msg)
		}
	}
}

// Sanity: the flock library we depend on does what we think it does
// (gofrs/flock returned false from TryLock when contended, no error).
// Catches dependency upgrade surprises.
func TestGofrsFlockContractMatchesAssumptions(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "probe.lock")

	a := flock.New(target)
	if locked, err := a.TryLock(); err != nil || !locked {
		t.Fatalf("first TryLock should succeed: locked=%v err=%v", locked, err)
	}
	defer a.Unlock()

	b := flock.New(target)
	locked, err := b.TryLock()
	if err != nil {
		t.Fatalf("second TryLock returned error (assumption broken): %v", err)
	}
	if locked {
		t.Error("second TryLock should report locked=false when first still holds (assumption broken)")
	}
}

// ---------------------------------------------------------------------------
// probePBDataLockAvailable — advisory probe used by mutating subcommands
// ---------------------------------------------------------------------------

func TestProbePBDataLockAvailable_TrueWhenUnheld(t *testing.T) {
	dir := t.TempDir()
	if !probePBDataLockAvailable(dir) {
		t.Error("expected probe=true on a fresh pb_data with no holder")
	}
	// Probe must not leave a held lock behind (otherwise serve would
	// fail to start after a subcommand ran).
	lock, err := acquirePBDataLock(dir)
	if err != nil {
		t.Fatalf("acquire after probe should succeed: %v", err)
	}
	_ = lock.Unlock()
}

func TestProbePBDataLockAvailable_FalseWhenHeld(t *testing.T) {
	dir := t.TempDir()
	holder, err := acquirePBDataLock(dir)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer holder.Unlock()

	if probePBDataLockAvailable(dir) {
		t.Error("expected probe=false while another holder still owns the lock")
	}
}

func TestProbePBDataLockAvailable_TrueOnEmptyDir(t *testing.T) {
	// Defensive: empty pbDataDir means "caller didn't configure a
	// path"; the probe should return true so subcommands don't warn
	// spuriously.
	if !probePBDataLockAvailable("") {
		t.Error("probe on empty dir should silently return true (no path to probe)")
	}
}
