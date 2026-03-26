// Package angzarr provides destination sequence access for sagas and process managers.
//
// Destinations provides sequence numbers for command stamping.
// Sagas/PMs receive destination sequences from the framework (config-driven).
//
// Design Philosophy:
// - Sagas/PMs are translators, NOT decision makers
// - They should NOT rebuild destination state to make business decisions
// - Business logic belongs in aggregates
// - Destinations provide only sequences for command stamping
//
// Example:
//
//	func (h *OrderSagaHandler) Execute(
//	    source *pb.EventBook,
//	    event *anypb.Any,
//	    destinations *Destinations,
//	) (*SagaHandlerResponse, error) {
//	    cmd := NewCommandBook("fulfillment", &CreateShipment{...})
//	    if err := destinations.StampCommand(cmd, "fulfillment"); err != nil {
//	        return nil, err
//	    }
//	    return &SagaHandlerResponse{Commands: []*pb.CommandBook{cmd}}, nil
//	}
package angzarr

import (
	"fmt"

	pb "github.com/benjaminabbitt/angzarr/client/go/proto/angzarr"
)

// Destinations provides access to destination sequences for command stamping.
//
// Sagas and PMs receive destination sequences from the framework based on
// output_domains configured in the component config.
type Destinations struct {
	sequences map[string]uint32
}

// NewDestinations creates a Destinations from a sequences map.
//
// The sequences map comes from the gRPC request's destination_sequences field.
func NewDestinations(sequences map[string]uint32) *Destinations {
	if sequences == nil {
		sequences = make(map[string]uint32)
	}
	return &Destinations{
		sequences: sequences,
	}
}

// SequenceFor returns the next sequence number for a domain.
// Returns 0 and false if the domain is not in the sequences map.
func (d *Destinations) SequenceFor(domain string) (uint32, bool) {
	if d == nil || d.sequences == nil {
		return 0, false
	}
	seq, ok := d.sequences[domain]
	return seq, ok
}

// StampCommand stamps all command pages with the sequence for the given domain.
//
// Returns an error if the domain is not in the sequences map. This indicates
// a configuration error -- the domain should be listed in output_domains.
func (d *Destinations) StampCommand(cmd *pb.CommandBook, domain string) error {
	if d == nil || d.sequences == nil {
		return fmt.Errorf("destinations is nil")
	}
	seq, ok := d.sequences[domain]
	if !ok {
		return fmt.Errorf("no sequence for domain '%s' - check output_domains config", domain)
	}
	for _, page := range cmd.Pages {
		page.Header = &pb.PageHeader{
			SequenceType: &pb.PageHeader_Sequence{Sequence: seq},
		}
	}
	return nil
}

// Has returns true if a sequence exists for the given domain.
func (d *Destinations) Has(domain string) bool {
	if d == nil || d.sequences == nil {
		return false
	}
	_, ok := d.sequences[domain]
	return ok
}

// Domains returns all domain names that have sequences.
func (d *Destinations) Domains() []string {
	if d == nil || d.sequences == nil {
		return nil
	}
	domains := make([]string, 0, len(d.sequences))
	for domain := range d.sequences {
		domains = append(domains, domain)
	}
	return domains
}
