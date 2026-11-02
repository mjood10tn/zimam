package configtx

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func setJournal(t *testing.T, dir string) {
	t.Helper()
	old := Journal
	Journal = dir
	t.Cleanup(func() { Journal = old })
}

func records(t *testing.T, dir string) []string {
	t.Helper()
	names, err := filepath.Glob(filepath.Join(dir, "*"))
	if err != nil {
		t.Fatal(err)
	}
	return names
}

// stageAll stages changes the way Apply does, under a record, and returns
// the record without finishing it.
func stageAll(t *testing.T, changes []Change) *record {
	t.Helper()
	rec, err := begin(changes)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	for _, c := range changes {
		if _, err := stage(c); err != nil {
			t.Fatalf("stage %s: %v", c.Path, err)
		}
	}
	return rec
}

// A process that dies between staging and its test leaves an untested file
// live (M1 review, M4). Recover puts the previous files back and removes the
// ones the change created.
func TestRecoverRollsBackAnAbandonedChange(t *testing.T) {
	dir := t.TempDir()
	journal := filepath.Join(dir, "configtx")
	setJournal(t, journal)
	kept := filepath.Join(dir, "kept.conf")
	writeFile(t, kept, "old")
	added := filepath.Join(dir, "added.conf")
	gone := filepath.Join(dir, "gone.conf")
	writeFile(t, gone, "removed by the change")
	rec := stageAll(t, []Change{change(kept, "new, untested"), change(added, "new file"), {Path: gone, Remove: true}})
	rec.f.Close() // the process dies; its lock goes with it

	touched, err := Recover(journal)
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if got := readFile(t, kept); got != "old" {
		t.Errorf("kept.conf = %q, want the old content back", got)
	}
	if _, err := os.Lstat(added); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("added.conf is still there (%v)", err)
	}
	if got := readFile(t, gone); got != "removed by the change" {
		t.Errorf("gone.conf = %q, want it back", got)
	}
	for _, p := range []string{kept, gone} {
		if _, err := os.Lstat(p + ".zimam-prev"); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s.zimam-prev is still there", p)
		}
	}
	slices.Sort(touched)
	if want := []string{added, gone, kept}; !slices.Equal(touched, want) {
		t.Errorf("touched %q, want %q", touched, want)
	}
	if left := records(t, journal); len(left) != 0 {
		t.Errorf("records left: %q", left)
	}
}

// A change whose process is alive holds its record's lock, and Recover
// leaves it alone.
func TestRecoverLeavesAChangeInFlight(t *testing.T) {
	dir := t.TempDir()
	journal := filepath.Join(dir, "configtx")
	setJournal(t, journal)
	kept := filepath.Join(dir, "kept.conf")
	writeFile(t, kept, "old")
	rec := stageAll(t, []Change{change(kept, "new")})

	if touched, err := Recover(journal); err != nil || len(touched) != 0 {
		t.Fatalf("Recover = %q, %v; want nothing touched", touched, err)
	}
	if got := readFile(t, kept); got != "new" || len(records(t, journal)) != 1 {
		t.Errorf("kept.conf = %q with %d records, want the change and its record untouched", got, len(records(t, journal)))
	}
	rec.done()
	if left := records(t, journal); len(left) != 0 {
		t.Errorf("records left after done: %q", left)
	}
}

// Apply removes its record whether the change is kept or undone.
func TestApplyLeavesNoRecord(t *testing.T) {
	dir := t.TempDir()
	journal := filepath.Join(dir, "configtx")
	setJournal(t, journal)
	conf := filepath.Join(dir, "site.conf")
	writeFile(t, conf, "old")
	pass := func(context.Context) error { return nil }
	if err := Apply(context.Background(), []Change{change(conf, "new")}, pass); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	failing := func(context.Context) error { return errors.New("bad") }
	if err := Apply(context.Background(), []Change{change(conf, "broken")}, failing); err == nil {
		t.Fatal("Apply kept a change whose test failed")
	}
	if got := readFile(t, conf); got != "new" {
		t.Errorf("site.conf = %q, want the passing change", got)
	}
	if left := records(t, journal); len(left) != 0 {
		t.Errorf("records left: %q", left)
	}
}

// Without a journal directory (tests of other packages), Apply keeps no
// records and Recover finds none.
func TestJournalOff(t *testing.T) {
	setJournal(t, "")
	dir := t.TempDir()
	conf := filepath.Join(dir, "site.conf")
	if err := Apply(context.Background(), []Change{change(conf, "new")}, func(context.Context) error { return nil }); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if touched, err := Recover(filepath.Join(dir, "missing")); err != nil || len(touched) != 0 {
		t.Errorf("Recover of a missing directory = %q, %v", touched, err)
	}
}
