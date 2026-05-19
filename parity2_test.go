package angzarr

// Cross-language parity tests for the second TDD pass.
//
// Adds tests for:
//   - Audit #18 / #72: multi-handler CommandHandler ban (DUPLICATE_COMMAND_HANDLER)
//   - Rust commit 804c362 / `#[handles_unknown]`: projector catch-all + WARN
//   - Audit #87: ANY_DECODE_FAILED cross-language error shape
//   - Audit #89: server_started / server_shutdown structured log shape
//
// References:
//   - Python source of truth at examples-python/angzarr-client-python
//   - Rust parity reference at examples-rust/angzarr-client-rust

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	pb "github.com/benjaminabbitt/angzarr/client/go/proto/angzarr_client/proto/angzarr"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

// ============================================================================
// Audit #18 / #72: multi-handler CommandHandler ban
// ============================================================================
//
// Python `Router.build()` raises with code=DUPLICATE_COMMAND_HANDLER when
// two CommandHandlers register for the same (domain, command_type_url).
// Rust `Router::build()` returns the same code.
//
// Go's typed-router design uses `NewCommandHandlerRouter(name, domain,
// handler)` — only ONE handler per router instance. The compile-time
// proof: there is no API to register a second handler. We pin this with
// a runtime CommandHandlerRouter check that detects duplicate handler
// registration if it ever sneaks in via a higher-level builder.

func TestMultiHandlerBan_StructuralByConstruction(t *testing.T) {
	// Go's CommandHandlerRouter has no `Add()` method — the only way
	// to register a handler is at construction time. This test
	// documents the contract: NewCommandHandlerRouter takes a single
	// `handler` argument. If a refactor introduces a multi-handler
	// API, this test will need to be replaced with a duplicate-
	// rejection check (see TestMultiHandlerBan_RuntimeCheck below).
	router := NewCommandHandlerRouter[any]("ch-order", "order", &noopCHHandler{})
	if router.Name() != "ch-order" {
		t.Errorf("name mismatch: %q", router.Name())
	}
	if router.Domain() != "order" {
		t.Errorf("domain mismatch: %q", router.Domain())
	}
}

// TestMultiHandlerBan_RuntimeCheck verifies the explicit duplicate-handler
// check on `CommandHandlerRouterRegistry` — the surface that aggregates
// multiple CommandHandlerRouters by (domain, command_type) pair.
//
// Python `router/builder.py:183` and Rust `router/builder.rs:130` raise
// DUPLICATE_COMMAND_HANDLER when two routers cover the same pair.
func TestMultiHandlerBan_RuntimeRegistryRejectsDuplicatePair(t *testing.T) {
	reg := NewCommandHandlerRegistry()
	a := NewCommandHandlerRouter[any]("ch-a", "order", &noopCHHandler{types: []string{"order.CreateOrder"}})
	b := NewCommandHandlerRouter[any]("ch-b", "order", &noopCHHandler{types: []string{"order.CreateOrder"}})

	if err := reg.Register(a); err != nil {
		t.Fatalf("first register must succeed: %v", err)
	}
	err := reg.Register(b)
	if err == nil {
		t.Fatal("expected DUPLICATE_COMMAND_HANDLER error on second register")
	}
	ce := AsClientError(err)
	if ce == nil {
		t.Fatalf("expected ClientError, got %T", err)
	}
	if ce.Code != CodeDuplicateCommandHandler {
		t.Errorf("expected code %q, got %q", CodeDuplicateCommandHandler, ce.Code)
	}
	if ce.Extras[ExtraKeyDomain] != "order" {
		t.Errorf("expected domain extra=order, got %v", ce.Extras)
	}
}

// Cross-domain CHs for the same command type are allowed (audit #18 C-0011).
func TestMultiHandlerBan_AcceptsDifferentDomains(t *testing.T) {
	reg := NewCommandHandlerRegistry()
	a := NewCommandHandlerRouter[any]("ch-orderA", "orderA", &noopCHHandler{types: []string{"order.CreateOrder"}})
	b := NewCommandHandlerRouter[any]("ch-orderB", "orderB", &noopCHHandler{types: []string{"order.CreateOrder"}})

	if err := reg.Register(a); err != nil {
		t.Fatalf("register a: %v", err)
	}
	if err := reg.Register(b); err != nil {
		t.Fatalf("register b across domains must succeed: %v", err)
	}
}

// A single CH with multiple handled types is allowed (audit #18 C-0012).
func TestMultiHandlerBan_AcceptsSingleHandlerMultipleTypes(t *testing.T) {
	reg := NewCommandHandlerRegistry()
	a := NewCommandHandlerRouter[any](
		"ch-player",
		"player",
		&noopCHHandler{types: []string{"player.RegisterPlayer", "player.DepositFunds"}},
	)
	if err := reg.Register(a); err != nil {
		t.Fatalf("single CH multi-type must succeed: %v", err)
	}
}

