package contract

import (
	"encoding/json"
	"errors"
	"slices"
	"time"
)

func init() {
	Register(Descriptor{
		Name:   "job.status",
		Kind:   KindRead,
		Params: func() Params { return &JobStatusParams{} },
		Result: JobStatusResult{},
	})
	// job.follow streams log lines, then ends with the job's status.
	Register(Descriptor{
		Name:   "job.follow",
		Kind:   KindRead,
		Params: func() Params { return &JobFollowParams{} },
		Result: JobStatusResult{},
	})
	Register(Descriptor{
		Name:    "job.cancel",
		Kind:    KindDirect,
		Audited: true,
		Params:  func() Params { return &JobCancelParams{} },
		Result:  JobCancelResult{},
	})
}

type JobStatusParams struct {
	Job JobID `json:"job"`
}

func (p JobStatusParams) Validate() error {
	f := FieldErrors{}
	f.Check("job", p.Job)
	return f.Err()
}

type JobFollowParams struct {
	Job        JobID     `json:"job"`
	FromOffset LogOffset `json:"from_offset"`
}

func (p JobFollowParams) Validate() error {
	f := FieldErrors{}
	f.Check("job", p.Job)
	f.Check("from_offset", p.FromOffset)
	return f.Err()
}

type JobCancelParams struct {
	Job JobID `json:"job"`
}

func (p JobCancelParams) Validate() error {
	f := FieldErrors{}
	f.Check("job", p.Job)
	return f.Err()
}

type JobStatusResult struct {
	Job        JobID           `json:"job"`
	Command    string          `json:"command"`
	Subject    string          `json:"subject"`
	State      JobState        `json:"state"`
	ErrorCode  *ErrorCode      `json:"error_code"` // set when the job failed
	Message    string          `json:"message"`
	Result     json.RawMessage `json:"result"` // the command's result; null until it succeeds
	StartedAt  *time.Time      `json:"started_at"`
	FinishedAt *time.Time      `json:"finished_at"`
	LogSize    int64           `json:"log_size"` // bytes; from_offset counts into the log
}

type JobCancelResult struct {
	State JobState `json:"state"`
}

type JobState string

const (
	JobQueued    JobState = "queued"
	JobRunning   JobState = "running"
	JobSucceeded JobState = "succeeded"
	JobFailed    JobState = "failed"
	JobCancelled JobState = "cancelled"
)

var jobStates = []JobState{JobQueued, JobRunning, JobSucceeded, JobFailed, JobCancelled}

func (s JobState) Validate() error {
	if !slices.Contains(jobStates, s) {
		return errors.New("is not a job state")
	}
	return nil
}

func (JobState) JSONSchema() Schema {
	return Schema{"type": "string", "enum": jobStates}
}

// LogOffset is a byte offset into a job log.
type LogOffset int64

func (o LogOffset) Validate() error {
	if o < 0 {
		return errors.New("must not be negative")
	}
	return nil
}

func (LogOffset) JSONSchema() Schema {
	return Schema{"type": "integer", "minimum": 0}
}
