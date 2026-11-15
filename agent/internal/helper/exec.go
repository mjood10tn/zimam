package helper

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"golang.org/x/sys/unix"

	"zimam/agent/contract"
)

// The exec operation (R02-D1) runs one program of a fixed set as the site
// user: in a new session, in a directory inside the home, with only an
// allowlisted environment, umask 027 and descriptors 0-2. Its output comes
// back to the agent in frames on the helper's stdout:
//
//	L <n>\n<n bytes>   one line for the job log (stdout and stderr, or only
//	                   stderr in capture mode)
//	C <n>\n<n bytes>   raw stdout, in capture mode
//	{"exit_code":N}\n  the end: the program's exit code
//
// Every line of the program goes through an L frame, so the program can't
// forge the end.

// Output limits (R02-D1): each line is cut at MaxExecLineBytes, and the job
// log gets at most MaxExecLogBytes of a run, then one notice.
const (
	MaxExecLineBytes = 16 << 10
	MaxExecLogBytes  = 16 << 20
)

const (
	execLineCutMarker  = " [line cut at 16 KiB]"
	execTotalCapNotice = "[output passed 16 MiB; the rest is not logged]"
	// maxExecFrame is the largest frame the client accepts: a cut line with
	// its marker, or one capture chunk.
	maxExecFrame = 64 << 10
	// execKillWait is how long the helper waits for the output pipes after
	// the program's process group is killed: a process that left the group
	// may still hold them.
	execKillWait = 5 * time.Second
)

// ComposerPhar is the pinned Composer (R02-D6, spike amendment A9). The
// Composer task moves the version into paths.
const ComposerPhar = "/usr/local/lib/zimam/composer/composer-2.11.2.phar"

// execProgram is one program the helper may run.
type execProgram struct {
	path  string   // absolute; "" for the PHP programs, which use /usr/bin/php<v>
	lead  []string // fixed arguments before the agent's
	noArg bool     // the agent may add no arguments
	stdin bool     // reads stdin from the agent; every other program gets /dev/null
	env   []string // environment keys the agent may set, besides PATH, HOME and LANG
}

var execPrograms = map[string]execProgram{
	"git":        {path: "/usr/bin/git", env: []string{"GIT_SSH_COMMAND"}},
	"ssh-keygen": {path: "/usr/bin/ssh-keygen"},
	"php":        {},
	"artisan":    {lead: []string{"artisan"}},
	"composer": {lead: []string{ComposerPhar},
		env: []string{"COMPOSER_HOME", "COMPOSER_CACHE_DIR", "COMPOSER_NO_INTERACTION"}},
	// R02-D2: the script comes on stdin, never in argv.
	"deploy-script": {path: "/bin/bash", lead: []string{"--noprofile", "--norc", "-eo", "pipefail", "-s"}, noArg: true, stdin: true},
}

// ProgramArgv returns the absolute path of the program name and the fixed
// arguments that come before the agent's. The PHP programs (php, artisan,
// composer) need a supported PHP version; the others ignore it.
func ProgramArgv(name string, php contract.PHPVersion) (string, []string, error) {
	p, ok := execPrograms[name]
	if !ok {
		return "", nil, fmt.Errorf("exec: %q is not a program the helper runs", name)
	}
	path := p.path
	if path == "" {
		if err := php.Validate(); err != nil {
			return "", nil, fmt.Errorf("exec %s: PHP version %q %w", name, php, err)
		}
		path = "/usr/bin/php" + string(php)
	}
	return path, slices.Clone(p.lead), nil
}

// execSpec is the exec operation's one argument, as JSON.
type execSpec struct {
	Program    string              `json:"program"`
	PHP        contract.PHPVersion `json:"php,omitempty"`
	Args       []string            `json:"args,omitempty"`
	Dir        string              `json:"dir,omitempty"`         // relative to the home; "" is the home
	Env        []string            `json:"env,omitempty"`         // KEY=value, keys unique
	CaptureMax int64               `json:"capture_max,omitempty"` // > 0: capture mode
}

