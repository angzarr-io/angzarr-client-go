package angzarr

import (
	pb "github.com/benjaminabbitt/angzarr/client/go/proto/angzarr"
)

// SagaContext provides access to destination sequences for sagas.
//
// Used when one event triggers commands to multiple aggregates.
// Provides sequence number lookup and command stamping for optimistic concurrency.
//
// Design Philosophy:
// - Sagas are translators, NOT decision makers
// - They should NOT rebuild destination state to make business decisions
// - Business logic belongs in aggregates
// - SagaContext provides only sequences for command stamping
//
// Example usage:
//
//	func HandleTableSettled(evt *examples.TableSettled, ctx *SagaContext) []*pb.CommandBook {
//	    commands := make([]*pb.CommandBook, 0, len(evt.Payouts))
//	    for _, payout := range evt.Payouts {
//	        cmd := NewCommandBook("player", &examples.TransferFunds{...})
//	        ctx.StampCommand(cmd, "player")
//	        commands = append(commands, cmd)
//	    }
//	    return commands
//	}
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
