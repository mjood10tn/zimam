package main

import (
	"fmt"
	"io"

	"zimam/agent/internal/helper"
)

// runAsUser is `zimam-agent as-user`, the site-user helper (design §5.6).
// The agent starts it with a site user's or zimam's credentials through
// helper.Client; run by root, it refuses.
func runAsUser(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	ids, err := helper.ProcessIDs()
	if err != nil {
		fmt.Fprintf(stderr, "zimam-agent as-user: %v\n", err)
		return 1
	}
	return helper.Main(ids, args, stdin, stdout, stderr)
}
