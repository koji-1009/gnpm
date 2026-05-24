// Package core holds the shared primitives used across gnpm: typed
// errors with process exit codes, a leveled logger, progress events,
// hashing helpers, and small concurrency utilities.
package core

import (
	"errors"
	"fmt"
)

// Process exit codes. See doc/spec.md §5.1. The 64 / 70 values match
// EX_USAGE / EX_SOFTWARE from sysexits.h; 1 is the npm/pnpm convention
// for "recoverable" command outcomes (audit findings, peer mismatches).
const (
	ExitOK          = 0
	ExitRecoverable = 1
	ExitUsage       = 64
	ExitSoftware    = 70
)

// coded is implemented by every error that maps to a specific process
// exit code. ExitCodeFor uses it to translate an error chain into the
// integer returned by main.
type coded interface {
	error
	ExitCode() int
}

// ExitCodeFor walks err's chain for the first value that declares an
// explicit ExitCode. nil → ExitOK; an unrecognized error → ExitSoftware
// (70), matching the spec's "any other failure" row.
func ExitCodeFor(err error) int {
	if err == nil {
		return ExitOK
	}
	var c coded
	if errors.As(err, &c) {
		return c.ExitCode()
	}
	return ExitSoftware
}

// ExitError carries an explicit exit code with no implied category. It
// covers the recoverable (1) outcomes and child-process propagation in
// run / exec / dlx, where the spec mandates returning the child's code
// verbatim.
type ExitError struct {
	Code    int
	Message string
}

func (e *ExitError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return fmt.Sprintf("exit status %d", e.Code)
}

func (e *ExitError) ExitCode() int { return e.Code }

// Recoverable builds an ExitError with code 1.
func Recoverable(format string, args ...any) *ExitError {
	return &ExitError{Code: ExitRecoverable, Message: fmt.Sprintf(format, args...)}
}

// UsageError is a malformed or rejected invocation: unknown command or
// flag, missing/invalid argument, a value outside the allowed set, or a
// policy gate (strictDepBuilds, pmOnFail=error, frozen-lockfile
// divergence, engine-strict). Maps to exit 64.
type UsageError struct {
	Message string
}

func (e *UsageError) Error() string { return e.Message }
func (e *UsageError) ExitCode() int { return ExitUsage }

// Usage builds a UsageError.
func Usage(format string, args ...any) *UsageError {
	return &UsageError{Message: fmt.Sprintf(format, args...)}
}

// Kind categorizes a typed gnpm software error (all map to exit 70).
type Kind int

const (
	KindNetwork Kind = iota
	KindIntegrity
	KindResolution
	KindIO
	KindLockfile
	KindScript
	KindCancelled
)

func (k Kind) String() string {
	switch k {
	case KindNetwork:
		return "NetworkError"
	case KindIntegrity:
		return "IntegrityError"
	case KindResolution:
		return "ResolutionError"
	case KindIO:
		return "IoError"
	case KindLockfile:
		return "LockfileError"
	case KindScript:
		return "ScriptError"
	case KindCancelled:
		return "CancelledError"
	default:
		return "Error"
	}
}

// Error is a typed gnpm failure. Every Kind maps to exit 70; the Kind is
// retained for messaging and so callers can branch on category via
// errors.As + the Is* helpers below.
type Error struct {
	Kind    Kind
	Message string
	Cause   error

	// Optional structured detail. StatusCode / URI populate on
	// NetworkError; Expected / Actual on IntegrityError.
	StatusCode int
	URI        string
	Expected   string
	Actual     string
}

func (e *Error) Error() string {
	if e.Cause != nil && e.Message != "" {
		return fmt.Sprintf("%s: %s: %v", e.Kind, e.Message, e.Cause)
	}
	if e.Cause != nil {
		return fmt.Sprintf("%s: %v", e.Kind, e.Cause)
	}
	return fmt.Sprintf("%s: %s", e.Kind, e.Message)
}

func (e *Error) Unwrap() error { return e.Cause }
func (e *Error) ExitCode() int { return ExitSoftware }

func newError(kind Kind, format string, args ...any) *Error {
	return &Error{Kind: kind, Message: fmt.Sprintf(format, args...)}
}

// NetworkError: DNS / TLS / connect / timeout / non-2xx HTTP.
func NetworkError(format string, args ...any) *Error {
	return newError(KindNetwork, format, args...)
}

// IntegrityError: tarball hash mismatch, signature failure, or a
// post-install audit finding raised from install.
func IntegrityError(format string, args ...any) *Error {
	return newError(KindIntegrity, format, args...)
}

// ResolutionError: the version solver could not find an assignment.
func ResolutionError(format string, args ...any) *Error {
	return newError(KindResolution, format, args...)
}

// IOError: permission, ENOSPC, EXDEV, and other filesystem failures.
func IOError(format string, args ...any) *Error {
	return newError(KindIO, format, args...)
}

// LockfileError: lockfile parse or round-trip failure.
func LockfileError(format string, args ...any) *Error {
	return newError(KindLockfile, format, args...)
}

// ScriptError: a lifecycle script that surfaced as a hard failure.
func ScriptError(format string, args ...any) *Error {
	return newError(KindScript, format, args...)
}

// CancelledError: the command's context was cancelled.
func CancelledError(format string, args ...any) *Error {
	return newError(KindCancelled, format, args...)
}

// Wrap attaches a cause to a typed error, preserving its Kind.
func (e *Error) Wrap(cause error) *Error {
	e.Cause = cause
	return e
}

// HasKind reports whether err's chain contains a typed gnpm Error of the
// given Kind.
func HasKind(err error, kind Kind) bool {
	var e *Error
	if errors.As(err, &e) {
		return e.Kind == kind
	}
	return false
}
