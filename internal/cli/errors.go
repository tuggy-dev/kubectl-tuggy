package cli

import (
	"context"
	"errors"
)

// Exit codes returned by kubectl-tuggy. They are part of the public contract
// documented in docs/design/0001-architecture.md; do not renumber them.
const (
	ExitOK        = 0 // success
	ExitError     = 1 // general failure
	ExitUsage     = 2 // invalid input: bad flags, arguments or spec
	ExitPreflight = 3 // a preflight check failed before any change was made
	ExitCancelled = 4 // cancelled by the user (Ctrl-C or declined confirmation)
	ExitCloud     = 5 // the cloud operation itself failed
)

// exitCodeError attaches an exit code to an error.
type exitCodeError struct {
	Code int
	Err  error
}

func (e *exitCodeError) Error() string { return e.Err.Error() }
func (e *exitCodeError) Unwrap() error { return e.Err }

// WithExitCode wraps err so that kubectl-tuggy exits with code. A nil err stays nil.
func WithExitCode(code int, err error) error {
	if err == nil {
		return nil
	}
	return &exitCodeError{Code: code, Err: err}
}

// ExitCodeFor maps an error returned by a command to a process exit code.
func ExitCodeFor(err error) int {
	if err == nil {
		return ExitOK
	}
	if ee, ok := errors.AsType[*exitCodeError](err); ok {
		return ee.Code
	}
	if errors.Is(err, context.Canceled) {
		return ExitCancelled
	}
	return ExitError
}
