package templates

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The scripts and site.env.sh live in a site's home, not on a server's root
// tree, so they are golden files of their own: configcheck never sees them.
const scriptsGoldenDir = "testdata/site-scripts"

func TestSiteScriptsGolden(t *testing.T) {
	for _, c := range []struct {
		file, tmpl string
		data       any
	}{
		{"worker-laravel-queue.sh", "site/worker.sh", WorkerScript{Argv: []string{"php", "artisan", "queue:work", "redis", "--queue=high,default", "--tries=3", "--timeout=0", "--memory=512"}}},
		{"worker-custom.sh", "site/worker.sh", WorkerScript{Command: "node server.js | tee -a \"$HOME/logs/node.log\""}},
		{"cron-job.sh", "site/cron.sh", CronScript{ID: "backup", Command: "php artisan backup:run && echo 'done'"}},
		{"scheduler.sh", "site/cron.sh", CronScript{ID: "scheduler", Command: "php artisan schedule:run"}},
		{"site.env.sh", "site/site.env.sh", SiteEnv{PHPVersion: "8.4"}},
		{"site.env-static.sh", "site/site.env.sh", SiteEnv{}},
	} {
		out := mustRender(t, c.tmpl, c.data)
		golden := filepath.Join(scriptsGoldenDir, c.file)
		if *update {
			if err := os.MkdirAll(scriptsGoldenDir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(golden, []byte(out), 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		want, err := os.ReadFile(golden)
		if err != nil {
			t.Errorf("%s: %v (go test ./internal/templates -update writes it)", c.file, err)
		} else if out != string(want) {
			t.Errorf("%s differs from %s:\n--- rendered\n%s--- golden\n%s", c.file, golden, out, want)
		}
	}
}

// Every script's third line sources site.env.sh (R04-D15), however the
// command reads.
func TestScriptsSourceSiteEnv(t *testing.T) {
	for _, out := range []string{
		mustRender(t, "site/worker.sh", WorkerScript{Argv: []string{"php"}}),
		mustRender(t, "site/worker.sh", WorkerScript{Command: "run"}),
		mustRender(t, "site/cron.sh", CronScript{ID: "a", Command: "run"}),
	} {
		if got := strings.Split(out, "\n")[2]; got != `. "$HOME/.zimam/site.env.sh"` {
			t.Errorf("third line is %q:\n%s", got, out)
		}
	}
}

// shquote's output must come back from bash as exactly the word, whatever
// it holds: quotes, spaces, expansions, a leading dash, and every character
// the validated queue, connection and number sets allow.
func TestShquoteRoundTripsThroughBash(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("no bash")
	}
	words := []string{"", "plain", "it's", `'`, `''`, "a b", "$(touch pwned)", "`touch pwned`", `a"b`, `\`, "-n", "--queue=high,default", "high:v1.2_x-y", "a;b&c|d", "*", "~", "é ü"}
	for _, w := range words {
		q, err := shquote(w)
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(bash, "-c", "printf %s "+q)
		cmd.Dir = t.TempDir()
		var out bytes.Buffer
		cmd.Stdout = &out
		if err := cmd.Run(); err != nil {
			t.Fatalf("%q: %v", w, err)
		}
		if out.String() != w {
			t.Errorf("%q came back as %q", w, out.String())
		}
		if _, err := os.Stat(filepath.Join(cmd.Dir, "pwned")); err == nil {
			t.Errorf("%q ran a command", w)
		}
	}
	if _, err := shquote("a\x00b"); err == nil {
		t.Error("shquote took NUL")
	}
	if _, err := shquote(3); err == nil {
		t.Error("shquote took an int")
	}
}