func (s execSpec) validate() error {
	p, ok := execPrograms[s.Program]
	if !ok {
		return fmt.Errorf("exec: %q is not a program the helper runs", s.Program)
	}
	if p.path == "" {
		if _, _, err := ProgramArgv(s.Program, s.PHP); err != nil {
			return err
		}
	} else if s.PHP != "" {
		return fmt.Errorf("exec %s: takes no PHP version", s.Program)
	}
	if p.noArg && len(s.Args) > 0 {
		return fmt.Errorf("exec %s: takes no arguments", s.Program)
	}
	for _, a := range s.Args {
		if strings.IndexByte(a, 0) >= 0 {
			return fmt.Errorf("exec %s: an argument contains NUL", s.Program)
		}
	}
	if s.Dir != "" && !filepath.IsLocal(s.Dir) {
		return fmt.Errorf("exec %s: the directory %q is not inside the home", s.Program, s.Dir)
	}
	seen := map[string]bool{}
	for _, kv := range s.Env {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			return fmt.Errorf("exec %s: the environment entry %q has no value", s.Program, k)
		}
		if seen[k] {
			return fmt.Errorf("exec %s: the environment variable %q is set twice", s.Program, k)
		}
		seen[k] = true
		if !slices.Contains(p.env, k) {
			return fmt.Errorf("exec %s: the environment variable %q is not allowed", s.Program, k)
		}
		if strings.ContainsAny(v, "\x00\n") || !utf8.ValidString(v) {
			return fmt.Errorf("exec %s: the environment variable %s has a bad value", s.Program, k)
		}
		if k == "COMPOSER_NO_INTERACTION" && v != "1" {
			return fmt.Errorf("exec %s: the environment variable %s must be 1", s.Program, k)
		}
	}
	if s.CaptureMax < 0 {
		return fmt.Errorf("exec %s: the capture limit can't be negative", s.Program)
	}
	return nil
}

func decodeExecSpec(arg string) (execSpec, error) {
	var s execSpec
	if err := contract.DecodeStrict([]byte(arg), &s); err != nil {
		return execSpec{}, usageError{fmt.Errorf("exec: the request: %w", err)}
	}
	if err := s.validate(); err != nil {
		return execSpec{}, usageError{err}
	}
	return s, nil
}

// execEnv is the program's whole environment: PATH, HOME, LANG and the
// allowlisted keys, sorted.
func execEnv(home string, s execSpec) []string {
	env := []string{"PATH=/usr/bin:/bin", "HOME=" + home, "LANG=C.UTF-8"}
	return append(env, slices.Sorted(slices.Values(s.Env))...)
}

// envList turns the client's map into execSpec's sorted KEY=value list.
func envList(m map[string]string) []string {
	var env []string
	for _, k := range slices.Sorted(maps.Keys(m)) {
		env = append(env, k+"="+m[k])
	}
	return env
}

// execResult is the exec operation's result line.
type execResult struct {
	ExitCode int `json:"exit_code"`
}

// frameWriter writes frames to the helper's stdout, from more than one
// goroutine in capture mode.
type frameWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (f *frameWriter) frame(kind byte, payload []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	hdr := append([]byte{kind, ' '}, strconv.Itoa(len(payload))...)
	if _, err := f.w.Write(append(hdr, '\n')); err != nil {
		return err
	}
	_, err := f.w.Write(payload)
	return err
}

// lineLog applies the line and total caps to what goes to the job log.
type lineLog struct {
	out    *frameWriter
	logged int
	full   bool
}

func (l *lineLog) add(line []byte, cut bool) error {
	if l.full {
		return nil // dropped; the program keeps running
	}
	if l.logged+len(line) > MaxExecLogBytes {
		l.full = true
		return l.out.frame('L', []byte(execTotalCapNotice))
	}
	l.logged += len(line)
	if cut {
		line = append(line, execLineCutMarker...)
	}
	return l.out.frame('L', line)
}

// pumpLines reads r to the end and calls emit for each line, without its
// newline, cut at MaxExecLineBytes on a rune boundary; the rest of a cut
// line is dropped.
func pumpLines(r io.Reader, emit func(line []byte, cut bool)) error {
	br := bufio.NewReaderSize(r, 32<<10)
	line := make([]byte, 0, MaxExecLineBytes)
	cut := false
	for {
		chunk, err := br.ReadSlice('\n')
		end := err == nil
		if end {
			chunk = chunk[:len(chunk)-1]
		}
		if !cut {
			if room := MaxExecLineBytes - len(line); len(chunk) > room {
				line, cut = append(line, chunk[:room]...), true
				line = trimPartialRune(line)
			} else {
				line = append(line, chunk...)
			}
		}
		if end || (err == io.EOF && (len(line) > 0 || cut)) {
			emit(line, cut)
			line, cut = line[:0], false
		}
		switch {
		case err == nil, errors.Is(err, bufio.ErrBufferFull):
		case errors.Is(err, io.EOF), errors.Is(err, os.ErrDeadlineExceeded):
			return nil
		default:
			return err
		}
	}
}

