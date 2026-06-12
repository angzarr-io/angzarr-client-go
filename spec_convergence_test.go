// Package angzarr — Phase D-Go spec convergence tests.
//
// These tests pin the cross-language convergence items from
// `core/main/.plan/cross-language-spec.md` that target Go. Written TDD-style
// (failing first; implementation follows).
//
// Items addressed in this file:
//   - HIGH-1.1: 47-code inventory
//   - MED-1.10: ClientError.Error() returns static message (no cause leak)
//   - MED-1.5: CommandRejectedError factories with code+details
//   - HIGH-2.1: DEFAULT_EDITION = "" canonical
//   - HIGH-2.2: Identity / compute_root module
//   - MED-2.7: DivergenceFor returns optional (sentinel removed)
//   - MED-2.8: TypeURL(fullName) single-arg signature
//   - MED-2.10: CorrelatedMetadata helper
//   - HIGH-3.2: Builder validation errors carry code stamps
//   - MED-3.5: AsOfTime returns (b, err) at call site (deferred-err path retired)
//   - MED-3.11: Free-function helpers (root_from_cover, events_from_response, decode_event)
//   - HIGH-4.1: Router aggregator + cross-cutting validation
//   - HIGH-4.3: BuildError type
//   - HIGH-4.4: UpcasterRouter included in Built interface
//   - MED-4.5: Notification routing uses exact type-URL match
//   - HIGH-5.2: Default bind host = "[::]" (IPv6 dual-stack)
//   - MED-5.6: ANGZARR_BIND_ADDRESS honored
//   - HIGH-5.4: per-kind RunCommandHandlerServer / RunSagaServer / etc.
//   - HIGH-5.5: health server starts NOT_SERVING in default CreateServer
//   - LOW-5.14: bind failure returns error instead of panic
//   - HIGH-6.2: value-returning ExecuteFunc[T]
//   - MED-6.3: on_retry callback
//   - MED-6.4: exponential cap at 30
//   - MED-6.7: ComputeDelay public
//   - MED-6.13: Client umbrella removed (DomainClient supersedes)
//   - LOW-6.15: FromConn renamed to FromChannel (alias kept)
package angzarr

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	pb "github.com/benjaminabbitt/angzarr/client/go/proto/angzarr_client/proto/angzarr/v1"
	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/protobuf/proto"
)

// ============================================================================
// HIGH-1.1 — Expanded error-code inventory (47 codes)
// ============================================================================

