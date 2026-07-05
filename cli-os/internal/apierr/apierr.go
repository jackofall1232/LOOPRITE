// Package apierr is the typed HTTP error carried through routing/upstream, ported from the
// httpError helper in router.js. It preserves status, an OpenAI-style error "type", an optional
// machine "code", and (for auto-router denials) a structured routing decision to log.
package apierr

// Error is a typed error with an HTTP status and OpenAI-shaped fields.
type Error struct {
	Status   int
	Message  string
	Type     string
	Code     string
	Decision map[string]any // optional: attached to auto-router denials so they reach the ledger
}

func (e *Error) Error() string { return e.Message }

// New builds a typed error (type defaults to "invalid_request_error" when empty).
func New(status int, message, typ string) *Error {
	if typ == "" {
		typ = "invalid_request_error"
	}
	return &Error{Status: status, Message: message, Type: typ}
}

// As extracts an *Error from err, or nil if it is not one.
func As(err error) *Error {
	if err == nil {
		return nil
	}
	if e, ok := err.(*Error); ok {
		return e
	}
	return nil
}
