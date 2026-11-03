package logs

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"slices"
	"strconv"
	"time"

	"zimam/agent/contract"
	"zimam/agent/internal/sysexec"
)

const journalctl = "/usr/bin/journalctl"

// journalSearchWindow is how many entries a journal search looks through,
// from the cursor on or the newest ones back from the end.
const journalSearchWindow = 20000

// Read answers logs.read (§11.2).
func Read(ctx context.Context, e Env, p contract.LogsReadParams) (contract.LogsReadResult, error) {
	src, err := resolve(e, p.Source, p.Name)
	if err != nil {
		return contract.LogsReadResult{}, err
	}
	if src.kind == journal {
		return readJournal(ctx, e, src, p)
	}
	return readFile(ctx, e, src, p)
}

// readJournal runs journalctl --output=json --no-pager with the source's
// units (or -k), a priority with -p, and pages with --cursor or
// --after-cursor. Searching is done here, in Go, as plain text: journald's
// own --grep takes a regex.
func readJournal(ctx context.Context, e Env, src source, p contract.LogsReadParams) (contract.LogsReadResult, error) {
	args := []string{"--output=json", "--no-pager"}
	if src.k {
		args = append(args, "-k")
	}
	for _, u := range src.units {
		args = append(args, "-u", u)
	}
	if p.Priority != nil {
		args = append(args, "-p", strconv.Itoa(int(*p.Priority)))
	}
	n := p.Limit.Lines()
	reverse := false
	switch {
	case p.Search != "" && p.Cursor != "":
		args = append(args, "--after-cursor="+string(p.Cursor), "-n", "+"+strconv.Itoa(journalSearchWindow))
	case p.Search != "":
		args = append(args, "-n", strconv.Itoa(journalSearchWindow))
	case p.Direction == contract.LogForward:
		args = append(args, "--after-cursor="+string(p.Cursor), "-n", "+"+strconv.Itoa(n))
	case p.Cursor != "":
		// From the cursor backwards: the cursor's own entry comes first.
		args = append(args, "--cursor="+string(p.Cursor), "--reverse", "-n", strconv.Itoa(n+1))
		reverse = true
	default:
		args = append(args, "--reverse", "-n", strconv.Itoa(n))
		reverse = true
	}
	res, err := e.D.Run.Run(ctx, sysexec.Cmd{Path: journalctl, Args: args})
	if err != nil {
		return contract.LogsReadResult{}, err
	}
	entries, err := parseJournal(res.Stdout)
	if err != nil {
		return contract.LogsReadResult{}, err
	}
	if reverse {
		if p.Cursor != "" && len(entries) > 0 && entries[0].Cursor == p.Cursor {
			entries = entries[1:]
		}
		slices.Reverse(entries)
	}
	out := contract.LogsReadResult{Lines: []contract.FileLogLine{}, Entries: entries}
	if p.Search != "" {
		start := searchClock()
		var matches []contract.JournalEntry
		out.SearchStopped = "end"
		for _, en := range entries {
			if ctx.Err() != nil || searchClock().Sub(start) >= searchTimeout {
				out.SearchStopped = "time"
				break
			}
			if p.Search.Match(en.Message) {
				matches = append(matches, en)
				if len(matches) >= MaxSearchMatches {
					out.SearchStopped = "matches"
					break
				}
			}
		}
		out.Entries = matches
	}
	if out.Entries == nil {
		out.Entries = []contract.JournalEntry{}
	}
	if len(out.Entries) > 0 {
		back, fwd := out.Entries[0].Cursor, out.Entries[len(out.Entries)-1].Cursor
		out.BackCursor, out.ForwardCursor = &back, &fwd
	} else if p.Cursor != "" {
		c := p.Cursor
		out.ForwardCursor = &c
	}
	return out, nil
}

// parseJournal reads journalctl's JSON lines. MESSAGE is a string, or an
// array of bytes when it isn't valid UTF-8; an entry without a priority is
// taken as info (6).
func parseJournal(out []byte) ([]contract.JournalEntry, error) {
	var entries []contract.JournalEntry
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 64<<10), 16<<20)
	for sc.Scan() {
		if len(bytes.TrimSpace(sc.Bytes())) == 0 {
			continue
		}
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(sc.Bytes(), &raw); err != nil {
			return nil, err
		}
		str := func(k string) string {
			var s string
			if json.Unmarshal(raw[k], &s) == nil {
				return s
			}
			var b []byte
			var nums []int
			if json.Unmarshal(raw[k], &nums) == nil {
				for _, n := range nums {
					b = append(b, byte(n))
				}
				return string(bytes.ToValidUTF8(b, []byte("�")))
			}
			return ""
		}
		en := contract.JournalEntry{Cursor: contract.JournalCursor(str("__CURSOR")), Priority: 6, Message: str("MESSAGE")}
		if us, err := strconv.ParseInt(str("__REALTIME_TIMESTAMP"), 10, 64); err == nil {
			en.Time = time.UnixMicro(us).UTC()
		}
		if pr, err := strconv.Atoi(str("PRIORITY")); err == nil {
			en.Priority = pr
		}
		en.Unit = str("_SYSTEMD_UNIT")
		if en.Unit == "" {
			en.Unit = str("SYSLOG_IDENTIFIER")
		}
		if len(en.Message) > maxLineBytes {
			en.Message = en.Message[:maxLineBytes]
		}
		entries = append(entries, en)
	}
	return entries, sc.Err()
}
