package rpc_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/lasttoss/nakama-go-server-kit/rpc"
	"github.com/lasttoss/nakama-go-server-kit/testkit"
)

type buyRequest struct {
	Item string `json:"item"`
	Qty  int    `json:"qty"`
}

func TestDecodeRefusesAFieldTheHandlerDoesNotKnow(t *testing.T) {
	// "amout" is a real bug in a real client, and the zero value of a price is free.
	var req buyRequest
	err := rpc.Decode([]byte(`{"item":"sword","amout":100}`), &req)

	if err == nil {
		t.Fatal("a payload with an unknown field was accepted")
	}
	var visible *rpc.Error
	if !errors.As(err, &visible) {
		t.Fatalf("err = %v, want an error the client may see", err)
	}
	if visible.Code != "invalid_argument" {
		t.Errorf("code = %q, want invalid_argument", visible.Code)
	}
	if !strings.Contains(visible.Message, "amout") {
		t.Errorf("the message does not name the field: %q", visible.Message)
	}
}

func TestDecodeReportsAFieldOfTheWrongType(t *testing.T) {
	var req buyRequest
	err := rpc.Decode([]byte(`{"qty":"two"}`), &req)

	var visible *rpc.Error
	if !errors.As(err, &visible) {
		t.Fatalf("err = %v, want a client error", err)
	}
	if !strings.Contains(visible.Message, "qty") {
		t.Errorf("the message does not name the field: %q", visible.Message)
	}
}

func TestDecodeAllowsAnEmptyPayloadAndASecondValueIsNotAllowed(t *testing.T) {
	var req buyRequest
	if err := rpc.Decode(nil, &req); err != nil {
		t.Errorf("an RPC with no arguments cannot be called: %v", err)
	}
	if err := rpc.Decode([]byte("   \n"), &req); err != nil {
		t.Errorf("a payload of whitespace was refused: %v", err)
	}
	if err := rpc.Decode([]byte(`{"item":"sword"} {"item":"shield"}`), &req); err == nil {
		t.Error("two values in one payload were accepted, and only the first was used")
	} else if !strings.Contains(err.Error(), "more") {
		t.Errorf("err = %v, want it to say the payload has more than one value", err)
	}
}

func TestCallPassesThePlayerAndThePayloadToTheHandler(t *testing.T) {
	registry := rpc.New()
	var gotUser string
	err := registry.Register("buy", func(_ context.Context, userID string, payload []byte) (any, error) {
		gotUser = userID
		var req buyRequest
		if err := rpc.Decode(payload, &req); err != nil {
			return nil, err
		}
		return map[string]any{"item": req.Item, "qty": req.Qty}, nil
	})
	if err != nil {
		t.Fatalf("register: %v", err)
	}

	result, err := registry.Call(context.Background(), "buy", "player-1", []byte(`{"item":"sword","qty":2}`))
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if gotUser != "player-1" {
		t.Errorf("the handler saw user %q", gotUser)
	}
	bought, ok := result.(map[string]any)
	if !ok || bought["item"] != "sword" || bought["qty"] != 2 {
		t.Errorf("result = %v", result)
	}
}

func TestRegisterRefusesNamesThatAreTakenOrMissing(t *testing.T) {
	registry := rpc.New()
	handler := func(context.Context, string, []byte) (any, error) { return nil, nil }

	if err := registry.Register("", handler); err == nil {
		t.Error("a handler with no name was registered")
	}
	if err := registry.Register("buy", nil); err == nil {
		t.Error("a nil handler was registered")
	}
	if err := registry.Register("buy", handler); err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := registry.Register("buy", handler); err == nil {
		t.Error("a second handler replaced the first, silently")
	}
	if names := registry.Names(); len(names) != 1 || names[0] != "buy" {
		t.Errorf("names = %v", names)
	}
}

func TestCallOfAnRPCNobodyRegisteredIsAClientError(t *testing.T) {
	registry := rpc.New()

	_, err := registry.Call(context.Background(), "buyyy", "player-1", nil)
	var visible *rpc.Error
	if !errors.As(err, &visible) || visible.Code != "not_found" {
		t.Fatalf("err = %v, want not_found", err)
	}
	if !strings.Contains(visible.Message, "buyyy") {
		t.Errorf("the message does not name the RPC: %q", visible.Message)
	}
}

