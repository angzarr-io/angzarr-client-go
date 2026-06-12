package features

// rejection.go — step bindings for client/rejection.feature (C-0040..C-0042),
// driven through the REAL engine AggregateDispatch in-process (no gRPC, no
// mocks): compensation handlers register by fully-qualified rejected-command
// type; a Notification command page routes to every registered compensator
// in registration order.
//
// Also owns the shared phrases "a rejection of ReserveStock arrives from
// inventory" and "no events are emitted" (used by
// rejected_compensation.feature too).

import (
	"fmt"

	angzarr "github.com/benjaminabbitt/angzarr/client/go"
	pb "github.com/benjaminabbitt/angzarr/client/go/proto/angzarr_client/proto/angzarr/v1"
	"github.com/cucumber/godog"
	"google.golang.org/protobuf/types/known/anypb"
)

const (
	fqReserveStock   = "test.ReserveStock"
	fqProcessPayment = "test.ProcessPayment"
	fqFundsReleased  = "test.FundsReleased"
)

// rejectionState is the Payment component's aggregate state. Bankroll is
// exercised by rejected_compensation.feature's stateful scenarios.
type rejectionState struct {
	Bankroll uint32
}

// RejectionContext holds per-scenario state for rejection.feature and
// rejected_compensation.feature (they share the arriving-rejection
// phrases, so they share one harness).
type RejectionContext struct {
	domain      string
	compensated int // registered compensators
	rebuilder   *angzarr.Rebuilder[*rejectionState]
	table       *angzarr.AggregateDispatch[*rejectionState]
	lastResp    *pb.BusinessResponse
	lastErr     error
	priorEvents *pb.EventBook
}

// currentRejection is the active scenario's harness. Both Init functions
// run per scenario; rejection.go's runs first and refreshes this.
var currentRejection *RejectionContext

func newRejectionContext() *RejectionContext {
	c := &RejectionContext{}
	currentRejection = c
	return c
}

func fundsReleasedPage() *pb.EventPage {
	return &pb.EventPage{Payload: &pb.EventPage_Event{Event: &anypb.Any{
		TypeUrl: angzarr.TypeURLPrefix + fqFundsReleased,
	}}}
}

func (c *RejectionContext) releaseFunds(*pb.Notification, *pb.RejectionNotification, *rejectionState, angzarr.CommandContext) (*pb.BusinessResponse, error) {
	return &pb.BusinessResponse{Result: &pb.BusinessResponse_Events{
		Events: &pb.EventBook{Pages: []*pb.EventPage{fundsReleasedPage()}},
	}}, nil
}

func (c *RejectionContext) givenComponentInDomain(domain string) error {
	c.domain = domain
	c.rebuilder = angzarr.NewRebuilder(func() *rejectionState { return &rejectionState{} })
	c.table = angzarr.NewAggregateDispatch("Payment", domain, c.rebuilder)
	return nil
}

func (c *RejectionContext) givenCompensatesReserveStock() error {
	c.table.OnRejected(fqReserveStock, c.releaseFunds)
	c.compensated++
	return nil
}

func (c *RejectionContext) whenRejectionArrives(fqCommand string) error {
	rejection := &pb.RejectionNotification{
		RejectedCommand: &pb.CommandBook{
			Cover: &pb.Cover{Domain: "inventory"},
			Pages: []*pb.CommandPage{
				{Payload: &pb.CommandPage_Command{Command: &anypb.Any{
					TypeUrl: angzarr.TypeURLPrefix + fqCommand,
				}}},
			},
		},
	}
	payload, err := anypb.New(rejection)
	if err != nil {
		return err
	}
	notification, err := anypb.New(&pb.Notification{Payload: payload})
	if err != nil {
		return err
	}
	c.lastResp, c.lastErr = c.table.Dispatch(&pb.ContextualCommand{
		Command: &pb.CommandBook{
			Cover: &pb.Cover{Domain: c.domain},
			Pages: []*pb.CommandPage{
				{Payload: &pb.CommandPage_Command{Command: notification}},
			},
		},
		Events: c.priorEvents,
	})
	return nil
}

func (c *RejectionContext) emittedPages() []*pb.EventPage {
	if c.lastResp == nil || c.lastResp.GetEvents() == nil {
		return nil
	}
	return c.lastResp.GetEvents().Pages
}

func (c *RejectionContext) thenFundsReleasedEmitted() error {
	if c.lastErr != nil {
		return fmt.Errorf("dispatch failed: %v", c.lastErr)
	}
	pages := c.emittedPages()
	if len(pages) != 1 {
		return fmt.Errorf("emitted %d events, want 1", len(pages))
	}
	if got := pages[0].GetEvent().GetTypeUrl(); got != angzarr.TypeURLPrefix+fqFundsReleased {
		return fmt.Errorf("emitted %q, want FundsReleased", got)
	}
	return nil
}

func (c *RejectionContext) thenNoEventsEmitted() error {
	if c.lastErr != nil {
		return fmt.Errorf("dispatch failed: %v", c.lastErr)
	}
	if pages := c.emittedPages(); len(pages) != 0 {
		return fmt.Errorf("emitted %d events, want none (silence, not error)", len(pages))
	}
	return nil
}

func (c *RejectionContext) thenTwoFundsReleasedInOrder() error {
	if c.lastErr != nil {
		return fmt.Errorf("dispatch failed: %v", c.lastErr)
	}
	pages := c.emittedPages()
	if len(pages) != 2 {
		return fmt.Errorf("emitted %d events, want 2 (C-0042 fan-out)", len(pages))
	}
	for i, page := range pages {
		if got := page.GetEvent().GetTypeUrl(); got != angzarr.TypeURLPrefix+fqFundsReleased {
			return fmt.Errorf("event %d is %q, want FundsReleased", i, got)
		}
	}
	return nil
}

// InitRejectionSteps registers rejection.feature step definitions. Wired
// into features_test.go's InitializeScenario.
func InitRejectionSteps(ctx *godog.ScenarioContext) {
	c := newRejectionContext()

	// --- Background ---
	ctx.Step(`^Payment is a component in domain "([^"]*)"$`, c.givenComponentInDomain)
	ctx.Step(`^Payment compensates a rejected ReserveStock from inventory by releasing funds$`, c.givenCompensatesReserveStock)
	ctx.Step(`^Payment is the active component$`, func() error {
		if c.table == nil {
			return fmt.Errorf("no component configured")
		}
		return nil
	})

	// --- When: arriving rejections ---
	ctx.Step(`^a rejection of ReserveStock arrives from inventory$`, func() error {
		return c.whenRejectionArrives(fqReserveStock)
	})
	ctx.Step(`^a rejection of ProcessPayment arrives from inventory$`, func() error {
		return c.whenRejectionArrives(fqProcessPayment)
	})

	// --- Then: emitted events ---
	ctx.Step(`^a FundsReleased event is emitted$`, c.thenFundsReleasedEmitted)
	ctx.Step(`^no events are emitted$`, c.thenNoEventsEmitted)
	ctx.Step(`^two FundsReleased events are emitted in registration order$`, c.thenTwoFundsReleasedInOrder)

	// --- Fan-out compensation (C-0042) ---
	ctx.Step(`^a second compensation handler for the same rejection also releases funds$`, c.givenCompensatesReserveStock)
	ctx.Step(`^Payment then Payment2 are configured$`, func() error {
		if c.compensated < 2 {
			return fmt.Errorf("expected two compensators registered, have %d", c.compensated)
		}
		return nil
	})
}
