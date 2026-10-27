// Package server is the agent's socket server: the caller check, the strict
// one-request protocol and the dispatch to command handlers (design §5.1 and
// §5.2).
package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"runtime/debug"
	"sync"
	"time"

	"zimam/agent/contract"
	"zimam/agent/internal/audit"
	"zimam/agent/internal/commands"
)

const maxLine = 1 << 20 // 1 MiB, not counting the newline (§5.2)

// writeTimeout bounds writing a reply, so a client that stops reading can't
// hold its connection open.
const writeTimeout = 10 * time.Second

var errLineTooLong = errors.New("line too long")

// Server answers one request on each connection it accepts.
type Server struct {
	Listener net.Listener
	// Allowed is the caller check. It gets the peer's uid from SO_PEERCRED.
	Allowed  func(uid uint32) bool
	Audit    Auditor
	Handlers commands.Handlers
	// RequestTimeout bounds reading the request line, and DirectTimeout a
	// command that answers directly (§5.2: 10 s and 30 s).
	RequestTimeout time.Duration
	DirectTimeout  time.Duration
}

// Auditor writes audit entries. audit.Writer is the real one.
type Auditor interface {
	Write(audit.Entry) error
}

// Serve accepts connections until ctx is cancelled. Then it closes the
// listener, waits for open connections to finish and returns nil.
func (s *Server) Serve(ctx context.Context) error {
	if s.Listener == nil || s.Allowed == nil || s.Audit == nil || s.Handlers == nil {
		return errors.New("server: Listener, Allowed, Audit and Handlers must all be set")
	}
	if s.RequestTimeout <= 0 || s.DirectTimeout <= 0 {
		return errors.New("server: RequestTimeout and DirectTimeout must be positive")
	}
	stop := context.AfterFunc(ctx, func() { s.Listener.Close() })
	defer stop()
	var conns sync.WaitGroup
	defer conns.Wait()
	var delay time.Duration
	for {
		c, err := s.Listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			if errors.Is(err, net.ErrClosed) {
				return err
			}
			// Running out of file descriptors passes. Back off and retry, as
			// net/http does.
			delay = min(max(2*delay, 5*time.Millisecond), time.Second)
			log.Printf("accept: %v; retrying in %v", err, delay)
			select {
			case <-time.After(delay):
			case <-ctx.Done():
				return nil
			}
			continue
		}
		delay = 0
		conns.Go(func() { s.serveConn(ctx, c) })
	}
}

// serveConn checks the caller, answers at most one request and closes c.
func (s *Server) serveConn(ctx context.Context, c net.Conn) {
	defer c.Close()
	defer func() {
		if r := recover(); r != nil {
			log.Printf("panic serving a connection: %v\n%s", r, debug.Stack())
		}
	}()
	cl := &call{start: time.Now()}
	uid, err := connUID(c)
	if err != nil {
		log.Printf("reading the caller's uid: %v", err)
	} else {
		cl.uid = &uid
	}
	if err != nil || !s.Allowed(uid) {
		s.reject(cl, contract.ErrForbidden, "", nil) // the caller gets no reply
		return
	}
	c.SetReadDeadline(cl.start.Add(s.RequestTimeout))
	var reply any
	if line, err := readLine(bufio.NewReader(c)); err != nil {
		reply = s.reject(cl, contract.ErrInvalidRequest, s.readFailure(err), nil)
	} else {
		reply = s.dispatch(ctx, cl, line)
	}
	if st, ok := reply.(commands.Stream); ok {
		s.stream(ctx, c, cl.req.Command, st)
		return
	}
	c.SetWriteDeadline(time.Now().Add(writeTimeout))
	if err := writeLine(c, reply); err != nil {
		log.Printf("writing the reply: %v", err)
	}
}

// stream runs a streaming reply on the connection's context, free of
// DirectTimeout. A client that has gone is noticed at the next send. If the
// stream fails before its last line, the server sends the error instead.
func (s *Server) stream(ctx context.Context, c net.Conn, command string, st commands.Stream) {
	var sendErr error
	send := func(v any) error {
		c.SetWriteDeadline(time.Now().Add(writeTimeout))
		sendErr = writeLine(c, v)
		return sendErr
	}
	if err := st(ctx, send); err != nil && sendErr == nil && ctx.Err() == nil {
		send(failure(command, err))
	}
}

func connUID(c net.Conn) (uint32, error) {
	uc, ok := c.(*net.UnixConn)
	if !ok {
		return 0, fmt.Errorf("%T is not a Unix socket connection", c)
	}
	return peerUID(uc)
}

// readLine reads one request line. The connection's read deadline (10 s)
// is set by the caller before the first read.
func readLine(r *bufio.Reader) ([]byte, error) {
	var line []byte
	for {
		chunk, err := r.ReadSlice('\n')
		line = append(line, chunk...)
		switch {
		case err == nil:
			line = bytes.TrimSuffix(line, []byte("\n"))
			if len(line) > maxLine {
				return nil, errLineTooLong
			}
			return line, nil
		case errors.Is(err, bufio.ErrBufferFull):
			if len(line) > maxLine {
				return nil, errLineTooLong
			}
		default:
			return nil, err // EOF before a newline is an invalid request too
		}
	}
}

func (s *Server) readFailure(err error) string {
	switch {
	case errors.Is(err, errLineTooLong):
		return "Request line too long (over 1 MiB)"
	case errors.Is(err, os.ErrDeadlineExceeded):
		return fmt.Sprintf("No request line within %v", s.RequestTimeout)
	default:
		return "Incomplete request line"
	}
}

func writeLine(w io.Writer, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = w.Write(append(b, '\n'))
	return err
}
