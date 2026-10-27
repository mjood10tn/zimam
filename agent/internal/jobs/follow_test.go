package jobs

import (
	"context"
	"encoding/json"
	"reflect"
	"sync"
	"testing"
	"time"

	"zimam/agent/contract"
)

// collect records what Follow sends.
type collect struct {
	mu   sync.Mutex
	sent []any
}

func (c *collect) send(v any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sent = append(c.sent, v)
	return nil
}

func (c *collect) all() []any {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]any(nil), c.sent...)
}

func resultStatus(t *testing.T, v any) contract.JobStatusResult {
	t.Helper()
	r, ok := v.(contract.Result)
	if !ok || r.Type != "result" || !r.OK {
		t.Fatalf("final line = %#v, want a result", v)
	}
	var res contract.JobStatusResult
	if err := json.Unmarshal(r.Data, &res); err != nil {
		t.Fatal(err)
	}
	return res
}

func TestFollowFromOffset(t *testing.T) {
	f := newFixture(t)
	id := f.submit(t, "php.default", &testParams{})
	f.run(t, id)
	first, second := "[2026-10-28T12:00:00Z] hello world\n", "[2026-10-28T12:00:00Z] two\n"
	var c collect
	if err := f.m.Follow(context.Background(), id, int64(len(first)), c.send); err != nil {
		t.Fatal(err)
	}
	got := c.all()
	want := []any{
		contract.LogLine{Type: "log", Job: id, Offset: int64(len(first)), Line: "[2026-10-28T12:00:00Z] two"},
		contract.LogLine{Type: "log", Job: id, Offset: int64(len(first) + len(second)), Line: "[2026-10-28T12:00:00Z] lines"},
	}
	if len(got) != 3 || !reflect.DeepEqual(got[:2], want) {
		t.Fatalf("sent %#v, want %#v and the result", got, want)
	}
	if res := resultStatus(t, got[2]); res.Status != contract.JobSucceeded || res.Job != id {
		t.Errorf("result = %+v, want the succeeded job", res)
	}
	var none collect
	err := f.m.Follow(context.Background(), "01JBX7Q3A1B2C3D4E5F6G7H8J9", 0, none.send)
	expectCode(t, err, contract.ErrNotFound)
}

func TestFollowEndsWithResult(t *testing.T) {
	poll := followPoll
	followPoll = 5 * time.Millisecond
	t.Cleanup(func() { followPoll = poll })
	f := newFixture(t)
	proceed := make(chan struct{})
	f.m.Handlers["php.default"] = func(_ context.Context, j *Context, _ contract.Params) (any, error) {
		j.Logf("working")
		<-proceed
		j.Logf("done")
		return testResult{Greeting: "hi"}, nil
	}
	id := f.submit(t, "php.default", &testParams{})
	runDone := make(chan error, 1)
	go func() { runDone <- f.m.RunJob(context.Background(), id) }()
	var c collect
	followDone := make(chan error, 1)
	go func() { followDone <- f.m.Follow(context.Background(), id, 0, c.send) }()
	for deadline := time.Now().Add(5 * time.Second); len(c.all()) == 0; time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("Follow sent nothing while the job ran")
		}
	}
	close(proceed)
	if err := <-runDone; err != nil {
		t.Fatal(err)
	}
	if err := <-followDone; err != nil {
		t.Fatal(err)
	}
	got := c.all()
	if len(got) != 3 {
		t.Fatalf("sent %#v, want two lines and the result", got)
	}
	for i, want := range []string{"[2026-10-28T12:00:00Z] working", "[2026-10-28T12:00:00Z] done"} {
		if l, ok := got[i].(contract.LogLine); !ok || l.Line != want {
			t.Errorf("line %d = %#v, want %q", i, got[i], want)
		}
	}
	if res := resultStatus(t, got[2]); res.Status != contract.JobSucceeded {
		t.Errorf("result = %+v, want succeeded", res)
	}
}

func TestFollowFailedEndsWithJobFailed(t *testing.T) {
	f := newFixture(t)
	id := f.submit(t, "site.provision", &testParams{Name: "taken"})
	f.run(t, id)
	var c collect
	if err := f.m.Follow(context.Background(), id, 0, c.send); err != nil {
		t.Fatal(err)
	}
	got := c.all()
	want := contract.Error{Type: "error", Code: contract.ErrJobFailed, Message: "the site exists"}
	if len(got) == 0 || !reflect.DeepEqual(got[len(got)-1], want) {
		t.Errorf("sent %#v, want it to end with %#v", got, want)
	}
}
