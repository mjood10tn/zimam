// Command zimam-agent is the root agent. Its command-line subcommands are
// root only and never reachable through the socket (design §5.3).
package main

import (
	"fmt"
	"io"
	"maps"
	"os"
	"slices"
	"strings"
)

const (
	exitUsage          = 2
	exitNotImplemented = 70
)

// subcommand runs with the arguments that follow its name.
type subcommand func(args []string, stdin io.Reader, stdout, stderr io.Writer) int

// subcommands maps each name, one or two words, to its handler. Later tasks
// replace the placeholders.
var subcommands = map[string]subcommand{
	"serve":              serve,
	"bootstrap":          notImplemented("bootstrap"),
	"panel setup-link":   notImplemented("panel setup-link"),
	"hook cert-deployed": notImplemented("hook cert-deployed"),
	"maintenance":        notImplemented("maintenance"),
	"job run":            notImplemented("job run"),
	"as-user":            notImplemented("as-user"),
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

// run dispatches args (os.Args without the program name) to a subcommand and
// returns the exit code.
func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	name, rest, ok := lookup(args)
	if !ok {
		if len(args) > 0 {
			fmt.Fprintf(stderr, "zimam-agent: unknown subcommand %q\n", strings.Join(args, " "))
		}
		usage(stderr)
		return exitUsage
	}
	return subcommands[name](rest, stdin, stdout, stderr)
}

// lookup finds the subcommand named by the first two words of args, or else
// by the first word.
func lookup(args []string) (name string, rest []string, ok bool) {
	if len(args) >= 2 {
		if name := args[0] + " " + args[1]; subcommands[name] != nil {
			return name, args[2:], true
		}
	}
	if len(args) >= 1 && !strings.Contains(args[0], " ") && subcommands[args[0]] != nil {
		return args[0], args[1:], true
	}
	return "", nil, false
}

func usage(w io.Writer) {
	fmt.Fprintln(w, "usage: zimam-agent <subcommand> [arguments]")
	fmt.Fprintln(w, "subcommands:")
	for _, name := range slices.Sorted(maps.Keys(subcommands)) {
		fmt.Fprintf(w, "  %s\n", name)
	}
}

func notImplemented(name string) subcommand {
	return func(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
		fmt.Fprintf(stderr, "zimam-agent %s: not implemented yet\n", name)
		return exitNotImplemented
	}
}
