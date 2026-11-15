package helper

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"zimam/agent/contract"
)

// script runs the deploy-script program (bash reading stdin) through the
// real helper process and returns the lines it logged.
func script(t *testing.T, ctx context.Context, c *Client, a Account, text string) ([]string, int, error) {
	t.Helper()
	var lines []string
	code, err := c.Exec(ctx, a, ExecRequest{
		Program: "deploy-script",
		Stdin:   strings.NewReader(text),
		Log:     func(l string) { lines = append(lines, l) },
	})
	return lines, code, err
}

// serveExec calls the exec operation in this process with a raw request.
func serveExec(t *testing.T, root *os.Root, spec any) (int, string, string) {
	t.Helper()
	arg, ok := spec.(string)
	if !ok {
		b, err := json.Marshal(spec)
		if err != nil {
			t.Fatal(err)
		}
		arg = string(b)
	}
	var stdout, stderr bytes.Buffer
	code := Serve(root, "exec", []string{arg}, strings.NewReader(""), &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func TestExecRefusesUnknownProgram(t *testing.T) {
	root, _, _ := testHome(t)
	for _, p := range []string{"curl", "/bin/bash", "/usr/bin/git", "bash", "sh", "Git", "", "php8.3"} {
		code, out, errOut := serveExec(t, root, map[string]any{"program": p})
		if code != exitUsage || out != "" || !strings.Contains(errOut, "program") {
			t.Errorf("exec %q = %d, %q, %q; want 2 and a refusal", p, code, out, errOut)
		}
	}
	// The client refuses it before starting anything.
	c := &Client{AgentBinary: "/nonexistent/zimam-agent"}
	a := Account{Name: "s_test", UID: 1001, GID: 1001, Home: "/home/s_test"}
	if _, err := c.Exec(context.Background(), a, ExecRequest{Program: "curl"}); err == nil || !strings.Contains(err.Error(), "program") {
		t.Errorf("Client.Exec(curl) = %v; want a refusal", err)
	}
}

func TestExecRefusesBadRequests(t *testing.T) {
	root, _, _ := testHome(t)
	for _, spec := range []any{
		"not json",
		`{"program":"git","nope":1}`,
		`{"program":"git"}{"program":"git"}`,
		map[string]any{"program": "php"},                                   // needs a PHP version
		map[string]any{"program": "php", "php": "7.4"},                     // unsupported
		map[string]any{"program": "git", "php": "8.3"},                     // git has none
		map[string]any{"program": "deploy-script", "args": []string{"-c"}}, // takes no arguments
		map[string]any{"program": "git", "args": []string{"a\x00b"}},
		map[string]any{"program": "git", "capture_max": -1},
	} {
		code, out, errOut := serveExec(t, root, spec)
		if code != exitUsage || out != "" || errOut == "" {
			t.Errorf("exec %v = %d, %q, %q; want 2 and a message", spec, code, out, errOut)
		}
	}
	if code, _, _ := serveExec(t, root, "a"); code != exitUsage {
		t.Errorf("exec with a bad request = %d, want 2", code)
	}
	var stdout, stderr bytes.Buffer
	if code := Serve(root, "exec", nil, strings.NewReader(""), &stdout, &stderr); code != exitUsage {
		t.Errorf("exec without its argument = %d, want 2", code)
	}
}

func TestExecRefusesEnvOutsideAllowlist(t *testing.T) {
	root, _, _ := testHome(t)
	for _, c := range []struct {
		program string
		php     string
		env     []string
	}{
		{"git", "", []string{"LD_PRELOAD=/tmp/x.so"}},
		{"git", "", []string{"PATH=/tmp"}},
		{"git", "", []string{"HOME=/"}},
		{"git", "", []string{"LANG=C"}},
		{"git", "", []string{"COMPOSER_HOME=/home/s_x/.composer"}}, // composer's, not git's
		{"git", "", []string{"GIT_SSH_COMMAND=ssh\nx"}},
		{"git", "", []string{"GIT_SSH_COMMAND=ssh\x00x"}},
		{"git", "", []string{"GIT_SSH_COMMAND"}}, // no value
		{"git", "", []string{"GIT_DIR=/"}},
		{"git", "", []string{"=x"}},
		{"composer", "8.3", []string{"GIT_SSH_COMMAND=ssh"}},
		{"composer", "8.3", []string{"COMPOSER_NO_INTERACTION=0"}},
		{"composer", "8.3", []string{"COMPOSER_ALLOW_SUPERUSER=1"}},
		{"deploy-script", "", []string{"GIT_SSH_COMMAND=ssh"}},
		{"php", "8.3", []string{"PHP_INI_SCAN_DIR=/tmp"}},
	} {
		spec := map[string]any{"program": c.program, "env": c.env}
		if c.php != "" {
			spec["php"] = c.php
		}
		code, out, errOut := serveExec(t, root, spec)
		if code != exitUsage || out != "" || !strings.Contains(errOut, "environment") {
			t.Errorf("exec %v = %d, %q, %q; want 2 and an environment refusal", spec, code, out, errOut)
		}
	}
}

func TestExecEnvAllowlistAccepts(t *testing.T) {
	for _, s := range []execSpec{
		{Program: "git", Env: []string{"GIT_SSH_COMMAND=ssh -i ~/.ssh/zimam_deploy -o IdentitiesOnly=yes"}},
		{Program: "composer", PHP: "8.3", Env: envList(map[string]string{
			"COMPOSER_HOME": "/home/s_x/.composer", "COMPOSER_CACHE_DIR": "/home/s_x/.cache/composer", "COMPOSER_NO_INTERACTION": "1",
		})},
	} {
		if err := s.validate(); err != nil {
			t.Errorf("validate(%+v) = %v, want nil", s, err)
		}
	}
	dup := execSpec{Program: "git", Env: []string{"GIT_SSH_COMMAND=a", "GIT_SSH_COMMAND=b"}}
	if err := dup.validate(); err == nil {
		t.Error("validate accepted a key set twice")
	}
	env := execEnv("/home/s_x", execSpec{Program: "git", Env: []string{"GIT_SSH_COMMAND=ssh"}})
	want := []string{"PATH=/usr/bin:/bin", "HOME=/home/s_x", "LANG=C.UTF-8", "GIT_SSH_COMMAND=ssh"}
	if !slices.Equal(env, want) {
		t.Errorf("execEnv = %q, want %q", env, want)
	}
}

func TestProgramArgv(t *testing.T) {
	for _, c := range []struct {
		program, php string
		path         string
		lead         []string
	}{
		{"git", "", "/usr/bin/git", nil},
		{"ssh-keygen", "", "/usr/bin/ssh-keygen", nil},
		{"php", "8.3", "/usr/bin/php8.3", nil},
		{"artisan", "8.4", "/usr/bin/php8.4", []string{"artisan"}},
		{"composer", "8.5", "/usr/bin/php8.5", []string{"/usr/local/lib/zimam/composer/composer-2.11.2.phar"}},
		{"deploy-script", "", "/bin/bash", []string{"--noprofile", "--norc", "-eo", "pipefail", "-s"}},
	} {
		path, lead, err := ProgramArgv(c.program, contract.PHPVersion(c.php))
		if err != nil || path != c.path || !slices.Equal(lead, c.lead) {
			t.Errorf("ProgramArgv(%s, %q) = %q, %q, %v; want %q, %q", c.program, c.php, path, lead, err, c.path, c.lead)
		}
	}
	for _, c := range [][2]string{{"php", ""}, {"composer", "7.4"}, {"artisan", "8.3; rm"}, {"nope", ""}} {
		if _, _, err := ProgramArgv(c[0], contract.PHPVersion(c[1])); err == nil {
			t.Errorf("ProgramArgv(%s, %q) = nil error, want one", c[0], c[1])
		}
	}
}

func TestExecDirMustStayInHome(t *testing.T) {
	root, home, _ := testHome(t)
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(home, "out")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(home, "in"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("in/../../"+filepath.Base(outside), filepath.Join(home, "sneaky")); err != nil {
		t.Fatal(err)
	}
	put(t, home, "file", "x")
	for _, dir := range []string{"../x", "..", "/tmp", outside, "out", "in/../../x", "sneaky", "file", "missing"} {
		code, out, errOut := serveExec(t, root, map[string]any{"program": "deploy-script", "dir": dir})
		if code == exitOK || out != "" || errOut == "" {
			t.Errorf("exec in %q = %d, %q, %q; want a refusal and nothing run", dir, code, out, errOut)
		}
	}
}

func TestExecRunsInDir(t *testing.T) {
	c, a := testClient(t)
	if err := os.MkdirAll(filepath.Join(a.Home, "releases/one"), 0o750); err != nil {
		t.Fatal(err)
	}
	var lines []string
	code, err := c.Exec(context.Background(), a, ExecRequest{
		Program: "deploy-script", Dir: "releases/one",
		Stdin: strings.NewReader("pwd -P\n"),
		Log:   func(l string) { lines = append(lines, l) },
	})
	want, _ := filepath.EvalSymlinks(filepath.Join(a.Home, "releases/one"))
	if err != nil || code != 0 || !slices.Equal(lines, []string{want}) {
		t.Errorf("pwd in releases/one = %q, %d, %v; want %q", lines, code, err, want)
	}
}

func TestExecRunsInNewSession(t *testing.T) {
	c, a := testClient(t)
	lines, code, err := script(t, context.Background(), c, a,
		`read -r pid comm state ppid pgrp sid tty rest </proc/$$/stat; echo "$pid $pgrp $sid $tty"`+"\n")
	if err != nil || code != 0 || len(lines) != 1 {
		t.Fatalf("script = %q, %d, %v", lines, code, err)
	}
	f := strings.Fields(lines[0])
	if len(f) != 4 || f[0] != f[1] || f[0] != f[2] || f[3] != "0" {
		t.Errorf("pid pgrp sid tty = %q; want a session leader without a terminal", lines[0])
	}
}

func TestExecEnvIsOnlyAllowlist(t *testing.T) {
	c, a := testClient(t)
	lines, code, err := script(t, context.Background(), c, a, `tr '\0' '\n' </proc/$$/environ`+"\n")
	want := []string{"PATH=/usr/bin:/bin", "HOME=" + a.Home, "LANG=C.UTF-8"}
	if err != nil || code != 0 || !slices.Equal(lines, want) {
		t.Errorf("environment = %q, %d, %v; want %q", lines, code, err, want)
	}
}

func TestExecUmaskAndFDs(t *testing.T) {
	c, a := testClient(t)
	lines, code, err := script(t, context.Background(), c, a, "umask\nls /proc/$$/fd | sort -n | tr '\\n' ' '; echo\n")
	if err != nil || code != 0 || len(lines) != 2 {
		t.Fatalf("script = %q, %d, %v", lines, code, err)
	}
	if lines[0] != "0027" {
		t.Errorf("umask = %q, want 0027", lines[0])
	}
	for _, fd := range strings.Fields(lines[1]) {
		if n, _ := strconv.Atoi(fd); n > 2 && n != 255 { // bash keeps 255 for itself
			t.Errorf("open descriptors = %q; want only 0-2", lines[1])
			break
		}
	}
}

func TestExecStdinOnlyForDeployScript(t *testing.T) {
	c := &Client{AgentBinary: "/nonexistent/zimam-agent"}
	a := Account{Name: "s_test", UID: 1001, GID: 1001, Home: "/home/s_test"}
	_, err := c.Exec(context.Background(), a, ExecRequest{Program: "git", Stdin: strings.NewReader("x")})
	if err == nil || !strings.Contains(err.Error(), "stdin") {
		t.Errorf("git with stdin = %v; want a refusal", err)
	}
}

func TestExecExitCode(t *testing.T) {
	c, a := testClient(t)
	lines, code, err := script(t, context.Background(), c, a, "echo out\necho err >&2\nexit 7\n")
	if err != nil || code != 7 || !slices.Equal(lines, []string{"out", "err"}) {
		t.Errorf("script = %q, %d, %v; want out, err and 7", lines, code, err)
	}
	// A child killed by a signal reports 128 + the signal.
	_, code, err = script(t, context.Background(), c, a, "kill -KILL $$\n")
	if err != nil || code != 128+int(syscall.SIGKILL) {
		t.Errorf("killed script = %d, %v; want %d", code, err, 128+int(syscall.SIGKILL))
	}
}

func TestExecOutputLineCut(t *testing.T) {
	c, a := testClient(t)
	lines, code, err := script(t, context.Background(), c, a,
		"head -c 40960 /dev/zero | tr '\\0' a; echo\necho next\n")
	if err != nil || code != 0 || len(lines) != 2 {
		t.Fatalf("script = %d lines, %d, %v; want 2 lines", len(lines), code, err)
	}
	if want := strings.Repeat("a", MaxExecLineBytes) + execLineCutMarker; lines[0] != want {
		t.Errorf("first line has %d bytes, ends %q; want %d a's and the marker", len(lines[0]), lines[0][max(0, len(lines[0])-40):], MaxExecLineBytes)
	}
	if lines[1] != "next" {
		t.Errorf("second line = %q, want next", lines[1])
	}
}

func TestCutLineKeepsRunes(t *testing.T) {
	line := append(bytes.Repeat([]byte("a"), MaxExecLineBytes-1), "é"...) // é is 2 bytes
	var got [][]byte
	err := pumpLines(bytes.NewReader(append(line, "zz\n"...)), func(l []byte, cut bool) {
		got = append(got, append([]byte(nil), l...))
		if !cut {
			t.Error("the line wasn't marked cut")
		}
	})
	if err != nil || len(got) != 1 || len(got[0]) != MaxExecLineBytes-1 {
		t.Errorf("pumpLines = %d lines, %v; want one line of %d bytes without the half rune", len(got), err, MaxExecLineBytes-1)
	}
}

func TestExecOutputTotalCap(t *testing.T) {
	c, a := testClient(t)
	// 20 MiB of 1 KiB lines, then a file only the end of the script writes.
	lines, code, err := script(t, context.Background(), c, a,
		"head -c 20971520 /dev/zero | tr '\\0' x | fold -w 1023\necho after\ntouch finished\n")
	if err != nil || code != 0 {
		t.Fatalf("script = %d, %v", code, err)
	}
	if _, err := os.Stat(filepath.Join(a.Home, "finished")); err != nil {
		t.Errorf("the script didn't run to the end: %v", err)
	}
	var logged int
	notices := 0
	for _, l := range lines {
		if l == execTotalCapNotice {
			notices++
			continue
		}
		logged += len(l)
	}
	if notices != 1 || lines[len(lines)-1] != execTotalCapNotice {
		t.Errorf("%d notices, last line %q; want one notice at the end", notices, lines[len(lines)-1])
	}
	if logged > MaxExecLogBytes || logged < MaxExecLogBytes-1024 {
		t.Errorf("logged %d bytes, want just under %d", logged, MaxExecLogBytes)
	}
}

func TestExecCancelKillsProcessGroup(t *testing.T) {
	c, a := testClient(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pids := make(chan int, 2)
	done := make(chan error, 1)
	go func() {
		_, err := c.Exec(ctx, a, ExecRequest{
			Program: "deploy-script",
			Stdin:   strings.NewReader("sleep 600 &\necho $! $$\nwait\n"),
			Log: func(l string) {
				for _, f := range strings.Fields(l) {
					n, _ := strconv.Atoi(f)
					pids <- n
				}
			},
		})
		done <- err
	}()
	var sleepPID, bashPID int
	select {
	case sleepPID = <-pids:
		bashPID = <-pids
	case err := <-done:
		t.Fatalf("Exec ended early: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("no pid within 10s")
	}
	start := time.Now()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("Exec after cancel = %v, want context.Canceled", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Exec didn't return within 10s of cancel")
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Errorf("cancel took %s; want the group killed at once", d)
	}
	for _, pid := range []int{sleepPID, bashPID} {
		if alive(pid) {
			t.Errorf("process %d is still running after cancel", pid)
		}
	}
}

// alive reports whether pid is a process that isn't a zombie, within 2s.
func alive(pid int) bool {
	for range 20 {
		stat, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
		if err != nil {
			return false
		}
		if _, after, ok := bytes.Cut(stat, []byte(") ")); ok && bytes.HasPrefix(after, []byte("Z")) {
			return false
		}
		time.Sleep(100 * time.Millisecond)
	}
	return true
}

func TestExecKillsLeftoversAfterExit(t *testing.T) {
	c, a := testClient(t)
	start := time.Now()
	lines, code, err := script(t, context.Background(), c, a, "sleep 600 &\necho $!\n")
	if err != nil || code != 0 || len(lines) != 1 {
		t.Fatalf("script = %q, %d, %v", lines, code, err)
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Errorf("Exec took %s; a background child held it up", d)
	}
	if pid, _ := strconv.Atoi(lines[0]); alive(pid) {
		t.Errorf("the background sleep %d outlived the script", pid)
	}
}

func TestExecCapture(t *testing.T) {
	c, a := testClient(t)
	var captured bytes.Buffer
	var lines []string
	code, err := c.Exec(context.Background(), a, ExecRequest{
		Program: "deploy-script",
		Stdin:   strings.NewReader("printf 'raw\\0bytes\\nmore'\necho on-stderr >&2\n"),
		Capture: &captured, CaptureMax: 100,
		Log: func(l string) { lines = append(lines, l) },
	})
	if err != nil || code != 0 {
		t.Fatalf("capture = %d, %v", code, err)
	}
	if captured.String() != "raw\x00bytes\nmore" {
		t.Errorf("captured %q, want the raw stdout", captured.String())
	}
	if !slices.Equal(lines, []string{"on-stderr"}) {
		t.Errorf("logged %q; want only stderr", lines)
	}
}

func TestExecCaptureOverCapFails(t *testing.T) {
	c, a := testClient(t)
	var captured bytes.Buffer
	start := time.Now()
	_, err := c.Exec(context.Background(), a, ExecRequest{
		Program: "deploy-script",
		Stdin:   strings.NewReader("head -c 1000000 /dev/zero\nsleep 600\n"),
		Capture: &captured, CaptureMax: 1000,
	})
	if err == nil || !strings.Contains(err.Error(), "1000 bytes") {
		t.Errorf("capture over the cap = %v; want an error naming the cap", err)
	}
	if captured.Len() > 1000 {
		t.Errorf("captured %d bytes, more than the cap", captured.Len())
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("took %s; the program should be killed at the cap", d)
	}
}

func TestExecCaptureNeedsBoth(t *testing.T) {
	c := &Client{AgentBinary: "/nonexistent/zimam-agent"}
	a := Account{Name: "s_test", UID: 1001, GID: 1001, Home: "/home/s_test"}
	for _, r := range []ExecRequest{
		{Program: "git", Capture: &bytes.Buffer{}},
		{Program: "git", CaptureMax: 10},
	} {
		if _, err := c.Exec(context.Background(), a, r); err == nil {
			t.Errorf("Exec(%+v) = nil error; want a refusal", r)
		}
	}
}

func TestReadExecFramesRejectsBadOutput(t *testing.T) {
	for _, out := range []string{
		"",                          // no result
		"L 3\nabc",                  // no result
		"{\"exit_code\":0}\nL 1\nx", // something after the result
		"X 1\nx{\"exit_code\":0}\n",
		"L x\n",
		"L -1\n",
		"L 99999999999\n",
		"L " + strconv.Itoa(maxExecFrame+1) + "\n",
		"C 3\nabc{\"exit_code\":0}\n", // capture without a writer
		"{\"exit_code\":0,\"x\":1}\n",
		"L 5\nabc",
	} {
		if _, err := readExecFrames(strings.NewReader(out), func(string) {}, nil, 0); err == nil {
			t.Errorf("readExecFrames(%q) = nil error, want one", out)
		}
	}
	var lines []string
	res, err := readExecFrames(strings.NewReader("L 2\nhiL 0\n{\"exit_code\":4}\n"), func(l string) { lines = append(lines, l) }, nil, 0)
	if err != nil || res.ExitCode != 4 || !slices.Equal(lines, []string{"hi", ""}) {
		t.Errorf("readExecFrames = %+v, %q, %v", res, lines, err)
	}
}

func FuzzReadExecFrames(f *testing.F) {
	f.Add([]byte("L 2\nhiC 3\nabc{\"exit_code\":4}\n"))
	f.Add([]byte("L 0\n{\"exit_code\":0}\n"))
	f.Fuzz(func(t *testing.T, out []byte) {
		var captured bytes.Buffer
		res, err := readExecFrames(bytes.NewReader(out), func(l string) {
			if len(l) > maxExecFrame*3 { // ToValidUTF8 may grow it
				t.Fatalf("logged a %d-byte line", len(l))
			}
		}, &captured, 64)
		if captured.Len() > 64 {
			t.Fatalf("captured %d bytes over the cap of 64", captured.Len())
		}
		if err == nil && res.ExitCode < 0 {
			t.Fatalf("exit code %d", res.ExitCode)
		}
	})
}

func FuzzExecSpec(f *testing.F) {
	f.Add(`{"program":"git","args":["fetch"],"dir":"repo.git","env":["GIT_SSH_COMMAND=ssh"]}`)
	f.Add(`{"program":"composer","php":"8.3","env":["COMPOSER_NO_INTERACTION=1"]}`)
	f.Fuzz(func(t *testing.T, arg string) {
		s, err := decodeExecSpec(arg)
		if err != nil {
			return
		}
		if _, _, err := ProgramArgv(s.Program, s.PHP); err != nil {
			t.Fatalf("decoded %q but its program is refused: %v", arg, err)
		}
		for _, kv := range execEnv("/home/s_x", s) {
			if strings.ContainsAny(kv, "\x00\n") {
				t.Fatalf("environment entry %q", kv)
			}
		}
		if s.Dir != "" && !filepath.IsLocal(s.Dir) {
			t.Fatalf("dir %q accepted", s.Dir)
		}
	})
}

// An inherited descriptor without close-on-exec must not reach the program:
// Go closes only the descriptors it opened itself.
func TestExecClosesInheritedFDs(t *testing.T) {
	c, a := testClient(t)
	f, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	fd := int(f.Fd())
	if _, err := unix.FcntlInt(uintptr(fd), unix.F_SETFD, 0); err != nil {
		t.Fatal(err)
	}
	lines, code, err := script(t, context.Background(), c, a, "ls /proc/$$/fd | sort -n | tr '\n' ' '; echo\n")
	if err != nil || code != 0 || len(lines) != 1 {
		t.Fatalf("script = %q, %d, %v", lines, code, err)
	}
	for _, s := range strings.Fields(lines[0]) {
		if n, _ := strconv.Atoi(s); n > 2 && n != 255 {
			t.Errorf("open descriptors = %q (inherited %d); want only 0-2", lines[0], fd)
			break
		}
	}
}

func TestCloseOnExecViaProc(t *testing.T) {
	f, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	fd := int(f.Fd())
	if _, err := unix.FcntlInt(uintptr(fd), unix.F_SETFD, 0); err != nil {
		t.Fatal(err)
	}
	if err := closeOnExecViaProc(fd); err != nil {
		t.Fatal(err)
	}
	flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0)
	if err != nil || flags&unix.FD_CLOEXEC == 0 {
		t.Errorf("flags of fd %d = %#x, %v; want FD_CLOEXEC", fd, flags, err)
	}
}

// The helper is not dumpable, so another process of the site user can't
// open /proc/<helper>/fd/1 and write frames into the agent's stream.
func TestExecHelperIsNotDumpable(t *testing.T) {
	c, a := testClient(t)
	lines, code, err := script(t, context.Background(), c, a,
		"readlink /proc/$PPID/fd/1 >/dev/null 2>&1 && echo open || echo denied\n")
	if err != nil || code != 0 || !slices.Equal(lines, []string{"denied"}) {
		t.Errorf("reading the helper's stdout link = %q, %d, %v; want denied", lines, code, err)
	}
}

func TestExecRefusesInvalidUTF8(t *testing.T) {
	root, _, _ := testHome(t)
	for _, spec := range []execSpec{
		{Program: "git", Args: []string{"a\xffb"}},
		{Program: "git", Dir: "re\xffpo"},
	} {
		// Written by hand: json.Marshal would replace the bad bytes.
		arg := `{"program":"git","args":["` + strings.Join(spec.Args, "") + `"]}`
		if spec.Dir != "" {
			arg = `{"program":"git","dir":"` + spec.Dir + `"}`
		}
		if code, out, _ := serveExec(t, root, arg); code != exitUsage || out != "" {
			t.Errorf("exec %q = %d, %q; want 2", arg, code, out)
		}
		if err := spec.validate(); err == nil {
			t.Errorf("validate(%+v) = nil; want an error", spec)
		}
	}
}

func TestExecDirFIFODoesntHang(t *testing.T) {
	root, home, _ := testHome(t)
	if err := unix.Mkfifo(filepath.Join(home, "fifo"), 0o600); err != nil {
		t.Fatal(err)
	}
	done := make(chan int, 1)
	go func() {
		code, _, _ := serveExec(t, root, map[string]any{"program": "deploy-script", "dir": "fifo"})
		done <- code
	}()
	select {
	case code := <-done:
		if code == exitOK {
			t.Error("exec in a FIFO succeeded")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("exec in a FIFO hung")
	}
}

// The helper's own cap, without the client's: capture past capture_max
// fails the run and sends no more than the cap.
func TestExecHelperCaptureCap(t *testing.T) {
	root, _, _ := testHome(t)
	t.Chdir(t.TempDir()) // runExec fchdirs this process
	arg, _ := json.Marshal(execSpec{Program: "deploy-script", CaptureMax: 100})
	var stdout, stderr bytes.Buffer
	code := Serve(root, "exec", []string{string(arg)}, strings.NewReader("head -c 100000 /dev/zero\n"), &stdout, &stderr)
	if code != exitFailed || !strings.Contains(stderr.String(), "100 bytes") {
		t.Errorf("exec = %d, %q; want 1 and the cap", code, stderr.String())
	}
	var captured bytes.Buffer
	if _, err := readExecFrames(&stdout, func(string) {}, &captured, 1<<20); !errors.Is(err, errNoResult) || captured.Len() > 100 {
		t.Errorf("frames = %v, %d captured bytes; want no result and at most 100", err, captured.Len())
	}
}

// deadlineReader returns its data, then os.ErrDeadlineExceeded.
type deadlineReader struct{ data []byte }

func (r *deadlineReader) Read(p []byte) (int, error) {
	if len(r.data) == 0 {
		return 0, os.ErrDeadlineExceeded
	}
	n := copy(p, r.data)
	r.data = r.data[n:]
	return n, nil
}

func TestPumpLinesKeepsPartialLineOnDeadline(t *testing.T) {
	var got []string
	err := pumpLines(&deadlineReader{data: []byte("one\npart")}, func(l []byte, _ bool) { got = append(got, string(l)) })
	if err != nil || !slices.Equal(got, []string{"one", "part"}) {
		t.Errorf("pumpLines = %q, %v; want one and part", got, err)
	}
}