func TestCallRefusesAPayloadOverTheLimitBeforeDecodingIt(t *testing.T) {
	registry := rpc.New(rpc.WithMaxPayload(64))
	called := false
	if err := registry.Register("buy", func(context.Context, string, []byte) (any, error) {
		called = true
		return nil, nil
	}); err != nil {
		t.Fatalf("register: %v", err)
	}

	_, err := registry.Call(context.Background(), "buy", "player-1", make([]byte, 65))
	if err == nil {
		t.Fatal("a payload over the limit was passed to the handler")
	}
	if called {
		t.Error("the handler was called with a payload that is too large")
	}
}

// The rule that keeps a schema out of a client's error toast.
func TestAServerFailureIsNotDescribedToTheClient(t *testing.T) {
	logger, recorder := testkit.NewLogger()
	registry := rpc.New(rpc.WithLogger(logger))
	secret := errors.New(`pq: password authentication failed for user "game_svc"`)
	if err := registry.Register("buy", func(context.Context, string, []byte) (any, error) {
		return nil, secret
	}); err != nil {
		t.Fatalf("register: %v", err)
	}

	_, err := registry.Call(context.Background(), "buy", "player-1", nil)
	var visible *rpc.Error
	if !errors.As(err, &visible) {
		t.Fatalf("err = %v, want an error to send to the client", err)
	}
	if visible.Code != "internal" {
		t.Errorf("code = %q, want internal", visible.Code)
	}
	if strings.Contains(err.Error(), "game_svc") || strings.Contains(err.Error(), "password") {
		t.Errorf("the client was told about the server's own failure: %q", err)
	}
	// and the operator was told, which is the other half of the rule
	if !recorder.Contains("pq: password authentication failed") {
		t.Errorf("the failure was not logged either: %v", recorder.Lines())
	}
}

// One malformed payload must not take the rooms of every other player with it.
func TestAPanickingHandlerIsOneFailedRequestAndNotAProcess(t *testing.T) {
	registry := rpc.New()
	if err := registry.Register("buy", func(context.Context, string, []byte) (any, error) {
		panic("index out of range: the item list was empty")
	}); err != nil {
		t.Fatalf("register: %v", err)
	}

	result, err := registry.Call(context.Background(), "buy", "player-1", nil)
	if err == nil {
		t.Fatalf("the panic was returned as a result: %v", result)
	}
	var visible *rpc.Error
	if !errors.As(err, &visible) || visible.Code != "internal" {
		t.Fatalf("err = %v, want an internal error", err)
	}
	if strings.Contains(err.Error(), "item list") {
		t.Errorf("the panic message reached the client: %q", err)
	}

	// the registry is still usable afterwards, which is the point of recovering rather than dying
	if _, err := registry.Call(context.Background(), "buy", "player-1", nil); err == nil {
		t.Error("the second call did not fail the same way")
	}
}

func TestAHandlerErrorTheClientShouldSeeIsPassedThrough(t *testing.T) {
	registry := rpc.New()
	refusal := rpc.Invalid("qty", "you can buy at most %d at a time", 10)
	if err := registry.Register("buy", func(context.Context, string, []byte) (any, error) {
		return nil, refusal
	}); err != nil {
		t.Fatalf("register: %v", err)
	}

	_, err := registry.Call(context.Background(), "buy", "player-1", nil)
	var visible *rpc.Error
	if !errors.As(err, &visible) {
		t.Fatalf("err = %v, want the handler's own error", err)
	}
	if visible != refusal {
		t.Errorf("err = %v, want the handler's error unchanged", err)
	}
	if visible.Field != "qty" {
		t.Errorf("field = %q, want qty so the client can highlight it", visible.Field)
	}
}

func TestErrorRendering(t *testing.T) {
	withField := rpc.Invalid("qty", "too many")
	if got := withField.Error(); got != "invalid_argument: qty: too many" {
		t.Errorf("Error() = %q", got)
	}
	without := rpc.NotFound("clan")
	if got := without.Error(); got != "not_found: clan not found" {
		t.Errorf("Error() = %q", got)
	}
}

func TestPanicRenderingIncludesTheStack(t *testing.T) {
	rendered := rpc.Panic("boom")

	if !strings.Contains(rendered, "boom") || !strings.Contains(rendered, "rpc_test.go") {
		t.Errorf("Panic() = %q, want the value and where it happened", rendered)
	}
}