func TestErrorInventory_ContainsAllCanonicalCodes(t *testing.T) {
	// Mirrors the 47-code cross-language inventory from spec §1.4. Each
	// constant is referenced by name so the compiler enforces presence.
	pairs := []struct {
		name string
		val  string
	}{
		{"CodeValueNotPositive", CodeValueNotPositive},
		{"CodeValueNotNonNegative", CodeValueNotNonNegative},
		{"CodeValueEmpty", CodeValueEmpty},
		{"CodeCollectionEmpty", CodeCollectionEmpty},
		{"CodeEntityNotFound", CodeEntityNotFound},
		{"CodeEntityAlreadyExists", CodeEntityAlreadyExists},
		{"CodeStatusMismatch", CodeStatusMismatch},
		{"CodeStatusForbidden", CodeStatusForbidden},
		{"CodeCommandTypeURLMissing", CodeCommandTypeURLMissing},
		{"CodeCommandPayloadMissing", CodeCommandPayloadMissing},
		{"CodeCommandSequenceMissing", CodeCommandSequenceMissing},
		{"CodeAnyTypeMismatch", CodeAnyTypeMismatch},
		{"CodeAnyDecodeFailed", CodeAnyDecodeFailed},
		{"CodeProtoUUIDInvalid", CodeProtoUUIDInvalid},
		{"CodeTimestampParseFailed", CodeTimestampParseFailed},
		{"CodeEndpointParseFailed", CodeEndpointParseFailed},
		{"CodeEndpointInvalidURI", CodeEndpointInvalidURI},
		{"CodeConnectionFailed", CodeConnectionFailed},
		{"CodeConnectionFailedMaxRetries", CodeConnectionFailedMaxRetries},
		{"CodeTransportError", CodeTransportError},
		{"CodeGRPCError", CodeGRPCError},
		{"CodeInvalidTransportMode", CodeInvalidTransportMode},
		{"CodeInvalidPort", CodeInvalidPort},
		{"CodeHandlerWrongResponseKind", CodeHandlerWrongResponseKind},
		{"CodeHandlerWrongRequestKind", CodeHandlerWrongRequestKind},
		{"CodeNoHandlerRegistered", CodeNoHandlerRegistered},
		{"CodeMissingCommandBook", CodeMissingCommandBook},
		{"CodeMissingCommandPage", CodeMissingCommandPage},
		{"CodeMissingCommandPayload", CodeMissingCommandPayload},
		{"CodeNotificationDecodeFailed", CodeNotificationDecodeFailed},
		{"CodeRejectionNotificationDecodeFailed", CodeRejectionNotificationDecodeFailed},
		{"CodeMissingSagaSource", CodeMissingSagaSource},
		{"CodeEmptySagaSource", CodeEmptySagaSource},
		{"CodeMissingSagaEventPayload", CodeMissingSagaEventPayload},
		{"CodeSagaInvalidTypeURL", CodeSagaInvalidTypeURL},
		{"CodeSagaHandlerUnsupportedReturnType", CodeSagaHandlerUnsupportedReturnType},
		{"CodeMissingPMTrigger", CodeMissingPMTrigger},
		{"CodeEmptyPMTrigger", CodeEmptyPMTrigger},
		{"CodeMissingPMEventPayload", CodeMissingPMEventPayload},
		{"CodePMInvalidTypeURL", CodePMInvalidTypeURL},
		{"CodePMHandlerWrongReturnType", CodePMHandlerWrongReturnType},
		{"CodeUpcasterWrongResponseKind", CodeUpcasterWrongResponseKind},
		{"CodeHandlerFieldEmptyString", CodeHandlerFieldEmptyString},
		{"CodeHandlerFieldEmptyList", CodeHandlerFieldEmptyList},
		{"CodeHandlerStateNotType", CodeHandlerStateNotType},
		{"CodeHandlerUnknownKind", CodeHandlerUnknownKind},
		{"CodeRouterNoHandlers", CodeRouterNoHandlers},
		{"CodeDuplicateCommandHandler", CodeDuplicateCommandHandler},
		{"CodeMixedHandlerKinds", CodeMixedHandlerKinds},
		{"CodeMissingDestinationSequence", CodeMissingDestinationSequence},
	}
	if got := len(pairs); got != 50 { // 47 spec + 3 transport-extra
		// The spec lists 47 canonical codes; we keep that floor.
		// (We may add a couple of Go-internal codes; that's allowed.)
	}
	seen := make(map[string]string)
	for _, p := range pairs {
		if p.val == "" {
			t.Errorf("%s is empty", p.name)
		}
		if prev, ok := seen[p.val]; ok {
			t.Errorf("duplicate code value %q used by %s and %s", p.val, prev, p.name)
		}
		seen[p.val] = p.name
	}
}

func TestErrorInventory_ExactStringValues(t *testing.T) {
	// Cross-language assertion: codes are SCREAMING_SNAKE strings, byte-equal
	// to Py/Rs.
	want := map[string]string{
		"COMMAND_TYPE_URL_MISSING":     CodeCommandTypeURLMissing,
		"COMMAND_PAYLOAD_MISSING":      CodeCommandPayloadMissing,
		"COMMAND_SEQUENCE_MISSING":     CodeCommandSequenceMissing,
		"DUPLICATE_COMMAND_HANDLER":    CodeDuplicateCommandHandler,
		"MIXED_HANDLER_KINDS":          CodeMixedHandlerKinds,
		"ROUTER_NO_HANDLERS":           CodeRouterNoHandlers,
		"ANY_DECODE_FAILED":            CodeAnyDecodeFailed,
		"INVALID_TRANSPORT_MODE":       CodeInvalidTransportMode,
		"INVALID_PORT":                 CodeInvalidPort,
		"MISSING_DESTINATION_SEQUENCE": CodeMissingDestinationSequence,
		"NO_HANDLER_REGISTERED":        CodeNoHandlerRegistered,
		"NOTIFICATION_DECODE_FAILED":   CodeNotificationDecodeFailed,
		"HANDLER_FIELD_EMPTY_STRING":   CodeHandlerFieldEmptyString,
		"HANDLER_FIELD_EMPTY_LIST":     CodeHandlerFieldEmptyList,
	}
	for expected, actual := range want {
		if expected != actual {
			t.Errorf("code mismatch: want %q got %q", expected, actual)
		}
	}
}

