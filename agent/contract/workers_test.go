package contract

import (
	"strings"
	"testing"
)

func ptr[T any](v T) *T { return &v }

func TestWorkerID(t *testing.T) {
	checkValues(t,
		[]WorkerID{"a", "queue", "default-1", WorkerID("a" + strings.Repeat("b", 15))},
		[]WorkerID{"", "1a", "-a", "A", "a_b", "a.b", "a\n", WorkerID("a" + strings.Repeat("b", 16)), "ش"})
}

func TestQueueNameRules(t *testing.T) {
	checkValues(t,
		[]QueueName{"default", "emails", "high:priority", "a.b-c_d", QueueName(strings.Repeat("q", 64))},
		[]QueueName{"", "a b", "a,b", "a\n", "a;b", "$x", "a/b", QueueName(strings.Repeat("q", 65)), "ش"})
}

func TestOwnerCommandRefusesNewline(t *testing.T) {
	checkValues(t,
		[]OwnerCommand{"php artisan horizon", "node server.js --port=3000", "echo 'a;b' | cat", "echo ش", "a % b", OwnerCommand(strings.Repeat("x", 4096))},
		[]OwnerCommand{"", "a\nb", "a\rb", "a\x00b", "a\n", "\xff", OwnerCommand(strings.Repeat("x", 4097))})
}

func laravel() WorkerSpec {
	return WorkerSpec{ID: "queue", Kind: WorkerLaravelQueue, Processes: 2}
}

func TestWorkerSpecRules(t *testing.T) {
	custom := WorkerSpec{ID: "horizon", Kind: WorkerCustom, Processes: 1, Command: "php artisan horizon"}
	ok := []WorkerSpec{
		laravel(), custom,
		{ID: "q", Kind: WorkerLaravelQueue, Processes: 10, MemoryMB: 4096, Connection: "redis", Queues: []QueueName{"a", "b"},
			Tries: ptr(3), Timeout: ptr(0), Sleep: ptr(0), MaxTime: ptr(3600), Backoff: ptr(60)},
		{ID: "q", Kind: WorkerLaravelQueue, Processes: 1, MemoryMB: 64},
	}
	for _, w := range ok {
		if err := w.Validate(); err != nil {
			t.Errorf("%+v: %v", w, err)
		}
	}
	mod := func(f func(*WorkerSpec)) WorkerSpec { w := laravel(); f(&w); return w }
	bad := map[string]WorkerSpec{
		"bad id":         mod(func(w *WorkerSpec) { w.ID = "A" }),
		"no kind":        mod(func(w *WorkerSpec) { w.Kind = "" }),
		"unknown kind":   mod(func(w *WorkerSpec) { w.Kind = "cron" }),
		"zero processes": mod(func(w *WorkerSpec) { w.Processes = 0 }),
		"11 processes":   mod(func(w *WorkerSpec) { w.Processes = 11 }),
		"memory 63":      mod(func(w *WorkerSpec) { w.MemoryMB = 63 }),
		"memory 4097":    mod(func(w *WorkerSpec) { w.MemoryMB = 4097 }),
		"bad connection": mod(func(w *WorkerSpec) { w.Connection = "Redis!" }),
		"11 queues": mod(func(w *WorkerSpec) {
			w.Queues = make([]QueueName, 11)
			for i := range w.Queues {
				w.Queues[i] = "q"
			}
		}),
		"bad queue":              mod(func(w *WorkerSpec) { w.Queues = []QueueName{"a b"} }),
		"tries 0":                mod(func(w *WorkerSpec) { w.Tries = ptr(0) }),
		"tries 101":              mod(func(w *WorkerSpec) { w.Tries = ptr(101) }),
		"timeout 86401":          mod(func(w *WorkerSpec) { w.Timeout = ptr(86401) }),
		"sleep 61":               mod(func(w *WorkerSpec) { w.Sleep = ptr(61) }),
		"max_time -1":            mod(func(w *WorkerSpec) { w.MaxTime = ptr(-1) }),
		"backoff 3601":           mod(func(w *WorkerSpec) { w.Backoff = ptr(3601) }),
		"command on laravel":     mod(func(w *WorkerSpec) { w.Command = "php artisan queue:work" }),
		"custom without command": {ID: "x", Kind: WorkerCustom, Processes: 1},
		"custom bad command":     {ID: "x", Kind: WorkerCustom, Processes: 1, Command: "a\nb"},
		"custom with connection": {ID: "x", Kind: WorkerCustom, Processes: 1, Command: "x", Connection: "redis"},
		"custom with queues":     {ID: "x", Kind: WorkerCustom, Processes: 1, Command: "x", Queues: []QueueName{"a"}},
		"custom with tries":      {ID: "x", Kind: WorkerCustom, Processes: 1, Command: "x", Tries: ptr(1)},
		"custom with backoff":    {ID: "x", Kind: WorkerCustom, Processes: 1, Command: "x", Backoff: ptr(1)},
	}
	for name, w := range bad {
		if err := w.Validate(); err == nil {
			t.Errorf("%s: accepted %+v", name, w)
		}
	}
}

func TestWorkerSpecDefaults(t *testing.T) {
	w := laravel()
	if w.Memory() != DefaultWorkerMemoryMB || w.ConnectionName() != DefaultQueueConnection {
		t.Errorf("defaults: memory %d, connection %q", w.Memory(), w.ConnectionName())
	}
	w.MemoryMB, w.Connection = 256, "redis"
	if w.Memory() != 256 || w.ConnectionName() != "redis" {
		t.Error("explicit values lost")
	}
	if DefaultWorkerMemoryMB != 512 || DefaultQueueConnection != "default" {
		t.Error("defaults are 512 and default")
	}
}

