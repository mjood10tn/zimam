package changes

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"zimam/agent/contract"
	"zimam/agent/internal/paths"
	"zimam/agent/internal/sysexec"
)

const id1 = contract.ChangeID("01JD5Z3W4X6Y7Z8A9B0C1D2E3F")

var now = time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)

// testKind records what its hooks were called with.
type testKind struct {
	reverted, confirmed [][]byte
	revertErr           error
}

func setup(t *testing.T, scripts ...func(paths.Paths) sysexec.Script) (sysexec.Deps, *sysexec.FakeRunner, *testKind) {
	t.Helper()
	p := paths.Paths{Root: t.TempDir()}
	if err := os.MkdirAll(p.LocksDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	run := sysexec.NewFakeRunner()
	for _, s := range scripts {
		run.Add(s(p))
	}
	k := &testKind{}
	old := kinds
	kinds = map[Kind]Hooks{"test": {
		Revert: func(_ context.Context, _ sysexec.Deps, prev []byte) error {
			k.reverted = append(k.reverted, prev)
			return k.revertErr
		},
		Confirm: func(_ context.Context, _ sysexec.Deps, prev []byte) error {
			k.confirmed = append(k.confirmed, prev)
			return nil
		},
	}}
	oldID := newID
	newID = func(time.Time) contract.ChangeID { return id1 }
	t.Cleanup(func() { kinds, newID = old, oldID })
	return sysexec.Deps{Paths: p, Run: run, Now: func() time.Time { return now }, Logf: t.Logf}, run, k
}

func startTimer(p paths.Paths) sysexec.Script {
	return sysexec.Script{Path: "/usr/bin/systemd-run", Args: []string{
		"--unit=zimam-revert-" + string(id1), "--on-active=120s", "--timer-property=AccuracySec=1s", "--quiet", "--",
		p.AgentBinary(), "change", "revert", string(id1),
	}}
}

func stopTimer(paths.Paths) sysexec.Script {
	return sysexec.Script{Path: "/usr/bin/systemctl", Args: []string{"stop", "zimam-revert-" + string(id1) + ".timer"}}
}

func TestBeginWritesRecordAndStartsTimer(t *testing.T) {
	d, run, _ := setup(t, startTimer)
	id, deadline, err := Begin(context.Background(), d, "test", []byte("before"), 120*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if id != id1 || !deadline.Equal(now.Add(120*time.Second)) {
		t.Errorf("Begin = %s, %v", id, deadline)
	}
	fi, err := os.Stat(d.Paths.ChangeRecord(id1))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("record: %v, %v", fi, err)
	}
	r, err := read(d.Paths, id1)
	if err != nil || r.Kind != "test" || string(r.Previous) != "before" || !r.Deadline.Equal(deadline) {
		t.Errorf("record = %+v, %v", r, err)
	}
	if calls := run.Calls(); len(calls) != 1 {
		t.Errorf("calls = %v", calls)
	}
}

func TestBeginWithoutTimerLeavesNoRecord(t *testing.T) {
	failing := func(p paths.Paths) sysexec.Script { s := startTimer(p); s.Result.ExitCode = 1; return s }
	d, _, _ := setup(t, failing)
	if _, _, err := Begin(context.Background(), d, "test", nil, 120*time.Second); err == nil {
		t.Fatal("Begin succeeded without its revert timer")
	}
	if ids, _ := pending(d.Paths); len(ids) != 0 {
		t.Errorf("left records %v", ids)
	}
}

func TestSecondPendingChangeIsBusy(t *testing.T) {
	d, _, _ := setup(t, startTimer)
	if _, _, err := Begin(context.Background(), d, "test", nil, 120*time.Second); err != nil {
		t.Fatal(err)
	}
	_, _, err := Begin(context.Background(), d, "test", nil, 120*time.Second)
	var ae *contract.AgentError
	if !errors.As(err, &ae) || ae.Code != contract.ErrBusy {
		t.Errorf("second Begin = %v, want busy", err)
	}
}

func TestUnknownKindRefused(t *testing.T) {
	d, _, _ := setup(t)
	if _, _, err := Begin(context.Background(), d, "nope", nil, time.Minute); err == nil {
		t.Error("Begin accepted a kind without hooks")
	}
}

func TestConfirmStopsTimerAndDeletesRecord(t *testing.T) {
	d, run, k := setup(t, startTimer, stopTimer)
	if _, _, err := Begin(context.Background(), d, "test", []byte("before"), 120*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := Confirm(context.Background(), d, id1); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(d.Paths.ChangeRecord(id1)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("record still there: %v", err)
	}
	if len(k.confirmed) != 1 || string(k.confirmed[0]) != "before" || len(k.reverted) != 0 {
		t.Errorf("hooks: confirmed %q, reverted %q", k.confirmed, k.reverted)
	}
	if last := run.Calls()[len(run.Calls())-1]; last.Path != "/usr/bin/systemctl" {
		t.Errorf("last call = %v, want the timer stopped", last)
	}
}

func TestConfirmAfterDeadlineIsNotFound(t *testing.T) {
	d, _, k := setup(t, startTimer)
	if _, _, err := Begin(context.Background(), d, "test", nil, 120*time.Second); err != nil {
		t.Fatal(err)
	}
	d.Now = func() time.Time { return now.Add(121 * time.Second) }
	var ae *contract.AgentError
	if err := Confirm(context.Background(), d, id1); !errors.As(err, &ae) || ae.Code != contract.ErrNotFound {
		t.Errorf("late Confirm = %v, want not_found", err)
	}
	if len(k.confirmed) != 0 {
		t.Error("a late confirm ran the confirm hook")
	}
}

func TestRevertRestoresAndDeletesRecord(t *testing.T) {
	d, _, k := setup(t, startTimer, stopTimer)
	if _, _, err := Begin(context.Background(), d, "test", []byte("before"), 120*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := Revert(context.Background(), d, id1); err != nil {
		t.Fatal(err)
	}
	if len(k.reverted) != 1 || string(k.reverted[0]) != "before" {
		t.Errorf("reverted %q", k.reverted)
	}
	if ids, _ := pending(d.Paths); len(ids) != 0 {
		t.Errorf("records left: %v", ids)
	}
}

func TestFailedRevertKeepsRecordForMaintenance(t *testing.T) {
	d, _, k := setup(t, startTimer, stopTimer)
	k.revertErr = errors.New("ufw failed")
	if _, _, err := Begin(context.Background(), d, "test", nil, 120*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := Revert(context.Background(), d, id1); err == nil {
		t.Fatal("Revert hid the hook's failure")
	}
	if ids, _ := pending(d.Paths); !slices.Equal(ids, []contract.ChangeID{id1}) {
		t.Errorf("records = %v, want the change kept for another try", ids)
	}
}

func TestConfirmAfterRevertIsNotFound(t *testing.T) {
	d, _, _ := setup(t, startTimer, stopTimer)
	if _, _, err := Begin(context.Background(), d, "test", nil, 120*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := Revert(context.Background(), d, id1); err != nil {
		t.Fatal(err)
	}
	var ae *contract.AgentError
	if err := Confirm(context.Background(), d, id1); !errors.As(err, &ae) || ae.Code != contract.ErrNotFound {
		t.Errorf("Confirm after revert = %v, want not_found", err)
	}
	// The timer that already fired finds nothing left to do.
	if err := Revert(context.Background(), d, id1); err != nil {
		t.Errorf("second Revert = %v, want nil", err)
	}
}

func TestRevertOverdueOnlyPastDeadline(t *testing.T) {
	d, _, k := setup(t, startTimer, stopTimer)
	if _, _, err := Begin(context.Background(), d, "test", nil, 120*time.Second); err != nil {
		t.Fatal(err)
	}
	if ids, err := RevertOverdue(context.Background(), d); err != nil || len(ids) != 0 {
		t.Fatalf("before the deadline: %v, %v", ids, err)
	}
	// A reboot dropped the transient timer; maintenance finds the record.
	d.Now = func() time.Time { return now.Add(10 * time.Minute) }
	ids, err := RevertOverdue(context.Background(), d)
	if err != nil || !slices.Equal(ids, []contract.ChangeID{id1}) || len(k.reverted) != 1 {
		t.Errorf("RevertOverdue = %v, %v; reverted %d", ids, err, len(k.reverted))
	}
}

func TestRecordsIgnoreStrayFiles(t *testing.T) {
	d, _, _ := setup(t)
	if err := os.MkdirAll(d.Paths.ChangesDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"notes.txt", "x.json", "01JD5Z3W4X6Y7Z8A9B0C1D2E3F.json.tmp"} {
		if err := os.WriteFile(filepath.Join(d.Paths.ChangesDir(), name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if ids, err := pending(d.Paths); err != nil || len(ids) != 0 {
		t.Errorf("pending = %v, %v", ids, err)
	}
}
