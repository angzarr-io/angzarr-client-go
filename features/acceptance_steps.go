//go:build acceptance

package features

// Step bindings for the acceptance hook. Every phrase the unit tier
// registers as pending (coordinator/bus-side semantics) gets its real
// implementation here, driving the deployed coordinator through the real
// client. Scaffolded pending until the cluster-side test domain lands —
// each step graduates as the harness grows.

import (
	"github.com/cucumber/godog"
)

// AcceptanceContext will hold the real client wiring (CommandHandlerClient
// / QueryClient / SpeculativeClient dialed at ANGZARR_ACCEPTANCE_ENDPOINT)
// plus per-scenario state once the cluster test domain lands.
type AcceptanceContext struct{}

// InitAcceptanceSteps registers the coordinator/bus-side step bindings.
func InitAcceptanceSteps(ctx *godog.ScenarioContext) {
	pending := func() error { return godog.ErrPending }
	for _, phrase := range []string{
		`^an aggregate at sequence (\d+)$`,
		`^an aggregate whose recorded history cannot be replayed$`,
		`^an event of type "([^"]*)" is published$`,
		`^a PM router$`,
		`^a process manager invoked with correlation ID "([^"]*)"$`,
		`^a process manager with (\d+) prior events persisted$`,
		`^a projector router$`,
		`^a router$`,
		`^a saga router$`,
		`^a snapshot at sequence (\d+)$`,
		`^a subscription to event type "([^"]*)"$`,
		`^an aggregate router$`,
		`^events (\d+), (\d+), (\d+)$`,
		`^events from sequence (\d+) to (\d+) are delivered again$`,
		`^events up to sequence (\d+) have been processed$`,
		`^events whose final dotted token is "([^"]*)" should match$`,
		`^events whose final dotted token is "([^"]*)" should NOT match$`,
		`^events with different correlation IDs should have separate state$`,
		`^I build state$`,
		`^I receive a command at sequence (\d+)$`,
		`^I receive a command for that aggregate$`,
		`^I receive an event without correlation ID$`,
		`^I receive correlated events with ID "([^"]*)"$`,
		`^I register handler for type "([^"]*)"$`,
		`^I speculatively process events$`,
		`^no external side effects should occur$`,
		`^no handler should be invoked$`,
		`^state should be maintained across events$`,
		`^the bus receives exactly (\d+) events$`,
		`^the command should be rejected with a sequence mismatch$`,
		`^the command should fail$`,
		`^the command should have correct saga_origin$`,
		`^the command should preserve correlation ID$`,
		`^the coordinator publishes the events$`,
		`^the (\d+) prior events are NOT re-fired$`,
		`^the event should be skipped$`,
		`^the PM handler emits (\d+) new events$`,
		`^the PM handler returns events with a blank cover correlation ID$`,
		`^the projection result should be returned$`,
		`^the projection should not change$`,
		`^the published cover carries correlation ID "([^"]*)"$`,
		`^the resulting state should reflect the snapshot with events (\d+) through (\d+) applied$`,
		`^the router should return the command$`,
		`^the subscriber does NOT receive it$`,
		`^the subscriber receives it$`,
		`^a handler produces a command$`,
	} {
		ctx.Step(phrase, pending)
	}
}
