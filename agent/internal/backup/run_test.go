package backup

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"zimam/agent/contract"
	"zimam/agent/internal/helper"
	"zimam/agent/internal/sysexec"
)

const runID = contract.BackupID("20261123T033000Z") // newEnv's clock

// runEnv has planEnv's sites and destination, the nightly plan saved
// (shop's files and s_shop_main), a good dump and a helper with an active
// release.
func runEnv(t *testing.T, dump sysexec.Script) (env, RunEnv) {
	t.Helper()
	e := planEnv(t, dump)
	if err := savePlans(e.d.Paths, []contract.BackupPlanParams{*nightlyPlan()}); err != nil {
		t.Fatal(err)
	}
	h := &fakeHelper{data: "site tar", stats: map[string]helper.FileInfo{
		"site/current": {Mode: fs.ModeSymlink | 0o777, Target: "releases/initial"},
	}}
	lookup := func(name string) (helper.Account, error) {
		return helper.Account{Name: name, UID: 1001, GID: 1001, Home: "/home/" + name}, nil
	}
	return e, RunEnv{Helper: h, Lookup: lookup, LogGID: -1}
}

func rcats(e env) []string {
	var out []string
	for _, c := range e.rclone.calls {
		if c[0] == "rcat" {
			out = append(out, c[1])
		}
	}
	return out
}

func readRecord(t *testing.T, e env, id contract.BackupID) contract.BackupRunRecord {
	t.Helper()
	var r contract.BackupRunRecord
	data, err := os.ReadFile(e.d.Paths.BackupRunRecord("nightly", id))
	if err != nil {
		t.Fatal(err)
	}
	if err := contract.DecodeStrict(data, &r); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestRunRecordsEachPart(t *testing.T) {
	e, re := runEnv(t, dumpScript(goodDump, 0))
	res, err := RunPlan(context.Background(), e.d, re, "nightly", contract.TriggerTimer)
	if err != nil {
		t.Fatal(err)
	}
	dir := "offsite:nightly/20261123T033000Z/"
	if got := rcats(e); !slices.Equal(got, []string{dir + "db-s_shop_main.sql.gz", dir + "files-shop.tar.gz", dir + "manifest.json"}) {
		t.Errorf("uploaded %q, want the parts and the manifest last", got)
	}
	if res.Backup != runID || len(res.Parts) != 2 || res.Parts[0].Part != "db-s_shop_main" || res.Parts[1].Part != "files-shop" {
		t.Errorf("result %+v", res)
	}
	rec := readRecord(t, e, runID)
	if rec.Status != contract.BackupSucceeded || rec.Trigger != contract.TriggerTimer || rec.FinishedAt == nil ||
		!slices.Equal(rec.Parts, res.Parts) || rec.Error != "" {
		t.Errorf("record %+v", rec)
	}
	if fi, err := os.Stat(e.d.Paths.BackupRunRecord("nightly", runID)); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("record file %v, %v; want 0600", fi, err)
	}
	log, err := os.ReadFile(e.d.Paths.BackupLog("nightly", runID))
	if err != nil || !strings.Contains(string(log), "Uploaded files-shop") {
		t.Errorf("run log %q, %v", log, err)
	}
	if fi, _ := os.Stat(e.d.Paths.BackupLog("nightly", runID)); fi == nil || fi.Mode().Perm() != 0o640 {
		t.Errorf("run log mode %v, want 0640", fi)
	}
	if h := re.Helper.(*fakeHelper); len(h.archived) != 1 || !slices.Equal(h.archived[0], []string{"s_shop", "site/releases/initial"}) {
		t.Errorf("archived %q", h.archived)
	}
	if !slices.ContainsFunc(e.rclone.calls, func(c []string) bool { return c[0] == "lsf" }) {
		t.Error("the run didn't prune by retention")
	}
}

func TestRunFailsWhenAnyPartFails(t *testing.T) {
	e, re := runEnv(t, dumpScript(goodDump, 0))
	re.Helper.(*fakeHelper).err = errors.New("as-user archive-tree as s_shop: disk error")
	_, err := RunPlan(context.Background(), e.d, re, "nightly", contract.TriggerPanel)
	if err == nil || !strings.Contains(err.Error(), "disk error") {
		t.Fatalf("err = %v", err)
	}
	if slices.Contains(rcats(e), "offsite:nightly/20261123T033000Z/manifest.json") {
		t.Error("a failed run uploaded its manifest")
	}
	if got := purged(e); !slices.Equal(got, []string{"offsite:nightly/20261123T033000Z"}) {
		t.Errorf("purged %q, want the failed backup's folder", got)
	}
	rec := readRecord(t, e, runID)
	if rec.Status != contract.BackupFailed || rec.Trigger != contract.TriggerPanel || !strings.Contains(rec.Error, "disk error") ||
		rec.FinishedAt == nil || len(rec.Parts) != 1 {
		t.Errorf("record %+v, want failed with the passed part", rec)
	}
}

