package angzarr

import (
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	pb "github.com/benjaminabbitt/angzarr/client/go/proto/angzarr_client/proto/angzarr"
	"github.com/google/uuid"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Constants matching Rust proto_ext::constants and Python helpers.
//
// HIGH-2.1: DEFAULT_EDITION is the empty string across all six client
// languages so cache_key / cover.edition stamping produces byte-equal
// outputs everywhere.
const (
	UnknownDomain          = "unknown"
	WildcardDomain         = "*"
	DefaultEdition         = ""
	MetaAngzarrDomain      = "_angzarr"
	ProjectionDomainPrefix = "_projection"
	ProjectionTypeURL      = "angzarr_client.proto.angzarr.Projection"
	CorrelationIDHeader    = "x-correlation-id"
	TypeURLPrefix          = "type.googleapis.com/"
)

// NotificationTypeURL is the canonical wire type_url for cross-domain
// rejection notifications. Routers MUST exact-match on this URL when
// deciding to invoke the compensation path (MED-4.5). Suffix-match risks
// misrouting any user-defined proto type whose name ends in "Notification".
const NotificationTypeURL = TypeURLPrefix + "angzarr_client.proto.angzarr.Notification"

// IsNotificationTypeURL returns true iff the type_url exactly equals the
// canonical NotificationTypeURL. Exact match per MED-4.5 / audit #25.
func IsNotificationTypeURL(typeURL string) bool {
	return typeURL == NotificationTypeURL
}

// Cover accessors - work with any type that has a Cover field

// CoverOf extracts the Cover from various proto types.
func CoverOf(v interface{}) *pb.Cover {
	switch t := v.(type) {
	case *pb.EventBook:
		return t.GetCover()
	case *pb.CommandBook:
		return t.GetCover()
	case *pb.Query:
		return t.GetCover()
	case *pb.Cover:
		return t
	default:
		return nil
	}
}

// Domain returns the domain from a Cover-bearing type, or UnknownDomain if missing.
func Domain(v interface{}) string {
	c := CoverOf(v)
	if c == nil || c.Domain == "" {
		return UnknownDomain
	}
	return c.Domain
}

// CorrelationID returns the correlation_id from a Cover-bearing type, or empty if missing.
func CorrelationID(v interface{}) string {
	c := CoverOf(v)
	if c == nil {
		return ""
	}
	return c.CorrelationId
}

// HasCorrelationID returns true if the correlation_id is present and non-empty.
func HasCorrelationID(v interface{}) bool {
	return CorrelationID(v) != ""
}

// RootUUID extracts the root UUID from a Cover-bearing type.
func RootUUID(v interface{}) (uuid.UUID, bool) {
	c := CoverOf(v)
	if c == nil || c.Root == nil {
		return uuid.UUID{}, false
	}
	u, err := uuid.FromBytes(c.Root.Value)
	if err != nil {
		return uuid.UUID{}, false
	}
	return u, true
}

// RootIDHex returns the root UUID as a hex string, or empty if missing.
func RootIDHex(v interface{}) string {
	c := CoverOf(v)
	if c == nil || c.Root == nil {
		return ""
	}
	return hex.EncodeToString(c.Root.Value)
}

// Edition returns the edition name from a Cover-bearing type, or nil if not set.
func Edition(v interface{}) *string {
	c := CoverOf(v)
	if c == nil || c.Edition == nil || c.Edition.Name == "" {
		return nil
	}
	return &c.Edition.Name
}

// RoutingKey computes the bus routing key for a Cover-bearing type.
func RoutingKey(v interface{}) string {
	return Domain(v)
}

// CacheKey generates a cache key for a Cover-bearing entity.
//
// Audit #75-#86 series: cross-language convention is "{edition}:{domain}:{root_hex}".
// Edition prefix prevents collision between aggregates in different timelines
// (caches populated by one client are then hit correctly by another).
// Mirrors Rust `CoverExt::cache_key` and Python `helpers.cache_key`.
//
// HIGH-2.1: default edition is the empty string; main-timeline keys take
// the form ":{domain}:{root_hex}".
func CacheKey(v interface{}) string {
	c := CoverOf(v)
	edition := ""
	if c != nil && c.Edition != nil {
		edition = c.Edition.Name
	}
	return fmt.Sprintf("%s:%s:%s", edition, Domain(v), RootIDHex(v))
}

// UUID conversion

// UUIDToProto converts a uuid.UUID to a proto UUID.
func UUIDToProto(u uuid.UUID) *pb.UUID {
	return &pb.UUID{Value: u[:]}
}

// ProtoToUUID converts a proto UUID to uuid.UUID.
func ProtoToUUID(u *pb.UUID) (uuid.UUID, error) {
	if u == nil {
		return uuid.UUID{}, fmt.Errorf("nil UUID")
	}
	return uuid.FromBytes(u.Value)
}

// BytesToUUIDText converts bytes to standard UUID text format.
// If bytes are exactly 16 bytes, formats as UUID (8-4-4-4-12).
// Otherwise returns hex encoding of the bytes.
func BytesToUUIDText(b []byte) string {
	if len(b) == 16 {
		u, err := uuid.FromBytes(b)
		if err == nil {
			return u.String()
		}
	}
	return hex.EncodeToString(b)
}

// ProtoUUIDToText converts a proto UUID to text format.
func ProtoUUIDToText(u *pb.UUID) string {
	if u == nil {
		return ""
	}
	return BytesToUUIDText(u.Value)
}

// RootIDText returns the root UUID as standard text format (8-4-4-4-12), or empty if missing.
func RootIDText(v interface{}) string {
	c := CoverOf(v)
	if c == nil || c.Root == nil {
		return ""
	}
	return BytesToUUIDText(c.Root.Value)
}

// Edition helpers

// MainTimeline returns an Edition representing the main timeline.
func MainTimeline() *pb.Edition {
	return &pb.Edition{Name: DefaultEdition}
}

// ImplicitEdition creates an edition with the given name but no divergences.
func ImplicitEdition(name string) *pb.Edition {
	return &pb.Edition{Name: name}
}

// ExplicitEdition creates an edition with divergence points.
func ExplicitEdition(name string, divergences []*pb.DomainDivergence) *pb.Edition {
	return &pb.Edition{Name: name, Divergences: divergences}
}

// IsMainTimeline checks if an edition represents the main timeline.
//
// HIGH-2.1: the main timeline is the empty-named edition (DefaultEdition
// is "").
func IsMainTimeline(e *pb.Edition) bool {
	return e == nil || e.Name == ""
}

// DivergenceFor returns (sequence, true) when the domain has an explicit
// divergence in the edition, or (0, false) when missing.
//
// Audit #49 / spec MED-2.7: mirrors Python `helpers.divergence_for(...) ->
// int | None` and Rust `EditionExt::divergence_for(...) -> Option<u32>`. The
// bool flag distinguishes "absent" from "present-with-sequence-0", which the
// previous -1 sentinel form could not.
func DivergenceFor(e *pb.Edition, domain string) (uint32, bool) {
	if e == nil {
		return 0, false
	}
	for _, d := range e.Divergences {
		if d.Domain == domain {
			return d.Sequence, true
		}
	}
	return 0, false
}

// DivergenceForOptional is a backward-compatible alias for DivergenceFor.
//
// Deprecated: use DivergenceFor — the optional form is now the sole shape.
func DivergenceForOptional(e *pb.Edition, domain string) (uint32, bool) {
	return DivergenceFor(e, domain)
}

// EventBook helpers

// NextSequence returns the next sequence number from an EventBook.
//
// # Why Use book.NextSequence Instead of Counting Events?
//
// The framework precomputes NextSequence when loading the EventBook because:
//  1. **Snapshots**: With snapshots, the EventBook may contain only post-snapshot
//     events. Counting events would give the wrong sequence.
//  2. **Consistency**: The framework knows the true last sequence from storage.
//  3. **Performance**: Avoids iterating through events to find max sequence.
//
// Command handlers MUST use this value when setting event sequences. Using
// len(book.Pages) would produce incorrect sequences when snapshots are involved.
func NextSequence(book *pb.EventBook) uint32 {
	if book == nil {
		return 0
	}
	return book.NextSequence
}

// EventPages returns the event pages from an EventBook, or empty slice if nil.
func EventPages(book *pb.EventBook) []*pb.EventPage {
	if book == nil {
		return nil
	}
	return book.Pages
}

// NewEventBook creates an EventBook with a single event page.
// This is the common pattern for command handlers returning a single event.
func NewEventBook(cover *pb.Cover, seq uint32, event *anypb.Any) *pb.EventBook {
	return &pb.EventBook{
		Cover: cover,
		Pages: []*pb.EventPage{
			{
				Header:    &pb.PageHeader{SequenceType: &pb.PageHeader_Sequence{Sequence: seq}},
				Payload:   &pb.EventPage_Event{Event: event},
				CreatedAt: timestamppb.Now(),
			},
		},
	}
}

// NewEventBookMulti creates an EventBook with multiple event pages.
// Useful for handlers that emit multiple events (e.g., AwardPot + HandComplete).
func NewEventBookMulti(cover *pb.Cover, startSeq uint32, events ...*anypb.Any) *pb.EventBook {
	pages := make([]*pb.EventPage, len(events))
	now := timestamppb.Now()
	for i, event := range events {
		pages[i] = &pb.EventPage{
			Header:    &pb.PageHeader{SequenceType: &pb.PageHeader_Sequence{Sequence: startSeq + uint32(i)}},
			Payload:   &pb.EventPage_Event{Event: event},
			CreatedAt: now,
		}
	}
	return &pb.EventBook{
		Cover: cover,
		Pages: pages,
	}
}

// CommandBook helpers

// CommandPages returns the command pages from a CommandBook, or empty slice if nil.
func CommandPages(book *pb.CommandBook) []*pb.CommandPage {
	if book == nil {
		return nil
	}
	return book.Pages
}

// CommandResponse helpers

// EventsFromResponse extracts the event pages from a CommandResponse.
func EventsFromResponse(resp *pb.CommandResponse) []*pb.EventPage {
	if resp == nil || resp.Events == nil {
		return nil
	}
	return resp.Events.Pages
}

// Type URL helpers

// TypeURL constructs a full type URL from a fully-qualified type name.
//
// MED-2.8: cross-language signature is a single fully-qualified name
// (e.g. "examples.CardsDealt"). Mirrors Py `helpers.type_url`, Rs
// `convert::type_url`, Ja `Helpers.typeUrl`, Cpp `helpers::type_url`.
func TypeURL(fullName string) string {
	return TypeURLPrefix + fullName
}

// TypeNameFromURL extracts the fully qualified type name from a type URL.
// For "type.googleapis.com/examples.CardsDealt", returns "examples.CardsDealt".
func TypeNameFromURL(typeURL string) string {
	if idx := strings.LastIndex(typeURL, "/"); idx >= 0 {
		return typeURL[idx+1:]
	}
	return typeURL
}

// TypeURLMatches checks if a type URL matches the given fully qualified type name.
// typeName should be fully qualified (e.g., "examples.CardsDealt").
func TypeURLMatches(typeURL, typeName string) bool {
	return typeURL == TypeURLPrefix+typeName
}

// Type-safe reflection helpers

// TypeMatches checks if an Any contains a message of type T using proto reflection.
// This is preferred over string-based suffix matching.
func TypeMatches[T proto.Message](any *anypb.Any) bool {
	if any == nil {
		return false
	}
	var zero T
	return any.MessageIs(zero)
}

// TryUnpack attempts to unpack an Any to type T, returning nil if type doesn't match.
func TryUnpack[T proto.Message](any *anypb.Any) T {
	var zero T
	if any == nil {
		return zero
	}
	msg := proto.Clone(zero).(T)
	if err := any.UnmarshalTo(msg); err != nil {
		return zero
	}
	return msg
}

// MustUnpack unpacks an Any to type T, panicking if the type doesn't match.
// Intended for test code where type mismatches indicate test bugs.
func MustUnpack[T proto.Message](any *anypb.Any) T {
	var zero T
	if any == nil {
		panic("MustUnpack: nil Any")
	}
	msg := proto.Clone(zero).(T)
	if err := any.UnmarshalTo(msg); err != nil {
		panic(fmt.Sprintf("MustUnpack: failed to unmarshal to %T: %v", zero, err))
	}
	return msg
}

// FullTypeName returns the fully-qualified proto type name for message type T.
// Example: FullTypeName[*examples.PlayerRegistered]() returns "examples.PlayerRegistered"
func FullTypeName[T proto.Message]() string {
	var zero T
	return string(zero.ProtoReflect().Descriptor().FullName())
}

// FullTypeURL returns the full type URL for message type T.
// Example: FullTypeURL[*examples.PlayerRegistered]() returns "type.googleapis.com/examples.PlayerRegistered"
func FullTypeURL[T proto.Message]() string {
	return TypeURLPrefix + FullTypeName[T]()
}

// Timestamp helpers

// Now returns the current time as a protobuf Timestamp.
func Now() *timestamppb.Timestamp {
	return timestamppb.Now()
}

// ParseTimestamp parses an RFC3339 timestamp string.
func ParseTimestamp(rfc3339 string) (*timestamppb.Timestamp, error) {
	t, err := time.Parse(time.RFC3339, rfc3339)
	if err != nil {
		return nil, InvalidTimestampError(err.Error())
	}
	return timestamppb.New(t), nil
}

// Event decoding

// DecodeEvent attempts to decode an event payload if the type URL matches.
func DecodeEvent(page *pb.EventPage, typeSuffix string, msg interface{ Unmarshal([]byte) error }) bool {
	event := page.GetEvent()
	if page == nil || event == nil {
		return false
	}
	if !TypeURLMatches(event.TypeUrl, typeSuffix) {
		return false
	}
	return msg.Unmarshal(event.Value) == nil
}

// NewCover creates a new Cover with the given parameters.
func NewCover(domain string, root uuid.UUID, correlationID string) *pb.Cover {
	return &pb.Cover{
		Domain:        domain,
		Root:          UUIDToProto(root),
		CorrelationId: correlationID,
	}
}

// NewCoverWithEdition creates a Cover with an edition.
func NewCoverWithEdition(domain string, root uuid.UUID, correlationID string, edition *pb.Edition) *pb.Cover {
	return &pb.Cover{
		Domain:        domain,
		Root:          UUIDToProto(root),
		CorrelationId: correlationID,
		Edition:       edition,
	}
}

// NewCommandPage creates a command page from a sequence and Any message.
func NewCommandPage(sequence uint32, command *anypb.Any) *pb.CommandPage {
	return &pb.CommandPage{
		Header:  &pb.PageHeader{SequenceType: &pb.PageHeader_Sequence{Sequence: sequence}},
		Payload: &pb.CommandPage_Command{Command: command},
	}
}

// NewCommandBook creates a CommandBook with a single command.
func NewCommandBook(cover *pb.Cover, pages ...*pb.CommandPage) *pb.CommandBook {
	return &pb.CommandBook{
		Cover: cover,
		Pages: pages,
	}
}

// NewQueryWithRange creates a Query with a cover and range selection.
func NewQueryWithRange(cover *pb.Cover, lower uint32, upper *uint32) *pb.Query {
	r := &pb.SequenceRange{Lower: lower}
	if upper != nil {
		r.Upper = upper
	}
	return &pb.Query{
		Cover:     cover,
		Selection: &pb.Query_Range{Range: r},
	}
}

// NewQueryWithTemporal creates a Query with a temporal selection.
func NewQueryWithTemporal(cover *pb.Cover, temporal *pb.TemporalQuery) *pb.Query {
	return &pb.Query{
		Cover:     cover,
		Selection: &pb.Query_Temporal{Temporal: temporal},
	}
}

// RangeSelection creates a sequence range selection (returns the oneof wrapper).
func RangeSelection(lower uint32, upper *uint32) *pb.Query_Range {
	r := &pb.SequenceRange{Lower: lower}
	if upper != nil {
		r.Upper = upper
	}
	return &pb.Query_Range{Range: r}
}

// TemporalSelectionBySequence creates a temporal selection as-of a sequence.
func TemporalSelectionBySequence(seq uint32) *pb.Query_Temporal {
	return &pb.Query_Temporal{
		Temporal: &pb.TemporalQuery{
			PointInTime: &pb.TemporalQuery_AsOfSequence{AsOfSequence: seq},
		},
	}
}

// TemporalSelectionByTime creates a temporal selection as-of a timestamp.
func TemporalSelectionByTime(ts *timestamppb.Timestamp) *pb.Query_Temporal {
	return &pb.Query_Temporal{
		Temporal: &pb.TemporalQuery{
			PointInTime: &pb.TemporalQuery_AsOfTime{AsOfTime: ts},
		},
	}
}

// IdempotencyKey builds the composite idempotency key for a saga-produced
// deferred sequence.
//
// Format: "{edition}:{domain}:{root_hex}:{source_seq}"
//
// Returns (key, true) when the deferred sequence has a source cover, or
// ("", false) when the source is missing — a malformed wire input is a
// missing-key signal, not a panic.
//
// Audit finding #55: mirrors Rust `AngzarrDeferredSequenceExt::idempotency_key`
// (returns Result) and Python `helpers.idempotency_key` (returns Optional).
func IdempotencyKey(deferred *pb.AngzarrDeferredSequence) (string, bool) {
	if deferred == nil || deferred.Source == nil {
		return "", false
	}
	source := deferred.Source
	editionName := ""
	if source.Edition != nil {
		editionName = source.Edition.Name
	}
	rootHex := ""
	if source.Root != nil {
		rootHex = hex.EncodeToString(source.Root.Value)
	}
	return fmt.Sprintf("%s:%s:%s:%d", editionName, source.Domain, rootHex, deferred.SourceSeq), true
}

// DeferredHeader builds a PageHeader carrying an AngzarrDeferredSequence.
//
// Use this on saga-produced commands so the framework can dedupe on
// (source.root, source_seq, target.root). AMQP at-least-once redelivery of
// the triggering event then becomes a no-op at the destination aggregate's
// pipeline rather than a retry storm masquerading as idempotent failure.
//
// Audit finding #31: mirrors Python `Destinations.deferred_header` (static)
// and Rust `Destinations::deferred_header` (static).
func DeferredHeader(sourceCover *pb.Cover, sourceSeq uint32) *pb.PageHeader {
	return &pb.PageHeader{
		SequenceType: &pb.PageHeader_AngzarrDeferred{
			AngzarrDeferred: &pb.AngzarrDeferredSequence{
				Source:    sourceCover,
				SourceSeq: sourceSeq,
			},
		},
	}
}

// ParseAnyE decodes raw Any-payload bytes into `target`, surfacing
// malformed payloads as INVALID_ARGUMENT with the cross-language
// ANY_DECODE_FAILED code (audit #87).
//
// Without the wrap, a bad payload propagates `proto.Unmarshal`'s
// generic error up to the gRPC layer and lands as INTERNAL — the same
// failure source as a server panic. Python `_parse_any` and Rust's
// macro-emitted dispatch both wrap as INVALID_ARGUMENT with
// {type_url, cause} extras; this is the Go equivalent.
func ParseAnyE(target proto.Message, value []byte, typeURL string) error {
	if err := proto.Unmarshal(value, target); err != nil {
		return &ClientError{
			Kind:    ErrInvalidArgument,
			Code:    CodeAnyDecodeFailed,
			Message: "failed to decode Any payload into expected message type",
			Extras: map[string]string{
				ExtraKeyTypeURL: typeURL,
				ExtraKeyCause:   err.Error(),
			},
			Cause: err,
		}
	}
	return nil
}

// UnpackAnyE is the `anypb.Any.UnmarshalTo` wrapper that surfaces
// malformed payloads as INVALID_ARGUMENT with ANY_DECODE_FAILED.
//
// Mirrors Python `_unpack_any` and Rust's macro-emitted Any decode
// (audit #87). Returns the ClientError on bad bytes; nil on success.
func UnpackAnyE(any *anypb.Any, target proto.Message) error {
	if any == nil {
		return &ClientError{
			Kind:    ErrInvalidArgument,
			Code:    CodeAnyDecodeFailed,
			Message: "nil Any",
			Extras:  map[string]string{},
		}
	}
	if err := any.UnmarshalTo(target); err != nil {
		return &ClientError{
			Kind:    ErrInvalidArgument,
			Code:    CodeAnyDecodeFailed,
			Message: "failed to decode Any payload into expected message type",
			Extras: map[string]string{
				ExtraKeyTypeURL: any.TypeUrl,
				ExtraKeyCause:   err.Error(),
			},
			Cause: err,
		}
	}
	return nil
}

// CorrelatedMetadata builds a gRPC outgoing metadata bundle carrying the
// correlation ID under CorrelationIDHeader. An empty correlationID returns an
// empty metadata (the framework treats absent and empty identically per
// audit #69). Mirrors Py `helpers.correlated_metadata` and Rs
// `proto_ext::grpc::correlated_request`. Spec MED-2.10.
func CorrelatedMetadata(correlationID string) metadata.MD {
	if correlationID == "" {
		return metadata.MD{}
	}
	return metadata.New(map[string]string{CorrelationIDHeader: correlationID})
}

// RootFromCover returns (root, true) if the cover carries a valid 16-byte
// UUID root, otherwise (zero, false). Mirrors Rs `root_from_cover`. Spec
// MED-3.11.
func RootFromCover(cover *pb.Cover) (uuid.UUID, bool) {
	if cover == nil || cover.Root == nil {
		return uuid.UUID{}, false
	}
	u, err := uuid.FromBytes(cover.Root.Value)
	if err != nil {
		return uuid.UUID{}, false
	}
	return u, true
}

// DestinationMap builds a map from root UUID hex to EventBook for destination lookup.
//
// Used in multi-destination sagas to look up the correct EventBook by aggregate
// root when stamping command sequences. Entries without a root are skipped.
//
// Cross-language alias: mirrors Rust `proto_ext::destination_map` and Python
// `helpers.destination_map`.
func DestinationMap(destinations []*pb.EventBook) map[string]*pb.EventBook {
	out := make(map[string]*pb.EventBook, len(destinations))
	for _, book := range destinations {
		if book == nil || book.Cover == nil || book.Cover.Root == nil {
			continue
		}
		out[hex.EncodeToString(book.Cover.Root.Value)] = book
	}
	return out
}