// noopCHHandler is a minimal CommandHandlerDomainHandler[any] for the
// registration tests above — it never dispatches.
type noopCHHandler struct {
	types []string
}

func (h *noopCHHandler) CommandTypes() []string         { return h.types }
func (h *noopCHHandler) StateRouter() *StateRouter[any] { return nil }
func (h *noopCHHandler) Rebuild(*pb.EventBook) any {
	return nil
}
func (h *noopCHHandler) HandleFact(facts *pb.EventBook, _ any) (*pb.EventBook, error) {
	return facts, nil
}
func (h *noopCHHandler) Handle(
	*pb.CommandBook, *anypb.Any, any, uint32,
) (*pb.EventBook, error) {
	return nil, nil
}
func (h *noopCHHandler) OnRejected(
	*pb.Notification, any, string, string,
) (*RejectionHandlerResponse, error) {
	return nil, nil
}

// ============================================================================
// Rust commit 804c362: projector `#[handles_unknown]` catch-all + WARN
// ============================================================================
//
// When a projector receives an event whose type_url matches no registered
// `#[handles]` arm, the macro emits an unconditional `tracing::warn!` and,
// if the user declared `#[handles_unknown]`, invokes that method too.
//
// Go-idiomatic equivalent: `OnUnknown(handler)` on ProjectorBase that
// registers a catch-all; the dispatcher always logs at WARN via slog and
// invokes the catch-all when no handler matches.

func TestProjectorBase_CatchAllInvokedOnUnknownType(t *testing.T) {
	p := &ProjectorBase{}
	p.Init("test-projector", []string{"order"})

	var seenTypeURL string
	p.OnUnknown(func(typeURL string) {
		seenTypeURL = typeURL
	})

	// Build an EventBook with an unknown event type.
	unknownAny := &anypb.Any{
		TypeUrl: "type.googleapis.com/examples.UnknownEvent",
		Value:   []byte{0x01, 0x02},
	}
	book := &pb.EventBook{
		Cover: &pb.Cover{Domain: "order"},
		Pages: []*pb.EventPage{{
			Header:  &pb.PageHeader{SequenceType: &pb.PageHeader_Sequence{Sequence: 1}},
			Payload: &pb.EventPage_Event{Event: unknownAny},
		}},
	}

	if _, err := p.Handle(book); err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}

	if seenTypeURL != "type.googleapis.com/examples.UnknownEvent" {
		t.Errorf("catch-all received %q, want unknown type URL", seenTypeURL)
	}
}

func TestProjectorBase_WarnLogOnUnknownType(t *testing.T) {
	// Capture slog WARN output via a JSON-handler-backed buffer.
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))
	prev := SetProjectorLogger(logger)
	defer SetProjectorLogger(prev)

	p := &ProjectorBase{}
	p.Init("test-projector", []string{"order"})

	unknownAny := &anypb.Any{
		TypeUrl: "type.googleapis.com/examples.UnknownEvent",
	}
	book := &pb.EventBook{
		Cover: &pb.Cover{Domain: "order"},
		Pages: []*pb.EventPage{{
			Header:  &pb.PageHeader{SequenceType: &pb.PageHeader_Sequence{Sequence: 1}},
			Payload: &pb.EventPage_Event{Event: unknownAny},
		}},
	}

	if _, err := p.Handle(book); err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "projector received event with no matching") {
		t.Errorf("expected WARN log for unknown type, got: %s", out)
	}
	if !strings.Contains(out, "test-projector") {
		t.Errorf("expected projector name in log fields, got: %s", out)
	}
	if !strings.Contains(out, "examples.UnknownEvent") {
		t.Errorf("expected type_url in log fields, got: %s", out)
	}
	if !strings.Contains(out, `"level":"WARN"`) {
		t.Errorf("expected WARN level, got: %s", out)
	}
}

func TestProjectorBase_CatchAllNotFiredForKnownType(t *testing.T) {
	p := &ProjectorBase{}
	p.Init("test-projector", []string{"player"})

	called := 0
	p.Projects(func(event *wrapperspb.StringValue) *pb.Projection {
		called++
		return nil
	})

	var unknownCalled bool
	p.OnUnknown(func(string) { unknownCalled = true })

	knownAny, _ := anypb.New(wrapperspb.String("hi"))
	book := &pb.EventBook{
		Cover: &pb.Cover{Domain: "player"},
		Pages: []*pb.EventPage{{
			Header:  &pb.PageHeader{SequenceType: &pb.PageHeader_Sequence{Sequence: 1}},
			Payload: &pb.EventPage_Event{Event: knownAny},
		}},
	}
	if _, err := p.Handle(book); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if called == 0 {
		t.Error("known-type handler must fire")
	}
	if unknownCalled {
		t.Error("OnUnknown must NOT fire for known type")
	}
}

