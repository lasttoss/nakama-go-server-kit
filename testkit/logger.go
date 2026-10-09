package testkit

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
)

// LogRecorder is a slog handler that keeps what was written, so that a test can assert on the line a
// slow tick produces and not only on the absence of a crash.
type LogRecorder struct {
	mu    sync.Mutex
	lines []string
}

// NewLogger returns a logger and the recorder behind it.
func NewLogger() (*slog.Logger, *LogRecorder) {
	recorder := &LogRecorder{}
	return slog.New(recorder), recorder
}

// Lines returns everything logged so far.
func (r *LogRecorder) Lines() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.lines...)
}

// Contains reports whether any line so far contains substr.
func (r *LogRecorder) Contains(substr string) bool {
	for _, line := range r.Lines() {
		if strings.Contains(line, substr) {
			return true
		}
	}
	return false
}

func (r *LogRecorder) Enabled(context.Context, slog.Level) bool { return true }

func (r *LogRecorder) Handle(_ context.Context, record slog.Record) error {
	line := record.Level.String() + " " + record.Message
	record.Attrs(func(attr slog.Attr) bool {
		line += fmt.Sprintf(" %s=%v", attr.Key, attr.Value.Any())
		return true
	})

	r.mu.Lock()
	defer r.mu.Unlock()
	r.lines = append(r.lines, line)
	return nil
}

func (r *LogRecorder) WithAttrs([]slog.Attr) slog.Handler { return r }
func (r *LogRecorder) WithGroup(string) slog.Handler      { return r }