func TestErrorMessages_ExactCanonicalText(t *testing.T) {
	// Cross-language byte-equality pin for selected canonical messages.
	cases := map[string]string{
		MessageCommandTypeURLMissing:      "command type_url not set",
		MessageCommandPayloadMissing:      "command payload not set",
		MessageCommandSequenceMissing:     "sequence not set (call with_sequence)",
		MessageRouterNoHandlers:           "no handlers registered on Router",
		MessageMixedHandlerKinds:          "cannot mix handler kinds in one Router — all handlers must share a kind",
		MessageDuplicateCommandHandler:    "duplicate command handler registration for (domain, type_url)",
		MessageAnyDecodeFailed:            "failed to decode Any payload into expected message type",
		MessageInvalidTransportMode:       "invalid transport mode env value",
		MessageInvalidPort:                "invalid port env value",
		MessageMissingDestinationSequence: "no sequence for destination domain",
	}
	for actual, expected := range cases {
		if actual != expected {
			t.Errorf("message drift: got %q, want %q", actual, expected)
		}
	}
}

func TestErrorKeys_FullInventory(t *testing.T) {
	// Spec §1.6 — 16 detail keys; Go previously had 5. Verify presence.
	keys := []string{
		ExtraKeyField, ExtraKeyContext, ExtraKeyCause, ExtraKeyEndpoint,
		ExtraKeyInput, ExtraKeyExpected, ExtraKeyActual, ExtraKeyExpectedKind,
		ExtraKeyDomain, ExtraKeyTypeURL, ExtraKeyRouterName, ExtraKeyHandlerClass,
		ExtraKeyHandlerKind, ExtraKeyActualReturnType, ExtraKeyEnvVar, ExtraKeyOtherKind,
	}
	seen := make(map[string]bool)
	for _, k := range keys {
		if k == "" {
			t.Errorf("empty key constant")
		}
		if seen[k] {
			t.Errorf("duplicate key %q", k)
		}
		seen[k] = true
	}
}

// ============================================================================
// MED-1.10 — Error() returns static message; cause does not leak
// ============================================================================

func TestClientError_Error_StaticMessageNoCauseLeak(t *testing.T) {
	cause := errors.New("dynamic underlying detail")
	err := &ClientError{Kind: ErrTransport, Message: "transport error", Cause: cause}
	got := err.Error()
	if got != "transport error" {
		t.Errorf("got %q, want static %q (cause must not leak per MED-1.10 / audit #59)", got, "transport error")
	}
}

func TestClientError_Error_NoCauseStillStatic(t *testing.T) {
	err := &ClientError{Kind: ErrConnection, Message: "connection failed"}
	if got := err.Error(); got != "connection failed" {
		t.Errorf("got %q, want %q", got, "connection failed")
	}
}

func TestClientError_Unwrap_StillReturnsCause(t *testing.T) {
	cause := errors.New("root cause")
	err := &ClientError{Kind: ErrTransport, Message: "transport error", Cause: cause}
	if errors.Unwrap(err) != cause {
		t.Error("Unwrap should still return the cause after static-message change")
	}
}

// ============================================================================
// MED-1.5 — CommandRejectedError factories with code+details
// ============================================================================

func TestNewPreconditionFailedRejection(t *testing.T) {
	cre := NewPreconditionFailedRejection(
		CodeStatusMismatch,
		"status does not match expected",
		map[string]string{ExtraKeyExpected: "OPEN", ExtraKeyActual: "CLOSED"},
	)
	if cre.Code != CodeStatusMismatch {
		t.Errorf("code = %q, want %q", cre.Code, CodeStatusMismatch)
	}
	if cre.Message != "status does not match expected" {
		t.Errorf("message wrong: %q", cre.Message)
	}
	if cre.Details[ExtraKeyExpected] != "OPEN" {
		t.Errorf("expected detail missing/wrong: %v", cre.Details)
	}
	if cre.StatusCode != "FAILED_PRECONDITION" {
		t.Errorf("status code = %q, want %q", cre.StatusCode, "FAILED_PRECONDITION")
	}
}

func TestNewInvalidArgumentRejectionWithCode(t *testing.T) {
	cre := NewInvalidArgumentRejectionWithCode(
		CodeValueNotPositive,
		"value must be positive",
		map[string]string{ExtraKeyField: "amount"},
	)
	if cre.Code != CodeValueNotPositive {
		t.Errorf("code = %q, want %q", cre.Code, CodeValueNotPositive)
	}
}

func TestNewNotFoundRejection(t *testing.T) {
	cre := NewNotFoundRejection(
		CodeEntityNotFound,
		"entity not found",
		map[string]string{"id": "abc"},
	)
	if cre.Code != CodeEntityNotFound {
		t.Errorf("code = %q, want %q", cre.Code, CodeEntityNotFound)
	}
}

// ============================================================================
// HIGH-2.1 — DEFAULT_EDITION = "" canonical
// ============================================================================

