package angzarr

// CommandRejectedError and its factories — the business-rejection error
// surface shared by every dispatch path.

import (
	"google.golang.org/grpc/codes"
)

// CommandRejectedError indicates a command was rejected due to business rule violation.
//
// # Why FAILED_PRECONDITION?
//
// Maps to gRPC FAILED_PRECONDITION because:
// 1. It signals the client SHOULD retry after updating state (fetching fresh events)
// 2. Distinguishes from INVALID_ARGUMENT (bad input, don't retry)
// 3. Matches the framework's retry policy which retries FAILED_PRECONDITION
//
// Use this for business rule rejections where the aggregate's current state
// doesn't allow the operation (e.g., "insufficient funds", "player already exists").
//
// Code carries a SCREAMING_SNAKE cross-language identifier (e.g.
// CodeStatusMismatch). Details carries structured context. Mirrors Rust
// `CommandRejectedError` and Python `CommandRejectedError` per spec §1.3.
type CommandRejectedError struct {
	Message    string
	StatusCode string // "FAILED_PRECONDITION", "INVALID_ARGUMENT", or "NOT_FOUND"
	Code       string
	Details    map[string]string
}

func (e CommandRejectedError) Error() string {
	return e.Message
}

// GrpcCode maps the rejection's StatusCode to a gRPC status code. Mirrors the
// Python/Rust mapping: FAILED_PRECONDITION → FailedPrecondition,
// INVALID_ARGUMENT → InvalidArgument, NOT_FOUND → NotFound. An empty or
// unrecognized StatusCode defaults to FailedPrecondition (legacy callers).
func (e CommandRejectedError) GrpcCode() codes.Code {
	switch e.StatusCode {
	case "INVALID_ARGUMENT":
		return codes.InvalidArgument
	case "NOT_FOUND":
		return codes.NotFound
	default:
		return codes.FailedPrecondition
	}
}

// NewCommandRejectedError creates a FAILED_PRECONDITION error (default for guard failures).
func NewCommandRejectedError(msg string) error {
	return CommandRejectedError{Message: msg, StatusCode: "FAILED_PRECONDITION"}
}

// NewInvalidArgumentError creates an INVALID_ARGUMENT error for input validation failures.
func NewInvalidArgumentError(msg string) error {
	return CommandRejectedError{Message: msg, StatusCode: "INVALID_ARGUMENT"}
}

// NewPreconditionFailedRejection builds a structured FAILED_PRECONDITION
// rejection carrying a cross-language `code`, a static `message`, and a
// `details` map. Mirrors Py `CommandRejectedError.precondition_failed`
// (`errors.py:208`), Rs `CommandRejectedError::precondition_failed`
// (`error.rs:247`), Ja `Errors.CommandRejectedError.preconditionFailed`,
// Cs `CommandRejectedError.PreconditionFailed`, Cpp
// `command_rejected_error::precondition_failed`. Spec MED-1.5.
func NewPreconditionFailedRejection(code, message string, details map[string]string) CommandRejectedError {
	return CommandRejectedError{
		Message:    message,
		StatusCode: "FAILED_PRECONDITION",
		Code:       code,
		Details:    cloneDetails(details),
	}
}

// NewInvalidArgumentRejectionWithCode is the (code, message, details)
// factory mirroring Py `CommandRejectedError.invalid_argument`,
// Rs `CommandRejectedError::invalid_argument`. Spec MED-1.5.
func NewInvalidArgumentRejectionWithCode(code, message string, details map[string]string) CommandRejectedError {
	return CommandRejectedError{
		Message:    message,
		StatusCode: "INVALID_ARGUMENT",
		Code:       code,
		Details:    cloneDetails(details),
	}
}

// NewInvalidArgumentRejection legacy short-form. Kept for backward
// compatibility with existing example handlers that don't carry a code.
func NewInvalidArgumentRejection(msg string) error {
	return CommandRejectedError{Message: msg, StatusCode: "INVALID_ARGUMENT"}
}

// NewNotFoundRejection mirrors Py `not_found`, Rs `not_found`. Spec MED-1.5.
func NewNotFoundRejection(code, message string, details map[string]string) CommandRejectedError {
	return CommandRejectedError{
		Message:    message,
		StatusCode: "NOT_FOUND",
		Code:       code,
		Details:    cloneDetails(details),
	}
}

func cloneDetails(in map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
