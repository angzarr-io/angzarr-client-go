package angzarr

import (
	"errors"
	"fmt"
	"testing"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func errorInfoOf(t *testing.T, err error) *errdetails.ErrorInfo {
	t.Helper()
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("not a status error: %v", err)
	}
	for _, d := range st.Details() {
		if info, ok := d.(*errdetails.ErrorInfo); ok {
			return info
		}
	}
	return nil
}

// mapHandlerError is the single handler-error → gRPC status mapping for
// every component adapter (replaces 8 byte-identical fallbacks that
// labeled all non-rejection errors InvalidArgument). The contract axis is
// retryability: FAILED_PRECONDITION = refetch state and retry; everything
// else = don't retry, with the gRPC code routing diagnosis. The
// cross-language SCREAMING_SNAKE code travels as ErrorInfo.Reason
// (Domain "angzarr.io") instead of being discarded at the boundary.
func TestMapHandlerError_Table(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantCode   codes.Code
		wantReason string
	}{
		{
			name:       "business guard rejection keeps FailedPrecondition",
			err:        NewPreconditionFailedRejection(CodeStatusMismatch, "wrong phase", map[string]string{"phase": "betting"}),
			wantCode:   codes.FailedPrecondition,
			wantReason: CodeStatusMismatch,
		},
		{
			name:       "business input validation keeps InvalidArgument",
			err:        NewInvalidArgumentRejectionWithCode(CodeValueNotPositive, "bet must be positive", nil),
			wantCode:   codes.InvalidArgument,
			wantReason: CodeValueNotPositive,
		},
		{
			name:       "malformed caller envelope is InvalidArgument",
			err:        InvalidArgumentErrorWithCode(CodeNotificationDecodeFailed, "failed to decode Notification", nil),
			wantCode:   codes.InvalidArgument,
			wantReason: CodeNotificationDecodeFailed,
		},
		{
			name:       "no handler registered is Unimplemented",
			err:        InvalidArgumentErrorWithCode(CodeNoHandlerRegistered, "no handler registered", nil),
			wantCode:   codes.Unimplemented,
			wantReason: CodeNoHandlerRegistered,
		},
		{
			name:       "corrupt persisted event is DataLoss",
			err:        InvalidArgumentErrorWithCode(CodePersistedEventCorrupt, "persisted event corrupt", nil),
			wantCode:   codes.DataLoss,
			wantReason: CodePersistedEventCorrupt,
		},
		{
			name:       "unclassified handler error is Internal",
			err:        fmt.Errorf("nil pointer in business logic"),
			wantCode:   codes.Internal,
			wantReason: CodeUnhandledHandlerError,
		},
		{
			name:       "wrapped rejection still classifies",
			err:        fmt.Errorf("dispatch: %w", NewCommandRejectedError("insufficient funds")),
			wantCode:   codes.FailedPrecondition,
			wantReason: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := mapHandlerError(tt.err)
			st, ok := status.FromError(got)
			if !ok {
				t.Fatalf("not a status error: %v", got)
			}
			if st.Code() != tt.wantCode {
				t.Fatalf("code = %v, want %v", st.Code(), tt.wantCode)
			}
			info := errorInfoOf(t, got)
			if tt.wantReason == "" {
				return // no Reason expected (legacy uncoded rejection)
			}
			if info == nil {
				t.Fatal("ErrorInfo detail missing")
			}
			if info.Reason != tt.wantReason {
				t.Errorf("Reason = %q, want %q", info.Reason, tt.wantReason)
			}
			if info.Domain != ErrorInfoDomain {
				t.Errorf("Domain = %q, want %q", info.Domain, ErrorInfoDomain)
			}
		})
	}
}

func TestMapHandlerError_CarriesRejectionDetailsAsMetadata(t *testing.T) {
	err := NewPreconditionFailedRejection(CodeStatusMismatch, "wrong phase",
		map[string]string{"phase": "betting"})
	info := errorInfoOf(t, mapHandlerError(err))
	if info == nil {
		t.Fatal("ErrorInfo detail missing")
	}
	if info.Metadata["phase"] != "betting" {
		t.Errorf("Metadata = %v, want phase=betting", info.Metadata)
	}
}

// Round-trip: the wire ErrorInfo populates ClientError.Code so callers
// (and godog steps) assert on the cross-language code, not message text.
func TestGRPCError_ExtractsErrorInfoCode(t *testing.T) {
	wireErr := mapHandlerError(InvalidArgumentErrorWithCode(
		CodeNoHandlerRegistered, "no handler registered", map[string]string{"command": "examples.v1.DealCards"}))

	clientErr := GRPCError(wireErr)
	if clientErr.Code != CodeNoHandlerRegistered {
		t.Fatalf("ClientError.Code = %q, want %q", clientErr.Code, CodeNoHandlerRegistered)
	}
	if clientErr.Extras["command"] != "examples.v1.DealCards" {
		t.Errorf("Extras = %v, want command preserved", clientErr.Extras)
	}
	var ce *ClientError
	if !errors.As(clientErr, &ce) {
		t.Fatal("GRPCError result must remain a *ClientError")
	}
}
