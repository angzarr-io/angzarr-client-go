package angzarr

import (
	"errors"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ErrorInfoDomain is the google.rpc.ErrorInfo domain for all angzarr
// errors, byte-identical across language clients.
const ErrorInfoDomain = "angzarr.io"

// mapHandlerError is the single handler-error → gRPC status mapping used
// by every component adapter. The contract axis is retryability, not
// blame (see CommandRejectedError): FAILED_PRECONDITION = refetch state
// and retry; every other code = don't retry, and the code routes
// diagnosis. The line for decode failures: a caller-constructed envelope
// that fails to decode is INVALID_ARGUMENT; a store-sourced persisted
// payload that fails to decode is DATA_LOSS.
//
// The cross-language SCREAMING_SNAKE code travels as ErrorInfo.Reason
// (Domain "angzarr.io", Metadata = details map) so clients assert on the
// code rather than message text. An UNKNOWN status observed in the wild
// means an error bypassed this mapper entirely (grpc-go's default) — a
// canary, not a row.
func mapHandlerError(err error) error {
	var rejected CommandRejectedError
	if errors.As(err, &rejected) {
		return statusWithErrorInfo(rejected.GrpcCode(), rejected.Message, rejected.Code, rejected.Details)
	}

	var clientErr *ClientError
	if errors.As(err, &clientErr) {
		code := codes.InvalidArgument
		switch clientErr.Code {
		case CodeNoHandlerRegistered:
			// Misrouted command / stale deployment, not a malformed payload.
			code = codes.Unimplemented
		case CodePersistedEventCorrupt:
			// Store-sourced bytes, unrecoverable by retry.
			code = codes.DataLoss
		}
		return statusWithErrorInfo(code, clientErr.Message, clientErr.Code, clientErr.Extras)
	}

	// Unclassified error escaping a business handler: server-side fault.
	return statusWithErrorInfo(codes.Internal, err.Error(), CodeUnhandledHandlerError, nil)
}

// statusWithErrorInfo builds a status carrying a google.rpc.ErrorInfo
// detail. An empty reason emits a bare status (legacy uncoded rejections).
func statusWithErrorInfo(code codes.Code, msg, reason string, metadata map[string]string) error {
	st := status.New(code, msg)
	if reason == "" {
		return st.Err()
	}
	detailed, err := st.WithDetails(&errdetails.ErrorInfo{
		Reason:   reason,
		Domain:   ErrorInfoDomain,
		Metadata: metadata,
	})
	if err != nil {
		// Detail attachment never fails for ErrorInfo; degrade to the
		// bare status rather than masking the original error.
		return st.Err()
	}
	return detailed.Err()
}
