package features

// upcaster.go — step bindings for client/upcaster.feature (C-0123..C-0125,
// C-0136..C-0137).
//
// The declaration scenarios (C-0123..C-0125) pin the upcaster declaration
// surface; in Go that surface is NewUpcasterGrpcHandler (name + domain) and
// UpcasterRouter.On (source → target rule). The dispatch scenarios
// (C-0136..C-0137) drive the real UpcasterRouter chain: every matching
// upcaster applies in registration order, the output of one feeding the
// next.

import (
	"fmt"
	"strconv"

	"github.com/cucumber/godog"
	"google.golang.org/protobuf/types/known/anypb"

	angzarr "github.com/benjaminabbitt/angzarr/client/go"
	pb "github.com/benjaminabbitt/angzarr/client/go/proto/angzarr_client/proto/angzarr/v1"
)

// versionedTypeURL names the synthetic event versions the chain scenarios
// register and dispatch (V1, V2, …).
func versionedTypeURL(version int) string {
	return fmt.Sprintf("test.upcaster.EventV%d", version)
}

// UpcasterContext holds per-scenario state for upcaster.feature.
type UpcasterContext struct {
	router   *angzarr.UpcasterRouter
	handler  *angzarr.UpcasterGrpcHandler
	declared bool
	incoming []*pb.EventPage
	result   []*pb.EventPage
}

func newUpcasterContext() *UpcasterContext {
	return &UpcasterContext{router: angzarr.NewUpcasterRouter("test")}
}

// InitUpcasterSteps registers upcaster.feature step definitions.
func InitUpcasterSteps(ctx *godog.ScenarioContext) {
	uc := newUpcasterContext()

	// --- Given: declaration / registration ---
	ctx.Step(`^an upcaster named "([^"]*)" in domain "([^"]*)"$`, uc.givenUpcasterNamedInDomain)
	ctx.Step(`^an upcasting rule from "([^"]*)" to "([^"]*)"$`, uc.givenUpcastingRuleFromTo)
	ctx.Step(`^an upcaster with a state factory$`, uc.givenUpcasterWithStateFactory)
	ctx.Step(`^an upcaster registered for V(\d+) → V(\d+)$`, uc.givenUpcasterRegisteredForVersions)
	ctx.Step(`^an incoming event of type V(\d+)$`, uc.givenIncomingEventOfVersion)

	// --- When ---
	ctx.Step(`^the V(\d+) event is upcasted$`, uc.whenVersionedEventIsUpcasted)

	// --- Then ---
	ctx.Step(`^the declaration is accepted$`, uc.thenDeclarationIsAccepted)
	ctx.Step(`^the emitted event has type V(\d+)$`, uc.thenEmittedEventHasVersion)
}

// C-0123: an upcaster declares its name and domain.
func (u *UpcasterContext) givenUpcasterNamedInDomain(name, domain string) error {
	u.handler = angzarr.NewUpcasterGrpcHandler(name, domain)
	u.declared = u.handler != nil
	return nil
}

// C-0124: an upcasting rule declares its source and target event types.
func (u *UpcasterContext) givenUpcastingRuleFromTo(fromType, toType string) error {
	u.router = u.router.On(fromType, func(old *anypb.Any) *anypb.Any {
		return &anypb.Any{TypeUrl: angzarr.TypeURLPrefix + toType}
	})
	u.declared = u.router != nil
	return nil
}

// C-0125: the Go upcaster surface has no state-factory declaration —
// upcasters here are stateless Any → Any transforms. Pending until the
// cross-language contract grows a Go realization.
func (u *UpcasterContext) givenUpcasterWithStateFactory() error {
	return godog.ErrPending
}

// C-0136/C-0137: register a Vn → Vm upcaster on the real router.
func (u *UpcasterContext) givenUpcasterRegisteredForVersions(from, to string) error {
	fromV, err := strconv.Atoi(from)
	if err != nil {
		return err
	}
	toV, err := strconv.Atoi(to)
	if err != nil {
		return err
	}
	u.router = u.router.On(versionedTypeURL(fromV), func(old *anypb.Any) *anypb.Any {
		return &anypb.Any{TypeUrl: angzarr.TypeURLPrefix + versionedTypeURL(toV)}
	})
	return nil
}

func (u *UpcasterContext) givenIncomingEventOfVersion(version string) error {
	v, err := strconv.Atoi(version)
	if err != nil {
		return err
	}
	u.incoming = []*pb.EventPage{
		{Payload: &pb.EventPage_Event{Event: &anypb.Any{
			TypeUrl: angzarr.TypeURLPrefix + versionedTypeURL(v),
		}}},
	}
	return nil
}

func (u *UpcasterContext) whenVersionedEventIsUpcasted(version string) error {
	u.result = u.router.Upcast(u.incoming)
	return nil
}

func (u *UpcasterContext) thenDeclarationIsAccepted() error {
	if !u.declared {
		return fmt.Errorf("declaration was not accepted")
	}
	return nil
}

func (u *UpcasterContext) thenEmittedEventHasVersion(version string) error {
	v, err := strconv.Atoi(version)
	if err != nil {
		return err
	}
	if len(u.result) != 1 {
		return fmt.Errorf("expected 1 emitted event, got %d", len(u.result))
	}
	want := angzarr.TypeURLPrefix + versionedTypeURL(v)
	got := u.result[0].GetEvent().GetTypeUrl()
	if got != want {
		return fmt.Errorf("emitted event type = %q, want %q", got, want)
	}
	return nil
}
