//go:build mutation

package angzarr

import (
	"testing"

	"github.com/gtramontina/ooze"
)

func TestMutation(t *testing.T) {
	ooze.Release(t,
		ooze.WithMinimumThreshold(0.70),
		ooze.Parallel(),
		// Scope: the dispatch engine + error mapper only. Mutating the
		// godog step harnesses, the codegen plugin, and the transport
		// plumbing buries the engine signal under thousands of mutants the
		// suite can never finish. ooze has no allow-list, so everything
		// that is not engine*.go/maperr.go is ignored explicitly — keep
		// this list in sync when adding root source files.
		ooze.IgnoreSourceFiles(`^(features/|cmd/|internal/|proto/|builder\.go|client\.go|cloudevents\.go|compensation\.go|destinations\.go|errors\.go|helpers\.go|identity\.go|readiness\.go|rejected\.go|retry\.go|server\.go|upcaster\.go|upcaster_grpc\.go|validation\.go|version\.go|wrappers\.go)`),
		// Kill layer: the root package's unit tests (engine_test.go,
		// engine_gen_test.go, maperr_test.go). Scoping the per-mutant run
		// to one package keeps the whole release inside the recipe
		// timeout; the godog suite is the acceptance layer, not the
		// mutant-killing layer.
		ooze.WithTestCommand("go test -count=1 -timeout 120s ."),
	)
}
