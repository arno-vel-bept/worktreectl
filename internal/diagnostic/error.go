// Package diagnostic separates actionable user messages from underlying errors.
package diagnostic

import "errors"

type Error struct {
	Message string
	Hint    string
	Cause   error
}

func (e *Error) Error() string { return e.Message }
func (e *Error) Unwrap() error { return e.Cause }

func New(message, hint string, cause error) error {
	return &Error{Message: message, Hint: hint, Cause: cause}
}

// Hint survives context wrappers and leaves exit-code classification unchanged.
func Hint(err error) string {
	for err != nil {
		if e, ok := err.(*Error); ok && e.Hint != "" {
			return e.Hint
		}
		err = errors.Unwrap(err)
	}
	return ""
}
