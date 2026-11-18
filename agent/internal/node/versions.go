package node

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"regexp"
	"strings"

	"zimam/agent/contract"
	"zimam/agent/internal/paths"
	"zimam/agent/internal/sysexec"
)

const dpkgQuery = "/usr/bin/dpkg-query"

// Installed returns the version of the installed nodejs package, "" when it
// is not installed. A package that was removed but not purged does not count.
func Installed(ctx context.Context, d sysexec.Deps) (string, error) {
	res, err := d.Run.Run(ctx, sysexec.Cmd{Path: dpkgQuery, Args: []string{"-W", "-f=${db:Status-Status} ${Version}", "nodejs"}})
	var exit *sysexec.ExitError
	if errors.As(err, &exit) && exit.Code == 1 { // no packages found matching nodejs
		return "", nil
	}
	if err != nil {
		return "", err
	}
	status, version, _ := strings.Cut(strings.TrimSpace(string(res.Stdout)), " ")
	if status != "installed" {
		return "", nil
	}
	return version, nil
}

// MajorOf is the major of a nodejs package version, "26" for
// "26.13.1-1nodesource1".
func MajorOf(version string) contract.NodeMajor {
	major, _, _ := strings.Cut(version, ".")
	return contract.NodeMajor(major)
}

var repoURI = regexp.MustCompile(`(?m)^URIs: https://deb\.nodesource\.com/node_(\d+)\.x$`)

// RepoMajor is the major in Zimam's NodeSource source file, "" when the file
// does not exist.
func RepoMajor(p paths.Paths) (contract.NodeMajor, error) {
	b, err := os.ReadFile(p.NodeSources())
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if m := repoURI.FindSubmatch(b); m != nil {
		return contract.NodeMajor(m[1]), nil
	}
	return "", nil
}
