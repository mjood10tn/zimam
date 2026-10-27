package contract

type ErrorCode string

const (
	ErrInvalidRequest      ErrorCode = "invalid_request"
	ErrUnsupportedProtocol ErrorCode = "unsupported_protocol"
	ErrUnknownCommand      ErrorCode = "unknown_command"
	ErrValidationFailed    ErrorCode = "validation_failed"
	ErrForbidden           ErrorCode = "forbidden"
	ErrBusy                ErrorCode = "busy"
	ErrNotFound            ErrorCode = "not_found"
	ErrConflict            ErrorCode = "conflict"
	ErrJobFailed           ErrorCode = "job_failed"
	ErrInternal            ErrorCode = "internal"
)

// AgentError carries a protocol error out of validation, prechecks and handlers.
type AgentError struct {
	Code    ErrorCode
	Message string
	Fields  FieldErrors
}

func (e *AgentError) Error() string { return string(e.Code) + ": " + e.Message }

// FieldErrors maps a parameter name to a message. It becomes the "fields"
// object of a validation_failed error.
type FieldErrors map[string]string

func (f FieldErrors) Check(field string, v interface{ Validate() error }) {
	if err := v.Validate(); err != nil {
		f[field] = err.Error()
	}
}

func (f FieldErrors) Err() error {
	if len(f) == 0 {
		return nil
	}
	return &AgentError{Code: ErrValidationFailed, Message: "Invalid parameters", Fields: f}
}
