package features

// command_handler.go — step bindings for client/command_handler.feature
// (C-0001..C-0006, C-0085, C-0146..C-0148), driven through the REAL
// engine AggregateDispatch. The table is rebuilt from recorded config at
// dispatch time so state-factory givens can override the Background.
//
// NOTE: the Background phrase `^a command handler "Order" for domain
// "..." with order state$` is registered by builder.go (shared with
// builder.feature); it delegates to currentCommandHandler.reset.
//
// C-0003 reconciliation: "rejected as invalid input" asserts the coded
// NO_HANDLER_REGISTERED ClientError (locally invalid-argument-kinded);
// on the wire it maps to UNIMPLEMENTED per the decided status table.

import (
	"fmt"

	angzarr "github.com/benjaminabbitt/angzarr/client/go"
	pb "github.com/benjaminabbitt/angzarr/client/go/proto/angzarr_client/proto/angzarr/v1"
	"github.com/cucumber/godog"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
)

const (
	fqCHCreateOrder   = "test.ch.CreateOrder"
	fqCHCompleteOrder = "test.ch.CompleteOrder"
	fqCHOrderCreated  = "test.ch.OrderCreated"
)

// chOrderState is the Order aggregate's state.
type chOrderState struct {
	created bool
}

// CommandHandlerContext holds per-scenario state.
type CommandHandlerContext struct {
	domain  string
	factory func() *chOrderState

	applyCreated bool // OrderCreated applier registered
	handlerKind  string
	handlerExt   *anypb.Any // handler-set EventBook cover ext
	observed     *bool      // handler's view of state.created
	priorEvents  *pb.EventBook
	commandExt   *anypb.Any

	resp *pb.BusinessResponse
	err  error
}

// currentCommandHandler is the active scenario's harness (builder.go's
// shared Background phrase delegates here).
var currentCommandHandler *CommandHandlerContext

func newCommandHandlerContext() *CommandHandlerContext {
	c := &CommandHandlerContext{}
	currentCommandHandler = c
	return c
}

// reset configures the Order handler for a domain (shared Background).
func (c *CommandHandlerContext) reset(domain string) {
	c.domain = domain
	c.factory = func() *chOrderState { return &chOrderState{} }
	c.handlerKind = "emit"
}

// buildTable assembles the dispatch table from the recorded config.
func (c *CommandHandlerContext) buildTable() *angzarr.AggregateDispatch[*chOrderState] {
	rebuilder := angzarr.NewRebuilder(c.factory)
	if c.applyCreated {
		rebuilder.Apply(fqCHOrderCreated, func(state *chOrderState, _ *anypb.Any) error {
			state.created = true
			return nil
		})
	}
	table := angzarr.NewAggregateDispatch("Order", c.domain, rebuilder)
	table.OnCommand(fqCHCreateOrder, func(_ *anypb.Any, state *chOrderState, cctx angzarr.CommandContext) (*pb.EventBook, error) {
		observed := state.created
		c.observed = &observed
		switch c.handlerKind {
		case "none":
			return nil, nil
		case "emit-if-created":
			if !state.created {
				return nil, nil
			}
		case "read-only":
			return nil, nil
		}
		book := &pb.EventBook{Pages: []*pb.EventPage{
			{Payload: &pb.EventPage_Event{Event: &anypb.Any{TypeUrl: angzarr.TypeURLPrefix + fqCHOrderCreated}}},
		}}
		if c.handlerExt != nil {
			book.Cover = &pb.Cover{Ext: c.handlerExt}
		}
		return book, nil
	})
	return table
}

func (c *CommandHandlerContext) dispatch(fqType string) error {
	cmd := &pb.ContextualCommand{
		Command: &pb.CommandBook{
			Cover: &pb.Cover{Domain: c.domain, Ext: c.commandExt},
			Pages: []*pb.CommandPage{
				{Payload: &pb.CommandPage_Command{Command: &anypb.Any{TypeUrl: angzarr.TypeURLPrefix + fqType}}},
			},
		},
		Events: c.priorEvents,
	}
	c.resp, c.err = c.buildTable().Dispatch(cmd)
	return nil
}

