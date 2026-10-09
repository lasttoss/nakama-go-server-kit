// Package rpc is the seam between a client's payload and the code that acts on it.
//
// An RPC in a game backend is called by a client that may be an old build, a bot, or somebody with a
// modified APK, so two things have to be true of every handler: a payload it does not understand is
// the client's problem and is answered as such, and a failure inside the server is not described to
// the client at all. Both are easy to get wrong one handler at a time, so they live here once.
package rpc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
)

// DefaultMaxPayload is the largest payload a handler will look at. A game RPC carries a few dozen
// bytes of intent; anything far larger is a client that is looping, and decoding it is work the
// server should not do.
const DefaultMaxPayload = 64 << 10

// Error is a failure the client is entitled to see: what it sent, and what to do about it.
type Error struct {
	// Code is a short machine-readable reason: "invalid_argument", "not_found", "unauthenticated".
	Code string
	// Field names the part of the payload at fault, when there is one.
	Field string
	// Message is for a human reading a log or a client's error toast. Never put anything from the
	// server in here.
	Message string
}

func (e *Error) Error() string {
	if e.Field == "" {
		return fmt.Sprintf("%s: %s", e.Code, e.Message)
	}
	return fmt.Sprintf("%s: %s: %s", e.Code, e.Field, e.Message)
}

// Invalid reports a payload the handler will not act on.
func Invalid(field, format string, args ...any) *Error {
	return &Error{Code: "invalid_argument", Field: field, Message: fmt.Sprintf(format, args...)}
}

// NotFound reports something the caller asked for that is not there.
func NotFound(what string) *Error {
	return &Error{Code: "not_found", Message: what + " not found"}
}

// Unauthenticated reports a call made without a usable session.
func Unauthenticated(reason string) *Error {
	return &Error{Code: "unauthenticated", Message: reason}
}

// internalError is what the client gets instead of a server failure: the same answer for every one of
// them, so that a database outage cannot be read out of the responses.
func internalError() *Error {
	return &Error{Code: "internal", Message: "the request failed; try again"}
}

// Handler is one RPC. userID is the authenticated player, which the transport has already established
// and which the handler must not take from the payload.
type Handler func(ctx context.Context, userID string, payload []byte) (any, error)

// Registry is the set of RPCs a server answers, by name.
type Registry struct {
	mu         sync.RWMutex
	handlers   map[string]Handler
	logger     *slog.Logger
	maxPayload int
}

// Option changes a Registry.
type Option func(*Registry)

// WithLogger is where the failures the client is not told about go. Without one they are dropped.
func WithLogger(logger *slog.Logger) Option { return func(r *Registry) { r.logger = logger } }

// WithMaxPayload replaces the payload size limit.
func WithMaxPayload(n int) Option { return func(r *Registry) { r.maxPayload = n } }

// New returns an empty registry.
func New(opts ...Option) *Registry {
	r := &Registry{handlers: map[string]Handler{}, maxPayload: DefaultMaxPayload}
	for _, opt := range opts {
		opt(r)
	}
	if r.maxPayload <= 0 {
		r.maxPayload = DefaultMaxPayload
	}
	return r
}

// Register adds a handler. A duplicate name is a bug in the server, not a request from a client, so it
// is returned rather than silently replacing the handler that was there.
func (r *Registry) Register(name string, handler Handler) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("rpc: a handler needs a name")
	}
	if handler == nil {
		return fmt.Errorf("rpc: %s has no handler", name)
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if _, taken := r.handlers[name]; taken {
		return fmt.Errorf("rpc: %s is already registered", name)
	}
	r.handlers[name] = handler
	return nil
}

// Names lists the registered RPCs, in order, which is what a health endpoint or a test wants.
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.handlers))
	for name := range r.handlers {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Call runs the handler registered under name.
//
// The error it returns is safe to send to the client as it is: either the handler's own *Error, or an
// internal one with no detail in it. Everything the client should not see has gone to the logger by
// the time Call returns.
func (r *Registry) Call(ctx context.Context, name, userID string, payload []byte) (any, error) {
	if len(payload) > r.maxPayload {
		return nil, Invalid("payload", "%d bytes is over the %d byte limit", len(payload), r.maxPayload)
	}

	r.mu.RLock()
	handler, registered := r.handlers[name]
	r.mu.RUnlock()
	if !registered {
		return nil, NotFound("rpc " + name)
	}

	result, err := run(ctx, userID, payload, handler)
	if err == nil {
		return result, nil
	}

	var visible *Error
	if errors.As(err, &visible) {
		return nil, visible
	}

	// The handler failed for a reason that is not the client's doing, so the client is told nothing
	// about it - a storage error naming a table is how a schema leaks - and the detail goes to the log.
	r.log("rpc failed", name, userID, "error", err)
	return nil, internalError()
}

// run calls the handler with the panic caught. One unexpected payload should cost that request, not
// the process: a game server panicking on a malformed RPC restarts every room it was hosting.
func run(ctx context.Context, userID string, payload []byte, handler Handler) (result any, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("panic: %s", Panic(recovered))
		}
	}()
	return handler(ctx, userID, payload)
}

func (r *Registry) log(message, name, userID string, args ...any) {
	if r.logger == nil {
		return
	}
	r.logger.Error(message, append([]any{"rpc", name, "user", userID}, args...)...)
}

// Panic is the value a recovered panic is logged with, stack and all, so that the line in the log
// says where it happened.
func Panic(recovered any) string {
	return fmt.Sprintf("%v\n%s", recovered, debug.Stack())
}

// Decode reads a payload into into.
//
// A field the handler does not know is refused rather than ignored: a client that sends "amout" has a
// bug, and a server that silently uses the zero value charges the player for nothing. Numbers and
// strings are otherwise the encoding package's business.
//
// An empty payload is not an error, because an RPC whose arguments are all optional is called that
// way; a payload with a second value in it is, because that is a client mistake that would otherwise
// pass silently.
func Decode(payload []byte, into any) error {
	if len(bytes.TrimSpace(payload)) == 0 {
		return nil
	}

	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()

	if err := decoder.Decode(into); err != nil {
		return Invalid("payload", "%s", jsonReason(err))
	}
	if decoder.More() {
		return Invalid("payload", "one value is expected, and the payload has more")
	}
	return nil
}

// jsonReason turns an encoding/json error into something a client can act on, without repeating the
// whole of it back.
func jsonReason(err error) string {
	var typeError *json.UnmarshalTypeError
	if errors.As(err, &typeError) {
		return fmt.Sprintf("field %q expects a %s", typeError.Field, typeError.Type)
	}
	var syntaxError *json.SyntaxError
	if errors.As(err, &syntaxError) {
		return fmt.Sprintf("not valid JSON at byte %d", syntaxError.Offset)
	}
	return err.Error()
}
