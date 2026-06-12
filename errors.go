// Package angzarr provides a client library for Angzarr gRPC services.
package angzarr

import (
	"errors"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ClientError represents errors from client operations.
//
// Code is a SCREAMING_SNAKE_CASE cross-language identifier (mirrors
// `error_codes::codes` in Rust and `error_codes.codes` in Python). It's
// stable across languages and suitable for cucumber assertions.
// Extras carries optional structured context (e.g. input, env_var, type_url).
type ClientError struct {
	Kind    ErrorKind
	Message string
	Code    string
	Extras  map[string]string
	Cause   error
}

// ErrorKind categorizes client errors.
type ErrorKind int

const (
	// ErrConnection indicates a connection failure.
	ErrConnection ErrorKind = iota
	// ErrTransport indicates a transport-level error.
	ErrTransport
	// ErrGRPC indicates a gRPC error from the server.
	ErrGRPC
	// ErrInvalidArgument indicates an invalid argument from the caller.
	ErrInvalidArgument
	// ErrInvalidTimestamp indicates a timestamp parsing failure.
	ErrInvalidTimestamp
)

// Error returns the static, cross-language-stable message.
//
// MED-1.10 / audit #59: the dynamic cause string MUST NOT leak into
// Error() so log greppers, cucumber assertions, and cross-language
// equality checks all see byte-identical text. Use errors.Unwrap /
// errors.As to reach the underlying cause.
func (e *ClientError) Error() string {
	return e.Message
}

func (e *ClientError) Unwrap() error {
	return e.Cause
}

// GRPCCode returns the gRPC status code if this is a gRPC error.
//
// Renamed from Code() because Code is now a field carrying the
// cross-language SCREAMING_SNAKE_CASE identifier.
func (e *ClientError) GRPCCode() codes.Code {
	if e.Kind != ErrGRPC || e.Cause == nil {
		return codes.Unknown
	}
	if s, ok := status.FromError(e.Cause); ok {
		return s.Code()
	}
	return codes.Unknown
}

// Status returns the gRPC Status if this is a gRPC error.
func (e *ClientError) Status() *status.Status {
	if e.Kind != ErrGRPC || e.Cause == nil {
		return nil
	}
	s, _ := status.FromError(e.Cause)
	return s
}

// IsNotFound returns true if this is a "not found" error.
func (e *ClientError) IsNotFound() bool {
	return e.GRPCCode() == codes.NotFound
}

// IsPreconditionFailed returns true if this is a "precondition failed" error.
func (e *ClientError) IsPreconditionFailed() bool {
	return e.GRPCCode() == codes.FailedPrecondition
}

// IsInvalidArgument returns true if this is an "invalid argument" error.
func (e *ClientError) IsInvalidArgument() bool {
	return e.Kind == ErrInvalidArgument || e.GRPCCode() == codes.InvalidArgument
}

// IsConnectionError returns true if this is a connection or transport error.
func (e *ClientError) IsConnectionError() bool {
	return e.Kind == ErrConnection || e.Kind == ErrTransport
}

// Error constructors

// ConnectionError creates a connection error.
func ConnectionError(msg string) *ClientError {
	return &ClientError{Kind: ErrConnection, Message: msg}
}

// TransportError wraps a transport error.
func TransportError(err error) *ClientError {
	return &ClientError{Kind: ErrTransport, Message: "transport error", Cause: err}
}

// GRPCError wraps a gRPC error. When the status carries a
// google.rpc.ErrorInfo detail (attached server-side by mapHandlerError),
// its Reason/Metadata populate Code/Extras so callers assert on the
// cross-language code instead of message text.
func GRPCError(err error) *ClientError {
	clientErr := &ClientError{Kind: ErrGRPC, Message: "grpc error", Cause: err}
	if st, ok := status.FromError(err); ok {
		for _, d := range st.Details() {
			if info, ok := d.(*errdetails.ErrorInfo); ok {
				clientErr.Code = info.Reason
				clientErr.Extras = info.Metadata
				break
			}
		}
	}
	return clientErr
}

// InvalidArgumentError creates an invalid argument error.
func InvalidArgumentError(msg string) *ClientError {
	return &ClientError{Kind: ErrInvalidArgument, Message: msg}
}

// InvalidTimestampError creates a timestamp parsing error.
func InvalidTimestampError(msg string) *ClientError {
	return &ClientError{Kind: ErrInvalidTimestamp, Message: msg}
}

// InvalidArgumentErrorWithCode creates an InvalidArgument error carrying a
// SCREAMING_SNAKE_CASE cross-language Code identifier and structured extras.
// Mirrors Rust `ClientError::invalid_argument(code, message, extras)` and
// Python `InvalidArgumentError(message, code=..., details=...)`.
func InvalidArgumentErrorWithCode(code, msg string, extras map[string]string) *ClientError {
	return &ClientError{
		Kind:    ErrInvalidArgument,
		Message: msg,
		Code:    code,
		Extras:  extras,
	}
}

// Cross-language error codes — full 47-code inventory matching Py/Rs/Ja/Cpp.
//
// SCREAMING_SNAKE strings, byte-identical across languages. Cross-language
// cucumber assertions key off these identifiers (audit #59).
//
// Spec reference: `core/main/.plan/cross-language-spec.md` §1.4.
const (
	// --- Validation predicates -------------------------------------------
	CodeValueNotPositive    = "VALUE_NOT_POSITIVE"
	CodeValueNotNonNegative = "VALUE_NOT_NON_NEGATIVE"
	CodeValueEmpty          = "VALUE_EMPTY"
	CodeCollectionEmpty     = "COLLECTION_EMPTY"
	CodeEntityNotFound      = "ENTITY_NOT_FOUND"
	CodeEntityAlreadyExists = "ENTITY_ALREADY_EXISTS"
	CodeStatusMismatch      = "STATUS_MISMATCH"
	CodeStatusForbidden     = "STATUS_FORBIDDEN"

	// --- Builder ---------------------------------------------------------
	CodeCommandTypeURLMissing  = "COMMAND_TYPE_URL_MISSING"
	CodeCommandPayloadMissing  = "COMMAND_PAYLOAD_MISSING"
	CodeCommandSequenceMissing = "COMMAND_SEQUENCE_MISSING"

	// --- Conversion / proto ----------------------------------------------
	CodeAnyTypeMismatch      = "ANY_TYPE_MISMATCH"
	CodeAnyDecodeFailed      = "ANY_DECODE_FAILED"
	CodeProtoUUIDInvalid     = "PROTO_UUID_INVALID"
	CodeTimestampParseFailed = "TIMESTAMP_PARSE_FAILED"

	// --- Endpoint / transport -------------------------------------------
	CodeEndpointParseFailed        = "ENDPOINT_PARSE_FAILED"
	CodeEndpointInvalidURI         = "ENDPOINT_INVALID_URI"
	CodeConnectionFailed           = "CONNECTION_FAILED"
	CodeConnectionFailedMaxRetries = "CONNECTION_FAILED_MAX_RETRIES"
	CodeTransportError             = "TRANSPORT_ERROR"
	CodeGRPCError                  = "GRPC_ERROR"
	CodeInvalidTransportMode       = "INVALID_TRANSPORT_MODE"
	CodeInvalidPort                = "INVALID_PORT"
	// Set-but-unparseable duration env value (e.g. ANGZARR_SHUTDOWN_DRAIN).
	// Py/Rs/Ja/Cs/Cpp must mirror this addition to the inventory.
	CodeInvalidDuration = "INVALID_DURATION"
	// Event book delivered without a Cover (projector dispatch needs the
	// domain). Py/Rs/Ja/Cs/Cpp must mirror this addition to the inventory.
	CodeMissingEventBookCover = "MISSING_EVENT_BOOK_COVER"

	// --- Dispatch --------------------------------------------------------
	CodeHandlerWrongResponseKind          = "HANDLER_WRONG_RESPONSE_KIND"
	CodeHandlerWrongRequestKind           = "HANDLER_WRONG_REQUEST_KIND"
	CodeNoHandlerRegistered               = "NO_HANDLER_REGISTERED"
	CodeMissingCommandBook                = "MISSING_COMMAND_BOOK"
	CodeMissingCommandPage                = "MISSING_COMMAND_PAGE"
	CodeMissingCommandPayload             = "MISSING_COMMAND_PAYLOAD"
	CodeNotificationDecodeFailed          = "NOTIFICATION_DECODE_FAILED"
	CodeRejectionNotificationDecodeFailed = "REJECTION_NOTIFICATION_DECODE_FAILED"
	// Store-sourced event payload failed to decode during state rebuild.
	// Maps to DATA_LOSS (unrecoverable by retry). Py/Rs/Ja/Cs/Cpp must
	// mirror this addition to the inventory.
	CodePersistedEventCorrupt = "PERSISTED_EVENT_CORRUPT"
	// Unclassified error escaped a business handler. Maps to INTERNAL.
	// Py/Rs/Ja/Cs/Cpp must mirror this addition to the inventory.
	CodeUnhandledHandlerError = "UNHANDLED_HANDLER_ERROR"

	// --- Saga ------------------------------------------------------------
	CodeMissingSagaSource                = "MISSING_SAGA_SOURCE"
	CodeEmptySagaSource                  = "EMPTY_SAGA_SOURCE"
	CodeMissingSagaEventPayload          = "MISSING_SAGA_EVENT_PAYLOAD"
	CodeSagaInvalidTypeURL               = "SAGA_INVALID_TYPE_URL"
	CodeSagaHandlerUnsupportedReturnType = "SAGA_HANDLER_UNSUPPORTED_RETURN_TYPE"

	// --- Process manager -------------------------------------------------
	CodeMissingPMTrigger         = "MISSING_PM_TRIGGER"
	CodeEmptyPMTrigger           = "EMPTY_PM_TRIGGER"
	CodeMissingPMEventPayload    = "MISSING_PM_EVENT_PAYLOAD"
	CodePMInvalidTypeURL         = "PM_INVALID_TYPE_URL"
	CodePMHandlerWrongReturnType = "PM_HANDLER_WRONG_RETURN_TYPE"

	// --- Upcaster --------------------------------------------------------
	CodeUpcasterWrongResponseKind = "UPCASTER_WRONG_RESPONSE_KIND"

	// --- Handler metadata ------------------------------------------------
	CodeHandlerFieldEmptyString = "HANDLER_FIELD_EMPTY_STRING"
	CodeHandlerFieldEmptyList   = "HANDLER_FIELD_EMPTY_LIST"
	CodeHandlerStateNotType     = "HANDLER_STATE_NOT_TYPE"
	CodeHandlerUnknownKind      = "HANDLER_UNKNOWN_KIND"
	CodeRouterNoHandlers        = "ROUTER_NO_HANDLERS"
	CodeDuplicateCommandHandler = "DUPLICATE_COMMAND_HANDLER"
	CodeMixedHandlerKinds       = "MIXED_HANDLER_KINDS"

	// --- Destinations ----------------------------------------------------
	CodeMissingDestinationSequence = "MISSING_DESTINATION_SEQUENCE"

	// --- Table seating ---------------------------------------------------
	// CodeSeatsIdentical signals a ChangeSeats command whose current_seat
	// equals requested_seat. PR #12 decision: reject before any seat
	// logic. Cross-language constant; matches Py/Rs/Ja/C#/Cpp
	// error_codes.SEATS_IDENTICAL. See `core/main/.plan/cross-language-interpretation.md`
	// §"PR #12 design decisions".
	CodeSeatsIdentical = "SEATS_IDENTICAL"
)

// MessageRouterNoHandlers etc. — canonical static text constants.
//
// Spec reference: `core/main/.plan/cross-language-spec.md` §1.5.
// These mirror Py `error_codes.messages.*`, Rs `messages::*`,
// Ja `Messages.*`, Cpp `messages::*`. Byte-equal across languages.
const (
	MessageValueNotPositive                  = "value must be positive"
	MessageValueNotNonNegative               = "value must be non-negative"
	MessageValueEmpty                        = "value must not be empty"
	MessageCollectionEmpty                   = "collection must not be empty"
	MessageEntityNotFound                    = "entity not found"
	MessageEntityAlreadyExists               = "entity already exists"
	MessageStatusMismatch                    = "status does not match expected"
	MessageStatusForbidden                   = "status is the forbidden value"
	MessageCommandTypeURLMissing             = "command type_url not set"
	MessageCommandPayloadMissing             = "command payload not set"
	MessageCommandSequenceMissing            = "sequence not set (call with_sequence)"
	MessageAnyTypeMismatch                   = "Any type_url does not match expected message type"
	MessageAnyDecodeFailed                   = "failed to decode Any payload into expected message type"
	MessageProtoUUIDInvalid                  = "proto UUID bytes are not a valid 16-byte UUID"
	MessageTimestampParseFailed              = "failed to parse RFC3339 timestamp"
	MessageEndpointParseFailed               = "failed to parse fallback endpoint"
	MessageEndpointInvalidURI                = "endpoint URI is invalid"
	MessageConnectionFailed                  = "connection failed"
	MessageConnectionFailedMaxRetries        = "connection failed after max retries"
	MessageTransportError                    = "transport error"
	MessageGRPCError                         = "grpc error"
	MessageInvalidTransportMode              = "invalid transport mode env value"
	MessageInvalidPort                       = "invalid port env value"
	MessageHandlerWrongResponseKind          = "handler returned wrong response kind"
	MessageHandlerWrongRequestKind           = "handler dispatched with wrong request kind"
	MessageNoHandlerRegistered               = "no handler registered for the given (domain, type_url)"
	MessageMissingCommandBook                = "missing command book"
	MessageMissingCommandPage                = "missing command page"
	MessageMissingCommandPayload             = "missing command payload"
	MessageNotificationDecodeFailed          = "failed to decode Notification payload"
	MessageRejectionNotificationDecodeFailed = "failed to decode RejectionNotification payload"
	// Py/Rs/Ja/Cs/Cpp must mirror (pairs with PERSISTED_EVENT_CORRUPT).
	MessagePersistedEventCorrupt            = "persisted event payload corrupt"
	MessageMissingSagaSource                = "missing saga source"
	MessageEmptySagaSource                  = "empty saga source"
	MessageMissingSagaEventPayload          = "missing event payload"
	MessageSagaInvalidTypeURL               = "saga trigger has invalid type_url"
	MessageSagaHandlerUnsupportedReturnType = "saga handler returned unsupported type"
	MessageMissingPMTrigger                 = "missing PM trigger"
	MessageEmptyPMTrigger                   = "empty PM trigger"
	MessageMissingPMEventPayload            = "missing event payload on PM trigger"
	MessagePMInvalidTypeURL                 = "PM trigger has invalid type_url"
	MessagePMHandlerWrongReturnType         = "PM handler must return ProcessManagerResponse"
	MessageUpcasterWrongResponseKind        = "upcaster handler returned non-Upcaster response"
	MessageHandlerFieldEmptyString          = "handler field must be a non-empty string"
	MessageHandlerFieldEmptyList            = "handler field must be a non-empty list"
	MessageHandlerStateNotType              = "handler 'state' must be a type"
	MessageHandlerUnknownKind               = "unknown handler kind"
	MessageRouterNoHandlers                 = "no handlers registered on Router"
	MessageDuplicateCommandHandler          = "duplicate command handler registration for (domain, type_url)"
	MessageMixedHandlerKinds                = "cannot mix handler kinds in one Router — all handlers must share a kind"
	MessageMissingDestinationSequence       = "no sequence for destination domain"

	// MessageSeatsIdentical mirrors Py/Rs/Ja/C#/Cpp canonical text for the
	// SEATS_IDENTICAL code (PR #12). See `core/main/.plan/cross-language-spec.md` §1.5.
	MessageSeatsIdentical = "current_seat and requested_seat are identical"
)

// Cross-language detail-key constants — full 16-key inventory.
//
// Spec reference: `core/main/.plan/cross-language-spec.md` §1.6.
const (
	ExtraKeyField            = "field"
	ExtraKeyContext          = "context"
	ExtraKeyCause            = "cause"
	ExtraKeyEndpoint         = "endpoint"
	ExtraKeyInput            = "input"
	ExtraKeyExpected         = "expected"
	ExtraKeyActual           = "actual"
	ExtraKeyExpectedKind     = "expected_kind"
	ExtraKeyDomain           = "domain"
	ExtraKeyTypeURL          = "type_url"
	ExtraKeyRouterName       = "router_name"
	ExtraKeyHandlerClass     = "handler_class"
	ExtraKeyHandlerKind      = "handler_kind"
	ExtraKeyActualReturnType = "actual_return_type"
	ExtraKeyEnvVar           = "env_var"
	ExtraKeyOtherKind        = "other_kind"
)

// IsClientError checks if an error is a ClientError.
func IsClientError(err error) bool {
	var clientErr *ClientError
	return errors.As(err, &clientErr)
}

// AsClientError extracts a ClientError from an error chain.
func AsClientError(err error) *ClientError {
	var clientErr *ClientError
	if errors.As(err, &clientErr) {
		return clientErr
	}
	return nil
}
