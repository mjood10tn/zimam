package commands

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"time"

	"zimam/agent/contract"
	"zimam/agent/internal/mysql"
	"zimam/agent/internal/sites"
	"zimam/agent/internal/sysexec"
)

// The database commands keep a site's databases in its state file and in
// MySQL. A database belongs to the site whose state lists it: the commands
// never touch a database that no site lists, even when its name carries a
// site's prefix.

func conflict(format string, args ...any) error {
	return &contract.AgentError{Code: contract.ErrConflict, Message: fmt.Sprintf(format, args...)}
}

// loadDBSite is the state of the site a database command acts for.
func loadDBSite(d sysexec.Deps, name contract.SiteName) (sites.State, error) {
	st, err := sites.Load(d.Paths, name)
	if errors.Is(err, fs.ErrNotExist) {
		return sites.State{}, &contract.AgentError{Code: contract.ErrNotFound, Message: fmt.Sprintf("Site %s doesn't exist.", name)}
	}
	return st, err
}

// requireMySQL refuses a server where Zimam didn't install MySQL.
func requireMySQL(d sysexec.Deps) error {
	ok, err := mysql.Installed(d)
	if err != nil {
		return err
	}
	if !ok {
		return conflict("MySQL isn't installed on this server. Install it first.")
	}
	return nil
}

// dbError turns a failure of MySQL into what the panel can show: a server
// that doesn't answer is a conflict with the server's state, and anything
// else stays an internal error that names the statement and not its text.
func dbError(err error) error {
	if mysql.IsDown(err) {
		return conflict("MySQL isn't running. Start it and try again.")
	}
	return err
}

// undoTimeout bounds putting MySQL back after a failure, which runs even
// when the job's own context has ended.
const undoTimeout = 2 * time.Minute

// undoCtx is a context for a rollback that survives the job's cancellation.
func undoCtx(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), undoTimeout)
}

// siteNames lists the names of the sites in all.
func siteNames(all []sites.State) []contract.SiteName {
	names := make([]contract.SiteName, len(all))
	for i, s := range all {
		names[i] = s.Name
	}
	return names
}

func withoutDatabase(dbs []contract.DatabaseName, name contract.DatabaseName) []contract.DatabaseName {
	out := slices.DeleteFunc(slices.Clone(dbs), func(n contract.DatabaseName) bool { return n == name })
	if len(out) == 0 {
		return nil // an empty list leaves the field out of the file
	}
	return out
}
