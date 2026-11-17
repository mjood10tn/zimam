package valkey

import (
	"slices"
	"strconv"
	"strings"
	"testing"

	"zimam/agent/internal/sysexec"
)

// purgeIn scripts a purge call in database db, which valkey-cli selects with
// -n (database 0 needs no option).
func purgeIn(db int, cursor, reply string) sysexec.Script {
	s := purgeStep(cursor, "shop", reply)
	if db != 0 {
		i := slices.Index(s.Args, "-p") + 2
		s.Args = slices.Insert(slices.Clone(s.Args), i, "-n", strconv.Itoa(db))
	}
	return s
}

// scans are the scanning calls made, as "db:cursor".
func scans(r *removal) []string {
	var out []string
	for _, c := range r.e.run.Calls() {
		i := slices.Index(c.Args, "EVAL")
		if i < 0 {
			continue
		}
		db := "0"
		if j := slices.Index(c.Args, "-n"); j >= 0 {
			db = c.Args[j+1]
		}
		out = append(out, db+":"+c.Args[i+3])
	}
	return out
}

func TestPurgeVisitsEveryDatabaseInOrder(t *testing.T) {
	r := newRemoval(t)
	r.serve(runA, loadOK) // placeholder, replaced below with three databases
	r.e.run = sysexec.NewFakeRunner(infoRun(runA), databases(3),
		purgeIn(0, "0", "0 4\n"), purgeIn(1, "0", "0 2\n"), purgeIn(2, "0", "0 0\n"), loadOK)
	r.e.srv = serverRunner{FakeRunner: r.e.run, killed: r.e.killed}
	r.e.d.Run = r.e.srv
	if err := r.run(t); err != nil {
		t.Fatal(err)
	}
	// Both passes cover all three, in order, and the user goes between them.
	want := []string{"0:0", "1:0", "2:0", "0:0", "1:0", "2:0"}
	if got := scans(r); !slices.Equal(got, want) {
		t.Errorf("scans %q, want %q", got, want)
	}
	all := lines(r.e.run.Calls())
	loadAt := slices.Index(all, line(loadOK))
	if loadAt != 5 { // info, databases, three scans, then the load
		t.Errorf("the users were loaded at call %d, want 5:\n%s", loadAt, strings.Join(all, "\n"))
	}
	if len(r.events) != 1 {
		t.Errorf("events %q", r.events)
	}
}

// Crossing into the next database is saved as progress: a run that runs out
// of time there continues in that database.
func TestPurgeProgressNamesTheDatabase(t *testing.T) {
	old := purgeBudget
	purgeBudget = 0
	defer func() { purgeBudget = old }()
	r := newRemoval(t)
	r.e.run = sysexec.NewFakeRunner(infoRun(runA), databases(3), purgeIn(0, "0", "0 4\n"))
	r.e.srv = serverRunner{FakeRunner: r.e.run, killed: r.e.killed}
	r.e.d.Run = r.e.srv
	if err := r.run(t); err == nil || r.prog != (Progress{1, "0", 1, runA}) {
		t.Fatalf("RemoveSiteUser = %v, progress %+v; want pass 1 at database 1, cursor 0", err, r.prog)
	}
	// Within a database: cursor and database both saved.
	r.e.run = sysexec.NewFakeRunner(infoRun(runA), databases(3), purgeIn(1, "0", "44 1\n"))
	r.e.srv = serverRunner{FakeRunner: r.e.run, killed: r.e.killed}
	r.e.d.Run = r.e.srv
	if err := r.run(t); err == nil || r.prog != (Progress{1, "44", 1, runA}) {
		t.Fatalf("RemoveSiteUser = %v, progress %+v", err, r.prog)
	}
}

// A resume continues at the saved database and cursor; the databases before
// it are not scanned again in that pass.
func TestResumeContinuesAtTheSavedDatabase(t *testing.T) {
	r := newRemoval(t)
	r.prog = Progress{1, "50", 1, runA}
	r.e.run = sysexec.NewFakeRunner(infoRun(runA), databases(3),
		purgeIn(1, "50", "0 2\n"), purgeIn(2, "0", "0 0\n"), loadOK, purgeIn(0, "0", "0 0\n"), purgeIn(1, "0", "0 0\n"))
	r.e.srv = serverRunner{FakeRunner: r.e.run, killed: r.e.killed}
	r.e.d.Run = r.e.srv
	if err := r.run(t); err != nil {
		t.Fatal(err)
	}
	want := []string{"1:50", "2:0", "0:0", "1:0", "2:0"}
	if got := scans(r); !slices.Equal(got, want) {
		t.Errorf("scans %q, want %q", got, want)
	}
}

