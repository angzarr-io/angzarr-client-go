package angzarr

import (
	pb "github.com/benjaminabbitt/angzarr/client/go/proto/angzarr"
)

// Deprecated: SagaContext is deprecated. Use Destinations directly instead.
// SagaContext wraps Destinations and will be removed in a future release.
//
// Migration: Replace SagaContext with *Destinations in handler signatures.
// All SagaContext methods delegate to Destinations equivalents:
//   - GetSequence → SequenceFor
//   - StampCommand → StampCommand
//   - HasDestination → Has
type SagaContext struct {
	destinations *Destinations
}

// NewSagaContext creates a context from destination sequences.
//
// The sequences map comes from the gRPC request's destination_sequences field.
func NewSagaContext(sequences map[string]uint32) *SagaContext {
	return &SagaContext{
		destinations: NewDestinations(sequences),
	}
}

// GetSequence returns the next sequence number for a destination domain.
// Returns 0 if the domain is not in the sequences.
func (ctx *SagaContext) GetSequence(domain string) uint32 {
	if ctx == nil || ctx.destinations == nil {
		return 0
	}
	seq, ok := ctx.destinations.SequenceFor(domain)
	if !ok {
		return 0
	}
	return seq
}

// StampCommand stamps all command pages with the sequence for the given domain.
// Returns an error if the domain is not in the sequences.
func (ctx *SagaContext) StampCommand(cmd *pb.CommandBook, domain string) error {
	if ctx == nil || ctx.destinations == nil {
		return nil
	}
	return ctx.destinations.StampCommand(cmd, domain)
}

// HasDestination checks if a sequence exists for the domain.
func (ctx *SagaContext) HasDestination(domain string) bool {
	if ctx == nil || ctx.destinations == nil {
		return false
	}
	return ctx.destinations.Has(domain)
}

// Destinations returns the underlying Destinations for advanced use.
func (ctx *SagaContext) Destinations() *Destinations {
	if ctx == nil {
		return nil
	}
	return ctx.destinations
}
