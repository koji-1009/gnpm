package core

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
)

// LogLevel orders verbosity from silent (no output) to trace. Mirrors
// the values accepted by the global --loglevel flag.
type LogLevel int

const (
	LevelSilent LogLevel = iota
	LevelError
	LevelWarn
	LevelInfo
	LevelDebug
	LevelTrace
)

// ParseLogLevel maps a --loglevel value to a LogLevel. ok is false when
// the string is not one of the allowed names.
func ParseLogLevel(s string) (LogLevel, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "silent":
		return LevelSilent, true
	case "error":
		return LevelError, true
	case "warn":
		return LevelWarn, true
	case "info":
		return LevelInfo, true
	case "debug":
		return LevelDebug, true
	case "trace":
		return LevelTrace, true
	default:
		return LevelInfo, false
	}
}

func (l LogLevel) String() string {
	switch l {
	case LevelSilent:
		return "silent"
	case LevelError:
		return "error"
	case LevelWarn:
		return "warn"
	case LevelInfo:
		return "info"
	case LevelDebug:
		return "debug"
	case LevelTrace:
		return "trace"
	default:
		return "info"
	}
}

// Logger writes leveled diagnostics to a writer (stderr by default).
// Diagnostics never go to stdout so machine-readable command output
// (--json, view, pkg, sbom) stays uncontaminated.
type Logger struct {
	mu    sync.Mutex
	name  string
	out   io.Writer
	level LogLevel
}

// NewLogger builds a logger writing to stderr at the given level.
func NewLogger(name string, level LogLevel) *Logger {
	return &Logger{name: name, out: os.Stderr, level: level}
}

// WithName returns a sibling logger sharing this one's writer and level
// but tagged with a different name.
func (l *Logger) WithName(name string) *Logger {
	return &Logger{name: name, out: l.out, level: l.level}
}

// SetLevel adjusts the threshold below which messages are dropped.
func (l *Logger) SetLevel(level LogLevel) {
	l.mu.Lock()
	l.level = level
	l.mu.Unlock()
}

// SetOutput redirects diagnostics, mainly for tests.
func (l *Logger) SetOutput(w io.Writer) {
	l.mu.Lock()
	l.out = w
	l.mu.Unlock()
}

func (l *Logger) Level() LogLevel {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.level
}

func (l *Logger) log(level LogLevel, tag, format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if level > l.level {
		return
	}
	msg := fmt.Sprintf(format, args...)
	fmt.Fprintf(l.out, "%s %s\n", tag, msg)
}

func (l *Logger) Error(format string, args ...any) { l.log(LevelError, "error", format, args...) }
func (l *Logger) Warn(format string, args ...any)  { l.log(LevelWarn, "warn", format, args...) }
func (l *Logger) Info(format string, args ...any)  { l.log(LevelInfo, "info", format, args...) }
func (l *Logger) Debug(format string, args ...any) { l.log(LevelDebug, "debug", format, args...) }
func (l *Logger) Trace(format string, args ...any) { l.log(LevelTrace, "trace", format, args...) }
