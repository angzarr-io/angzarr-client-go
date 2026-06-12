package features

// compensation.go — step bindings for client/compensation.feature, driven
// through the REAL SDK compensation surface (angzarr.CompensationContext +
// the rejection-notification builders). The former shadow taxonomy
// (local SagaOrigin/CompensationCtx/RejectionNotification structs) is
// gone: steps assert against wire types only.

import (
	"fmt"
	"time"

	angzarr "github.com/benjaminabbitt/angzarr/client/go"
	pb "github.com/benjaminabbitt/angzarr/client/go/proto/angzarr_client/proto/angzarr/v1"
	"github.com/cucumber/godog"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
)

// CompensationSteps holds per-scenario state for compensation.feature.
type CompensationSteps struct {
	rejectedCommand *pb.CommandBook
	reason          string

	context      *angzarr.CompensationContext
	rejection    *pb.RejectionNotification
	notification *pb.Notification
	commandBook  *pb.CommandBook
}

func newCompensationSteps() *CompensationSteps {
	return &CompensationSteps{}
}

// sagaCommand builds a rejected saga command carrying full routing context:
// the command targets a downstream domain while its angzarr_deferred header
// records the SOURCE aggregate, triggering sequence, and issuing component.
func sagaCommand(saga, sourceDomain string, sourceRoot []byte, sourceSeq uint32, correlationID string) *pb.CommandBook {
	return &pb.CommandBook{
		Cover: &pb.Cover{Domain: "inventory", CorrelationId: correlationID},
		Pages: []*pb.CommandPage{
			{
				Header: &pb.PageHeader{
					SequenceType: &pb.PageHeader_AngzarrDeferred{AngzarrDeferred: &pb.AngzarrDeferredSequence{
						Source:          &pb.Cover{Domain: sourceDomain, Root: &pb.UUID{Value: sourceRoot}},
						SourceSeq:       sourceSeq,
						SourceComponent: saga,
					}},
				},
				Payload: &pb.CommandPage_Command{Command: &anypb.Any{
					TypeUrl: angzarr.TypeURLPrefix + fqReserveStock,
				}},
			},
		},
	}
}

func (c *CompensationSteps) reject(reason string) {
	c.reason = reason
	c.rejection = angzarr.RejectionNotificationFor(c.rejectedCommand, reason)
}

func (c *CompensationSteps) buildContext() error {
	payload, err := anypb.New(c.rejection)
	if err != nil {
		return err
	}
	c.context = angzarr.NewCompensationContext(&pb.Notification{Payload: payload})
	return nil
}

func (c *CompensationSteps) buildNotification() error {
	if c.rejection == nil {
		c.reject("rejected")
	}
	n, err := angzarr.NotificationForRejection(c.rejection)
	if err != nil {
		return err
	}
	c.notification = n
	return nil
}

func (c *CompensationSteps) deferredHeader() *pb.AngzarrDeferredSequence {
	if c.rejection.GetRejectedCommand() == nil || len(c.rejection.GetRejectedCommand().Pages) == 0 {
		return nil
	}
	return c.rejection.GetRejectedCommand().Pages[0].GetHeader().GetAngzarrDeferred()
}

