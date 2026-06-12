package features

// edition_propagation.go — step bindings for
// coordinator-contract/edition_propagation.feature (C-0138..C-0145).
//
// These scenarios pin the saga / process-manager coordinator's guarantee
// that emitted commands and events inherit the source / trigger cover's
// edition, including divergence preservation and coordinator-side override
// of handler-set editions. The Go client's coordinator-side enforcement is
// tracked in its own implementation task — these step bodies are FAIL
// stubs for now so the scenarios surface as work-in-progress until the
// real plumbing lands.

import (
	"fmt"

	"github.com/cucumber/godog"
)

// EditionPropagationContext holds per-scenario state for
// edition_propagation.feature. Currently empty — the WIP stubs do not need
// shared state. Kept so the future real implementation can attach the
// configured saga / PM, the source / trigger event, and the captured
// outgoing covers without disrupting the wire-up.
type EditionPropagationContext struct{}

func newEditionPropagationContext() *EditionPropagationContext {
	return &EditionPropagationContext{}
}

// InitEditionPropagationSteps registers edition_propagation.feature step
// definitions. Wired into features_test.go's InitializeScenario alongside
// the other Init*Steps callbacks.
func InitEditionPropagationSteps(ctx *godog.ScenarioContext) {
	_ = newEditionPropagationContext()

	// --- Given: saga / process-manager configuration ---

	// TODO (WIP): Implement this step matcher properly.
	ctx.Step(`^a saga "([^"]*)" translating from "([^"]*)" to "([^"]*)"$`, func(name, from, to string) error {
		currentSaga.configure(name, from, to)
		return nil
	})
	// TODO (WIP): Implement this step matcher properly.
	ctx.Step(`^the saga handles OrderCreated by emitting a ReserveStock command$`, func() error {
		currentSaga.handleOrderCreatedEmit(currentSaga.targets[0])
		return nil
	})
	// TODO (WIP): Implement this step matcher properly.
	ctx.Step(`^the saga handles OrderCreated by emitting an OrderObserved event$`, func() error {
		return fmt.Errorf("WIP: step needs implementation")
	})
	// TODO (WIP): Implement this step matcher properly.
	ctx.Step(`^a process manager "([^"]*)" with sources "([^"]*)" and targets "([^"]*)"$`, func(name, sources, targets string) error {
		return fmt.Errorf("WIP: step needs implementation")
	})
	// TODO (WIP): Implement this step matcher properly.
	ctx.Step(`^the PM also emits an OrderTracked process_event on OrderCreated$`, func() error {
		return fmt.Errorf("WIP: step needs implementation")
	})

	// --- Given: source / trigger edition configuration ---

	// TODO (WIP): Implement this step matcher properly.
	ctx.Step(`^the source event has edition "([^"]*)"$`, func(edition string) error {
		return fmt.Errorf("WIP: step needs implementation")
	})
	// TODO (WIP): Implement this step matcher properly.
	ctx.Step(`^the source event has no edition set$`, func() error {
		return fmt.Errorf("WIP: step needs implementation")
	})
	// TODO (WIP): Implement this step matcher properly.
	ctx.Step(`^the source event has edition "([^"]*)" with divergence at "([^"]*)"=(\d+)$`, func(edition, domain string, seq int) error {
		return fmt.Errorf("WIP: step needs implementation")
	})
	// TODO (WIP): Implement this step matcher properly.
	ctx.Step(`^the trigger event has edition "([^"]*)"$`, func(edition string) error {
		return fmt.Errorf("WIP: step needs implementation")
	})

	// --- Given: handler-set outgoing edition (coordinator-override scenarios) ---

	// TODO (WIP): Implement this step matcher properly.
	ctx.Step(`^the saga handler sets outgoing edition "([^"]*)"$`, func(edition string) error {
		return fmt.Errorf("WIP: step needs implementation")
	})
	// TODO (WIP): Implement this step matcher properly.
	ctx.Step(`^the PM handler sets outgoing edition "([^"]*)"$`, func(edition string) error {
		return fmt.Errorf("WIP: step needs implementation")
	})

	// --- When: dispatch source / trigger event ---

	// TODO (WIP): Implement this step matcher properly.
	ctx.Step(`^an OrderCreated event is dispatched to the saga$`, func() error {
		return fmt.Errorf("WIP: step needs implementation")
	})
	// TODO (WIP): Implement this step matcher properly.
	ctx.Step(`^an OrderCreated trigger is dispatched to the PM$`, func() error {
		return fmt.Errorf("WIP: step needs implementation")
	})

	// --- Then: edition propagation assertions on emitted / persisted covers ---

	// TODO (WIP): Implement this step matcher properly.
	ctx.Step(`^the emitted command's cover has edition "([^"]*)"$`, func(edition string) error {
		return fmt.Errorf("WIP: step needs implementation")
	})
	// TODO (WIP): Implement this step matcher properly.
	ctx.Step(`^the emitted event's cover has edition "([^"]*)"$`, func(edition string) error {
		return fmt.Errorf("WIP: step needs implementation")
	})
	// TODO (WIP): Implement this step matcher properly.
	ctx.Step(`^the emitted command's cover has no edition set$`, func() error {
		return fmt.Errorf("WIP: step needs implementation")
	})
	// TODO (WIP): Implement this step matcher properly.
	ctx.Step(`^the emitted command's cover has edition "([^"]*)" with divergence at "([^"]*)"=(\d+)$`, func(edition, domain string, seq int) error {
		return fmt.Errorf("WIP: step needs implementation")
	})
	// TODO (WIP): Implement this step matcher properly.
	ctx.Step(`^the persisted command's cover has edition "([^"]*)"$`, func(edition string) error {
		return fmt.Errorf("WIP: step needs implementation")
	})
	// TODO (WIP): Implement this step matcher properly.
	ctx.Step(`^every emitted process_events book's cover has edition "([^"]*)"$`, func(edition string) error {
		return fmt.Errorf("WIP: step needs implementation")
	})
}
