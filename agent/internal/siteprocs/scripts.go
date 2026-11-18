// Package siteprocs writes what a site's workers and cron jobs run: the
// scripts in the site's home (R04-D1) and the environment file they source
// (R04-D15). Everything goes through the helper as the site user, so root
// never opens a file in a home, and the owner's command lines reach nothing
// root, systemd or cron reads.
package siteprocs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"strconv"
	"strings"

	"zimam/agent/contract"
	"zimam/agent/internal/helper"
	"zimam/agent/internal/sites"
	"zimam/agent/internal/templates"
)

// Helper is what the scripts need of the site-user helper; *helper.Client is
// the real one.
type Helper interface {
	Exec(ctx context.Context, a helper.Account, r helper.ExecRequest) (int, error)
	Mkdir(ctx context.Context, a helper.Account, name string, mode fs.FileMode) error
	Write(ctx context.Context, a helper.Account, name string, mode fs.FileMode, content io.Reader) error
	List(ctx context.Context, a helper.Account, dir string) (helper.Listing, error)
	RemoveTree(ctx context.Context, a helper.Account, name string) error
	Read(ctx context.Context, a helper.Account, name string, offset, limit int64) (helper.ReadResult, error)
}

// maxScriptBytes bounds reading a script back: a header and one command line
// of at most 4096 bytes.
const maxScriptBytes = 64 << 10

var _ Helper = (*helper.Client)(nil)

const (
	zimamDir    = ".zimam"
	workersDir  = zimamDir + "/workers"
	cronDir     = zimamDir + "/cron"
	siteEnvFile = zimamDir + "/site.env.sh"
	// SchedulerID is the Laravel scheduler's script, .zimam/cron/scheduler.sh;
	// the contract refuses it as a cron job's id.
	SchedulerID = "scheduler"
)

// WriteSiteEnv writes ~/.zimam/site.env.sh (0600) for the site's PHP version
// and makes the version's php directory, by running that php once through the
// helper's exec, which does it for every PHP program (R04-D15). Workers and
// cron jobs call it first, so a site whose PHP changed is fixed by any of them.
func WriteSiteEnv(ctx context.Context, h Helper, a helper.Account, st sites.State) error {
	out, err := templates.Render("site/site.env.sh", templates.SiteEnv{PHPVersion: st.PHPVersion})
	if err != nil {
		return err
	}
	if err := h.Mkdir(ctx, a, zimamDir, 0o700); err != nil {
		return err
	}
	if err := h.Write(ctx, a, siteEnvFile, 0o600, bytes.NewReader(out)); err != nil {
		return err
	}
	if st.PHPVersion == "" {
		return nil
	}
	code, err := h.Exec(ctx, a, helper.ExecRequest{Program: "php", PHP: st.PHPVersion, Args: []string{"-v"}})
	if err != nil {
		return fmt.Errorf("making PHP %s the scripts' php: %w", st.PHPVersion, err)
	}
	if code != 0 {
		return fmt.Errorf("php %s -v exited with status %d", st.PHPVersion, code)
	}
	return nil
}

// WriteWorkerScripts writes one script per worker into ~/.zimam/workers (0700)
// and deletes the others there (site.workers, R04-D3). It returns the workers
// whose script it changed, which site.workers restarts.
func WriteWorkerScripts(ctx context.Context, h Helper, a helper.Account, st sites.State, workers []contract.WorkerSpec) (map[contract.WorkerID]bool, error) {
	if err := WriteSiteEnv(ctx, h, a, st); err != nil {
		return nil, err
	}
	scripts := map[string]templates.WorkerScript{}
	for _, w := range workers {
		scripts[string(w.ID)] = workerScript(w)
	}
	changed, err := writeScripts(ctx, h, a, workersDir, "site/worker.sh", scripts)
	if err != nil {
		return nil, err
	}
	ids := map[contract.WorkerID]bool{}
	for id := range changed {
		ids[contract.WorkerID(id)] = true
	}
	return ids, nil
}

// WriteCronScripts writes the script of every enabled cron job, and the
// scheduler's when it's on, into ~/.zimam/cron (0700), and deletes the other
// scripts there (site.cron, R04-D5). A .lock file stays: it holds nothing, and
// a job that comes back reuses it.
func WriteCronScripts(ctx context.Context, h Helper, a helper.Account, st sites.State, cron contract.SiteCronParams) error {
	if err := WriteSiteEnv(ctx, h, a, st); err != nil {
		return err
	}
	scripts := map[string]templates.CronScript{}
	for _, j := range cron.Jobs {
		if j.Enabled {
			scripts[string(j.ID)] = templates.CronScript{ID: j.ID, Command: j.Command}
		}
	}
	if cron.Scheduler {
		scripts[SchedulerID] = templates.CronScript{ID: SchedulerID, Command: "php artisan schedule:run"}
	}
	_, err := writeScripts(ctx, h, a, cronDir, "site/cron.sh", scripts)
	return err
}

// writeScripts makes dir (0700), writes each script as <id>.sh (0700) and
// removes every other .sh file in dir; the helper's remove deletes a link
// itself, never its target. A script already there with the same content is
// left alone; the ids of the others are returned. The helper reads it back
// as the site user, so root still opens nothing in the home, and a read that
// fails (a link, say) only means the script is written again.
func writeScripts[D any](ctx context.Context, h Helper, a helper.Account, dir, tmpl string, scripts map[string]D) (map[string]bool, error) {
	if err := h.Mkdir(ctx, a, dir, 0o700); err != nil {
		return nil, err
	}
	changed := map[string]bool{}
	for id, data := range scripts {
		out, err := templates.Render(tmpl, data)
		if err != nil {
			return nil, fmt.Errorf("script %s: %w", id, err)
		}
		name := path.Join(dir, id+".sh")
		if r, err := h.Read(ctx, a, name, 0, maxScriptBytes); err == nil && bytes.Equal(r.Data, out) {
			continue
		}
		if err := h.Write(ctx, a, name, 0o700, bytes.NewReader(out)); err != nil {
			return nil, err
		}
		changed[id] = true
	}
	l, err := h.List(ctx, a, dir)
	if err != nil {
		return nil, err
	}
	for _, e := range l.Entries {
		id, ok := strings.CutSuffix(e.Name, ".sh")
		if _, wanted := scripts[id]; !ok || wanted {
			continue
		}
		if err := h.RemoveTree(ctx, a, path.Join(dir, e.Name)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
	}
	return changed, nil
}

// workerScript is a worker's script data: the owner's command as it is, or
// artisan's command line built from the validated options. A nil option is
// left out, so artisan's own default holds; 0 is a value.
func workerScript(w contract.WorkerSpec) templates.WorkerScript {
	if w.Kind == contract.WorkerCustom {
		return templates.WorkerScript{Command: w.Command}
	}
	argv := []string{"php", "artisan", "queue:work", string(w.ConnectionName())}
	if len(w.Queues) > 0 {
		qs := make([]string, len(w.Queues))
		for i, q := range w.Queues {
			qs[i] = string(q)
		}
		argv = append(argv, "--queue="+strings.Join(qs, ","))
	}
	for _, o := range []struct {
		flag string
		v    *int
	}{{"tries", w.Tries}, {"timeout", w.Timeout}, {"sleep", w.Sleep}, {"max-time", w.MaxTime}, {"backoff", w.Backoff}} {
		if o.v != nil {
			argv = append(argv, "--"+o.flag+"="+strconv.Itoa(*o.v))
		}
	}
	return templates.WorkerScript{Argv: append(argv, "--memory="+strconv.Itoa(int(w.Memory())))}
}
