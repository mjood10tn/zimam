package mysql

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"zimam/agent/internal/sysexec"
)

const clientPath = "/usr/bin/mysql"

// clientArgs make the client connect as MySQL's root over the socket. Root's
// account uses auth_socket, so being OS root is the whole login: there is no
// MySQL root password to keep or leak.
var clientArgs = []string{"--protocol=socket", "--user=root", "--batch", "--skip-column-names"}

// queryTimeout bounds one statement, so a server that accepts connections
// but never answers can't hold a job until its time limit.
var queryTimeout = 30 * time.Second

// query is the one way the agent talks to MySQL: it runs the client as root
// with the statements on stdin and returns what it prints, rows separated by
// newlines and columns by tabs. Statements are written in this package, from
// constants and validated values; nothing outside the package gives it SQL.
func query(ctx context.Context, d sysexec.Deps, sql string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	res, err := d.Run.Run(ctx, sysexec.Cmd{Path: clientPath, Args: clientArgs, Stdin: strings.NewReader(sql + "\n")})
	if err != nil {
		return "", err
	}
	return string(res.Stdout), nil
}

// Statement is one SQL statement for Exec. It can only be made by the
// package's constructors, which take validated types and quote them: the
// type has no exported field, and no function takes SQL text. Some hold a
// password, so a Statement prints as its kind and never as its SQL.
type Statement struct {
	kind string // what the statement does, for errors and logs
	sql  string
	// secret marks a statement that holds a password: the client's message
	// about it is kept out of errors, since MySQL can quote the statement.
	secret bool
	err    error // a value that failed its Validate; Exec refuses to run it
}

func (s Statement) String() string   { return s.kind }
func (s Statement) GoString() string { return "mysql.Statement(" + s.kind + ")" }

// invalid is the statement for a value that failed its type's Validate. It
// carries no SQL.
func invalid(kind, field string, err error) Statement {
	return Statement{kind: kind, err: fmt.Errorf("%s: %s %v", kind, field, err)}
}

// Error is a statement that MySQL refused, or a client that couldn't be
// reached. It never wraps the client's own error or holds the statement's
// text: a statement with a password would otherwise reach the job log.
type Error struct {
	kind  string
	errno int
	msg   string
}

func (e *Error) Error() string { return e.msg }

// Kind is what the failed statement was doing, such as "create database".
func (e *Error) Kind() string { return e.kind }

// Errno is MySQL's or the client's error number, or 0 when the client's
// message had none.
func (e *Error) Errno() int { return e.errno }

// MySQL error numbers the commands tell apart.
const (
	ErrDatabaseExists   = 1007 // CREATE DATABASE of a name that exists
	ErrCannotConnect    = 2002 // no server on the socket
	errCannotConnectTCP = 2003
	errLostConnection   = 2013
)

// IsDown reports whether err means no MySQL server answered: it isn't
// running, or it dropped the connection.
func IsDown(err error) bool {
	var e *Error
	if !errors.As(err, &e) {
		return false
	}
	return e.errno == ErrCannotConnect || e.errno == errCannotConnectTCP || e.errno == errLostConnection
}

var errnoRe = regexp.MustCompile(`ERROR (\d+)`)

// Exec runs the statements one after another, each in its own client run
// as MySQL's root, and returns the rows they print, in order, one slice of
// columns per row. It stops at the first failure; the statements before it
// have taken effect, since DDL isn't rolled back. Every statement is checked
// first, so one made from an invalid value stops the whole batch before it
// starts.
func Exec(ctx context.Context, d sysexec.Deps, statements ...Statement) ([][]string, error) {
	for _, s := range statements {
		if s.err != nil {
			return nil, s.err
		}
		if s.sql == "" {
			return nil, errors.New("mysql: a statement with no SQL (not made by a constructor)")
		}
	}
	var rows [][]string
	for _, s := range statements {
		out, err := query(ctx, d, s.sql)
		if err != nil {
			return rows, s.failure(err)
		}
		for line := range strings.SplitSeq(out, "\n") {
			if line != "" {
				fields := strings.Split(line, "\t")
				for i, f := range fields {
					fields[i] = unescapeBatch(f)
				}
				rows = append(rows, fields)
			}
		}
	}
	return rows, nil
}

// failure turns what the runner returned into an Error that names the
// statement by its kind.
func (s Statement) failure(err error) error {
	var exit *sysexec.ExitError
	if !errors.As(err, &exit) {
		// Not a MySQL answer: the client didn't start, or the context ended.
		// These errors hold paths and codes, never stdin.
		return fmt.Errorf("%s: %w", s.kind, err)
	}
	stderr := strings.TrimSpace(string(exit.Stderr))
	e := &Error{kind: s.kind}
	if m := errnoRe.FindStringSubmatch(stderr); m != nil {
		e.errno, _ = strconv.Atoi(m[1])
	}
	switch {
	case s.secret && e.errno != 0:
		e.msg = fmt.Sprintf("%s failed: MySQL error %d", s.kind, e.errno)
	case s.secret:
		e.msg = fmt.Sprintf("%s failed: the client exited with code %d", s.kind, exit.Code)
	default:
		const tail = 500
		if len(stderr) > tail {
			stderr = stderr[len(stderr)-tail:]
		}
		e.msg = s.kind + " failed: " + stderr
	}
	return e
}

// batchUnescaper undoes what the client's batch mode does to a value: it
// writes a backslash as two, and a tab, newline and NUL as a backslash and a
// letter. A grant on s\_a_x comes back as s\\_a_x.
var batchUnescaper = strings.NewReplacer("\\\\", "\\", "\\t", "\t", "\\n", "\n", "\\0", "\x00")

func unescapeBatch(s string) string {
	if !strings.Contains(s, "\\") {
		return s
	}
	return batchUnescaper.Replace(s)
}
