// Package output prints results. With --json every command prints exactly one
// Envelope on stdout. Human tables, NDJSON progress and the JSON Schemas are
// added in Wave 3 (W3-B).
package output

import (
	"encoding/json"
	"io"
)

// SchemaVersion of the --json output. Minor releases only add fields.
const SchemaVersion = 1

// Exit codes.
const (
	ExitOK        = 0
	ExitError     = 1
	ExitUsage     = 2
	ExitPartial   = 3 // some items were skipped or failed
	ExitCancelled = 130
)

type Envelope struct {
	SchemaVersion int    `json:"schema_version"`
	Command       string `json:"command"`
	OK            bool   `json:"ok"`
	Data          any    `json:"data,omitempty"`
	Error         *Error `json:"error,omitempty"`
}

type Error struct {
	Code    string `json:"code"` // e.g. "not_found", "refused", "cancelled"
	Message string `json:"message"`
}

// JSON writes a successful envelope.
func JSON(w io.Writer, command string, data any) error {
	return write(w, Envelope{SchemaVersion: SchemaVersion, Command: command, OK: true, Data: data})
}

// JSONError writes a failed envelope.
func JSONError(w io.Writer, command, code, message string) error {
	return write(w, Envelope{SchemaVersion: SchemaVersion, Command: command, Error: &Error{Code: code, Message: message}})
}

func write(w io.Writer, env Envelope) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(env)
}
