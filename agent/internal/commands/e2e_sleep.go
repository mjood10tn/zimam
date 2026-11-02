//go:build e2e

package commands

import (
	"context"
	"time"

	"zimam/agent/contract"
	"zimam/agent/internal/jobs"
)

// e2e.sleep is a job that only sleeps, built into e2e agents alone (D22),
// so the milestone 1 contract scenarios and probes can run a job's
// lifecycle, busy and cancel before any real job command exists. Its
// descriptor lives here rather than in contract, so schemagen and the
// contract tests never see it.

func init() {
	contract.Register(contract.Descriptor{
		Name:    "e2e.sleep",
		Kind:    contract.KindJob,
		Audited: true,
		Params:  func() contract.Params { return &E2ESleepParams{} },
		Result:  E2ESleepResult{},
		Subject: func(p contract.Params) string { return "e2e:" + string(p.(*E2ESleepParams).Subject) },
		E2EOnly: true,
	})
	jobHandlers["e2e.sleep"] = e2eSleep
}

// E2ESleepParams is e2e.sleep's params. Subject follows the site-name rule,
// because it becomes part of a lock file name under /run/zimam/locks/.
type E2ESleepParams struct {
	Seconds int               `json:"seconds"`
	Subject contract.SiteName `json:"subject"`
}

func (p *E2ESleepParams) Validate() error {
	f := contract.FieldErrors{}
	if p.Seconds < 1 || p.Seconds > 120 {
		f["seconds"] = "must be 1 to 120"
	}
	f.Check("subject", p.Subject)
	return f.Err()
}

type E2ESleepResult struct {
	Slept int `json:"slept"`
}

// e2eSleep logs a line a second, so following the job has something to
// show, and stops as soon as systemd stops the unit.
func e2eSleep(ctx context.Context, j *jobs.Context, p contract.Params) (any, error) {
	seconds := p.(*E2ESleepParams).Seconds
	j.Logf("Sleeping for %d s", seconds)
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for slept := 1; slept <= seconds; slept++ {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-tick.C:
			j.Logf("Slept %d of %d s", slept, seconds)
		}
	}
	return E2ESleepResult{Slept: seconds}, nil
}
