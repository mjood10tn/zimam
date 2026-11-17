//go:build roottest

package mysql_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"zimam/agent/contract"
	"zimam/agent/internal/commands"
	"zimam/agent/internal/jobs"
	"zimam/agent/internal/mysql"
	"zimam/agent/internal/paths"
	"zimam/agent/internal/sites"
	"zimam/agent/internal/sysexec"
)

// failingUndo runs everything for real, except that, while armed, a GRANT
// is followed by what the test wants to go wrong (breakState, or a lost
// reply) and the statements that undo a create (REVOKE, DROP DATABASE) fail.
type failingUndo struct {
	sysexec.Runner
	armed      bool
	afterGrant func() error
}

func (f *failingUndo) Run(ctx context.Context, c sysexec.Cmd) (sysexec.Result, error) {
	if !f.armed || c.Stdin == nil {
		return f.Runner.Run(ctx, c)
	}
	in, err := io.ReadAll(c.Stdin)
	if err != nil {
		return sysexec.Result{}, err
	}
	c.Stdin = bytes.NewReader(in)
	sql := string(in)
	switch {
	case strings.HasPrefix(sql, "REVOKE IF EXISTS") || strings.HasPrefix(sql, "DROP DATABASE"):
		return sysexec.Result{}, errors.New("the connection to MySQL was lost")
	case strings.HasPrefix(sql, "GRANT "):
		res, err := f.Runner.Run(ctx, c)
		if err != nil {
			return res, err
		}
		return res, f.afterGrant()
	}
	return f.Runner.Run(ctx, c)
}