// The state file keeps every option but the command (R04-D16): its checks
// are the spec's, minus the command.
func TestWorkerSpecValidateStored(t *testing.T) {
	if err := laravel().ValidateStored(); err != nil {
		t.Error(err)
	}
	if err := (WorkerSpec{ID: "h", Kind: WorkerCustom, Processes: 1}).ValidateStored(); err != nil {
		t.Errorf("custom without its command: %v", err)
	}
	if err := (WorkerSpec{ID: "h", Kind: WorkerCustom, Processes: 1, Command: "x"}).ValidateStored(); err == nil {
		t.Error("a stored worker carried a command")
	}
	if err := (WorkerSpec{ID: "h", Kind: WorkerCustom, Processes: 0}).ValidateStored(); err == nil {
		t.Error("accepted 0 processes")
	}
}

func TestSiteWorkersParams(t *testing.T) {
	list := func(n int) []WorkerSpec {
		out := make([]WorkerSpec, n)
		for i := range out {
			out[i] = WorkerSpec{ID: WorkerID("w" + string(rune('a'+i))), Kind: WorkerLaravelQueue, Processes: 1}
		}
		return out
	}
	for _, p := range []SiteWorkersParams{{Name: "shop", Workers: WorkerList{}}, {Name: "shop", Workers: list(10)}} {
		if err := p.Validate(); err != nil {
			t.Errorf("%d workers: %v", len(p.Workers), err)
		}
	}
	if f := fields(t, &SiteWorkersParams{Name: "shop", Workers: list(11)}); f["workers"] == "" {
		t.Error("accepted 11 workers")
	}
	if f := fields(t, &SiteWorkersParams{Name: "shop"}); f["workers"] == "" {
		t.Error("accepted workers null")
	}
	dup := list(2)
	dup[1].ID = dup[0].ID
	if f := fields(t, &SiteWorkersParams{Name: "shop", Workers: dup}); f["workers"] == "" {
		t.Error("accepted a repeated id")
	}
	if f := fields(t, &SiteWorkersParams{Name: "Bad", Workers: WorkerList{}}); f["name"] == "" {
		t.Error("accepted a bad site name")
	}
	bad := list(2)
	bad[1].Processes = 0
	if f := fields(t, &SiteWorkersParams{Name: "shop", Workers: bad}); !strings.Contains(f["workers"], "wb") {
		t.Errorf("error %q does not name the worker", f["workers"])
	}
}

func TestSiteWorkersStatusAndRestartParams(t *testing.T) {
	if err := (&SiteWorkersStatusParams{Name: "shop"}).Validate(); err != nil {
		t.Error(err)
	}
	if f := fields(t, &SiteWorkersStatusParams{}); f["name"] == "" {
		t.Error("accepted no site")
	}
	if err := (&SiteWorkersRestartParams{Name: "shop"}).Validate(); err != nil {
		t.Errorf("restart all: %v", err)
	}
	if err := (&SiteWorkersRestartParams{Name: "shop", Worker: "queue"}).Validate(); err != nil {
		t.Errorf("restart one: %v", err)
	}
	if f := fields(t, &SiteWorkersRestartParams{Name: "shop", Worker: "Q"}); f["worker"] == "" {
		t.Error("accepted a bad worker id")
	}
}

func TestCronJobSpecAndParams(t *testing.T) {
	job := CronJobSpec{ID: "backup", Schedule: "0 3 * * *", Command: "php artisan backup:run", Enabled: true}
	if err := job.Validate(); err != nil {
		t.Error(err)
	}
	for name, j := range map[string]CronJobSpec{
		"bad id":            {ID: "1", Schedule: "* * * * *", Command: "x"},
		"scheduler is ours": {ID: "scheduler", Schedule: "* * * * *", Command: "x"},
		"bad schedule":      {ID: "a", Schedule: "@reboot", Command: "x"},
		"newline command":   {ID: "a", Schedule: "* * * * *", Command: "x\ny"},
		"no command":        {ID: "a", Schedule: "* * * * *"},
	} {
		if err := j.Validate(); err == nil {
			t.Errorf("%s: accepted %+v", name, j)
		}
	}
	if err := (&SiteCronParams{Name: "shop", Scheduler: true, Jobs: CronJobList{}}).Validate(); err != nil {
		t.Error(err)
	}
	if f := fields(t, &SiteCronParams{Name: "shop"}); f["jobs"] == "" {
		t.Error("accepted jobs null")
	}
	many := make(CronJobList, 51)
	for i := range many {
		many[i] = CronJobSpec{ID: WorkerID("j" + strings.Repeat("a", i/26) + string(rune('a'+i%26))), Schedule: "* * * * *", Command: "x"}
	}
	if f := fields(t, &SiteCronParams{Name: "shop", Jobs: many[:50]}); f["jobs"] != "" {
		t.Errorf("50 jobs: %v", f)
	}
	if f := fields(t, &SiteCronParams{Name: "shop", Jobs: many}); f["jobs"] == "" {
		t.Error("accepted 51 jobs")
	}
	if f := fields(t, &SiteCronParams{Name: "shop", Jobs: CronJobList{job, job}}); f["jobs"] == "" {
		t.Error("accepted a repeated job id")
	}
}

func TestNewSiteWorkersStatusResultIsNotNull(t *testing.T) {
	if NewSiteWorkersStatusResult("shop").Workers == nil {
		t.Error("workers is nil, which encodes as null")
	}
}
