package contract

import (
	"errors"
	"fmt"
)

func init() {
	Register(Descriptor{
		Name:    "site.cron",
		Kind:    KindJob,
		Audited: true,
		Params:  func() Params { return &SiteCronParams{} },
		Result:  SiteNameResult{},
		Subject: func(p Params) string { return "site:" + string(p.(*SiteCronParams).Name) },
	})
}

const maxCronJobs = 50

// schedulerScriptID is the file name of the Laravel scheduler's script
// (.zimam/cron/scheduler.sh), so no cron job may take it as an id.
const schedulerScriptID WorkerID = "scheduler"

// CronJobSpec is one cron job of a site: Command runs as the site user on
// Schedule, unless Enabled is false.
type CronJobSpec struct {
	ID       WorkerID     `json:"id"`
	Schedule CronSchedule `json:"schedule"`
	Command  OwnerCommand `json:"command"`
	Enabled  bool         `json:"enabled"`
}

func (j CronJobSpec) Validate() error {
	f := FieldErrors{}
	f.Check("id", j.ID)
	if j.ID == schedulerScriptID {
		f["id"] = "is reserved for the Laravel scheduler"
	}
	f.Check("schedule", j.Schedule)
	f.Check("command", j.Command)
	return fieldsError(f)
}

func (CronJobSpec) JSONSchema() Schema {
	id := WorkerID("").JSONSchema()
	id["not"] = Schema{"enum": []WorkerID{schedulerScriptID}}
	return Schema{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []string{"command", "enabled", "id", "schedule"},
		"properties": map[string]Schema{
			"id":       id,
			"schedule": CronSchedule("").JSONSchema(),
			"command":  OwnerCommand("").JSONSchema(),
			"enabled":  {"type": "boolean"},
		},
	}
}

// CronJobList is a site's whole list of cron jobs, at most 50 (OQ3), each id once.
type CronJobList []CronJobSpec

func (l CronJobList) Validate() error {
	if l == nil {
		return errors.New("is required; an empty list removes every job")
	}
	if len(l) > maxCronJobs {
		return errors.New("must have at most 50 jobs")
	}
	seen := map[WorkerID]bool{}
	for _, j := range l {
		if err := j.Validate(); err != nil {
			return fmt.Errorf("job %q: %v", j.ID, err)
		}
		if seen[j.ID] {
			return fmt.Errorf("job %q is listed twice", j.ID)
		}
		seen[j.ID] = true
	}
	return nil
}

func (CronJobList) JSONSchema() Schema {
	return Schema{"type": "array", "maxItems": maxCronJobs, "items": CronJobSpec{}.JSONSchema()}
}

// SiteCronParams is site.cron's params: whether the Laravel scheduler runs
// every minute, and the site's whole list of cron jobs. The agent makes
// /etc/cron.d/zimam-<site> and the scripts match it (R04-D5).
type SiteCronParams struct {
	Name      SiteName    `json:"name"`
	Scheduler bool        `json:"scheduler"`
	Jobs      CronJobList `json:"jobs"`
}

func (p *SiteCronParams) Validate() error {
	f := FieldErrors{}
	f.Check("name", p.Name)
	f.Check("jobs", p.Jobs)
	return f.Err()
}