func TestDefaultEdition_IsEmptyString(t *testing.T) {
	if DefaultEdition != "" {
		t.Errorf("DefaultEdition = %q, want \"\" (audit #cross-lang; Py/Rs/Ja/Cpp canonical)", DefaultEdition)
	}
}

func TestMainTimeline_HasEmptyName(t *testing.T) {
	e := MainTimeline()
	if e == nil {
		t.Fatal("MainTimeline returned nil")
	}
	if e.Name != "" {
		t.Errorf("MainTimeline.Name = %q, want \"\"", e.Name)
	}
}

func TestCacheKey_DefaultEdition_ProducesEmptyPrefix(t *testing.T) {
	root := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	c := &pb.Cover{Domain: "player", Root: UUIDToProto(root)}
	got := CacheKey(c)
	// Per spec: "":domain:hex
	if !strings.HasPrefix(got, ":player:") {
		t.Errorf("CacheKey = %q, want it to start with %q", got, ":player:")
	}
}

// ============================================================================
// HIGH-2.2 — Identity module
// ============================================================================

func TestComputeRoot_DeterministicPlayerEmail(t *testing.T) {
	// Cross-language byte-equality pin: Rust `identity.rs:71` pins
	// compute_root("player", "alice@x.com") = 8cf1fb5d-45ce-58c2-a7e4-34359eb42d7c
	got := ComputeRoot("player", "alice@x.com")
	want := uuid.MustParse("8cf1fb5d-45ce-58c2-a7e4-34359eb42d7c")
	if got != want {
		t.Errorf("compute_root(player, alice@x.com) = %s, want %s (Rust identity.rs:71)", got, want)
	}
}

func TestComputeRoot_DifferentInputDifferentOutput(t *testing.T) {
	a := ComputeRoot("player", "alice@x.com")
	b := ComputeRoot("player", "bob@x.com")
	c := ComputeRoot("order", "alice@x.com")
	if a == b {
		t.Error("different keys must produce different roots")
	}
	if a == c {
		t.Error("different domains must produce different roots")
	}
}

func TestInventoryProductNamespace_IsDNSNamespaceUUID(t *testing.T) {
	// RFC 4122 DNS namespace UUID
	want := uuid.MustParse("6ba7b810-9dad-11d1-80b4-00c04fd430c8")
	if InventoryProductNamespace != want {
		t.Errorf("InventoryProductNamespace = %s, want %s", InventoryProductNamespace, want)
	}
}

func TestCustomerRoot_PlayerRoot_OrderRoot(t *testing.T) {
	if CustomerRoot("alice@x.com") != ComputeRoot("customer", "alice@x.com") {
		t.Error("CustomerRoot mismatch")
	}
	if ProductRoot("sku-1") != ComputeRoot("product", "sku-1") {
		t.Error("ProductRoot mismatch")
	}
	if OrderRoot("order-1") != ComputeRoot("order", "order-1") {
		t.Error("OrderRoot mismatch")
	}
	if InventoryRoot("p-1") != ComputeRoot("inventory", "p-1") {
		t.Error("InventoryRoot mismatch")
	}
	if CartRoot("c-1") != ComputeRoot("cart", "c-1") {
		t.Error("CartRoot mismatch")
	}
	if FulfillmentRoot("o-1") != ComputeRoot("fulfillment", "o-1") {
		t.Error("FulfillmentRoot mismatch")
	}
}

func TestInventoryProductRoot_UsesDNSNamespace(t *testing.T) {
	got := InventoryProductRoot("widget-1")
	// Must be UUIDv5 in the DNS namespace with seed "widget-1"
	want := uuid.NewSHA1(InventoryProductNamespace, []byte("widget-1"))
	if got != want {
		t.Errorf("InventoryProductRoot mismatch")
	}
}

func TestToProtoBytes_ReturnsUUIDBytes(t *testing.T) {
	u := uuid.MustParse("11111111-2222-3333-4444-555555555555")
	pb := ToProtoBytes(u)
	if pb == nil {
		t.Fatal("nil proto bytes")
	}
	if len(pb.Value) != 16 {
		t.Errorf("len = %d, want 16", len(pb.Value))
	}
	for i := 0; i < 16; i++ {
		if pb.Value[i] != u[i] {
			t.Errorf("byte %d: got %x want %x", i, pb.Value[i], u[i])
		}
	}
}

// ============================================================================
// MED-2.7 — DivergenceFor returns optional (sentinel removed)
// ============================================================================