// InitCommandHandlerSteps registers command_handler.feature steps.
func InitCommandHandlerSteps(ctx *godog.ScenarioContext) {
	c := newCommandHandlerContext()

	// --- Background / configuration ---
	ctx.Step(`^OrderCreated marks the order as created$`, func() error {
		c.applyCreated = true
		return nil
	})
	ctx.Step(`^CreateOrder emits OrderCreated$`, func() error {
		c.handlerKind = "emit"
		return nil
	})
	ctx.Step(`^Order is the active aggregate handler$`, func() error {
		if c.domain == "" {
			return fmt.Errorf("no handler configured")
		}
		return nil
	})
	ctx.Step(`^a command handler whose handler returns None for CreateOrder$`, func() error {
		c.handlerKind = "none"
		return nil
	})
	ctx.Step(`^the aggregate supplies its own initial state with created = true$`, func() error {
		c.factory = func() *chOrderState { return &chOrderState{created: true} }
		return nil
	})
	ctx.Step(`^the aggregate does not supply its own initial state$`, func() error {
		c.factory = func() *chOrderState { return &chOrderState{} }
		return nil
	})
	ctx.Step(`^Order handles CreateOrder by emitting OrderCreated only when the order is already created$`, func() error {
		c.handlerKind = "emit-if-created"
		return nil
	})
	ctx.Step(`^Order handles CreateOrder by reading whether the order is created$`, func() error {
		c.handlerKind = "read-only"
		return nil
	})

	// --- Prior history ---
	ctx.Step(`^a prior history with an OrderCreated event at sequence (\d+)$`, func(seq int) error {
		c.priorEvents = &pb.EventBook{
			NextSequence: uint32(seq) + 1,
			Pages: []*pb.EventPage{
				{
					Header:  &pb.PageHeader{SequenceType: &pb.PageHeader_Sequence{Sequence: uint32(seq)}},
					Payload: &pb.EventPage_Event{Event: &anypb.Any{TypeUrl: angzarr.TypeURLPrefix + fqCHOrderCreated}},
				},
			},
		}
		return nil
	})
	ctx.Step(`^no prior events in the incoming ContextualCommand$`, func() error {
		c.priorEvents = nil
		return nil
	})

	// --- Cover.ext givens (C-0146..C-0148) ---
	ctx.Step(`^the incoming command has cover\.ext set to a packed parent Cover$`, func() error {
		packed, err := anypb.New(&pb.Cover{Domain: "parent"})
		if err != nil {
			return err
		}
		c.commandExt = packed
		return nil
	})
	ctx.Step(`^a command handler whose emit step sets EventBook cover\.ext explicitly$`, func() error {
		packed, err := anypb.New(&pb.Cover{Domain: "handler-set"})
		if err != nil {
			return err
		}
		c.handlerExt = packed
		return nil
	})
	ctx.Step(`^the incoming command also has a different cover\.ext set$`, func() error {
		packed, err := anypb.New(&pb.Cover{Domain: "command-set"})
		if err != nil {
			return err
		}
		c.commandExt = packed
		return nil
	})
	ctx.Step(`^the incoming command's cover has no ext field set$`, func() error {
		c.commandExt = nil
		return nil
	})

	// --- Whens ---
	ctx.Step(`^CreateOrder\(order_id="([^"]*)"\) is dispatched$`, func(string) error {
		return c.dispatch(fqCHCreateOrder)
	})
	ctx.Step(`^a command is dispatched against the aggregate$`, func() error {
		return c.dispatch(fqCHCreateOrder)
	})
	ctx.Step(`^CompleteOrder\(order_id="([^"]*)"\) is dispatched$`, func(string) error {
		return c.dispatch(fqCHCompleteOrder)
	})

	// --- Thens ---
	ctx.Step(`^the response emits an OrderCreated event$`, func() error {
		if c.err != nil {
			return fmt.Errorf("dispatch failed: %v", c.err)
		}
		pages := c.resp.GetEvents().GetPages()
		if len(pages) != 1 || pages[0].GetEvent().GetTypeUrl() != angzarr.TypeURLPrefix+fqCHOrderCreated {
			return fmt.Errorf("emitted %v, want one OrderCreated", pages)
		}
		return nil
	})
	ctx.Step(`^the emitted event sequence is (\d+)$`, func(seq int) error {
		pages := c.resp.GetEvents().GetPages()
		if got := pages[0].GetHeader().GetSequence(); got != uint32(seq) {
			return fmt.Errorf("sequence = %d, want %d (fill-only stamping, C-0001)", got, seq)
		}
		return nil
	})
	ctx.Step(`^the order is treated as already created$`, func() error {
		if c.observed == nil || !*c.observed {
			return fmt.Errorf("handler did not observe created state (C-0002 rebuild)")
		}
		return nil
	})
	ctx.Step(`^the unknown command is rejected as invalid input$`, func() error {
		clientErr := angzarr.AsClientError(c.err)
		if clientErr == nil || clientErr.Code != angzarr.CodeNoHandlerRegistered {
			return fmt.Errorf("want coded %s, got %v", angzarr.CodeNoHandlerRegistered, c.err)
		}
		if !clientErr.IsInvalidArgument() {
			return fmt.Errorf("unknown command must read as invalid input locally")
		}
		return nil
	})
	ctx.Step(`^when the handler emits nothing, no events are produced$`, func() error {
		if c.err != nil {
			return fmt.Errorf("dispatch failed: %v", c.err)
		}
		if pages := c.resp.GetEvents().GetPages(); len(pages) != 0 {
			return fmt.Errorf("emitted %d events, want 0 (C-0004)", len(pages))
		}
		return nil
	})
	ctx.Step(`^the handler observes that the order is not created$`, func() error {
		if c.observed == nil {
			return fmt.Errorf("handler never ran")
		}
		if *c.observed {
			return fmt.Errorf("handler observed created=true, want default state (C-0006/C-0085)")
		}
		return nil
	})
	ctx.Step(`^the response's EventBook cover\.ext is the same packed parent Cover$`, func() error {
		got := c.resp.GetEvents().GetCover().GetExt()
		if !proto.Equal(got, c.commandExt) {
			return fmt.Errorf("ext = %v, want the command's packed parent (C-0146)", got)
		}
		return nil
	})
	ctx.Step(`^the response's EventBook cover\.ext is the handler-set value$`, func() error {
		got := c.resp.GetEvents().GetCover().GetExt()
		if !proto.Equal(got, c.handlerExt) {
			return fmt.Errorf("ext = %v, want handler-set value (C-0147 fill-only)", got)
		}
		return nil
	})
	ctx.Step(`^the response's EventBook cover has no ext field set$`, func() error {
		if got := c.resp.GetEvents().GetCover().GetExt(); got != nil {
			return fmt.Errorf("ext = %v, want unset (C-0148)", got)
		}
		return nil
	})
}
