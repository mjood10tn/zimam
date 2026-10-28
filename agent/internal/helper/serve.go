package helper

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"syscall"
	"time"

	"zimam/agent/contract"
)

// Exit codes of `zimam-agent as-user`.
const (
	exitOK       = 0
	exitFailed   = 1 // the error is on stderr
	exitUsage    = 2
	exitNotExist = 3 // the path doesn't exist; the client wraps fs.ErrNotExist
)

const usage = "usage: zimam-agent as-user --home <home> <operation> [arguments]"

// IDs are a process's user and group ids.
type IDs struct {
	UID, EUID, GID, EGID int
	Groups               []int
}

// ProcessIDs returns this process's ids.
func ProcessIDs() (IDs, error) {
	groups, err := os.Getgroups()
	if err != nil {
		return IDs{}, err
	}
	return IDs{UID: os.Getuid(), EUID: os.Geteuid(), GID: os.Getgid(), EGID: os.Getegid(), Groups: groups}, nil
}

// Main is `zimam-agent as-user --home <home> <operation> [arguments]`,
// started by Client with an account's credentials (§5.6). It serves one
// operation inside the home and returns the exit code.
func Main(ids IDs, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	// Run directly by root (no credential switch), this would act as root.
	if ids.UID == 0 || ids.EUID == 0 || ids.GID == 0 || ids.EGID == 0 || slices.Contains(ids.Groups, 0) {
		fmt.Fprintln(stderr, "as-user must not run as root")
		return exitFailed
	}
	if len(args) < 3 || args[0] != "--home" || !filepath.IsAbs(args[1]) || filepath.Clean(args[1]) != args[1] {
		fmt.Fprintln(stderr, usage)
		return exitUsage
	}
	home, op, opArgs := args[1], args[2], args[3:]
	syscall.Umask(0o027)
	root, err := os.OpenRoot(home)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return exitFailed
	}
	defer root.Close()
	// The home must belong to the user we are running as.
	if fi, err := root.Stat("."); err != nil || fi.Sys().(*syscall.Stat_t).Uid != uint32(ids.EUID) {
		fmt.Fprintln(stderr, "home is not owned by the current user")
		return exitFailed
	}
	return Serve(root, op, opArgs, stdin, stdout, stderr)
}

// operation is one helper operation: its argument count, and what it does
// with the arguments and stdin. Its result becomes the JSON line.
type operation struct {
	nargs int
	run   func(root *os.Root, args []string, stdin io.Reader) (result any, err error)
}

var operations = map[string]operation{
	"mkdir": {2, func(root *os.Root, a []string, _ io.Reader) (any, error) {
		mode, err := parseMode(a[1])
		if err != nil {
			return nil, err
		}
		return nil, mkdir(root, a[0], mode)
	}},
	"write": {2, func(root *os.Root, a []string, stdin io.Reader) (any, error) {
		mode, err := parseMode(a[1])
		if err != nil {
			return nil, err
		}
		return nil, writeFile(root, a[0], mode, stdin, MaxWriteBytes)
	}},
	"read": {3, func(root *os.Root, a []string, _ io.Reader) (any, error) {
		n, err := parseInts(a[1:]...)
		if err != nil {
			return nil, err
		}
		return readFile(root, a[0], n[0], n[1])
	}},
	"stat": {1, func(root *os.Root, a []string, _ io.Reader) (any, error) {
		return stat(root, a[0])
	}},
	"list": {1, func(root *os.Root, a []string, _ io.Reader) (any, error) {
		return list(root, a[0], MaxListEntries)
	}},
	"symlink-swap": {2, func(root *os.Root, a []string, _ io.Reader) (any, error) {
		return nil, symlinkSwap(root, a[0], a[1])
	}},
	"remove-tree": {1, func(root *os.Root, a []string, _ io.Reader) (any, error) {
		return nil, removeTree(root, a[0])
	}},
	"scan-world-writable": {1, func(root *os.Root, a []string, _ io.Reader) (any, error) {
		return scanWorldWritable(root, a[0], MaxScanEntries)
	}},
	"tail": {3, func(root *os.Root, a []string, _ io.Reader) (any, error) {
		n, err := parseInts(a[1:]...)
		if err != nil {
			return nil, err
		}
		return tail(root, a[0], n[0], int(n[1]))
	}},
	"search": {4, func(root *os.Root, a []string, _ io.Reader) (any, error) {
		n, err := parseInts(a[1:3]...)
		if err != nil {
			return nil, err
		}
		q := contract.LogSearch(a[3])
		if err := q.Validate(); err != nil {
			return nil, usageError{fmt.Errorf("search text: %w", err)}
		}
		return search(root, a[0], n[0], q, int(n[1]), time.Now)
	}},
	"extract-tar": {3, func(root *os.Root, a []string, stdin io.Reader) (any, error) {
		n, err := parseInts(a[1:]...)
		if err != nil {
			return nil, err
		}
		if n[0] < 0 || n[1] < 0 {
			return nil, usageError{errors.New("extract-tar: the limits can't be negative")}
		}
		if err := root.MkdirAll(a[0], 0o750); err != nil {
			return nil, err
		}
		return nil, extractTarGz(root, a[0], stdin, ExtractLimits{MaxBytes: n[0], MaxEntries: int(n[1])})
	}},
}

// usageError is a malformed argument; it exits like a wrong argument count.
type usageError struct{ error }

func parseInts(ss ...string) ([]int64, error) {
	ns := make([]int64, len(ss))
	for i, s := range ss {
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return nil, usageError{fmt.Errorf("%q is not an integer", s)}
		}
		ns[i] = n
	}
	return ns, nil
}

// parseMode reads an octal permission mode, 0 to 0777.
func parseMode(s string) (fs.FileMode, error) {
	m, err := strconv.ParseUint(s, 8, 32)
	if err != nil || m > 0o777 {
		return 0, usageError{fmt.Errorf("%q is not a permission mode", s)}
	}
	return fs.FileMode(m), nil
}

// Serve runs the operation op inside root. It writes the result to stdout
// as one JSON line ({} for an operation without one), or the error to
// stderr, and returns the exit code.
func Serve(root *os.Root, op string, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	o, ok := operations[op]
	if !ok {
		fmt.Fprintf(stderr, "%s: unknown operation %q\n", usage, op)
		return exitUsage
	}
	if len(args) != o.nargs {
		fmt.Fprintf(stderr, "%s: %s takes %d arguments, not %d\n", usage, op, o.nargs, len(args))
		return exitUsage
	}
	result, err := o.run(root, args, stdin)
	if err != nil {
		fmt.Fprintln(stderr, err)
		if errors.As(err, new(usageError)) {
			return exitUsage
		}
		if errors.Is(err, fs.ErrNotExist) {
			return exitNotExist
		}
		return exitFailed
	}
	if result == nil {
		result = struct{}{}
	}
	line, err := json.Marshal(result)
	if err == nil {
		_, err = stdout.Write(append(line, '\n'))
	}
	if err != nil {
		fmt.Fprintln(stderr, err)
		return exitFailed
	}
	return exitOK
}