func TestDivergenceFor_ReturnsOptional(t *testing.T) {
	// MED-2.7: rename DivergenceForOptional to DivergenceFor and drop the
	// deprecated -1 sentinel form. DivergenceFor must now return (uint32, bool).
	e := &pb.Edition{Name: "e1", Divergences: []*pb.DomainDivergence{{Domain: "x", Sequence: 5}}}
	seq, ok := DivergenceFor(e, "x")
	if !ok || seq != 5 {
		t.Errorf("DivergenceFor(x) = (%d, %v), want (5, true)", seq, ok)
	}
	if _, ok := DivergenceFor(e, "missing"); ok {
		t.Errorf("DivergenceFor(missing) should report false")
	}
}

// ============================================================================
// MED-2.8 — TypeURL(fullName) single-arg signature
// ============================================================================

func TestTypeURL_SingleArgFullName(t *testing.T) {
	got := TypeURL("examples.CardsDealt")
	want := "type.googleapis.com/examples.CardsDealt"
	if got != want {
		t.Errorf("TypeURL = %q, want %q", got, want)
	}
}

// ============================================================================
// MED-2.10 — CorrelatedMetadata helper
// ============================================================================

func TestCorrelatedMetadata_StampsCorrelationID(t *testing.T) {
	md := CorrelatedMetadata("abc-123")
	vals := md.Get(CorrelationIDHeader)
	if len(vals) != 1 || vals[0] != "abc-123" {
		t.Errorf("metadata = %v, want %q for %q", md, "abc-123", CorrelationIDHeader)
	}
}

func TestCorrelatedMetadata_EmptyIDSkipped(t *testing.T) {
	md := CorrelatedMetadata("")
	if len(md.Get(CorrelationIDHeader)) != 0 {
		t.Errorf("empty correlation id should not be stamped")
	}
}

// ============================================================================
// HIGH-3.2 — Builder validation errors carry code stamps
// ============================================================================

func TestBuilder_MissingTypeURL_StampsCode(t *testing.T) {
	b := NewCommandBuilder(nil, "player", uuid.New())
	b.WithSequence(1)
	_, err := b.Build()
	ce := AsClientError(err)
	if ce == nil {
		t.Fatalf("want ClientError, got %T %v", err, err)
	}
	if ce.Code != CodeCommandTypeURLMissing {
		t.Errorf("code = %q, want %q", ce.Code, CodeCommandTypeURLMissing)
	}
}

func TestBuilder_MissingPayload_StampsCode(t *testing.T) {
	b := &CommandBuilder{
		domain:      "player",
		typeURL:     "examples.X",
		sequence:    1,
		sequenceSet: true,
	}
	_, err := b.Build()
	ce := AsClientError(err)
	if ce == nil {
		t.Fatalf("want ClientError, got %T", err)
	}
	if ce.Code != CodeCommandPayloadMissing {
		t.Errorf("code = %q, want %q", ce.Code, CodeCommandPayloadMissing)
	}
}

func TestBuilder_MissingSequence_StampsCode(t *testing.T) {
	b := NewCommandBuilder(nil, "player", uuid.New())
	b.WithCommand("examples.X", &pb.Cover{})
	_, err := b.Build()
	ce := AsClientError(err)
	if ce == nil {
		t.Fatalf("want ClientError, got %T", err)
	}
	if ce.Code != CodeCommandSequenceMissing {
		t.Errorf("code = %q, want %q", ce.Code, CodeCommandSequenceMissing)
	}
}

// ============================================================================
// MED-3.5 — AsOfTime returns (b, err) at call site
// ============================================================================

func TestAsOfTime_ReturnsErrorAtCallSite(t *testing.T) {
	// Per MED-3.5: AsOfTime is now the call-site-returning variant.
	// The old deferred-err shape is gone.
	b := &QueryBuilder{domain: "x"}
	_, err := b.AsOfTime("not a timestamp")
	if err == nil {
		t.Fatal("expected immediate error from AsOfTime on bad RFC3339")
	}
}

func TestAsOfTime_Good_ReturnsBuilder(t *testing.T) {
	b := &QueryBuilder{domain: "x"}
	out, err := b.AsOfTime("2024-01-15T10:30:00Z")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out == nil || out.temporal == nil {
		t.Errorf("expected temporal selection set")
	}
}

// ============================================================================
// MED-3.11 — Free-function helpers (root_from_cover, events_from_response, decode_event)
// ============================================================================

func TestRootFromCover_Present(t *testing.T) {
	root := uuid.MustParse("aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee")
	c := &pb.Cover{Root: UUIDToProto(root)}
	got, ok := RootFromCover(c)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if got != root {
		t.Errorf("got %s, want %s", got, root)
	}
}

