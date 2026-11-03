package contract

import (
	"errors"
	"regexp"
	"slices"
	"time"
)

func init() {
	Register(Descriptor{
		Name:   "logs.list",
		Kind:   KindRead,
		Params: func() Params { return &LogsListParams{} },
		Result: LogsListResult{},
	})
	Register(Descriptor{
		Name:   "logs.read",
		Kind:   KindRead,
		Params: func() Params { return &LogsReadParams{} },
		Result: LogsReadResult{},
	})
}

// LogsListParams is logs.list's params (§11.2): a source, and the site for
// a site source.
type LogsListParams struct {
	Source LogSource `json:"source"`
	Name   SiteName  `json:"name,omitempty"`
}

func (p *LogsListParams) Validate() error {
	f := FieldErrors{}
	f.Check("source", p.Source)
	checkSiteName(f, p.Source, p.Name)
	return f.Err()
}

// checkSiteName: a site source needs a site's name; any other leaves it out.
func checkSiteName(f FieldErrors, s LogSource, name SiteName) {
	switch {
	case s.IsSite() && name == "":
		f["name"] = "is required for a site's log"
	case s.IsSite():
		f.Check("name", name)
	case name != "":
		f["name"] = "must be left out for a server log"
	}
}

// IsJournal reports whether the source is read from the journal.
func (s LogSource) IsJournal() bool { return len(s) > 8 && s[:8] == "journal." }

// LogFile is a log file's size and when it last changed (§11.2).
type LogFile struct {
	Name     string    `json:"name"`
	Size     int64     `json:"size"`
	Modified time.Time `json:"modified"`
}

// LogsListResult is the current file, null when it doesn't exist (yet) or
// for a journal source, and a file source's archives, newest first.
type LogsListResult struct {
	Source   LogSource `json:"source"`
	Current  *LogFile  `json:"current"`
	Archives []LogFile `json:"archives"`
}

// LogDirection is which way logs.read reads (§11.2).
type LogDirection string

const (
	LogBack    LogDirection = "back"
	LogForward LogDirection = "forward"
)

var logDirections = []LogDirection{LogBack, LogForward}

func (d LogDirection) Validate() error {
	if !slices.Contains(logDirections, d) {
		return errors.New("must be back or forward")
	}
	return nil
}

func (LogDirection) JSONSchema() Schema { return Schema{"type": "string", "enum": logDirections} }

// JournalCursor is a position in the journal, as journalctl prints it
// ("s=…;i=…;b=…;m=…;t=…;x=…"). It only ever reaches journalctl as an
// argument, never a shell.
type JournalCursor string

var journalCursorRe = regexp.MustCompile(`^[A-Za-z0-9=;_-]{1,512}$`)

func (c JournalCursor) Validate() error {
	if !journalCursorRe.MatchString(string(c)) {
		return errors.New("is not a journal cursor")
	}
	return nil
}

func (JournalCursor) JSONSchema() Schema {
	return Schema{"type": "string", "pattern": journalCursorRe.String()}
}

// LogLimit is how many lines or entries one read returns: 1–1000, 0 for
// the default of 200 (§11.2).
type LogLimit int

const (
	MaxLogLimit     = 1000
	DefaultLogLimit = 200
)

func (l LogLimit) Validate() error {
	if l < 0 || l > MaxLogLimit {
		return errors.New("must be between 1 and 1000")
	}
	return nil
}

func (LogLimit) JSONSchema() Schema {
	return Schema{"type": "integer", "minimum": 0, "maximum": MaxLogLimit}
}

// Lines is the limit with the default applied.
func (l LogLimit) Lines() int {
	if l == 0 {
		return DefaultLogLimit
	}
	return int(l)
}

// LogPriority is a syslog priority, 0 (emerg) to 7 (debug): a journal read
// shows that level and the more severe ones.
type LogPriority int

func (p LogPriority) Validate() error {
	if p < 0 || p > 7 {
		return errors.New("must be between 0 and 7")
	}
	return nil
}

func (LogPriority) JSONSchema() Schema { return Schema{"type": "integer", "minimum": 0, "maximum": 7} }

// LogsReadParams is logs.read's params (§11.2). Files take an offset,
// journal sources a cursor; reading back without either starts at the end.
// Archives are file sources' only, and a priority journal sources' only.
type LogsReadParams struct {
	Source    LogSource     `json:"source"`
	Name      SiteName      `json:"name,omitempty"`
	Archive   ArchiveName   `json:"archive,omitempty"`
	Direction LogDirection  `json:"direction"`
	Offset    *LogOffset    `json:"offset,omitempty"`
	Cursor    JournalCursor `json:"cursor,omitempty"`
	Limit     LogLimit      `json:"limit,omitempty"`
	Search    LogSearch     `json:"search,omitempty"`
	Priority  *LogPriority  `json:"priority,omitempty"`
}

func (p *LogsReadParams) Validate() error {
	f := FieldErrors{}
	f.Check("source", p.Source)
	checkSiteName(f, p.Source, p.Name)
	f.Check("direction", p.Direction)
	f.Check("limit", p.Limit)
	f.Check("search", p.Search)
	journal := p.Source.IsJournal()
	switch {
	case p.Archive != "" && journal:
		f["archive"] = "is only for file logs"
	case p.Archive != "":
		f.Check("archive", p.Archive)
	}
	switch {
	case p.Offset != nil && journal:
		f["offset"] = "is only for file logs; journal logs take a cursor"
	case p.Offset != nil:
		f.Check("offset", *p.Offset)
	}
	switch {
	case p.Cursor != "" && !journal:
		f["cursor"] = "is only for journal logs; file logs take an offset"
	case p.Cursor != "":
		f.Check("cursor", p.Cursor)
	}
	switch {
	case p.Priority != nil && !journal:
		f["priority"] = "is only for journal logs"
	case p.Priority != nil:
		f.Check("priority", *p.Priority)
	}
	if p.Direction == LogForward && p.Offset == nil && p.Cursor == "" && p.Search == "" {
		f["direction"] = "forward needs an offset or a cursor to start from"
	}
	return f.Err()
}

// FileLogLine is one line of a file log at its byte offset (in the decompressed
// stream, for an archive); Cut marks a line cut at 16 KiB.
type FileLogLine struct {
	Offset int64  `json:"offset"`
	Text   string `json:"text"`
	Cut    bool   `json:"cut"`
}

// JournalEntry is one journal entry.
type JournalEntry struct {
	Cursor   JournalCursor `json:"cursor"`
	Time     time.Time     `json:"time"`
	Priority int           `json:"priority"` // 6 (info) when the entry has none
	Unit     string        `json:"unit"`
	Message  string        `json:"message"`
}

// LogsReadResult holds lines for a file source or entries for a journal
// source, oldest first, with where to read next in each direction: an
// offset for files, a cursor for the journal, null when there's nothing
// more that way. A search says why it stopped: end, matches (2,000) or
// time (10 s).
type LogsReadResult struct {
	Lines         []FileLogLine  `json:"lines"`
	Entries       []JournalEntry `json:"entries"`
	BackOffset    *int64         `json:"back_offset"`
	ForwardOffset *int64         `json:"forward_offset"`
	BackCursor    *JournalCursor `json:"back_cursor"`
	ForwardCursor *JournalCursor `json:"forward_cursor"`
	SearchStopped string         `json:"search_stopped"`
}
