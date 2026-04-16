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
	)
}