// InitCompensationSteps registers compensation step definitions.
func InitCompensationSteps(ctx *godog.ScenarioContext) {
	c := newCompensationSteps()

	// --- Background / givens ---
	ctx.Step(`^a compensation handling context$`, func() error { return nil })
	ctx.Step(`^a saga command that was rejected$`, func() error {
		c.rejectedCommand = sagaCommand("order-fulfillment", "orders", []byte("order-root-1"), 5, "corr-1")
		c.reject("out_of_stock")
		return nil
	})
	ctx.Step(`^a saga "([^"]*)" triggered by "([^"]*)" aggregate at sequence (\d+)$`, func(saga, domain string, seq int) error {
		c.rejectedCommand = sagaCommand(saga, domain, []byte("root"), uint32(seq), "corr-1")
		return nil
	})
	ctx.Step(`^the saga command was rejected$`, func() error {
		c.reject("rejected")
		return nil
	})
	ctx.Step(`^a saga command with correlation ID "([^"]*)"$`, func(cid string) error {
		c.rejectedCommand = sagaCommand("order-fulfillment", "orders", []byte("root"), 1, cid)
		return nil
	})
	ctx.Step(`^the command was rejected$`, func() error {
		c.reject("rejected")
		return nil
	})
	ctx.Step(`^a CompensationContext for rejected command$`, func() error {
		c.rejectedCommand = sagaCommand("order-fulfillment", "orders", []byte("order-root-1"), 5, "corr-1")
		c.reject("out_of_stock")
		return c.buildContext()
	})
	ctx.Step(`^a CompensationContext from "([^"]*)" aggregate at sequence (\d+)$`, func(domain string, seq int) error {
		c.rejectedCommand = sagaCommand("order-fulfillment", domain, []byte("root"), uint32(seq), "corr-1")
		c.reject("rejected")
		return c.buildContext()
	})
	ctx.Step(`^a CompensationContext from saga "([^"]*)"$`, func(saga string) error {
		c.rejectedCommand = sagaCommand(saga, "orders", []byte("root"), 1, "corr-1")
		c.reject("rejected")
		return c.buildContext()
	})
	ctx.Step(`^a CompensationContext from "([^"]*)" aggregate root "([^"]*)"$`, func(domain, root string) error {
		c.rejectedCommand = sagaCommand("order-fulfillment", domain, []byte(root), 1, "corr-1")
		c.reject("rejected")
		return c.buildContext()
	})
	ctx.Step(`^a command rejected with reason "([^"]*)"$`, func(reason string) error {
		c.rejectedCommand = sagaCommand("order-fulfillment", "orders", []byte("root"), 1, "corr-1")
		c.reject(reason)
		return nil
	})
	ctx.Step(`^a command rejected with structured reason$`, func() error {
		c.rejectedCommand = sagaCommand("order-fulfillment", "orders", []byte("root"), 1, "corr-1")
		c.reject(`{"code":"INSUFFICIENT_FUNDS","required":100,"available":40}`)
		return nil
	})
	ctx.Step(`^a saga command with specific payload$`, func() error {
		c.rejectedCommand = sagaCommand("order-fulfillment", "orders", []byte("root"), 9, "corr-payload")
		return nil
	})
	ctx.Step(`^a nested saga scenario$`, func() error {
		// Inner saga's command: its deferred header records the INNER
		// origin; preserving the command verbatim preserves the chain.
		c.rejectedCommand = sagaCommand("inner-saga", "middle", []byte("middle-root"), 3, "corr-chain")
		return nil
	})
	ctx.Step(`^an inner saga command was rejected$`, func() error {
		c.reject("inner rejection")
		return nil
	})

	// --- Whens ---
	ctx.Step(`^the compensation context is constructed from the rejection$`, c.buildContext)
	ctx.Step(`^I build a RejectionNotification$`, func() error {
		if c.rejection == nil {
			c.reject("rejected")
		}
		return nil
	})
	ctx.Step(`^I build a Notification from the context$`, c.buildNotification)
	ctx.Step(`^I build a Notification from a CompensationContext$`, func() error {
		if c.rejectedCommand == nil {
			c.rejectedCommand = sagaCommand("order-fulfillment", "orders", []byte("root"), 1, "corr-1")
		}
		return c.buildNotification()
	})
	ctx.Step(`^I build a notification CommandBook$`, func() error {
		if err := c.buildNotification(); err != nil {
			return err
		}
		book, err := angzarr.NotificationCommandBook(c.notification)
		if err != nil {
			return err
		}
		c.commandBook = book
		return nil
	})

	// --- Thens: context ---
	ctx.Step(`^the context carries the rejected command$`, func() error {
		if c.context.RejectedCommand == nil {
			return fmt.Errorf("context lost the rejected command")
		}
		return nil
	})
	ctx.Step(`^the context carries the rejection reason$`, func() error {
		if c.context.RejectionReason != c.reason {
			return fmt.Errorf("reason = %q, want %q", c.context.RejectionReason, c.reason)
		}
		return nil
	})
	ctx.Step(`^the context carries the saga origin$`, func() error {
		if c.context.SourceAggregate == nil || c.context.SourceAggregate.Domain == "" {
			return fmt.Errorf("context lost the saga origin")
		}
		return nil
	})
	ctx.Step(`^the saga origin is preserved$`, func() error {
		if got := c.context.SourceAggregate.GetDomain(); got != "orders" {
			return fmt.Errorf("origin domain = %q, want orders", got)
		}
		if c.context.SourceEventSequence != 5 {
			return fmt.Errorf("origin sequence = %d, want 5", c.context.SourceEventSequence)
		}
		return nil
	})
	ctx.Step(`^the correlation ID is preserved$`, func() error {
		if got := c.context.RejectedCommand.GetCover().GetCorrelationId(); got != "workflow-123" {
			return fmt.Errorf("correlation = %q, want workflow-123", got)
		}
		return nil
	})

	// --- Thens: rejection notification ---
	ctx.Step(`^the notification carries the rejected command$`, func() error {
		if !proto.Equal(c.rejection.GetRejectedCommand(), c.rejectedCommand) {
			return fmt.Errorf("rejected command not preserved")
		}
		return nil
	})
	ctx.Step(`^the notification carries the rejection reason$`, func() error {
		if c.rejection.GetRejectionReason() != c.reason {
			return fmt.Errorf("reason = %q, want %q", c.rejection.GetRejectionReason(), c.reason)
		}
		return nil
	})
	ctx.Step(`^the source aggregate and sequence are recorded$`, func() error {
		deferred := c.deferredHeader()
		if deferred.GetSource().GetDomain() != "orders" || deferred.GetSourceSeq() != 5 {
			return fmt.Errorf("source = %v seq %d, want orders/5", deferred.GetSource(), deferred.GetSourceSeq())
		}
		return nil
	})
	ctx.Step(`^the notification identifies the issuing saga as "([^"]*)"$`, func(saga string) error {
		if got := c.deferredHeader().GetSourceComponent(); got != saga {
			return fmt.Errorf("issuer = %q, want %q", got, saga)
		}
		return nil
	})
	ctx.Step(`^the rejection reason equals "([^"]*)"$`, func(reason string) error {
		if c.rejection.GetRejectionReason() != reason {
			return fmt.Errorf("reason = %q, want %q", c.rejection.GetRejectionReason(), reason)
		}
		return nil
	})
	ctx.Step(`^the rejection reason carries the full error details$`, func() error {
		if c.rejection.GetRejectionReason() != c.reason || c.reason == "" {
			return fmt.Errorf("structured reason not preserved: %q", c.rejection.GetRejectionReason())
		}
		return nil
	})
	ctx.Step(`^the rejected command is the original command$`, func() error {
		if !proto.Equal(c.rejection.GetRejectedCommand(), c.rejectedCommand) {
			return fmt.Errorf("rejected command differs from the original")
		}
		return nil
	})
	ctx.Step(`^all command fields are preserved$`, func() error {
		got := c.rejection.GetRejectedCommand()
		if got.GetCover().GetCorrelationId() != c.rejectedCommand.GetCover().GetCorrelationId() ||
			len(got.GetPages()) != len(c.rejectedCommand.GetPages()) {
			return fmt.Errorf("command fields lost")
		}
		return nil
	})
	ctx.Step(`^the full saga origin chain is preserved$`, func() error {
		deferred := c.deferredHeader()
		if deferred.GetSourceComponent() != "inner-saga" || deferred.GetSource().GetDomain() != "middle" {
			return fmt.Errorf("inner origin lost: %v", deferred)
		}
		return nil
	})
	ctx.Step(`^the root cause can be traced through the chain$`, func() error {
		if c.rejection.GetRejectionReason() == "" || c.deferredHeader().GetSourceSeq() != 3 {
			return fmt.Errorf("chain trace incomplete")
		}
		return nil
	})

	// --- Thens: wrapped Notification ---
	ctx.Step(`^the notification has a cover$`, func() error {
		if c.notification.GetCover() == nil || c.notification.GetCover().GetDomain() == "" {
			return fmt.Errorf("notification lacks a routing cover")
		}
		return nil
	})
	ctx.Step(`^the notification payload contains a RejectionNotification$`, func() error {
		unpacked := &pb.RejectionNotification{}
		if err := c.notification.GetPayload().UnmarshalTo(unpacked); err != nil {
			return fmt.Errorf("payload is not a RejectionNotification: %v", err)
		}
		return nil
	})
	ctx.Step(`^the notification carries its dispatch time$`, func() error {
		sent := c.notification.GetSentAt()
		if sent == nil {
			return fmt.Errorf("sent_at missing")
		}
		if d := time.Since(sent.AsTime()); d < 0 || d > time.Minute {
			return fmt.Errorf("sent_at not recent: %v", d)
		}
		return nil
	})

	// --- Thens: notification CommandBook ---
	ctx.Step(`^the command book targets the source aggregate$`, func() error {
		if got := c.commandBook.GetCover().GetDomain(); got != "orders" {
			return fmt.Errorf("command book targets %q, want the source aggregate (orders)", got)
		}
		return nil
	})
	ctx.Step(`^the command book preserves the correlation ID$`, func() error {
		if got := c.commandBook.GetCover().GetCorrelationId(); got != "corr-1" {
			return fmt.Errorf("correlation = %q, want corr-1", got)
		}
		return nil
	})

	// --- Router-driven compensation notifications ---
	ctx.Step(`^a saga router handling rejections$`, func() error {
		c.rejectedCommand = sagaCommand("order-fulfillment", "orders", []byte("root"), 2, "corr-r")
		return nil
	})
	ctx.Step(`^a command execution fails with precondition error$`, func() error {
		rejErr := angzarr.NewCommandRejectedError("precondition failed")
		c.reject(rejErr.Error())
		return c.buildNotification()
	})
	ctx.Step(`^saga rejections produce a compensation notification$`, func() error {
		if c.notification == nil || c.notification.GetPayload() == nil {
			return fmt.Errorf("no compensation notification produced")
		}
		return nil
	})
	ctx.Step(`^a process manager router$`, func() error {
		c.rejectedCommand = sagaCommand("fulfillment-pm", "orders", []byte("root"), 2, "corr-pm")
		return nil
	})
	ctx.Step(`^a PM command is rejected$`, func() error {
		c.reject("pm rejection")
		return c.buildNotification()
	})
	ctx.Step(`^process manager rejections produce a compensation notification$`, func() error {
		if c.notification == nil || c.notification.GetPayload() == nil {
			return fmt.Errorf("no compensation notification produced")
		}
		return nil
	})
}
