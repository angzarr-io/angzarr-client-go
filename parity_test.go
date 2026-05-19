package angzarr

// Cross-language parity tests porting Python/Rust audit findings into Go.
//
// References:
//   - Python source of truth at examples-python/angzarr-client-python
//   - Rust parity reference at examples-rust/angzarr-client-rust
//
// Each test cites the audit finding it pins.

import (
	"strings"
	"testing"

	pb "github.com/benjaminabbitt/angzarr/client/go/proto/angzarr_client/proto/angzarr"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

// ----------------------------------------------------------------------------
// Audit #40: loud-fail on bad ANGZARR_MODE / ANGZARR_CH_PORT
// ----------------------------------------------------------------------------
//
// Python `client.py::resolve_ch_endpoint` and Rust `transport.rs::TransportMode::from_env`
// both error on operator typos like ANGZARR_MODE=Distrib or ANGZARR_CH_PORT=1310<space>.
// Go currently silently falls back to defaults — this loses the typo at startup.

func TestResolveCHEndpointError_UnrecognizedMode(t *testing.T) {
	t.Setenv(EnvMode, "Distrib") // operator typo
	_, err := ResolveCHEndpointE("player", "")
	if err == nil {
		t.Fatal("expected error on unrecognized ANGZARR_MODE, got nil")
	}
	ce := AsClientError(err)
	if ce == nil {
		t.Fatalf("expected ClientError, got %T: %v", err, err)
	}
	if ce.Code != CodeInvalidTransportMode {
		t.Errorf("expected code %q, got %q", CodeInvalidTransportMode, ce.Code)
	}
}

func TestResolveCHEndpointError_BadPort(t *testing.T) {
	t.Setenv(EnvCHPort, "1310 ") // trailing space typo
	_, err := ResolveCHEndpointE("player", TransportDistributed)
	if err == nil {
		t.Fatal("expected error on non-numeric ANGZARR_CH_PORT")
	}
	ce := AsClientError(err)
	if ce == nil || ce.Code != CodeInvalidPort {
		t.Fatalf("expected ClientError code %q, got %v", CodeInvalidPort, err)
	}
}

func TestResolveCHEndpointE_DefaultsWhenUnset(t *testing.T) {
	// Unset env vars must fall through to defaults — only SET-but-bad errors.
	t.Setenv(EnvMode, "")
	t.Setenv(EnvCHPort, "")
	t.Setenv(EnvNamespace, "")
	ep, err := ResolveCHEndpointE("player", TransportDistributed)
	if err != nil {
		t.Fatalf("unexpected error with unset env: %v", err)
	}
	if ep != "ch-player.angzarr.svc:1310" {
		t.Errorf("default endpoint mismatch: got %q", ep)
	}
}

func TestResolveCHEndpointE_StandaloneOK(t *testing.T) {
	t.Setenv(EnvMode, "")
	t.Setenv(EnvUDSBase, "")
	ep, err := ResolveCHEndpointE("player", TransportStandalone)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ep != "/tmp/angzarr/ch-player.sock" {
		t.Errorf("standalone endpoint mismatch: got %q", ep)
	}
}

// ----------------------------------------------------------------------------
// Audit #39: lenient UDS prefix detection
// ----------------------------------------------------------------------------
//
// Rust commit 7d54510. The endpoint formatter must recognize three prefix
// shapes for UDS paths:
//   - absolute path "/abs/path"    → unix:///abs/path (preserves authority sep)
//   - relative path "./rel/path"   → unix:./rel/path (no double-slash)
//   - already-prefixed "unix:..."  → passthrough unchanged

func TestFormatEndpoint_AbsolutePath(t *testing.T) {
	got := formatEndpoint("/tmp/angzarr/ch-player.sock")
	want := "unix:///tmp/angzarr/ch-player.sock"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestFormatEndpoint_RelativePath(t *testing.T) {
	got := formatEndpoint("./sockets/player.sock")
	want := "unix:./sockets/player.sock"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestFormatEndpoint_UnixPrefixed(t *testing.T) {
	got := formatEndpoint("unix:///tmp/x.sock")
	if got != "unix:///tmp/x.sock" {
		t.Errorf("passthrough failed: got %q", got)
	}
}

func TestFormatEndpoint_TCP(t *testing.T) {
	got := formatEndpoint("ch-player.angzarr.svc:1310")
	if got != "ch-player.angzarr.svc:1310" {
		t.Errorf("TCP passthrough failed: got %q", got)
	}
}

// ----------------------------------------------------------------------------
// Audit #49: DivergenceFor returns Optional (uint32, bool)
// ----------------------------------------------------------------------------
//
// Python `helpers.divergence_for` returns Optional[int]; Rust returns Option<u32>.
// Go currently returns int64 with -1 sentinel — this loses the missing/present
// distinction when the actual sequence happens to be 0 vs unset.

func TestDivergenceForOptional_Found(t *testing.T) {
	edition := &pb.Edition{
		Divergences: []*pb.DomainDivergence{
			{Domain: "orders", Sequence: 10},
		},
	}
	seq, ok := DivergenceForOptional(edition, "orders")
	if !ok {
		t.Fatal("expected ok=true for present domain")
	}
	if seq != 10 {
		t.Errorf("got %d, want 10", seq)
	}
}

func TestDivergenceForOptional_Missing(t *testing.T) {
	edition := &pb.Edition{
		Divergences: []*pb.DomainDivergence{
			{Domain: "orders", Sequence: 10},
		},
	}
	_, ok := DivergenceForOptional(edition, "shipping")
	if ok {
		t.Error("expected ok=false for missing domain")
	}
}

func TestDivergenceForOptional_NilEdition(t *testing.T) {
	_, ok := DivergenceForOptional(nil, "orders")
	if ok {
		t.Error("expected ok=false for nil edition")
	}
}

func TestDivergenceForOptional_ZeroSequencePresent(t *testing.T) {
	// Zero is a valid divergence sequence; ok must distinguish it from "missing".
	edition := &pb.Edition{
		Divergences: []*pb.DomainDivergence{
			{Domain: "orders", Sequence: 0},
		},
	}
	seq, ok := DivergenceForOptional(edition, "orders")
	if !ok {
		t.Fatal("expected ok=true for zero-sequence divergence")
	}
	if seq != 0 {
		t.Errorf("got %d, want 0", seq)
	}
}

// ----------------------------------------------------------------------------
// Audit #20: CommandNew auto-generates UUID v4 root
// ----------------------------------------------------------------------------
//
// Python `client.command_new(domain)` generates `uuid4()` and hands it to the
// CommandBuilder ctor. Rust `command_new` does the same. Go currently leaves
// root nil, which means build() produces a Cover without a root — violating
// the cross-language convention that aggregate roots are always client-assigned.

func TestCommandNew_AutoGeneratesUUIDv4Root(t *testing.T) {
	client := &CommandHandlerClient{}
	msg := wrapperspb.String("hello")
	book, err := client.CommandNew("orders").
		WithSequence(0).
		WithCommand("type.googleapis.com/test.Cmd", msg).
		Build()
	if err != nil {
		t.Fatalf("build should succeed — command_new auto-fills root: %v", err)
	}
	if book.Cover == nil || book.Cover.Root == nil {
		t.Fatal("cover.root must be auto-generated")
	}
	if len(book.Cover.Root.Value) != 16 {
		t.Fatalf("UUID v4 must be 16 bytes, got %d", len(book.Cover.Root.Value))
	}
	id, err := uuid.FromBytes(book.Cover.Root.Value)
	if err != nil {
		t.Fatalf("could not parse UUID: %v", err)
	}
	if id.Version() != 4 {
		t.Errorf("expected version 4 UUID, got %d", id.Version())
	}
}

func TestCommandNew_EachCallYieldsDistinctRoot(t *testing.T) {
	client := &CommandHandlerClient{}
	msg := wrapperspb.String("hello")
	a, err := client.CommandNew("orders").
		WithSequence(0).
		WithCommand("type.googleapis.com/test.Cmd", msg).
		Build()
	if err != nil {
		t.Fatalf("build a: %v", err)
	}
	b, err := client.CommandNew("orders").
		WithSequence(0).
		WithCommand("type.googleapis.com/test.Cmd", msg).
		Build()
	if err != nil {
		t.Fatalf("build b: %v", err)
	}
	if uuid.UUID(a.Cover.Root.Value).String() == uuid.UUID(b.Cover.Root.Value).String() {
		t.Error("two CommandNew calls must yield distinct UUIDs")
	}
}

// ----------------------------------------------------------------------------
// Audit #34: InvalidTimestamp raised synchronously (no deferred error)
// ----------------------------------------------------------------------------
//
// Python `QueryBuilder.as_of_time(rfc3339)` raises InvalidTimestampError
// at the call site rather than storing it on the builder for later. This
// avoids the "stale error survives a subsequent setter" bug where
// `qb.as_of_time("bad").as_of_sequence(5).build()` would surface the
// dead "bad" error from the as_of_time step.
//
// Go currently stores b.err and surfaces only at Build() — same bug.
// Fix: AsOfTimeE returns an error AT THE CALL SITE.

func TestAsOfTimeE_RaisesSynchronouslyOnBadInput(t *testing.T) {
	b := &QueryBuilder{}
	_, err := b.AsOfTimeE("not a timestamp")
	if err == nil {
		t.Error("expected synchronous error from AsOfTimeE")
	}
}

func TestAsOfTimeE_SuccessReturnsBuilder(t *testing.T) {
	b := &QueryBuilder{}
	r, err := b.AsOfTimeE("2024-01-15T10:30:00Z")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r != b {
		t.Error("expected same builder returned for chaining")
	}
	if b.temporal == nil {
		t.Fatal("expected temporal to be set")
	}
}

// AsOfTimeE on bad input leaves no sticky state — a subsequent successful
// setter must produce a clean Build() with the new selection. This is
// the regression Python's `as_of_time` synchronous-raise fix (audit #34)
// closes: deferred errors survived last-wins overrides.
func TestAsOfTimeE_NoStickyErrorAfterRecover(t *testing.T) {
	b := &QueryBuilder{domain: "orders"}
	_, _ = b.AsOfTimeE("not a timestamp")
	// Caller handles the error and proceeds with a different selection.
	b.AsOfSequence(5)
	q, err := b.Build()
	if err != nil {
		t.Fatalf("build must succeed after recovery; got %v", err)
	}
	if q.GetTemporal().GetAsOfSequence() != 5 {
		t.Errorf("expected as_of_sequence=5, got %v", q.GetTemporal())
	}
}

// ----------------------------------------------------------------------------
// Audit #54: domain() falls back when cover.domain is empty
// ----------------------------------------------------------------------------
//
// Rust `proto_ext::cover.rs::domain()` returns UNKNOWN_DOMAIN for both
// cover-missing AND cover-with-empty-domain. Go's `Domain()` already does
// this (helpers.go:46-52). Pin it to prevent regression.

func TestDomain_FallsBackWhenCoverPresentButEmpty(t *testing.T) {
	book := &pb.EventBook{Cover: &pb.Cover{Domain: ""}}
	if got := Domain(book); got != UnknownDomain {
		t.Errorf("expected %q for empty domain, got %q", UnknownDomain, got)
	}
}

// ----------------------------------------------------------------------------
// Audit #48: BytesToUUIDText emits dashed 8-4-4-4-12 for 16 bytes
// ----------------------------------------------------------------------------
//
// Rust `to_uuid_text()` and Python `bytes_to_uuid_text()` both emit the
// dashed canonical form for 16-byte input; non-16-byte input falls back
// to dashless hex. Go has `BytesToUUIDText` — pin its contract.

func TestBytesToUUIDText_16Bytes_Dashed(t *testing.T) {
	b := []byte{0x6b, 0xa7, 0xb8, 0x10, 0x9d, 0xad, 0x11, 0xd1,
		0x80, 0xb4, 0x00, 0xc0, 0x4f, 0xd4, 0x30, 0xc8}
	got := BytesToUUIDText(b)
	want := "6ba7b810-9dad-11d1-80b4-00c04fd430c8"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestBytesToUUIDText_NonCanonical_Hex(t *testing.T) {
	got := BytesToUUIDText([]byte{0xAB, 0xCD, 0xEF})
	if got != "abcdef" {
		t.Errorf("got %q, want %q", got, "abcdef")
	}
}

// ----------------------------------------------------------------------------
// Audit #55: idempotency_key returns Optional (no panic on missing source)
// ----------------------------------------------------------------------------
//
// Python `helpers.idempotency_key(deferred)` returns Optional[str]; Rust
// `AngzarrDeferredSequenceExt::idempotency_key()` returns Result<String, &str>.
// Both treat "no source cover" as a recoverable missing-key signal — not a panic.

func TestIdempotencyKey_NilDeferred(t *testing.T) {
	_, ok := IdempotencyKey(nil)
	if ok {
		t.Error("expected ok=false for nil deferred sequence")
	}
}

func TestIdempotencyKey_MissingSource(t *testing.T) {
	deferred := &pb.AngzarrDeferredSequence{}
	_, ok := IdempotencyKey(deferred)
	if ok {
		t.Error("expected ok=false for deferred without source")
	}
}

func TestIdempotencyKey_Composite(t *testing.T) {
	rootBytes := []byte{0xDE, 0xAD, 0xBE, 0xEF, 0xDE, 0xAD, 0xBE, 0xEF,
		0xDE, 0xAD, 0xBE, 0xEF, 0xDE, 0xAD, 0xBE, 0xEF}
	deferred := &pb.AngzarrDeferredSequence{
		Source: &pb.Cover{
			Domain:  "order",
			Root:    &pb.UUID{Value: rootBytes},
			Edition: &pb.Edition{Name: "angzarr"},
		},
		SourceSeq: 7,
	}
	key, ok := IdempotencyKey(deferred)
	if !ok {
		t.Fatal("expected ok=true with present source")
	}
	// Format: {edition}:{domain}:{root_hex}:{source_seq}
	if !strings.Contains(key, "order") || !strings.HasSuffix(key, ":7") {
		t.Errorf("unexpected idempotency key format: %q", key)
	}
	if !strings.Contains(key, "angzarr") {
		t.Errorf("expected edition prefix in key, got %q", key)
	}
}

// ----------------------------------------------------------------------------
// Audit #31: Destinations.deferred_header helper (static)
// ----------------------------------------------------------------------------
//
// Both Python (`destinations.py::deferred_header`) and Rust
// (`router::state.rs::deferred_header`) expose a static helper that builds
// a PageHeader carrying an AngzarrDeferredSequence. Saga-produced commands
// use this so the framework can dedupe on (source.root, source_seq, target.root).

func TestDeferredHeader_CarriesSourceAndSeq(t *testing.T) {
	rootBytes := []byte{0xDE, 0xAD, 0xBE, 0xEF, 0xDE, 0xAD, 0xBE, 0xEF,
		0xDE, 0xAD, 0xBE, 0xEF, 0xDE, 0xAD, 0xBE, 0xEF}
	source := &pb.Cover{
		Domain: "order",
		Root:   &pb.UUID{Value: rootBytes},
	}
	header := DeferredHeader(source, 9)
	if header == nil {
		t.Fatal("expected non-nil header")
	}
	deferred := header.GetAngzarrDeferred()
	if deferred == nil {
		t.Fatal("expected AngzarrDeferred sequence")
	}
	if deferred.SourceSeq != 9 {
		t.Errorf("got source_seq %d, want 9", deferred.SourceSeq)
	}
	if deferred.Source == nil || deferred.Source.Domain != "order" {
		t.Errorf("source not carried through: %v", deferred.Source)
	}
}

// ----------------------------------------------------------------------------
// destination_map helper (cross-language alias)
// ----------------------------------------------------------------------------
//
// Python `helpers.destination_map(destinations)` and Rust
// `proto_ext::destination_map(destinations)` build a map[root_hex]->EventBook
// from a slice of EventBooks. Rootless books are skipped.

func TestDestinationMap_KeysByRootHexAndSkipsRootless(t *testing.T) {
	rootBytes := []byte{0xAB, 0xAB, 0xAB, 0xAB, 0xAB, 0xAB, 0xAB, 0xAB,
		0xAB, 0xAB, 0xAB, 0xAB, 0xAB, 0xAB, 0xAB, 0xAB}
	withRoot := &pb.EventBook{
		Cover: &pb.Cover{Root: &pb.UUID{Value: rootBytes}},
	}
	withoutRoot := &pb.EventBook{Cover: &pb.Cover{}}
	m := DestinationMap([]*pb.EventBook{withRoot, withoutRoot})
	if len(m) != 1 {
		t.Errorf("expected 1 entry (rootless skipped), got %d", len(m))
	}
	key := RootIDHex(withRoot)
	if m[key] != withRoot {
		t.Error("withRoot must be keyed by its hex root")
	}
}

// ----------------------------------------------------------------------------
// Cache key parity: Rust convention is "{edition}:{domain}:{root}"
// ----------------------------------------------------------------------------
//
// Audit #75-#86 series pinned the cache key shape across languages so a cache
// populated by one client's Cover is hit by another's. Go currently builds
// "{domain}:{root}" — missing the edition prefix.

func TestCacheKey_IncludesEdition(t *testing.T) {
	rootBytes := []byte{0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77, 0x88,
		0x99, 0xAA, 0xBB, 0xCC, 0xDD, 0xEE, 0xFF, 0x00}
	cover := &pb.Cover{
		Domain:  "order",
		Root:    &pb.UUID{Value: rootBytes},
		Edition: &pb.Edition{Name: "branch"},
	}
	key := CacheKey(cover)
	// Rust contract: "{edition}:{domain}:{root_hex}"
	want := "branch:order:" + strings.ToLower("112233445566778899aabbccddeeff00")
	if key != want {
		t.Errorf("got %q, want %q", key, want)
	}
}

// ----------------------------------------------------------------------------
// Audit #64: StampCommand returns structured MISSING_DESTINATION_SEQUENCE
// ----------------------------------------------------------------------------
//
// Python `Destinations.stamp_command` raises InvalidArgumentError with
// code=MISSING_DESTINATION_SEQUENCE; Rust returns ClientError::invalid_argument
// with the same code. Cucumber assertions across languages rely on the code.

func TestStampCommand_MissingDomain_ErrorCarriesCode(t *testing.T) {
	d := NewDestinations(map[string]uint32{"inventory": 5})
	cmd := &pb.CommandBook{}
	err := d.StampCommand(cmd, "fulfillment")
	if err == nil {
		t.Fatal("expected error for unknown domain")
	}
	ce := AsClientError(err)
	if ce == nil {
		t.Fatalf("expected ClientError, got %T: %v", err, err)
	}
	if ce.Code != CodeMissingDestinationSequence {
		t.Errorf("got code %q, want %q", ce.Code, CodeMissingDestinationSequence)
	}
	if ce.Extras[ExtraKeyDomain] != "fulfillment" {
		t.Errorf("expected extras.domain = fulfillment, got %v", ce.Extras)
	}
}

func TestCacheKey_DefaultEditionWhenUnset(t *testing.T) {
	rootBytes := []byte{0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77, 0x88,
		0x99, 0xAA, 0xBB, 0xCC, 0xDD, 0xEE, 0xFF, 0x00}
	cover := &pb.Cover{
		Domain: "order",
		Root:   &pb.UUID{Value: rootBytes},
	}
	key := CacheKey(cover)
	// Rust uses DEFAULT_EDITION ("" in Python's helpers.py, "angzarr" in Go).
	// Go's DefaultEdition constant is "angzarr"; keep that and require it in the prefix.
	wantPrefix := DefaultEdition + ":order:"
	if !strings.HasPrefix(key, wantPrefix) {
		t.Errorf("got %q, want prefix %q", key, wantPrefix)
	}
}