// ============================================================================
// Audit #87: ANY_DECODE_FAILED — cross-language error shape on bad Any bytes
// ============================================================================
//
// Python `_parse_any`/`_unpack_any` and Rust's macro-emitted dispatch wrap
// proto decode failures as INVALID_ARGUMENT with code=ANY_DECODE_FAILED
// and extras={type_url, cause}. Pre-fix Python bubbled up as INTERNAL.
//
// Go's parallel helper: ParseAnyE / UnpackAnyE.

func TestParseAnyE_WrapsDecodeErrorAsInvalidArgument(t *testing.T) {
	target := &wrapperspb.StringValue{}
	err := ParseAnyE(target, []byte{0xff, 0xff, 0xff, 0xff, 0xff}, "type.googleapis.com/google.protobuf.StringValue")
	if err == nil {
		t.Fatal("expected error on bad proto bytes")
	}
	ce := AsClientError(err)
	if ce == nil {
		t.Fatalf("expected ClientError, got %T: %v", err, err)
	}
	if ce.Code != CodeAnyDecodeFailed {
		t.Errorf("expected code %q, got %q", CodeAnyDecodeFailed, ce.Code)
	}
	if ce.Kind != ErrInvalidArgument {
		t.Errorf("expected Kind=ErrInvalidArgument, got %v", ce.Kind)
	}
	if ce.Extras[ExtraKeyTypeURL] != "type.googleapis.com/google.protobuf.StringValue" {
		t.Errorf("expected type_url extra, got %v", ce.Extras)
	}
	if ce.Extras[ExtraKeyCause] == "" {
		t.Errorf("expected cause extra to be set, got %v", ce.Extras)
	}
}

func TestParseAnyE_SuccessfulDecode(t *testing.T) {
	src := wrapperspb.String("hello")
	any, _ := anypb.New(src)

	dst := &wrapperspb.StringValue{}
	if err := ParseAnyE(dst, any.Value, any.TypeUrl); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dst.Value != "hello" {
		t.Errorf("got %q, want hello", dst.Value)
	}
}

func TestUnpackAnyE_WrapsBadPayloadAsInvalidArgument(t *testing.T) {
	any := &anypb.Any{
		TypeUrl: "type.googleapis.com/google.protobuf.StringValue",
		Value:   []byte{0xff, 0xff, 0xff, 0xff, 0xff},
	}
	target := &wrapperspb.StringValue{}
	err := UnpackAnyE(any, target)
	if err == nil {
		t.Fatal("expected error on bad bytes")
	}
	ce := AsClientError(err)
	if ce == nil || ce.Code != CodeAnyDecodeFailed {
		t.Fatalf("expected ANY_DECODE_FAILED, got %v", err)
	}
}

// ============================================================================
// Audit #89: server_started / server_shutdown structured log shape
// ============================================================================
//
// Python `_run_server_async` and Rust `server.rs::run_server` emit
// `info!("server_started", service=..., name=..., transport=..., address=...)`
// and `info!("server_shutdown", service=..., name=...)`. Operators query
// these by structured field across language deployments.

func TestServerStartedLog_HasParityFields(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))

	LogServerStarted(logger, "BusinessService", "player", "tcp", "0.0.0.0:50052")

	out := buf.String()
	if !strings.Contains(out, `"msg":"server_started"`) {
		t.Errorf("expected msg=server_started, got: %s", out)
	}
	if !strings.Contains(out, `"service":"BusinessService"`) {
		t.Errorf("expected service field, got: %s", out)
	}
	if !strings.Contains(out, `"name":"player"`) {
		t.Errorf("expected name field, got: %s", out)
	}
	if !strings.Contains(out, `"transport":"tcp"`) {
		t.Errorf("expected transport field, got: %s", out)
	}
	if !strings.Contains(out, `"address":"0.0.0.0:50052"`) {
		t.Errorf("expected address field, got: %s", out)
	}
}

func TestServerShutdownLog_HasParityFields(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))

	LogServerShutdown(logger, "BusinessService", "player")

	out := buf.String()
	if !strings.Contains(out, `"msg":"server_shutdown"`) {
		t.Errorf("expected msg=server_shutdown, got: %s", out)
	}
	if !strings.Contains(out, `"service":"BusinessService"`) {
		t.Errorf("expected service field, got: %s", out)
	}
	if !strings.Contains(out, `"name":"player"`) {
		t.Errorf("expected name field, got: %s", out)
	}
}

// Smoke: ensure the context import isn't dropped by tooling.
var _ = context.Background