// trimPartialRune drops an incomplete UTF-8 sequence at the end of b.
func trimPartialRune(b []byte) []byte {
	for i := len(b) - 1; i >= 0 && i >= len(b)-utf8.UTFMax; i-- {
		if utf8.RuneStart(b[i]) {
			if !utf8.FullRune(b[i:]) {
				return b[:i]
			}
			break
		}
	}
	return b
}

// runExec is the exec operation: it runs the program and streams frames to
// stdout until it ends. SIGTERM (the client's cancel) kills the program's
// process group.
func runExec(root *os.Root, arg string, stdin io.Reader, stdout io.Writer) (any, error) {
	s, err := decodeExecSpec(arg)
	if err != nil {
		return nil, err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	path, lead, err := ProgramArgv(s.Program, s.PHP)
	if err != nil {
		return nil, usageError{err}
	}
	dir := s.Dir
	if dir == "" {
		dir = "."
	}
	// os.Root refuses a directory that leaves the home, symlinks included;
	// the program starts in the directory that was opened, not a path.
	d, err := root.Open(dir)
	if err != nil {
		return nil, fmt.Errorf("exec %s: %w", s.Program, err)
	}
	fi, err := d.Stat()
	if err == nil && !fi.IsDir() {
		err = fmt.Errorf("%s is not a directory", s.Dir)
	}
	if err == nil {
		err = syscall.Fchdir(int(d.Fd()))
	}
	d.Close()
	if err != nil {
		return nil, fmt.Errorf("exec %s: %w", s.Program, err)
	}

	cmd := &exec.Cmd{
		Path:        path,
		Args:        append(append([]string{path}, lead...), s.Args...),
		Env:         execEnv(root.Name(), s),
		SysProcAttr: &syscall.SysProcAttr{Setsid: true},
	}
	if execPrograms[s.Program].stdin {
		cmd.Stdin = stdin
	}
	// Go closes only the descriptors it opened itself at exec; one this
	// process inherited without close-on-exec would reach the program.
	if err := closeOnExecFrom(3); err != nil {
		return nil, fmt.Errorf("exec %s: %w", s.Program, err)
	}
	// The program gets plain pipes, so Wait returns when it exits, not when
	// every process holding them has.
	logR, logW, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	defer logR.Close()
	cmd.Stdout, cmd.Stderr = logW, logW
	var capR, capW *os.File
	if s.CaptureMax > 0 {
		if capR, capW, err = os.Pipe(); err != nil {
			logW.Close()
			return nil, err
		}
		defer capR.Close()
		cmd.Stdout = capW
	}
	err = cmd.Start()
	logW.Close()
	if capW != nil {
		capW.Close()
	}
	if err != nil {
		return nil, fmt.Errorf("exec %s: %w", s.Program, err)
	}
	pgid := cmd.Process.Pid // a session leader leads its own process group
	killGroup := func() { syscall.Kill(-pgid, syscall.SIGKILL) }

	var (
		failOnce sync.Once
		failErr  error
		failed   = make(chan struct{})
	)
	fail := func(err error) {
		failOnce.Do(func() { failErr = err; close(failed); killGroup() })
	}
	out := &frameWriter{w: stdout}
	var readers sync.WaitGroup
	readers.Go(func() {
		log := &lineLog{out: out}
		err := pumpLines(logR, func(line []byte, cut bool) {
			if err := log.add(line, cut); err != nil {
				fail(fmt.Errorf("exec %s: writing the output: %w", s.Program, err))
			}
		})
		if err != nil {
			fail(fmt.Errorf("exec %s: reading the output: %w", s.Program, err))
		}
	})
	if capR != nil {
		readers.Go(func() {
			if err := pumpCapture(capR, out, s.CaptureMax); err != nil {
				fail(fmt.Errorf("exec %s: %w", s.Program, err))
			}
		})
	}

	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()
	var waitErr error
	select {
	case waitErr = <-waited:
	case <-ctx.Done():
		killGroup()
		waitErr = <-waited
	case <-failed:
		waitErr = <-waited
	}
	// Whatever the program left behind in its group goes with it.
	killGroup()
	deadline := time.Now().Add(execKillWait)
	logR.SetReadDeadline(deadline)
	if capR != nil {
		capR.SetReadDeadline(deadline)
	}
	readers.Wait()

	if ctx.Err() != nil {
		return nil, fmt.Errorf("exec %s: stopped", s.Program)
	}
	select {
	case <-failed:
		return nil, failErr
	default:
	}
	var exitErr *exec.ExitError
	if waitErr != nil && !errors.As(waitErr, &exitErr) {
		return nil, fmt.Errorf("exec %s: %w", s.Program, waitErr)
	}
	ws := cmd.ProcessState.Sys().(syscall.WaitStatus)
	code := ws.ExitStatus()
	if ws.Signaled() {
		code = 128 + int(ws.Signal())
	}
	return execResult{ExitCode: code}, nil
}

// closeOnExecFrom marks every descriptor from first up close-on-exec, so
// the program starts with 0-2 only. Linux before 5.11 lacks close_range's
// flag; there it walks /proc/self/fd.
func closeOnExecFrom(first int) error {
	if err := unix.CloseRange(uint(first), ^uint(0), unix.CLOSE_RANGE_CLOEXEC); err == nil {
		return nil
	}
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		return err
	}
	for _, e := range entries {
		fd, err := strconv.Atoi(e.Name())
		if err != nil || fd < first {
			continue
		}
		// EBADF: the descriptor ReadDir itself used, now closed.
		if _, err := unix.FcntlInt(uintptr(fd), unix.F_SETFD, unix.FD_CLOEXEC); err != nil && !errors.Is(err, unix.EBADF) {
			return err
		}
	}
	return nil
}

