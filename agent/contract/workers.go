package contract

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"
)

// The worker commands (v0.4.0 plan, "New agent commands"). Each job runs
// under its site's subject, so it never overlaps another job of the site.
func init() {
	Register(Descriptor{
		Name:    "site.workers",
		Kind:    KindJob,
		Audited: true,
		Params:  func() Params { return &SiteWorkersParams{} },
		Result:  SiteNameResult{},
		Subject: func(p Params) string { return "site:" + string(p.(*SiteWorkersParams).Name) },
	})
	Register(Descriptor{
		Name:   "site.workers.status",
		Kind:   KindRead,
		Params: func() Params { return &SiteWorkersStatusParams{} },
		Result: SiteWorkersStatusResult{},
	})
	Register(Descriptor{
		Name:    "site.workers.restart",
		Kind:    KindJob,
		Audited: true,
		Params:  func() Params { return &SiteWorkersRestartParams{} },
		Result:  SiteNameResult{},
		Subject: func(p Params) string { return "site:" + string(p.(*SiteWorkersRestartParams).Name) },
	})
}

// As elsewhere, RE2's $ matches only at the very end of the text; PHP
// mirrors of these patterns anchor with \z.
var (
	workerIDRe        = regexp.MustCompile(`^[a-z][a-z0-9-]{0,15}$`)
	queueNameRe       = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,64}$`)
	queueConnectionRe = regexp.MustCompile(`^[a-z0-9_-]{1,32}$`)
	// ownerCommandRe is the schema's half of OwnerCommand: no NUL, CR or LF.
	ownerCommandRe = regexp.MustCompile(`^[^\x00\r\n]+$`)
)

const (
	maxWorkers             = 10
	maxWorkerProcesses     = 10
	maxWorkerQueues        = 10
	minWorkerMemoryMB      = 64
	maxWorkerMemoryMB      = 4096
	maxOwnerCommandBytes   = 4096
	minMaxTime             = 60 // a max_time other than 0 (ruling R5)
	DefaultWorkerMemoryMB  = WorkerMemoryMB(512)
	DefaultQueueConnection = QueueConnection("default")
	// MaxWorkerProcesses is the most processes a worker has; the agent
	// stops @1..@10 of a worker it removes, whatever its state said.
	MaxWorkerProcesses = maxWorkerProcesses
)

// WorkerID names a worker, or a cron job, within a site. It ends up in unit
// names and script paths.
type WorkerID string

func (id WorkerID) Validate() error {
	if !workerIDRe.MatchString(string(id)) {
		return errors.New("must be 1–16 lowercase letters, digits or hyphens and start with a letter")
	}
	return nil
}

func (WorkerID) JSONSchema() Schema {
	return Schema{"type": "string", "pattern": workerIDRe.String()}
}

// QueueName is one queue of a Laravel worker; it reaches artisan's argv.
type QueueName string

func (q QueueName) Validate() error {
	if !queueNameRe.MatchString(string(q)) {
		return errors.New("must be 1–64 letters, digits, dots, colons, underscores or hyphens")
	}
	return nil
}

func (QueueName) JSONSchema() Schema {
	return Schema{"type": "string", "pattern": queueNameRe.String()}
}

// QueueConnection is a Laravel queue connection name; it reaches artisan's argv.
type QueueConnection string

func (c QueueConnection) Validate() error {
	if !queueConnectionRe.MatchString(string(c)) {
		return errors.New("must be 1–32 lowercase letters, digits, underscores or hyphens")
	}
	return nil
}

func (QueueConnection) JSONSchema() Schema {
	return Schema{"type": "string", "pattern": queueConnectionRe.String()}
}

// WorkerMemoryMB is a worker process's memory limit, 64–4096 MB.
type WorkerMemoryMB int

func (m WorkerMemoryMB) Validate() error {
	if m < minWorkerMemoryMB || m > maxWorkerMemoryMB {
		return errors.New("must be between 64 and 4096")
	}
	return nil
}

func (WorkerMemoryMB) JSONSchema() Schema {
	return Schema{"type": "integer", "minimum": minWorkerMemoryMB, "maximum": maxWorkerMemoryMB}
}

// OwnerCommand is a command line the owner wrote, for a custom worker or a
// cron job. The agent only writes it, as the site user, into a script in the
// site's home (R04-D1): it reaches no file root reads, and no shell of the
// agent's. So the rules are the ones that keep it one line of one file: valid
// UTF-8, no NUL, CR or LF, at most 4096 bytes, and not empty.
type OwnerCommand string

func (c OwnerCommand) Validate() error {
	switch {
	case c == "":
		return errors.New("must not be empty")
	case len(c) > maxOwnerCommandBytes:
		return errors.New("must be at most 4096 bytes")
	case !utf8.ValidString(string(c)):
		return errors.New("must be valid UTF-8")
	case strings.ContainsAny(string(c), "\x00\r\n"):
		return errors.New("must be one line without NUL characters")
	}
	return nil
}

// JSONSchema can't count bytes: its length is in characters, so only
// Validate refuses a long line of multi-byte characters.
func (OwnerCommand) JSONSchema() Schema {
	return Schema{"type": "string", "maxLength": maxOwnerCommandBytes, "pattern": ownerCommandRe.String()}
}

type WorkerKind string

const (
	WorkerLaravelQueue WorkerKind = "laravel-queue"
	WorkerCustom       WorkerKind = "custom"
)

var workerKinds = []WorkerKind{WorkerLaravelQueue, WorkerCustom}

func (k WorkerKind) Validate() error {
	if !slices.Contains(workerKinds, k) {
		return errors.New("must be laravel-queue or custom")
	}
	return nil
}

func (WorkerKind) JSONSchema() Schema { return Schema{"type": "string", "enum": workerKinds} }

// WorkerSpec is one worker: Processes copies of a systemd unit that run as
// the site user. A laravel-queue worker takes the queue options, and the
// agent builds its artisan command line; a custom worker takes a Command,
// and no queue option. Without MemoryMB the limit is DefaultWorkerMemoryMB,
// without Connection DefaultQueueConnection. A nil option leaves artisan's
// own default (0 is a value: no timeout, no sleep).
type WorkerSpec struct {
	ID         WorkerID        `json:"id"`
	Kind       WorkerKind      `json:"kind"`
	Processes  int             `json:"processes"`
	MemoryMB   WorkerMemoryMB  `json:"memory_mb,omitempty"`
	Connection QueueConnection `json:"connection,omitempty"`
	Queues     []QueueName     `json:"queues,omitempty"`
	Tries      *int            `json:"tries,omitempty"`
	Timeout    *int            `json:"timeout,omitempty"`
	Sleep      *int            `json:"sleep,omitempty"`
	MaxTime    *int            `json:"max_time,omitempty"`
	Backoff    *int            `json:"backoff,omitempty"`
	Command    OwnerCommand    `json:"command,omitempty"`
}

// Memory is the limit with the default applied.
func (w WorkerSpec) Memory() WorkerMemoryMB {
	if w.MemoryMB == 0 {
		return DefaultWorkerMemoryMB
	}
	return w.MemoryMB
}

// ConnectionName is the connection with the default applied.
func (w WorkerSpec) ConnectionName() QueueConnection {
	if w.Connection == "" {
		return DefaultQueueConnection
	}
	return w.Connection
}

func (w WorkerSpec) Validate() error { return fieldsError(w.check(true)) }

// ValidateStored checks a worker as the state file keeps it (R04-D16): the
// same rules, but a command is never there, so a custom worker has none and a
// laravel-queue worker has none either.
func (w WorkerSpec) ValidateStored() error {
	if w.Command != "" {
		return errors.New("command must not be stored")
	}
	return fieldsError(w.check(false))
}

func (w WorkerSpec) check(commandRequired bool) FieldErrors {
	f := FieldErrors{}
	f.Check("id", w.ID)
	f.Check("kind", w.Kind)
	if w.Processes < 1 || w.Processes > maxWorkerProcesses {
		f["processes"] = "must be between 1 and 10"
	}
	if w.MemoryMB != 0 {
		f.Check("memory_mb", w.MemoryMB)
	}
	if w.Connection != "" {
		f.Check("connection", w.Connection)
	}
	if len(w.Queues) > maxWorkerQueues {
		f["queues"] = "must have at most 10 queues"
	}
	for i, q := range w.Queues {
		if err := q.Validate(); err != nil {
			f["queues"] = fmt.Sprintf("queue %d %v", i+1, err)
			break
		}
	}
	for _, o := range []struct {
		name   string
		v      *int
		lo, hi int
	}{
		{"tries", w.Tries, 1, 100}, {"timeout", w.Timeout, 0, 86400}, {"sleep", w.Sleep, 0, 60},
		{"max_time", w.MaxTime, 0, 86400}, {"backoff", w.Backoff, 0, 3600},
	} {
		if o.v != nil && (*o.v < o.lo || *o.v > o.hi) {
			f[o.name] = fmt.Sprintf("must be between %d and %d", o.lo, o.hi)
		}
	}
	// Every exit, a clean one too, counts against the unit's start limit (10
	// starts in 600 s), so a worker may not end itself more often than once a
	// minute (ruling R5). 0 is no limit.
	if m := w.MaxTime; m != nil && *m >= 1 && *m < minMaxTime {
		f["max_time"] = "must be 0 or between 60 and 86400"
	}
	switch w.Kind {
	case WorkerLaravelQueue:
		if w.Command != "" {
			f["command"] = "is only for a custom worker"
		}
	case WorkerCustom:
		if commandRequired {
			f.Check("command", w.Command)
		}
		if w.Connection != "" || len(w.Queues) > 0 || w.Tries != nil || w.Timeout != nil || w.Sleep != nil || w.MaxTime != nil || w.Backoff != nil {
			f["kind"] = "a custom worker takes no connection, queues or queue options"
		}
	}
	return f
}

// fieldsError turns field errors into one plain error, for a Validate that
// is itself one field of a command's params.
func fieldsError(f FieldErrors) error {
	if len(f) == 0 {
		return nil
	}
	keys := make([]string, 0, len(f))
	for k := range f {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = k + " " + f[k]
	}
	return errors.New(strings.Join(parts, "; "))
}

func intSchema(lo, hi int) Schema {
	return Schema{"anyOf": []Schema{{"type": "integer", "minimum": lo, "maximum": hi}, {"type": "null"}}}
}

// JSONSchema can't say which options a kind takes or that a custom worker
// needs a command: only Validate checks those.
func (WorkerSpec) JSONSchema() Schema {
	return Schema{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []string{"id", "kind", "processes"},
		"properties": map[string]Schema{
			"id":         WorkerID("").JSONSchema(),
			"kind":       WorkerKind("").JSONSchema(),
			"processes":  {"type": "integer", "minimum": 1, "maximum": maxWorkerProcesses},
			"memory_mb":  WorkerMemoryMB(0).JSONSchema(),
			"connection": QueueConnection("").JSONSchema(),
			"queues":     {"type": "array", "maxItems": maxWorkerQueues, "items": QueueName("").JSONSchema()},
			"tries":      intSchema(1, 100),
			"timeout":    intSchema(0, 86400),
			"sleep":      intSchema(0, 60),
			"max_time":   {"anyOf": []Schema{{"anyOf": []Schema{{"const": 0}, {"type": "integer", "minimum": minMaxTime, "maximum": 86400}}}, {"type": "null"}}},
			"backoff":    intSchema(0, 3600),
			"command":    OwnerCommand("").JSONSchema(),
		},
	}
}

// WorkerList is a site's whole list of workers, at most 10 (OQ3), each id once.
type WorkerList []WorkerSpec

func (l WorkerList) Validate() error {
	if l == nil {
		return errors.New("is required; an empty list removes every worker")
	}
	if len(l) > maxWorkers {
		return errors.New("must have at most 10 workers")
	}
	seen := map[WorkerID]bool{}
	for _, w := range l {
		if err := w.Validate(); err != nil {
			return fmt.Errorf("worker %q: %v", w.ID, err)
		}
		if seen[w.ID] {
			return fmt.Errorf("worker %q is listed twice", w.ID)
		}
		seen[w.ID] = true
	}
	return nil
}

func (WorkerList) JSONSchema() Schema {
	return Schema{"type": "array", "maxItems": maxWorkers, "items": WorkerSpec{}.JSONSchema()}
}

// SiteWorkersParams is site.workers's params: the site's whole list of
// workers. The agent makes the units match it (R04-D3): a worker left out is
// removed.
type SiteWorkersParams struct {
	Name    SiteName   `json:"name"`
	Workers WorkerList `json:"workers"`
}

func (p *SiteWorkersParams) Validate() error {
	f := FieldErrors{}
	f.Check("name", p.Name)
	f.Check("workers", p.Workers)
	return f.Err()
}

// SiteWorkersStatusParams is site.workers.status's params.
type SiteWorkersStatusParams struct {
	Name SiteName `json:"name"`
}

func (p *SiteWorkersStatusParams) Validate() error {
	f := FieldErrors{}
	f.Check("name", p.Name)
	return f.Err()
}

// WorkerInstanceStatus is one process of a worker, as systemd reports its
// unit. Active and Sub are systemd's ActiveState and SubState. Restarts is
// NRestarts, which also counts clean exits. Since is when the unit entered
// its state, RFC 3339, or empty when systemd has no time. Failing is true
// for a crash loop, which never reaches "failed" (it sits in auto-restart):
// the last exit was a failure and the unit is restarting (R04-D17).
type WorkerInstanceStatus struct {
	Instance int    `json:"instance"`
	Active   string `json:"active"`
	Sub      string `json:"sub"`
	Restarts int    `json:"restarts"`
	Since    string `json:"since"`
	Failing  bool   `json:"failing"`
}

// WorkerStatus is one worker with its processes, in instance order.
type WorkerStatus struct {
	ID        WorkerID               `json:"id"`
	Instances []WorkerInstanceStatus `json:"instances"`
}

// SiteWorkersStatusResult is site.workers.status's answer, the site's workers
// in id order. The lists are empty, never null: the schema expects arrays.
type SiteWorkersStatusResult struct {
	Name    SiteName       `json:"name"`
	Workers []WorkerStatus `json:"workers"`
}

// NewSiteWorkersStatusResult is a result with an empty list, which encodes as [].
func NewSiteWorkersStatusResult(name SiteName) SiteWorkersStatusResult {
	return SiteWorkersStatusResult{Name: name, Workers: []WorkerStatus{}}
}

// SiteWorkersRestartParams is site.workers.restart's params: one worker, or
// every worker of the site without one.
type SiteWorkersRestartParams struct {
	Name   SiteName `json:"name"`
	Worker WorkerID `json:"worker,omitempty"`
}

func (p *SiteWorkersRestartParams) Validate() error {
	f := FieldErrors{}
	f.Check("name", p.Name)
	if p.Worker != "" {
		f.Check("worker", p.Worker)
	}
	return f.Err()
}
