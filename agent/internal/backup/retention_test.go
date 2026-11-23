package backup

import (
	"context"
	"slices"
	"strings"
	"testing"

	"zimam/agent/contract"
	"zimam/agent/internal/sysexec"
)

// 2026-11-23 is a Monday: a is in ISO week 48, b–e in week 47, f (Sunday
// the 15th) in week 46; g and h are a month apart.
var retentionIDs = []contract.BackupID{
	"20261123T033000Z", // a
	"20261122T153000Z", // b
	"20261122T033000Z", // c
	"20261121T033000Z", // d
	"20261120T033000Z", // e
	"20261115T033000Z", // f
	"20261101T033000Z", // g
	"20261001T033000Z", // h
}

func TestRetentionKeepsRules(t *testing.T) {
	a, b, c, d, f, h := retentionIDs[0], retentionIDs[1], retentionIDs[2], retentionIDs[3], retentionIDs[5], retentionIDs[7]
	for _, tc := range []struct {
		r    contract.Retention
		want []contract.BackupID
	}{
		{contract.Retention{KeepLast: 1}, []contract.BackupID{a}},
		{contract.Retention{KeepLast: 1, KeepDaily: 3}, []contract.BackupID{a, b, d}},
		{contract.Retention{KeepLast: 2, KeepWeekly: 2}, []contract.BackupID{a, b}},
		{contract.Retention{KeepLast: 1, KeepWeekly: 3}, []contract.BackupID{a, b, f}},
		{contract.Retention{KeepLast: 1, KeepMonthly: 3}, []contract.BackupID{a, h}},
		{contract.Retention{KeepLast: 3, KeepDaily: 1, KeepMonthly: 2}, []contract.BackupID{a, b, c, h}},
		{contract.Retention{KeepLast: 365}, retentionIDs},
	} {
		kept := keep(retentionIDs, tc.r)
		var got []contract.BackupID
		for _, id := range retentionIDs {
			if kept[id] {
				got = append(got, id)
			}
		}
		if !slices.Equal(got, tc.want) {
			t.Errorf("%+v keeps %v, want %v", tc.r, got, tc.want)
		}
	}
}

// lsfAnswer lists the plan's folder as rclone lsf --max-depth 2 does.
func lsfAnswer(listing string) func([]string) (sysexec.Result, error) {
	return func(args []string) (sysexec.Result, error) {
		if args[0] == "lsf" {
			return sysexec.Result{Stdout: []byte(listing)}, nil
		}
		return sysexec.Result{}, nil
	}
}

func complete(ids ...contract.BackupID) string {
	var b strings.Builder
	for _, id := range ids {
		b.WriteString(string(id) + "/\n" + string(id) + "/db-s_shop_main.sql.gz\n" + string(id) + "/manifest.json\n")
	}
	return b.String()
}

func purged(e env) []string {
	var out []string
	for _, c := range e.rclone.calls {
		if c[0] == "purge" {
			out = append(out, c[1])
		}
	}
	return out
}

// Prune keeps what the rules keep among complete backups and purges the
// rest; the clock is 2026-11-23 03:30 UTC.
func TestRetentionPurgesWhatNoRuleKeeps(t *testing.T) {
	e := newEnv(t)
	e.rclone.answer = lsfAnswer(complete(retentionIDs...))
	if err := Prune(context.Background(), e.d, "offsite", "nightly", contract.Retention{KeepLast: 1, KeepDaily: 3}); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(e.rclone.calls[0], []string{"lsf", "--max-depth", "2", "offsite:nightly"}) {
		t.Errorf("listed with %q", e.rclone.calls[0])
	}
	want := []string{"offsite:nightly/20261122T033000Z", "offsite:nightly/20261120T033000Z", "offsite:nightly/20261115T033000Z",
		"offsite:nightly/20261101T033000Z", "offsite:nightly/20261001T033000Z"}
	if got := purged(e); !slices.Equal(got, want) {
		t.Errorf("purged %q, want %q", got, want)
	}
}

// A backup without its manifest never counts toward a rule; one older
// than 24 hours is purged, a newer one may still be running.
func TestRetentionDeletesStaleIncomplete(t *testing.T) {
	e := newEnv(t)
	e.rclone.answer = lsfAnswer(complete("20261123T020000Z") + "20261122T020000Z/\n20261122T020000Z/db-s_shop_main.sql.gz\n" +
		"20261123T010000Z/\n20261123T010000Z/db-s_shop_main.sql.gz\n")
	if err := Prune(context.Background(), e.d, "offsite", "nightly", contract.Retention{KeepLast: 5}); err != nil {
		t.Fatal(err)
	}
	if got := purged(e); !slices.Equal(got, []string{"offsite:nightly/20261122T020000Z"}) {
		t.Errorf("purged %q, want only the stale incomplete backup", got)
	}
}

// Only names that are backup ids are ever purged, whatever else the
// owner keeps in the plan's folder.
func TestRetentionNeverPurgesNonMatchingNames(t *testing.T) {
	e := newEnv(t)
	e.rclone.answer = lsfAnswer(complete("20261123T020000Z", "20261001T020000Z") +
		"notes/\nnotes/a.txt\nREADME\n20261101T000000Z.bak/\n20261101T000000Z.bak/manifest.json\n20261399T000000Z/\nold/20261001T000000Z/\n")
	if err := Prune(context.Background(), e.d, "offsite", "nightly", contract.Retention{KeepLast: 1}); err != nil {
		t.Fatal(err)
	}
	if got := purged(e); !slices.Equal(got, []string{"offsite:nightly/20261001T020000Z"}) {
		t.Errorf("purged %q", got)
	}
}

// A plan folder that doesn't exist yet has no backups.
func TestListBackupsOfNewPlan(t *testing.T) {
	e := newEnv(t)
	e.rclone.answer = func(args []string) (sysexec.Result, error) {
		return sysexec.Result{ExitCode: 3}, &sysexec.ExitError{Path: "rclone", Code: 3, Stderr: []byte("directory not found")}
	}
	got, err := listBackups(context.Background(), e.d, "offsite", "nightly")
	if err != nil || len(got) != 0 {
		t.Errorf("listBackups = %v, %v; want none", got, err)
	}
}
