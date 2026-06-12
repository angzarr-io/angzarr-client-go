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

	pb "github.com/benjaminabbitt/angzarr/client/go/proto/angzarr_client/proto/angzarr/v1"
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

// parityCH builds a minimal engine aggregate table for the ban tests.
func parityCH(name, domain string, types ...string) *AggregateDispatch[*struct{}] {
	table := NewAggregateDispatch(name, domain, NewRebuilder(func() *struct{} { return &struct{}{} }))
	for _, fqType := range types {
		table.OnCommand(fqType, func(*anypb.Any, *struct{}, CommandContext) (*pb.EventBook, error) {
			return nil, nil
		})
	}
	return table
}

// Python `Router.build()` and Rust `Router::build()` raise
// DUPLICATE_COMMAND_HANDLER when two CommandHandlers register the same
// (domain, command_type) pair. Go pins the same on ComposeCommandHandlers.
func TestMultiHandlerBan_RejectsDuplicatePair(t *testing.T) {
	_, err := ComposeCommandHandlers(
		parityCH("ch-a", "order", "order.CreateOrder"),
		parityCH("ch-b", "order", "order.CreateOrder"),
	)
	if err == nil {
		t.Fatal("expected DUPLICATE_COMMAND_HANDLER error")
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
	if _, err := ComposeCommandHandlers(
		parityCH("ch-orderA", "orderA", "order.CreateOrder"),
		parityCH("ch-orderB", "orderB", "order.CreateOrder"),
	); err != nil {
		t.Fatalf("cross-domain registration must succeed: %v", err)
	}
}

// A single CH with multiple handled types is allowed (audit #18 C-0012).
func TestMultiHandlerBan_AcceptsSingleHandlerMultipleTypes(t *testing.T) {
	if _, err := ComposeCommandHandlers(
		parityCH("ch-player", "player", "player.RegisterPlayer", "player.DepositFunds"),
	); err != nil {
		t.Fatalf("single CH multi-type must succeed: %v", err)
	}
}

// ============================================================================
// Rust commit 804c362: projector `#[handles_unknown]` catch-all + WARN
// ============================================================================
//
// When a projector receives an event whose type_url matches no registered
// `#[handles]` arm, the macro emits an unconditional `tracing::warn!` and,
// if the user declared `#[handles_unknown]`, invokes that method too.
//
// Go-idiomatic equivalent: `OnUnknown(handler)` on ProjectorDispatch:
// the catch-all is invoked for unmatched types; without a catch-all the
// dispatcher logs at WARN via the projector logger.

func TestProjectorDispatch_CatchAllInvokedOnUnknownType(t *testing.T) {
	var seenTypeURL string
	p := NewProjectorDispatch("test-projector", func() *struct{} { return &struct{}{} }).
		ForDomains("order").
		OnUnknown(func(typeURL string) {
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

	if _, err := p.Dispatch(book); err != nil {
		t.Fatalf("Dispatch returned error: %v", err)
	}

	if seenTypeURL != "type.googleapis.com/examples.UnknownEvent" {
		t.Errorf("catch-all received %q, want unknown type URL", seenTypeURL)
	}
}

func TestProjectorDispatch_WarnLogOnUnknownType(t *testing.T) {
	// Capture slog WARN output via a JSON-handler-backed buffer.
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))
	prev := SetProjectorLogger(logger)
	defer SetProjectorLogger(prev)

	p := NewProjectorDispatch("test-projector", func() *struct{} { return &struct{}{} }).
		ForDomains("order")

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

	if _, err := p.Dispatch(book); err != nil {
		t.Fatalf("Dispatch returned error: %v", err)
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

func TestProjectorDispatch_CatchAllNotFiredForKnownType(t *testing.T) {
	called := 0
	var unknownCalled bool
	p := NewProjectorDispatch("test-projector", func() *struct{} { return &struct{}{} }).
		ForDomains("player").
		OnEvent("google.protobuf.StringValue", func(*struct{}, *anypb.Any) error {
			called++
			return nil
		}).
		OnUnknown(func(string) { unknownCalled = true })

	knownAny, _ := anypb.New(wrapperspb.String("hi"))
	book := &pb.EventBook{
		Cover: &pb.Cover{Domain: "player"},
		Pages: []*pb.EventPage{{
			Header:  &pb.PageHeader{SequenceType: &pb.PageHeader_Sequence{Sequence: 1}},
			Payload: &pb.EventPage_Event{Event: knownAny},
		}},
	}
	if _, err := p.Dispatch(book); err != nil {
		t.Fatalf("Dispatch: %v", err)
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