func TestResumeInPassTwoContinuesAtTheSavedDatabase(t *testing.T) {
	r := newRemoval(t)
	r.prog = Progress{2, "7", 2, runA}
	r.e.run = sysexec.NewFakeRunner(infoRun(runA), databases(3), loadOK, purgeIn(2, "7", "0 1\n"))
	r.e.srv = serverRunner{FakeRunner: r.e.run, killed: r.e.killed}
	r.e.d.Run = r.e.srv
	if err := r.run(t); err != nil {
		t.Fatal(err)
	}
	if got := scans(r); !slices.Equal(got, []string{"2:7"}) {
		t.Errorf("scans %q, want only 2:7", got)
	}
}

// After a restart (another run id) the pass starts again at database 0,
// cursor 0, whatever database the progress was in.
func TestRestartStartsAgainAtDatabaseZero(t *testing.T) {
	r := newRemoval(t)
	r.prog = Progress{2, "7", 2, runB}
	r.e.run = sysexec.NewFakeRunner(infoRun(runA), databases(3), loadOK,
		purgeIn(0, "0", "0 1\n"), purgeIn(1, "0", "0 0\n"), purgeIn(2, "0", "0 0\n"))
	r.e.srv = serverRunner{FakeRunner: r.e.run, killed: r.e.killed}
	r.e.d.Run = r.e.srv
	if err := r.run(t); err != nil {
		t.Fatal(err)
	}
	if got := scans(r); !slices.Equal(got, []string{"0:0", "1:0", "2:0"}) {
		t.Errorf("scans %q, want the whole pass from 0:0", got)
	}
	if !strings.Contains(r.e.logs.String(), "Valkey has restarted since the progress") {
		t.Errorf("the log doesn't say why:\n%s", r.e.logs.String())
	}
}

// A database index the server no longer has (its number of databases was
// lowered) starts the pass again from the first.
func TestSavedDatabaseBeyondTheServersStartsAgain(t *testing.T) {
	r := newRemoval(t)
	r.prog = Progress{2, "7", 9, runA}
	r.e.run = sysexec.NewFakeRunner(infoRun(runA), databases(2), loadOK,
		purgeIn(0, "0", "0 1\n"), purgeIn(1, "0", "0 0\n"))
	r.e.srv = serverRunner{FakeRunner: r.e.run, killed: r.e.killed}
	r.e.d.Run = r.e.srv
	if err := r.run(t); err != nil {
		t.Fatal(err)
	}
	if got := scans(r); !slices.Equal(got, []string{"0:0", "1:0"}) {
		t.Errorf("scans %q", got)
	}
}

// A progress saved before the index existed has none, and that is database 0.
func TestProgressWithoutADatabaseIsDatabaseZero(t *testing.T) {
	r := newRemoval(t)
	r.prog = Progress{Pass: 2, Cursor: "7", RunID: runA}
	r.e.run = sysexec.NewFakeRunner(infoRun(runA), databases(2), loadOK,
		purgeIn(0, "7", "0 1\n"), purgeIn(1, "0", "0 0\n"))
	r.e.srv = serverRunner{FakeRunner: r.e.run, killed: r.e.killed}
	r.e.d.Run = r.e.srv
	if err := r.run(t); err != nil {
		t.Fatal(err)
	}
	if got := scans(r); !slices.Equal(got, []string{"0:7", "1:0"}) {
		t.Errorf("scans %q", got)
	}
}

func TestDatabaseCountParse(t *testing.T) {
	for name, tc := range map[string]struct {
		reply string
		want  int
		bad   bool
	}{
		"16":           {"databases\n16\n", 16, false},
		"CRLF":         {"databases\r\n1\r\n", 1, false},
		"zero":         {"databases\n0\n", 0, true},
		"not a name":   {"dbs\n16\n", 0, true},
		"not a number": {"databases\nmany\n", 0, true},
		"one line":     {"databases\n", 0, true},
		"too many":     {"databases\n99999999\n", 0, true},
	} {
		t.Run(name, func(t *testing.T) {
			e := cliEnv(t)
			e.add(live(tc.reply, "CONFIG", "GET", "databases"))
			got, err := databaseCount(t.Context(), e.d)
			if (err != nil) != tc.bad || got != tc.want {
				t.Errorf("databaseCount = %d, %v", got, err)
			}
		})
	}
}
