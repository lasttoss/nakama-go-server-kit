package testkit_test

import (
	"context"
	"log/slog"
	"testing"

	"github.com/lasttoss/nakama-go-server-kit/testkit"
)

func TestTheRecorderKeepsWhatWasLogged(t *testing.T) {
	logger, recorder := testkit.NewLogger()

	logger.Warn("tick overran its budget", "tick", 7, "took", "30ms")
	logger.Info("match loop stopped", "ticks", 3)

	if !recorder.Contains("overran") {
		t.Errorf("the recorder lost the warning: %v", recorder.Lines())
	}
	if !recorder.Contains("tick=7") || !recorder.Contains("took=30ms") {
		t.Errorf("the attributes are missing: %v", recorder.Lines())
	}
	if recorder.Contains("something that was never logged") {
		t.Error("Contains reported a line that does not exist")
	}
	if lines := recorder.Lines(); len(lines) != 2 {
		t.Errorf("kept %d lines, want 2: %v", len(lines), lines)
	}
}

// The logger is handed to code under test that may call WithGroup, so the handler has to survive it
// rather than panic on a nil receiver.
func TestTheRecorderSurvivesTheUsualHandlerCalls(t *testing.T) {
	logger, recorder := testkit.NewLogger()
	logger.With("component", "loop").WithGroup("tick").Warn("still here")

	if !recorder.Contains("still here") {
		t.Errorf("a grouped logger lost the line: %v", recorder.Lines())
	}
}

func TestTheHandlerIsAlwaysEnabled(t *testing.T) {
	var handler slog.Handler = func() slog.Handler { _, recorder := testkit.NewLogger(); return recorder }()

	// A recorder that filtered by level would hide the line a test is waiting for.
	if !handler.Enabled(context.Background(), slog.LevelDebug) {
		t.Fatal("the handler is not enabled for debug")
	}
	if grouped := handler.WithGroup("tick").WithAttrs([]slog.Attr{slog.String("k", "v")}); grouped == nil {
		t.Fatal("WithGroup or WithAttrs returned nothing")
	}
}