// pumpCapture copies the program's stdout to C frames, failing past max
// bytes: a cut archive or key would be wrong, not shorter.
func pumpCapture(r io.Reader, out *frameWriter, max int64) error {
	buf := make([]byte, 32<<10)
	var total int64
	for {
		n, err := r.Read(buf)
		if n > 0 {
			if total += int64(n); total > max {
				return fmt.Errorf("the captured output passed %d bytes", max)
			}
			if err := out.frame('C', buf[:n]); err != nil {
				return fmt.Errorf("writing the captured output: %w", err)
			}
		}
		if errors.Is(err, io.EOF) || errors.Is(err, os.ErrDeadlineExceeded) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("reading the captured output: %w", err)
		}
	}
}

// ExecRequest is one program for Client.Exec to run as the account.
type ExecRequest struct {
	Program string              // one of the fixed set: see ProgramArgv
	PHP     contract.PHPVersion // php, artisan and composer only
	Args    []string            // built by the agent from validated values
	Dir     string              // relative to the home; "" is the home
	Env     map[string]string   // allowlisted keys of the program only
	Stdin   io.Reader           // deploy-script only; nil is empty
	// Capture, with CaptureMax > 0, receives the program's raw stdout instead
	// of the job log (spike amendment A8). More than CaptureMax bytes fails
	// the run.
	Capture    io.Writer
	CaptureMax int64
	Log        func(line string) // each output line; nil drops them
}

// Exec runs r's program as a, through the helper's exec operation, and
// returns its exit code. A non-zero exit is not an error here; the caller
// decides. Cancelling ctx kills the program's whole process group.
func (c *Client) Exec(ctx context.Context, a Account, r ExecRequest) (int, error) {
	if a.UID == 0 || a.GID == 0 || slices.Contains(a.Groups, 0) {
		return 0, fmt.Errorf("as-user exec as %s: refusing an account with root's uid, gid or group", a.Name)
	}
	s := execSpec{Program: r.Program, PHP: r.PHP, Args: r.Args, Dir: r.Dir, Env: envList(r.Env), CaptureMax: r.CaptureMax}
	if err := s.validate(); err != nil {
		return 0, err
	}
	if r.Stdin != nil && !execPrograms[r.Program].stdin {
		return 0, fmt.Errorf("exec %s: takes no stdin", r.Program)
	}
	if (r.Capture == nil) != (r.CaptureMax == 0) {
		return 0, fmt.Errorf("exec %s: capture needs both a writer and a limit", r.Program)
	}
	arg, err := json.Marshal(s)
	if err != nil {
		return 0, err
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := c.command(runCtx, a, "exec", string(arg))
	if c.prepare != nil {
		c.prepare(cmd)
	}
	// The helper turns SIGTERM into a kill of the program's group; SIGKILL
	// after WaitDelay is the backstop.
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = 2 * execKillWait
	cmd.Stdin = r.Stdin
	stderr := &capped{max: maxStderr}
	cmd.Stderr = stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return 0, err
	}
	if err := cmd.Start(); err != nil {
		return 0, fmt.Errorf("as-user exec as %s: %w", a.Name, err)
	}
	logf := r.Log
	if logf == nil {
		logf = func(string) {}
	}
	res, readErr := readExecFrames(stdout, logf, r.Capture, r.CaptureMax)
	if readErr != nil {
		cancel()
		io.Copy(io.Discard, stdout) // until the helper exits
	}
	err = cmd.Wait()
	if ctx.Err() != nil {
		return 0, fmt.Errorf("as-user exec %s as %s: %w", r.Program, a.Name, ctx.Err())
	}
	// A helper that failed by itself explains why on stderr.
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && (readErr == nil || errors.Is(readErr, errNoResult)) {
		return 0, &opError{op: "exec " + r.Program, account: a.Name, code: exitErr.ExitCode(), stderr: stderr.buf.Bytes()}
	}
	if readErr != nil {
		if msg := bytes.TrimSpace(stderr.buf.Bytes()); len(msg) > 0 {
			return 0, fmt.Errorf("as-user exec %s as %s: %w (%s)", r.Program, a.Name, readErr, lastBytes(msg, 500))
		}
		return 0, fmt.Errorf("as-user exec %s as %s: %w", r.Program, a.Name, readErr)
	}
	if err != nil {
		return 0, fmt.Errorf("as-user exec %s as %s: %w", r.Program, a.Name, err)
	}
	return res.ExitCode, nil
}

