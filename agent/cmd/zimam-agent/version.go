package main

import (
	"flag"
	"fmt"
	"io"

	"zimam/agent/internal/release"
)

// version prints the agent's release version. With --at-least <v>, it
// prints nothing and exits 0 when its version is v or later in SemVer
// precedence, and 1 when it's older. install.sh asks the new agent this
// about the installed one's version, so a repair run never moves a server
// to an older release (D25).
func version(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("version", flag.ContinueOnError)
	fs.SetOutput(stderr)
	atLeast := fs.String("at-least", "", "")
	if err := fs.Parse(args); err != nil || fs.NArg() > 0 {
		fmt.Fprintln(stderr, "usage: zimam-agent version [--at-least <version>]")
		return exitUsage
	}
	visited := false
	fs.Visit(func(f *flag.Flag) { visited = visited || f.Name == "at-least" })
	if !visited {
		fmt.Fprintln(stdout, release.Version)
		return 0
	}
	if !release.ValidVersion(*atLeast) {
		fmt.Fprintf(stderr, "zimam-agent version: %q isn't a SemVer version\n", *atLeast)
		return exitUsage
	}
	if release.CompareVersions(release.Version, *atLeast) < 0 {
		fmt.Fprintf(stderr, "zimam-agent %s is older than %s\n", release.Version, *atLeast)
		return 1
	}
	return 0
}
