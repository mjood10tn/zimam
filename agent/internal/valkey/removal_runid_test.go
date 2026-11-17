package valkey

import (
	"context"
	"slices"
	"strings"
	"testing"

	"zimam/agent/internal/sysexec"
)

// serve gives the server a new set of scripts and says which process it is.
func (r *removal) serve(runID string, scripts ...sysexec.Script) {
	r.e.run = sysexec.NewFakeRunner(append([]sysexec.Script{infoRun(runID)}, scripts...)...)
	r.e.srv = serverRunner{FakeRunner: r.e.run, killed: r.e.killed}
	r.e.d.Run = r.e.srv
}

// purgeCursors are the cursor arguments of the scanning calls made, in order.
func purgeCursors(r *removal) []string {
	var out []string
	for _, c := range r.e.run.Calls() {
		if i := slices.Index(c.Args, "EVAL"); i >= 0 {
			out = append(out, c.Args[i+3])
		}
	}
	return out
}

// A cursor is good only for the process that gave it. When the saved progress
// is from another process (a different run id) or has none, the pass in
// progress is scanned again from 0, it keeps its number (pass 2 stays pass 2,
// since the user is already removed), the progress saved afterwards names the
// live process, and the log says why.
func TestSavedCursorOfAnotherProcessIsNotTrusted(t *testing.T) {
	old := purgeBudget
	purgeBudget = 0 // one call, then the progress is saved
	defer func() { purgeBudget = old }()
	for name, tc := range map[string]struct {
		saved   Progress
		want    Progress // saved after the run
		scripts []sysexec.Script
	}{
		"pass 2, another run id": {
			Progress{2, "33", runA}, Progress{2, "9", runB},
			[]sysexec.Script{loadOK, purgeStep("0", "shop", "9 1\n")},
		},
		"pass 2, no run id saved": {
			Progress{2, "33", ""}, Progress{2, "9", runB},
			[]sysexec.Script{loadOK, purgeStep("0", "shop", "9 1\n")},
		},
		"pass 1, another run id": {
			Progress{1, "77", runA}, Progress{1, "9", runB},
			[]sysexec.Script{purgeStep("0", "shop", "9 1\n")},
		},
	} {
		t.Run(name, func(t *testing.T) {
			r := newRemoval(t)
			r.prog = tc.saved
			r.serve(runB, tc.scripts...)
			err := r.run(t)
			if err == nil || !strings.Contains(err.Error(), "run the command again to continue") {
				t.Fatalf("RemoveSiteUser = %v, want it to stop after one call with its progress saved", err)
			}
			if got := purgeCursors(r); !slices.Equal(got, []string{"0"}) {
				t.Errorf("the scanning calls started from %q, want one from 0 (the saved cursor %q belongs to another process)", got, tc.saved.Cursor)
			}
			if r.prog != tc.want {
				t.Errorf("progress afterwards %+v, want %+v", r.prog, tc.want)
			}
			if !strings.Contains(r.e.logs.String(), "Valkey has restarted since the progress") {
				t.Errorf("the log doesn't say the pass starts again:\n%s", r.e.logs.String())
			}
		})
	}
	t.Run("the same process resumes, with no such line in the log", func(t *testing.T) {
		r := newRemoval(t)
		r.prog = Progress{2, "33", runB}
		r.serve(runB, loadOK, purgeStep("33", "shop", "9 1\n"))
		if err := r.run(t); err == nil || !slices.Equal(purgeCursors(r), []string{"33"}) || r.prog != (Progress{2, "9", runB}) {
			t.Errorf("RemoveSiteUser = %v, cursors %q, progress %+v", err, purgeCursors(r), r.prog)
		}
		if strings.Contains(r.e.logs.String(), "has restarted") {
			t.Errorf("the log says Valkey restarted:\n%s", r.e.logs.String())
		}
	})
}

// run_id is read from INFO server: either line ending, exactly 40 lowercase hex
// digits, and there must be a line. Anything else is an error, and an error
// stops the removal before anything is scanned, loaded or saved.
func TestRunIDParse(t *testing.T) {
	for name, tc := range map[string]struct {
		reply string
		want  string
		bad   bool
	}{
		"CRLF":              {"# Server\r\nvalkey_version:9.0.4\r\nrun_id:" + runA + "\r\ntcp_port:6379\r\n", runA, false},
		"LF":                {"# Server\nvalkey_version:9.0.4\nrun_id:" + runB + "\ntcp_port:6379\n", runB, false},
		"the last line":     {"# Server\r\nrun_id:" + runA, runA, false},
		"39 characters":     {"run_id:" + runA[:39] + "\r\n", "", true},
		"41 characters":     {"run_id:" + runA + "a\r\n", "", true},
		"upper case":        {"run_id:" + strings.ToUpper(runA) + "\r\n", "", true},
		"not hex":           {"run_id:" + strings.Repeat("z", 40) + "\r\n", "", true},
		"empty":             {"run_id:\r\n", "", true},
		"no run_id line":    {"# Server\r\nvalkey_version:9.0.4\r\n", "", true},
		"a similar name":    {"xrun_id:" + runA + "\r\n", "", true},
		"a space after it":  {"run_id: " + runA + "\r\n", "", true},
		"an empty reply":    {"\n", "", true},
		"trailing garbage":  {"run_id:" + runA + " \r\ntcp_port:6379\r\n", "", true},
		"the id on its own": {runA + "\r\n", "", true},
	} {
		t.Run(name, func(t *testing.T) {
			e := cliEnv(t)
			e.add(live(tc.reply, "INFO", "server"))
			got, err := runID(context.Background(), e.d)
			if (err != nil) != tc.bad || got != tc.want {
				t.Errorf("runID = %q, %v; want %q, error %v", got, err, tc.want, tc.bad)
			}
		})
	}
}

func TestRemovalStopsBeforeAnyScanWhenTheRunIDCantBeRead(t *testing.T) {
	for name, reply := range map[string]string{
		"a bad id":   "run_id:" + strings.ToUpper(runA) + "\r\n",
		"no id line": "# Server\r\nvalkey_version:9.0.4\r\n",
	} {
		t.Run(name, func(t *testing.T) {
			r := newRemoval(t)
			r.prog = Progress{2, "33", runA}
			r.e.run = sysexec.NewFakeRunner(live(reply, "INFO", "server"))
			r.e.srv = serverRunner{FakeRunner: r.e.run, killed: r.e.killed}
			r.e.d.Run = r.e.srv
			before := readFile(t, r.e.p.ValkeyACL())
			if err := r.run(t); err == nil {
				t.Fatal("RemoveSiteUser passed")
			}
			if got := len(r.e.run.Calls()); got != 1 {
				t.Errorf("%d calls (%v); only the INFO may run", got, lines(r.e.run.Calls()))
			}
			if readFile(t, r.e.p.ValkeyACL()) != before || r.prog != (Progress{2, "33", runA}) || len(r.saves) != 0 || len(r.events) != 0 {
				t.Errorf("something changed: saves %v, progress %+v, events %q", r.saves, r.prog, r.events)
			}
		})
	}
}
