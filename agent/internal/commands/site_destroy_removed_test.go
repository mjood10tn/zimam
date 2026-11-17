package commands_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"zimam/agent/contract"
	"zimam/agent/internal/commands"
	"zimam/agent/internal/jobs"
	"zimam/agent/internal/sysexec"
	"zimam/agent/internal/valkey"
)

// removedOf returns what a failed site.destroy reports about the site's
// service users: the job fails, and the result still says which are gone.
func removedOf(t *testing.T, res any, err error) contract.SiteDestroyRemoved {
	t.Helper()
	var landed *jobs.Landed
	if !errors.As(err, &landed) {
		t.Fatalf("err = %v, want a failure that keeps its result", err)
	}
	r, ok := res.(contract.SiteDestroyResult)
	if !ok || r.Removed == nil || r.Name != "shop" {
		t.Fatalf("result = %#v, want a SiteDestroyResult for shop with removed", res)
	}
	return *r.Removed
}

// A destroy refused before it touches a user says both are still there.
func TestFailedDestroyBeforeUsersKeepsBoth(t *testing.T) {
	t.Run("MySQL down", func(t *testing.T) {
		e := shopWithUser(t, "s_shop_a")
		e.answer("", errDown)
		res, err := e.job(t, "site.destroy", destroyParams(true))
		wantCode(t, err, contract.ErrConflict)
		if got := removedOf(t, res, err); got != (contract.SiteDestroyRemoved{ValkeyUser: true}) {
			t.Errorf("removed = %+v, want only the Valkey user, which the site never had", got)
		}
	})
	t.Run("a site that has databases", func(t *testing.T) {
		e := shopWithUser(t, "s_shop_a")
		res, err := e.job(t, "site.destroy", destroyParams(false))
		wantCode(t, err, contract.ErrConflict)
		if got := removedOf(t, res, err); got != (contract.SiteDestroyRemoved{ValkeyUser: true}) {
			t.Errorf("removed = %+v, want only the Valkey user, which the site never had", got)
		}
	})
	t.Run("Valkey down", func(t *testing.T) {
		e := withValkey(t, newDBEnv(t))
		st := e.site(t, "shop")
		st.CacheUserHash = sum(pwOne)
		st.DBUser = true
		e.saveSite(t, st)
		e.run.Add(cli("Could not connect to Valkey at 127.0.0.1:6379: Connection refused\n", 1, "PING"))
		res, err := e.job(t, "site.destroy", destroyParams(false))
		wantCode(t, err, contract.ErrConflict)
		if got := removedOf(t, res, err); got != (contract.SiteDestroyRemoved{}) {
			t.Errorf("removed = %+v, want both users kept", got)
		}
	})
}

// The Valkey user goes first; a later MySQL failure says so.
func TestFailedDestroyAfterTheValkeyUser(t *testing.T) {
	e := withValkey(t, newDBEnv(t))
	st := e.site(t, "shop")
	st.CacheUserHash = sum(pwOne)
	st.Databases = []contract.DatabaseName{"s_shop_a"}
	st.DBUser = true
	e.saveSite(t, st)
	e.run.Add(cliPing)
	e.addPurge("shop")
	e.run.Add(cliLoad)
	e.answer("", "") // MySQL answers a ping first
	e.answer("", "ERROR 1396 (HY000) at line 1: Operation failed")
	res, err := e.job(t, "site.destroy", destroyParams(true))
	if err == nil {
		t.Fatal("the destroy passed")
	}
	want := contract.SiteDestroyRemoved{ValkeyUser: true}
	if got := removedOf(t, res, err); got != want {
		t.Errorf("removed = %+v, want %+v", got, want)
	}
}

// Valkey's second purge failing leaves the hash in the state: the user is
// not reported gone, because running the destroy again still has work there.
func TestFailedDestroyWhileValkeyStepsRemain(t *testing.T) {
	e := withValkey(t, newDBEnv(t))
	st := e.site(t, "shop")
	st.CacheUserHash = sum(pwOne)
	e.saveSite(t, st)
	if err := os.WriteFile(e.p.ValkeyACL(), []byte(e.acl(t)+aclLine("shop", pwOne)), 0o640); err != nil {
		t.Fatal(err)
	}
	e.run.Add(cliPing)
	e.run.Add(cli("0 3\n", 0, valkey.PurgeArgs("0", "shop")...))
	e.run.Add(cliLoad)
	e.run.Add(cli("ERR boom\n", 1, valkey.PurgeArgs("0", "shop")...))
	res, err := e.job(t, "site.destroy", destroyParams(false))
	if err == nil {
		t.Fatal("the destroy passed")
	}
	if got := removedOf(t, res, err); got.ValkeyUser {
		t.Errorf("removed = %+v, want the Valkey user kept", got)
	}
}