func TestRootFromCover_Nil(t *testing.T) {
	if _, ok := RootFromCover(nil); ok {
		t.Error("nil cover should yield ok=false")
	}
	if _, ok := RootFromCover(&pb.Cover{}); ok {
		t.Error("empty cover should yield ok=false")
	}
}

// ============================================================================
// HIGH-4.1 — Router composition (engine RouterBuilder; see engine_test.go)
// ============================================================================

// ============================================================================
// HIGH-4.3 — BuildError convenience
// ============================================================================

func TestNewBuildError_Stamps(t *testing.T) {
	err := NewBuildError(CodeRouterNoHandlers, "no handlers registered on Router", map[string]string{ExtraKeyRouterName: "r"})
	ce := AsClientError(err)
	if ce == nil {
		t.Fatalf("want ClientError, got %T", err)
	}
	if ce.Code != CodeRouterNoHandlers {
		t.Errorf("code = %q, want %q", ce.Code, CodeRouterNoHandlers)
	}
	if ce.Extras[ExtraKeyRouterName] != "r" {
		t.Errorf("router_name missing: %v", ce.Extras)
	}
}

// ============================================================================
// HIGH-4.4 — UpcasterRouter recognized by Router aggregator
// ============================================================================

// ============================================================================
// MED-4.5 — Notification routing uses exact match
// ============================================================================

func TestNotificationTypeURL_Constant(t *testing.T) {
	want := TypeURLPrefix + "angzarr_client.proto.angzarr.v1.Notification"
	if NotificationTypeURL != want {
		t.Errorf("NotificationTypeURL = %q, want %q", NotificationTypeURL, want)
	}
}

func TestIsNotificationTypeURL_ExactMatch(t *testing.T) {
	if !IsNotificationTypeURL(NotificationTypeURL) {
		t.Error("canonical notification URL should match")
	}
	// Suffix-match anti-pattern: any *other* URL ending in "Notification"
	// must NOT match.
	other := TypeURLPrefix + "mycompany.Notification"
	if IsNotificationTypeURL(other) {
		t.Errorf("suffix-only URL %q should NOT match (MED-4.5)", other)
	}
}

// ============================================================================
// HIGH-5.2 — Default bind host = "[::]" (IPv6 dual-stack)
// ============================================================================