// A timer run and a panel run of one plan never overlap; nor does deleting
// a plan while it runs.
func TestOverlappingRunsRefused(t *testing.T) {
	e, re := runEnv(t, dumpScript(goodDump, 0))
	unlock, ok, err := sysexec.TryLock(e.d.Paths.BackupLock("nightly"))
	if err != nil || !ok {
		t.Fatal(err)
	}
	defer unlock()
	_, err = RunPlan(context.Background(), e.d, re, "nightly", contract.TriggerTimer)
	var ae *contract.AgentError
	if !errors.As(err, &ae) || ae.Code != contract.ErrBusy {
		t.Fatalf("err = %v, want busy", err)
	}
	if _, err := os.Stat(e.d.Paths.BackupRunsDir("nightly")); !errors.Is(err, fs.ErrNotExist) {
		t.Error("a refused run wrote a record")
	}
	if _, err := DeletePlan(context.Background(), e.d, "nightly"); !errors.As(err, &ae) || ae.Code != contract.ErrBusy {
		t.Errorf("deleting a running plan: %v, want busy", err)
	}
}

// A clock set back can give an id a backup already has: the run fails
// before it uploads anything, and never touches the older record.
func TestRunRefusesExistingBackupID(t *testing.T) {
	e, re := runEnv(t, dumpScript(goodDump, 0))
	e.rclone.answer = lsfAnswer(complete(runID))
	if _, err := RunPlan(context.Background(), e.d, re, "nightly", contract.TriggerTimer); err == nil || !strings.Contains(err.Error(), string(runID)) {
		t.Fatalf("err = %v, want the id named", err)
	}
	if got := rcats(e); len(got) != 0 || len(purged(e)) != 0 {
		t.Errorf("uploaded %q, purged %q; want nothing", got, purged(e))
	}

	e, re = runEnv(t, dumpScript(goodDump, 0))
	old := []byte(`{"plan":"nightly","backup":"20261123T033000Z","trigger":"panel","status":"succeeded","parts":[],"started_at":"2026-11-23T03:30:00Z"}`)
	os.MkdirAll(e.d.Paths.BackupRunsDir("nightly"), 0o700)
	os.WriteFile(e.d.Paths.BackupRunRecord("nightly", runID), old, 0o600)
	if _, err := RunPlan(context.Background(), e.d, re, "nightly", contract.TriggerTimer); err == nil {
		t.Fatal("a run with a recorded id passed")
	}
	if got, _ := os.ReadFile(e.d.Paths.BackupRunRecord("nightly", runID)); string(got) != string(old) {
		t.Errorf("the older record changed: %s", got)
	}
}

// A record a killed run left as running is marked failed by the plan's
// next run, which holds the lock, so nothing else is running.
func TestRunMarksStaleRunningRecordFailed(t *testing.T) {
	e, re := runEnv(t, dumpScript(goodDump, 0))
	stale := contract.BackupRunRecord{Plan: "nightly", Backup: "20261122T033000Z", Trigger: contract.TriggerTimer,
		Status: contract.BackupRunning, Parts: []contract.BackupPartInfo{}, StartedAt: time.Date(2026, 11, 22, 3, 30, 0, 0, time.UTC)}
	if err := saveRecord(e.d.Paths, stale); err != nil {
		t.Fatal(err)
	}
	if _, err := RunPlan(context.Background(), e.d, re, "nightly", contract.TriggerTimer); err != nil {
		t.Fatal(err)
	}
	if r := readRecord(t, e, "20261122T033000Z"); r.Status != contract.BackupFailed || r.Error == "" {
		t.Errorf("stale record %+v", r)
	}
}

func TestRunsListsRecordsSince(t *testing.T) {
	e := newEnv(t)
	at := func(h int) time.Time { return time.Date(2026, 11, 23, h, 0, 0, 0, time.UTC) }
	for _, r := range []contract.BackupRunRecord{
		{Plan: "nightly", Backup: "20261123T010000Z", Status: contract.BackupSucceeded, StartedAt: at(1)},
		{Plan: "weekly", Backup: "20261123T020000Z", Status: contract.BackupFailed, StartedAt: at(2)},
		{Plan: "nightly", Backup: "20261123T030000Z", Status: contract.BackupRunning, StartedAt: at(3)},
	} {
		r.Trigger, r.Parts = contract.TriggerTimer, []contract.BackupPartInfo{}
		if err := saveRecord(e.d.Paths, r); err != nil {
			t.Fatal(err)
		}
	}
	ids := func(res contract.BackupRunsResult) (out []string) {
		for _, r := range res.Runs {
			out = append(out, string(r.Plan)+"/"+string(r.Backup))
		}
		return out
	}
	all, err := Runs(e.d.Paths, "", "")
	if err != nil || !slices.Equal(ids(all), []string{"nightly/20261123T010000Z", "weekly/20261123T020000Z", "nightly/20261123T030000Z"}) {
		t.Errorf("all runs %q, %v", ids(all), err)
	}
	if got, _ := Runs(e.d.Paths, "nightly", "20261123T010000Z"); !slices.Equal(ids(got), []string{"nightly/20261123T030000Z"}) {
		t.Errorf("nightly since 01:00: %q", ids(got))
	}
	if got, err := Runs(e.d.Paths, "gone", ""); err != nil || got.Runs == nil || len(got.Runs) != 0 {
		t.Errorf("a plan without records: %+v, %v; want an empty list", got, err)
	}
}
