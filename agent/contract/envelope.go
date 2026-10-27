// Package contract holds the protocol between the panel and the agent: the
// envelope, the error codes, strict decoding and the validated input types
// (design §5.2–§5.4).
package contract

import "encoding/json"

// SupportedProtocols is reported by agent.version. A newer agent keeps
// serving an older panel by accepting every version listed here.
var SupportedProtocols = []int{1}

type Request struct {
	V       int             `json:"v"`
	ID      string          `json:"id"`
	Command string          `json:"command"`
	Actor   string          `json:"actor"`
	Params  json.RawMessage `json:"params"`
}

type Accepted struct {
	Type string `json:"type"` // "accepted"
	Job  JobID  `json:"job"`
}

type LogLine struct {
	Type   string `json:"type"` // "log"
	Job    JobID  `json:"job"`
	Offset int64  `json:"offset"`
	Line   string `json:"line"`
}

type Result struct {
	Type string          `json:"type"` // "result"
	OK   bool            `json:"ok"`
	Data json.RawMessage `json:"data"`
}

type Error struct {
	Type    string      `json:"type"` // "error"
	Code    ErrorCode   `json:"code"`
	Message string      `json:"message"`
	Fields  FieldErrors `json:"fields,omitempty"`
}