// A create whose cleanup fails after its grant succeeded leaves the database
// and the grant in MySQL. Whether or not the state could record the
// database, db.list, db.user, db.create and db.drop must then behave.
func TestDbCreateWhoseCleanupFailsAgainstRealMySQL(t *testing.T) {
	if os.Getenv("ZIMAM_ROOTTEST_MYSQL") != "1" {
		t.Skip("set ZIMAM_ROOTTEST_MYSQL=1 to install MySQL on this machine")
	}
	if os.Geteuid() != 0 {
		t.Fatal("the roottest tests need root")
	}
	ctx := context.Background()
	p := paths.Paths{}
	d := sysexec.Deps{Paths: p, Run: sysexec.ExecRunner{}, Logf: t.Logf, Now: time.Now}
	if _, err := mysql.Install(ctx, d); err != nil {
		t.Fatalf("Install: %v", err)
	}
	const db = contract.DatabaseName("s_bb_one")
	cleanup := func() {
		mysql.Exec(ctx, d, mysql.RevokeAll("bb", db), mysql.DropDatabase(db))
		mysql.Exec(ctx, d, mysql.DropUser("bb"))
		os.RemoveAll(p.SiteState("bb"))
	}
	cleanup()
	t.Cleanup(cleanup)
	site := sites.State{Name: "bb", Domain: "bb.example.com", Type: contract.SiteStatic, TemplateVersion: 1, Security: sites.DefaultSecurity(), DBUser: true}
	pw := contract.ServicePassword("Bbbbbbbbbbbbbbbbbbbbbbbb-1_bbbb")
	exists := func() bool {
		sizes, err := mysql.Sizes(ctx, d)
		if err != nil {
			t.Fatal(err)
		}
		_, ok := sizes[string(db)]
		return ok
	}
	listed := func() []contract.DatabaseName {
		st, err := sites.Load(p, "bb")
		if err != nil {
			t.Fatal(err)
		}
		return st.Databases
	}
	listAnswer := func() []string {
		res, err := commands.New(d, nil)["db.list"](ctx, &contract.DbListParams{Site: "bb"})
		if err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal(res)
		var out struct {
			Databases []struct {
				Name string `json:"name"`
			} `json:"databases"`
		}
		if err := json.Unmarshal(b, &out); err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, x := range out.Databases {
			names = append(names, x.Name)
		}
		return names
	}

	for name, tc := range map[string]struct {
		afterGrant func() error
		wantMsg    []string
		wantListed bool
	}{
		// The grant is in place and the state can't be saved: nothing records
		// the database.
		"state cannot be saved": {
			afterGrant: func() error {
				if err := os.Remove(p.SiteState("bb")); err != nil {
					return err
				}
				return os.Mkdir(p.SiteState("bb"), 0o700)
			},
			wantMsg: []string{"The database s_bb_one was left in MySQL", "db.create for s_bb_one again"},
		},
		// The grant is in place, its reply is lost, and the cleanup fails: the
		// state records the database.
		"reply lost": {
			afterGrant: func() error { return errors.New("the connection to MySQL was lost") },
			wantMsg:    []string{"s_bb_one was kept", "db.drop"},
			wantListed: true,
		},
	} {
		t.Run(name, func(t *testing.T) {
			cleanup()
			if err := sites.Save(p, site); err != nil {
				t.Fatal(err)
			}
			if _, err := mysql.Exec(ctx, d, mysql.CreateUser("bb"), mysql.AlterUserPassword("bb", pw)); err != nil {
				t.Fatal(err)
			}
			faulty := &failingUndo{Runner: sysexec.ExecRunner{}, armed: true, afterGrant: tc.afterGrant}
			j := &jobs.Context{ID: "01JBX7Q3A1B2C3D4E5F6G7H8J9", Logf: t.Logf, Paths: p, Run: faulty}
			run := func(command string, params contract.Params) error {
				_, err := commands.JobHandlers()[command](ctx, j, params)
				return err
			}
			err := run("db.create", &contract.DbCreateParams{Site: "bb", Database: db})
			if err == nil {
				t.Fatal("db.create passed")
			}
			for _, want := range tc.wantMsg {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q lacks %q", err, want)
				}
			}
			faulty.armed = false
			if !exists() {
				t.Fatal("the database is gone from MySQL")
			}
			if g, _ := mysql.Grantees(ctx, d, db); len(g) == 0 {
				t.Fatal("the grant is gone")
			}
			if !tc.wantListed {
				// Put the state back as it was: the grant is on an unlisted database.
				os.RemoveAll(p.SiteState("bb"))
				if err := sites.Save(p, site); err != nil {
					t.Fatal(err)
				}
				if got := listAnswer(); len(got) != 0 {
					t.Errorf("db.list shows %v for a database the state doesn't list", got)
				}
				err := run("db.user", &contract.DbUserParams{Site: "bb", Password: pw})
				var ae *contract.AgentError
				if !errors.As(err, &ae) || !strings.Contains(ae.Message, "db.create for s_bb_one") || strings.Contains(ae.Message, "did not give") {
					t.Fatalf("db.user with the grant on an unlisted database: %v", err)
				}
				// Creating the name again adopts it.
				if err := run("db.create", &contract.DbCreateParams{Site: "bb", Database: db}); err != nil {
					t.Fatalf("db.create again: %v", err)
				}
			}
			if got := listed(); len(got) != 1 || got[0] != db {
				t.Fatalf("state lists %v", got)
			}
			if got := listAnswer(); len(got) != 1 || got[0] != string(db) {
				t.Errorf("db.list shows %v", got)
			}
			if err := run("db.user", &contract.DbUserParams{Site: "bb", Password: pw}); err != nil {
				t.Fatalf("db.user: %v", err)
			}
			if err := run("db.drop", &contract.DbDropParams{Site: "bb", Database: db}); err != nil {
				t.Fatalf("db.drop: %v", err)
			}
			if exists() || len(listed()) != 0 {
				t.Error("the database is still there after db.drop")
			}
			if g, _ := mysql.Grantees(ctx, d, db); len(g) != 0 {
				t.Errorf("grants left after db.drop: %v", g)
			}
		})
	}
}
