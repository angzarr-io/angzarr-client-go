package features

// builder.go — step bindings for client/builder.feature (C-0060..C-0065,
// C-0088), driven through the REAL engine RouterBuilder: empty configs,
// unrecognised components, mixed kinds, and duplicate command claims all
// fail at BUILD time with coded errors; valid configs produce the
// appropriate composed router.
//
// Owns the shared Background phrase `a command handler "Order" for domain
// "..." with order state` (delegates to currentCommandHandler.reset —
// command_handler.feature shares it).

import (
	"fmt"

	angzarr "github.com/benjaminabbitt/angzarr/client/go"
	pb "github.com/benjaminabbitt/angzarr/client/go/proto/angzarr_client/proto/angzarr/v1"
	"github.com/cucumber/godog"
	"google.golang.org/protobuf/types/known/anypb"
)

// introspectionCounter wraps a CommandDispatcher counting CommandTypes
// calls (spec C-0065: build introspects each handler exactly once).
type introspectionCounter struct {
	angzarr.CommandDispatcher
	calls int
}

func (c *introspectionCounter) CommandTypes() []string {
	c.calls++
	return c.CommandDispatcher.CommandTypes()
}

// BuilderContext holds per-scenario state for builder.feature.
type BuilderContext struct {
	builder *angzarr.RouterBuilder
	counter *introspectionCounter
	router  *angzarr.Router
	err     error
}

func newBuilderContext() *BuilderContext {
	return &BuilderContext{builder: angzarr.NewRouterBuilder()}
}

func simpleCH(name, domain string, cmdTypes ...string) *angzarr.AggregateDispatch[*multiState] {
	table := angzarr.NewAggregateDispatch(name, domain,
		angzarr.NewRebuilder(func() *multiState { return &multiState{} }))
	for _, fqType := range cmdTypes {
		table.OnCommand(fqType, func(*anypb.Any, *multiState, angzarr.CommandContext) (*pb.EventBook, error) {
			return &pb.EventBook{}, nil
		})
	}
	return table
}

func (b *BuilderContext) expectCode(code string) error {
	clientErr := angzarr.AsClientError(b.err)
	if clientErr == nil || clientErr.Code != code {
		return fmt.Errorf("want coded %s at build, got %v", code, b.err)
	}
	return nil
}

// InitBuilderSteps registers builder.feature step definitions.
func InitBuilderSteps(ctx *godog.ScenarioContext) {
	b := newBuilderContext()
	var registerOrder, registerSaga bool

	// --- Givens ---
	ctx.Step(`^an empty handler configuration$`, func() error { return nil })
	ctx.Step(`^a component that has not been marked as a handler kind$`, func() error {
		return nil // registered (and rejected) by the attempt step
	})
	// Shared with command_handler.feature (Background).
	ctx.Step(`^a command handler "Order" for domain "([^"]*)" with order state$`, func(domain string) error {
		currentCommandHandler.reset(domain)
		registerOrder = true
		return nil
	})
	ctx.Step(`^another command handler "Payment" for domain "([^"]*)" with payment state$`, func(domain string) error {
		b.builder.Register(simpleCH("Payment", domain, "test.ProcessPayment"))
		return nil
	})
	ctx.Step(`^two command handlers Alpha and Beta for domain "([^"]*)" both handling the same command$`, func(domain string) error {
		b.builder.
			Register(simpleCH("Alpha", domain, fqCreateOrder)).
			Register(simpleCH("Beta", domain, fqCreateOrder))
		return nil
	})
	ctx.Step(`^the handler reports how many times it has been introspected$`, func() error {
		b.counter = &introspectionCounter{CommandDispatcher: currentCommandHandler.buildTable()}
		registerOrder = false // the counter takes Order's place
		return nil
	})

	// --- Whens ---
	ctx.Step(`^I build the router$`, func() error {
		if registerOrder {
			b.builder.Register(currentCommandHandler.buildTable())
		}
		if registerSaga || (currentSaga != nil && currentSaga.table != nil && !registerOrder) {
			// saga-only configuration (C-0088)
		}
		if currentSaga != nil && currentSaga.table != nil {
			b.builder.Register(currentSaga.table)
		}
		b.router, b.err = b.builder.Build()
		return nil
	})
	ctx.Step(`^I attempt to register it$`, func() error {
		b.router, b.err = b.builder.Register(struct{ unmarked bool }{}).Build()
		return nil
	})
	ctx.Step(`^I register the handler and build the router$`, func() error {
		b.router, b.err = b.builder.Register(b.counter).Build()
		return nil
	})

	// --- Thens ---
	ctx.Step(`^the configuration is rejected because no handlers are registered$`, func() error {
		return b.expectCode(angzarr.CodeRouterNoHandlers)
	})
	ctx.Step(`^the configuration is rejected because the component is not a recognised handler$`, func() error {
		return b.expectCode(angzarr.CodeHandlerUnknownKind)
	})
	ctx.Step(`^the result routes commands to their handlers$`, func() error {
		if b.err != nil {
			return fmt.Errorf("build failed: %v", b.err)
		}
		if b.router.CommandMux == nil {
			return fmt.Errorf("expected a command-routing router (C-0062)")
		}
		return nil
	})
	ctx.Step(`^the configuration is rejected for mixing handler kinds$`, func() error {
		return b.expectCode(angzarr.CodeMixedHandlerKinds)
	})
	ctx.Step(`^the configuration is rejected because two command handlers share the same domain and command$`, func() error {
		return b.expectCode(angzarr.CodeDuplicateCommandHandler)
	})
	ctx.Step(`^the handler has been introspected exactly once$`, func() error {
		if b.counter == nil {
			return fmt.Errorf("no counting handler configured")
		}
		if b.counter.calls != 1 {
			return fmt.Errorf("introspected %d times, want exactly 1 (C-0065)", b.counter.calls)
		}
		return nil
	})
	ctx.Step(`^the result routes saga notifications to their handlers$`, func() error {
		if b.err != nil {
			return fmt.Errorf("build failed: %v", b.err)
		}
		if b.router.SagaFanout == nil {
			return fmt.Errorf("expected a saga-routing router (C-0088)")
		}
		return nil
	})
}