var errNoResult = errors.New("the helper ended without a result")

func lastBytes(b []byte, n int) []byte {
	if len(b) > n {
		return b[len(b)-n:]
	}
	return b
}

// readExecFrames reads the helper's frames until its result line. It
// trusts nothing: a frame over maxExecFrame, an unknown frame, capture
// past captureMax, or anything after the result is an error.
func readExecFrames(r io.Reader, log func(string), capture io.Writer, captureMax int64) (execResult, error) {
	br := bufio.NewReaderSize(r, 4<<10)
	buf := make([]byte, maxExecFrame)
	var logged, captured int64
	for {
		hdr, err := br.ReadSlice('\n')
		if err != nil {
			if errors.Is(err, io.EOF) {
				return execResult{}, errNoResult
			}
			return execResult{}, fmt.Errorf("reading the helper's output: %w", err)
		}
		if hdr[0] == '{' {
			var res execResult
			if err := contract.DecodeStrict(hdr, &res); err != nil {
				return execResult{}, fmt.Errorf("the helper's result: %w", err)
			}
			if res.ExitCode < 0 {
				return execResult{}, fmt.Errorf("the helper's result: exit code %d", res.ExitCode)
			}
			if _, err := br.ReadByte(); !errors.Is(err, io.EOF) {
				return execResult{}, errors.New("the helper wrote more after its result")
			}
			return res, nil
		}
		kind, n, err := parseFrameHeader(hdr)
		if err != nil {
			return execResult{}, err
		}
		switch kind {
		case 'L':
			if _, err := io.ReadFull(br, buf[:n]); err != nil {
				return execResult{}, fmt.Errorf("reading the helper's output: %w", err)
			}
			// The helper caps the log; this only bounds a helper that doesn't.
			if logged += int64(n); logged <= MaxExecLogBytes+maxExecFrame {
				log(strings.ToValidUTF8(string(buf[:n]), "�"))
			}
		case 'C':
			if capture == nil {
				return execResult{}, errors.New("the helper sent captured output that wasn't asked for")
			}
			if captured += int64(n); captured > captureMax {
				return execResult{}, fmt.Errorf("the captured output passed %d bytes", captureMax)
			}
			if _, err := io.CopyN(capture, br, int64(n)); err != nil {
				return execResult{}, fmt.Errorf("the captured output: %w", err)
			}
		}
	}
}

// parseFrameHeader reads "L <n>\n" or "C <n>\n", n at most maxExecFrame.
func parseFrameHeader(hdr []byte) (byte, int, error) {
	bad := fmt.Errorf("the helper wrote a bad frame header %q", lastBytes(hdr, 40))
	if len(hdr) < 4 || (hdr[0] != 'L' && hdr[0] != 'C') || hdr[1] != ' ' || len(hdr) > 3+len(strconv.Itoa(maxExecFrame)) {
		return 0, 0, bad
	}
	digits := hdr[2 : len(hdr)-1]
	for _, c := range digits {
		if c < '0' || c > '9' {
			return 0, 0, bad
		}
	}
	n, err := strconv.Atoi(string(digits))
	if err != nil || n > maxExecFrame {
		return 0, 0, bad
	}
	return hdr[0], n, nil
}