// After the MySQL user is dropped, a failure further on (here the Nginx file
// can't be removed) reports both users gone.
func TestFailedDestroyAfterTheMySQLUser(t *testing.T) {
	e := shopWithUser(t)
	e.answer("", "")
	stuck := e.p.NginxSiteAvailable("shop")
	if err := os.MkdirAll(filepath.Join(stuck, "x"), 0o755); err != nil {
		t.Fatal(err)
	}
	res, err := e.job(t, "site.destroy", destroyParams(false))
	if err == nil {
		t.Fatal("the destroy passed")
	}
	if got := removedOf(t, res, err); !got.MySQLUser || !got.ValkeyUser {
		t.Errorf("removed = %+v, want both gone", got)
	}
}

// A site that is already gone has no users left: the destroy answers
// not_found and nothing is kept.
func TestDestroyOfAGoneSiteReportsBothGone(t *testing.T) {
	e := newDBEnv(t)
	if err := os.Remove(e.p.SiteState("shop")); err != nil {
		t.Fatal(err)
	}
	res, err := e.job(t, "site.destroy", destroyParams(false))
	wantCode(t, err, contract.ErrNotFound)
	if got := removedOf(t, res, err); !got.MySQLUser || !got.ValkeyUser {
		t.Errorf("removed = %+v", got)
	}
}

// Both users removed, then a later step fails: both are reported gone.
func TestFailedDestroyAfterBothUsersLaterStepFails(t *testing.T) {
	e := shopWithCache(t)
	st := e.site(t, "shop")
	st.DBUser = true
	e.saveSite(t, st)
	e.run.Add(cliLoad)
	e.answer("", "") // MySQL ping
	e.answer("", "") // drop user
	if err := os.MkdirAll(filepath.Join(e.p.NginxSiteAvailable("shop"), "x"), 0o755); err != nil {
		t.Fatal(err)
	}
	res, err := e.job(t, "site.destroy", destroyParams(false))
	if err == nil {
		t.Fatal("the destroy passed")
	}
	if got := removedOf(t, res, err); !got.MySQLUser || !got.ValkeyUser {
		t.Errorf("removed = %+v, want both gone", got)
	}
}

// stoppingRunner cancels or outlasts the job's context at its first run.
type stoppingRunner struct {
	inner  sysexec.Runner
	cancel context.CancelFunc
}

func (r stoppingRunner) Run(ctx context.Context, c sysexec.Cmd) (sysexec.Result, error) {
	if r.cancel != nil {
		r.cancel()
	} else {
		<-ctx.Done()
	}
	return sysexec.Result{}, ctx.Err()
}

// A stop or a timeout is not wrapped: the job must end cancelled or timed
// out, not failed with a result.
func TestStoppedDestroyIsNotAFailureThatLanded(t *testing.T) {
	for name, mk := range map[string]func() (context.Context, stoppingRunner){
		"cancelled": func() (context.Context, stoppingRunner) {
			ctx, cancel := context.WithCancel(context.Background())
			return ctx, stoppingRunner{cancel: cancel}
		},
		"timed out": func() (context.Context, stoppingRunner) {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
			_ = cancel
			return ctx, stoppingRunner{}
		},
	} {
		t.Run(name, func(t *testing.T) {
			e := shopWithUser(t, "s_shop_a")
			ctx, r := mk()
			j := &jobs.Context{ID: "01JBX7Q3A1B2C3D4E5F6G7H8J9", Logf: t.Logf, Paths: e.p, Run: r}
			res, err := commands.JobHandlers()["site.destroy"](ctx, j, destroyParams(true))
			if err == nil || ctx.Err() == nil {
				t.Fatalf("err = %v, ctx.Err = %v", err, ctx.Err())
			}
			var landed *jobs.Landed
			if errors.As(err, &landed) || res != nil {
				t.Errorf("a stopped destroy returned %#v with %v; the job would end failed", res, err)
			}
		})
	}
}
