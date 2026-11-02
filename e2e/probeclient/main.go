// Command probeclient sends its standard input to the agent's socket as one
// raw line and prints every line the agent answers, until the agent closes
// the connection. The root-escalation probes (design §14.4) use it to send
// requests that the panel's client never would.
//
// Usage: probeclient <socket> < request
package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"syscall"
	"time"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: probeclient <socket> < request")
		os.Exit(2)
	}
	if err := run(os.Args[1], os.Stdin, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "probeclient: %v\n", err)
		os.Exit(1)
	}
}

func run(socket string, in io.Reader, out io.Writer) error {
	line, err := io.ReadAll(in)
	if err != nil {
		return err
	}
	if !bytes.HasSuffix(line, []byte("\n")) {
		line = append(line, '\n')
	}
	conn, err := net.DialTimeout("unix", socket, 5*time.Second)
	if err != nil {
		return err
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(60 * time.Second))
	// The agent may answer and close before it has read a long line, so
	// the line goes out while the answers are read.
	go func() {
		conn.Write(line)
		conn.(*net.UnixConn).CloseWrite()
	}()
	sc := bufio.NewScanner(conn)
	sc.Buffer(make([]byte, 64<<10), 8<<20)
	for sc.Scan() {
		fmt.Fprintln(out, sc.Text())
	}
	if err := sc.Err(); err != nil && !errors.Is(err, syscall.ECONNRESET) {
		return err
	}
	return nil
}