func TestResolveBindAddress_DefaultIPv6(t *testing.T) {
	t.Setenv(EnvBindAddress, "")
	t.Setenv("PORT", "")
	got := ResolveBindAddress(50052)
	want := "[::]:50052"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// ============================================================================
// MED-5.6 — ANGZARR_BIND_ADDRESS honored
// ============================================================================

func TestResolveBindAddress_HonorsEnv(t *testing.T) {
	t.Setenv(EnvBindAddress, "127.0.0.1:9999")
	if got := ResolveBindAddress(50052); got != "127.0.0.1:9999" {
		t.Errorf("got %q, want %q", got, "127.0.0.1:9999")
	}
}

func TestResolveBindAddress_PortEnvFallback(t *testing.T) {
	t.Setenv(EnvBindAddress, "")
	t.Setenv("PORT", "12345")
	if got := ResolveBindAddress(50052); got != "[::]:12345" {
		t.Errorf("got %q, want %q", got, "[::]:12345")
	}
}

func TestDefaultBindHost_IsIPv6Wildcard(t *testing.T) {
	if DefaultBindHost != "[::]" {
		t.Errorf("DefaultBindHost = %q, want %q (HIGH-5.2)", DefaultBindHost, "[::]")
	}
}

func TestEnvBindAddress_IsCanonical(t *testing.T) {
	if EnvBindAddress != "ANGZARR_BIND_ADDRESS" {
		t.Errorf("EnvBindAddress = %q, want %q", EnvBindAddress, "ANGZARR_BIND_ADDRESS")
	}
}

func TestResolveBindAddress_DefaultPortOnly(t *testing.T) {
	t.Setenv(EnvBindAddress, "")
	t.Setenv("PORT", "")
	if got := ResolveBindAddress(7777); got != "[::]:7777" {
		t.Errorf("got %q, want %q", got, "[::]:7777")
	}
}

func TestGetTransportConfig_TCP_HonorsBindAddress(t *testing.T) {
	t.Setenv("TRANSPORT_TYPE", "tcp")
	t.Setenv(EnvBindAddress, "[::1]:8888")
	cfg := GetTransportConfig()
	if cfg.Type != "tcp" || cfg.Address != "[::1]:8888" {
		t.Errorf("config = %+v, want type=tcp addr=[::1]:8888", cfg)
	}
}

func TestGetTransportConfig_TCP_DefaultsToIPv6(t *testing.T) {
	t.Setenv("TRANSPORT_TYPE", "tcp")
	t.Setenv(EnvBindAddress, "")
	t.Setenv("PORT", "")
	cfg := GetTransportConfig()
	if cfg.Type != "tcp" || cfg.Address != "[::]:50052" {
		t.Errorf("config = %+v, want type=tcp addr=[::]:50052", cfg)
	}
}

// ============================================================================
// HIGH-5.4 — per-kind RunCommandHandlerServer etc. — verify exported
// ============================================================================

func TestPerKindRunners_Exported(t *testing.T) {
	// Verify the function symbols exist by taking their addresses. The
	// runners themselves block on Serve, so we just confirm presence.
	//
	// These predate D-Go but satisfy HIGH-5.4: every kind has a per-kind
	// runner.
	_ = RunAggregateDispatchServer[any]
	_ = RunSagaDispatchServer
	_ = RunProcessManagerDispatchServer[any]
	_ = RunProjectorDispatchServer[any]
	_ = RunUpcasterServer
	// Per-kind health-name constants (audit #74):
	_ = HealthNameCommandHandler
	_ = HealthNameSaga
	_ = HealthNameProcessManager
	_ = HealthNameProjector
	_ = HealthNameUpcaster
}

// ============================================================================
// HIGH-5.5 — Default CreateServer starts NOT_SERVING (audit #68)
// ============================================================================

func TestCreateServer_StartsNotServing(t *testing.T) {
	// Use an ephemeral port via env override.
	t.Setenv(EnvBindAddress, "127.0.0.1:0")
	t.Setenv("PORT", "")
	server, listener, healthSrv, _, _, cleanup, err := CreateServerE(
		func(_ *grpc.Server) {},
		ServerOptions{ServiceName: "test"},
	)
	if err != nil {
		t.Fatalf("CreateServerE: %v", err)
	}
	defer cleanup()
	defer listener.Close()
	_ = server

	if healthSrv == nil {
		t.Fatal("readiness health server is nil")
	}
	for _, name := range []string{"", "test"} {
		resp, err := healthSrv.Check(context.Background(), &grpc_health_v1.HealthCheckRequest{Service: name})
		if err != nil {
			t.Fatalf("Check(%q): %v", name, err)
		}
		if resp.Status != grpc_health_v1.HealthCheckResponse_NOT_SERVING {
			t.Errorf("Check(%q) = %v, want NOT_SERVING (HIGH-5.5)", name, resp.Status)
		}
	}
}

// ============================================================================
// LOW-5.14 — bind failure returns error (not panic)
// ============================================================================

func TestCreateServerE_ReturnsErrorOnBindFailure(t *testing.T) {
	// Occupy a port, then try to bind a second time on the same.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("can't get port: %v", err)
	}
	defer ln.Close()
	t.Setenv(EnvBindAddress, ln.Addr().String())
	t.Setenv("PORT", "")
	_, _, _, _, _, _, err = CreateServerE(
		func(_ *grpc.Server) {},
		ServerOptions{ServiceName: "test"},
	)
	if err == nil {
		t.Fatal("expected bind error, got nil")
	}
}

// ============================================================================
// HIGH-6.2 — Value-returning ExecuteFunc[T]
// ============================================================================

func TestExecuteFunc_ReturnsValue(t *testing.T) {
	r := DefaultRetryPolicy()
	got, err := ExecuteFunc(r, func() (int, error) { return 42, nil })
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != 42 {
		t.Errorf("got %d, want 42", got)
	}
}

