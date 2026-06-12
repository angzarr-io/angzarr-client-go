// Package angzarr provides compensation flow helpers for saga revocation handling.
//
// When a saga/PM command is rejected by a target aggregate, the framework sends
// a Notification with RejectionNotification payload to the triggering aggregate.
// These helpers make it easy to implement compensation logic.
//
// Example in aggregate:
//
//	router := NewCommandRouter("order", rebuildState).
//	    On("CreateOrder", handleCreateOrder).
//	    OnRejected("fulfillment", "CreateShipment", handleRevocation)
//
//	func handleRevocation(notification *pb.Notification, state OrderState) *pb.BusinessResponse {
//	    ctx := NewCompensationContext(notification)
//
//	    // Option 1: Emit compensation events
//	    event := &OrderCancelled{
//	        OrderId: state.OrderId,
//	        Reason:  fmt.Sprintf("Fulfillment failed: %s", ctx.RejectionReason),
//	    }
//	    return EmitCompensationEvents(PackEvents(event))
//
//	    // Option 2: Delegate to framework
//	    return DelegateToFramework("No custom compensation for " + ctx.IssuerName)
//	}
package angzarr

import (
	pb "github.com/benjaminabbitt/angzarr/client/go/proto/angzarr_client/proto/angzarr/v1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// NotificationTypeName is the fully-qualified proto type name for Notification.
const NotificationTypeName = "angzarr_client.proto.angzarr.v1.Notification"

// CompensationContext provides easy access to rejection details.
type CompensationContext struct {
	// SourceEventSequence is the sequence of the event that triggered the saga/PM.
	SourceEventSequence uint32

	// RejectionReason is why the command was rejected.
	RejectionReason string

	// RejectedCommand is the command that was rejected (may be nil).
	RejectedCommand *pb.CommandBook

	// SourceAggregate is the cover of the aggregate that triggered the flow.
	SourceAggregate *pb.Cover
}

// NewCompensationContext extracts context from a Notification.
func NewCompensationContext(notification *pb.Notification) *CompensationContext {
	ctx := &CompensationContext{}

	if notification.Payload != nil {
		var rejection pb.RejectionNotification
		if err := proto.Unmarshal(notification.Payload.Value, &rejection); err == nil {
			ctx.RejectionReason = rejection.RejectionReason
			ctx.RejectedCommand = rejection.RejectedCommand

			// Extract source info from rejected_command.pages[].header.angzarr_deferred
			if rejection.RejectedCommand != nil && len(rejection.RejectedCommand.Pages) > 0 {
				page := rejection.RejectedCommand.Pages[0]
				if header := page.GetHeader(); header != nil {
					if deferred := header.GetAngzarrDeferred(); deferred != nil {
						ctx.SourceAggregate = deferred.Source
						ctx.SourceEventSequence = deferred.SourceSeq
					}
				}
			}
		}
	}

	return ctx
}

// DispatchKey returns a routing key in the format "domain/command" for rejection handlers.
// Returns empty string if the rejected command is not available.
func (c *CompensationContext) DispatchKey() string {
	if c.RejectedCommand == nil || c.RejectedCommand.Cover == nil {
		return ""
	}
	domain := c.RejectedCommand.Cover.Domain
	cmdType := c.RejectedCommandType()
	if domain == "" || cmdType == "" {
		return ""
	}
	return domain + "/" + cmdType
}

// RejectedCommandType returns the type URL of the rejected command, if available.
func (c *CompensationContext) RejectedCommandType() string {
	if c.RejectedCommand != nil && len(c.RejectedCommand.Pages) > 0 {
		page := c.RejectedCommand.Pages[0]
		if cmd := page.GetCommand(); cmd != nil {
			return cmd.TypeUrl
		}
	}
	return ""
}

// --- Aggregate helpers ---

// DelegateToFramework creates a response that delegates compensation to the framework.
//
// Use when the aggregate doesn't have custom compensation logic for a saga.
// The framework will emit a SagaCompensationFailed event to the fallback domain.
func DelegateToFramework(reason string) *pb.BusinessResponse {
	return &pb.BusinessResponse{
		Result: &pb.BusinessResponse_Revocation{
			Revocation: &pb.RevocationResponse{
				EmitSystemRevocation: true,
				Reason:               reason,
			},
		},
	}
}

// DelegateToFrameworkWithOptions creates a response with custom revocation flags.
func DelegateToFrameworkWithOptions(reason string, emitSystemEvent, sendToDLQ, escalate, abort bool) *pb.BusinessResponse {
	return &pb.BusinessResponse{
		Result: &pb.BusinessResponse_Revocation{
			Revocation: &pb.RevocationResponse{
				EmitSystemRevocation:  emitSystemEvent,
				SendToDeadLetterQueue: sendToDLQ,
				Escalate:              escalate,
				Abort:                 abort,
				Reason:                reason,
			},
		},
	}
}

// EmitCompensationEvents creates a response containing compensation events.
//
// Use when the aggregate emits events to record compensation.
// The framework will persist these events and NOT emit a system event.
func EmitCompensationEvents(events *pb.EventBook) *pb.BusinessResponse {
	return &pb.BusinessResponse{
		Result: &pb.BusinessResponse_Events{Events: events},
	}
}

// --- Process Manager helpers ---

// RejectionHandlerResponse is returned from rejection handlers.
// Can contain events (compensation), notification (upstream propagation), or both.
type RejectionHandlerResponse struct {
	// Events to persist to own state (compensation).
	Events *pb.EventBook

	// Notification to forward upstream (rejection propagation).
	Notification *pb.Notification
}

// PMRevocationResponse holds PM compensation results.
type PMRevocationResponse struct {
	// ProcessEvents contains PM events to persist (may be nil).
	ProcessEvents *pb.EventBook

	// RevocationResponse contains framework action flags.
	Revocation *pb.RevocationResponse
}

// PMDelegateToFramework creates a PM response that delegates compensation.
//
// Use when the PM doesn't have custom compensation logic.
func PMDelegateToFramework(reason string) *PMRevocationResponse {
	return &PMRevocationResponse{
		ProcessEvents: nil,
		Revocation: &pb.RevocationResponse{
			EmitSystemRevocation: true,
			Reason:               reason,
		},
	}
}

// PMEmitCompensationEvents creates a PM response with compensation events.
//
// Use when the PM emits events to record the failure in its state.
func PMEmitCompensationEvents(events *pb.EventBook, alsoEmitSystemEvent bool, reason string) *PMRevocationResponse {
	return &PMRevocationResponse{
		ProcessEvents: events,
		Revocation: &pb.RevocationResponse{
			EmitSystemRevocation: alsoEmitSystemEvent,
			Reason:               reason,
		},
	}
}

// --- Helper functions ---

// IsNotification checks if a command is a rejection Notification.
func IsNotification(typeURL string) bool {
	return typeURL == TypeURLPrefix+NotificationTypeName
}

// --- Compensation builders ---
//
// The saga/PM side of the compensation flow: when a downstream aggregate
// rejects a command, these builders carry the rejected command and its
// context back to the source aggregate. Mirrors Py/Rs compensation
// builders; shapes pinned by client/compensation.feature.

// RejectionNotificationFor builds the rejection payload from the rejected
// command and the rejection reason, preserving the command verbatim for
// debugging and FQ-keyed compensation dispatch.
func RejectionNotificationFor(rejectedCommand *pb.CommandBook, reason string) *pb.RejectionNotification {
	return &pb.RejectionNotification{
		RejectedCommand: rejectedCommand,
		RejectionReason: reason,
	}
}

// NotificationForRejection wraps a RejectionNotification into a routable
// Notification: the cover routes back to the SOURCE aggregate (from the
// rejected command's angzarr_deferred header), the correlation ID is
// preserved from the rejected command, and sent_at is stamped.
func NotificationForRejection(rejection *pb.RejectionNotification) (*pb.Notification, error) {
	payload, err := anypb.New(rejection)
	if err != nil {
		return nil, err
	}

	cover := &pb.Cover{}
	if cmd := rejection.GetRejectedCommand(); cmd != nil {
		if len(cmd.Pages) > 0 {
			if deferred := cmd.Pages[0].GetHeader().GetAngzarrDeferred(); deferred != nil && deferred.Source != nil {
				cover.Domain = deferred.Source.Domain
				cover.Root = deferred.Source.Root
				cover.Edition = deferred.Source.Edition
			}
		}
		if cmd.Cover != nil {
			cover.CorrelationId = cmd.Cover.CorrelationId
		}
	}

	return &pb.Notification{
		Cover:   cover,
		Payload: payload,
		SentAt:  timestamppb.Now(),
	}, nil
}

// NotificationCommandBook wraps a Notification as a CommandBook targeting
// the notification's cover (the source aggregate), so the framework can
// deliver the compensation signal through the ordinary command channel.
func NotificationCommandBook(notification *pb.Notification) (*pb.CommandBook, error) {
	packed, err := anypb.New(notification)
	if err != nil {
		return nil, err
	}
	return &pb.CommandBook{
		Cover: notification.Cover,
		Pages: []*pb.CommandPage{
			{Payload: &pb.CommandPage_Command{Command: packed}},
		},
	}, nil
}
