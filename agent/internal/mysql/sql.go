package mysql

import (
	"context"
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