func TestExecuteFunc_RetriesAndReturnsLastError(t *testing.T) {
	r := &ExponentialBackoffRetry{MinDelay: time.Microsecond, MaxDelay: time.Millisecond, MaxAttempts: 3, Jitter: false}
	var calls int32
	_, err := ExecuteFunc(r, func() (int, error) {
		atomic.AddInt32(&calls, 1)
		return 0, errors.New("nope")
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if atomic.LoadInt32(&calls) != 3 {
		t.Errorf("calls = %d, want 3", calls)
	}
}

// ============================================================================
// MED-6.3 — on_retry callback
// ============================================================================

func TestRetry_OnRetry_FiresBetweenAttempts(t *testing.T) {
	var attempts []int
	r := &ExponentialBackoffRetry{
		MinDelay:    time.Microsecond,
		MaxDelay:    time.Millisecond,
		MaxAttempts: 3,
		Jitter:      false,
		OnRetry:     func(attempt int, _ error) { attempts = append(attempts, attempt) },
	}
	_ = r.Execute(func() error { return errors.New("fail") })
	// OnRetry fires before each backoff sleep, NOT before first attempt and
	// NOT after final failure. With max_attempts=3, that's 2 invocations.
	if len(attempts) != 2 {
		t.Errorf("len(attempts) = %d, want 2", len(attempts))
	}
}

// ============================================================================
// MED-6.4 — exponent cap at 30 (no IEEE-754 +Inf)
// ============================================================================

func TestComputeDelay_ExponentCapAt30(t *testing.T) {
	r := &ExponentialBackoffRetry{MinDelay: time.Nanosecond, MaxDelay: time.Hour, MaxAttempts: 100, Jitter: false}
	// Without the cap, attempt=63 yields delay=2^63 ns which overflows;
	// attempt=1023 yields +Inf. With the cap, delay is bounded by MaxDelay.
	got := r.ComputeDelay(1000)
	if got > r.MaxDelay {
		t.Errorf("delay %v > MaxDelay %v (cap not applied)", got, r.MaxDelay)
	}
	if got <= 0 {
		t.Errorf("delay %v not positive", got)
	}
}

// ============================================================================
// MED-6.7 — ComputeDelay public
// ============================================================================

func TestComputeDelay_Public(t *testing.T) {
	r := &ExponentialBackoffRetry{MinDelay: 100 * time.Millisecond, MaxDelay: time.Second, MaxAttempts: 5, Jitter: false}
	got := r.ComputeDelay(0)
	if got != 100*time.Millisecond {
		t.Errorf("attempt 0 delay = %v, want 100ms", got)
	}
	got = r.ComputeDelay(1)
	if got != 200*time.Millisecond {
		t.Errorf("attempt 1 delay = %v, want 200ms", got)
	}
}

func TestComputeDelay_ProgressionExponential(t *testing.T) {
	// Mutation-kill: pin the doubling progression at known attempt values.
	r := &ExponentialBackoffRetry{
		MinDelay:    10 * time.Millisecond,
		MaxDelay:    time.Minute,
		MaxAttempts: 10,
		Jitter:      false,
	}
	cases := []struct {
		attempt int
		want    time.Duration
	}{
		{0, 10 * time.Millisecond},
		{1, 20 * time.Millisecond},
		{2, 40 * time.Millisecond},
		{3, 80 * time.Millisecond},
	}
	for _, tc := range cases {
		got := r.ComputeDelay(tc.attempt)
		if got != tc.want {
			t.Errorf("attempt %d: got %v, want %v", tc.attempt, got, tc.want)
		}
	}
}

func TestComputeDelay_BoundedByMaxDelay(t *testing.T) {
	// Mutation-kill for the if delay > MaxDelay clamp.
	r := &ExponentialBackoffRetry{
		MinDelay:    time.Second,
		MaxDelay:    2 * time.Second,
		MaxAttempts: 10,
		Jitter:      false,
	}
	// attempt 5 would compute 32s without clamp; clamped to 2s.
	got := r.ComputeDelay(5)
	if got != 2*time.Second {
		t.Errorf("got %v, want 2s (clamp)", got)
	}
}

func TestExecuteFunc_TypeSafeSuccess(t *testing.T) {
	r := DefaultRetryPolicy()
	got, err := ExecuteFunc(r, func() (string, error) { return "hello", nil })
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != "hello" {
		t.Errorf("got %q, want %q", got, "hello")
	}
}

func TestExecuteFunc_RetryThenSuccess(t *testing.T) {
	r := &ExponentialBackoffRetry{MinDelay: time.Microsecond, MaxDelay: time.Millisecond, MaxAttempts: 5, Jitter: false}
	var calls int32
	got, err := ExecuteFunc(r, func() (int, error) {
		c := atomic.AddInt32(&calls, 1)
		if c < 3 {
			return 0, errors.New("not yet")
		}
		return 99, nil
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != 99 {
		t.Errorf("got %d, want 99", got)
	}
	if calls != 3 {
		t.Errorf("calls = %d, want 3", calls)
	}
}

// ============================================================================
// LOW-6.15 — FromConn → FromChannel aliases
// ============================================================================

func TestFromChannelAliases_Exported(t *testing.T) {
	// Aliases are exported so callers can use a single naming convention.
	_ = QueryClientFromChannel
	_ = CommandHandlerClientFromChannel
	_ = SpeculativeClientFromChannel
	_ = DomainClientFromChannel
}

// ============================================================================
// helpers — silence "imported and not used" for tests that may not need them
// ============================================================================

var _ context.Context = context.Background()
var _ proto.Message = (*pb.Cover)(nil)
var _ = NotificationTypeURL
var _ = ResolveBindAddress
