package features

// projector.go — step bindings for client/projector.feature (C-0030..C-0032,
// C-0086), driven through the REAL engine ProjectorDispatch in-process: one
// instance per delivery folds every declared event; undeclared domains and
// event types never fire.

import (
	"fmt"
	"strings"

	angzarr "github.com/benjaminabbitt/angzarr/client/go"
	pb "github.com/benjaminabbitt/angzarr/client/go/proto/angzarr_client/proto/angzarr/v1"
	"github.com/cucumber/godog"
	"google.golang.org/protobuf/types/known/anypb"
)

const fqOrderCompleted = "test.OrderCompleted"

// writeLog is the per-delivery projection instance: each handled event
// appends one entry tagged with the instance's identity (C-0086).
type writeLog struct {
	entries []string
}

// ProjectorContext holds per-scenario state for projector.feature.
type ProjectorContext struct {
	table   *angzarr.ProjectorDispatch[*writeLog]
	domains []string
	folded  *writeLog // the instance the delivery folded into
	err     error
}

func newProjectorContext() *ProjectorContext {
	return &ProjectorContext{}
}

func (c *ProjectorContext) dispatchBook(domain string, eventTypes ...string) error {
	pages := make([]*pb.EventPage, 0, len(eventTypes))
	for _, et := range eventTypes {
		pages = append(pages, &pb.EventPage{Payload: &pb.EventPage_Event{Event: &anypb.Any{
			TypeUrl: angzarr.TypeURLPrefix + et,
		}}})
	}
	book := &pb.EventBook{Cover: &pb.Cover{Domain: domain}, Pages: pages}
	_, c.err = c.table.Dispatch(book)
	return c.err
}

// InitProjectorSteps registers projector.feature step definitions.
func InitProjectorSteps(ctx *godog.ScenarioContext) {
	c := newProjectorContext()

	// --- Background ---
	ctx.Step(`^a projector "([^"]*)" consuming domains "([^"]*)"$`, func(name, domains string) error {
		c.domains = strings.Split(domains, ",")
		c.table = angzarr.NewProjectorDispatch(name, func() *writeLog {
			log := &writeLog{}
			c.folded = log // the engine creates one instance per delivery
			return log
		}).ForDomains(c.domains...)
		return nil
	})
	ctx.Step(`^the projector handles OrderCreated by appending to a write log$`, func() error {
		c.table.OnEvent(fqOrderCreated, func(log *writeLog, _ *anypb.Any) error {
			log.entries = append(log.entries, fmt.Sprintf("%p:OrderCreated", log))
			return nil
		})
		return nil
	})
	ctx.Step(`^Output is the active projector$`, func() error {
		if c.table == nil {
			return fmt.Errorf("no projector configured")
		}
		return nil
	})

	// --- Whens ---
	ctx.Step(`^an EventBook with three OrderCreated events is dispatched$`, func() error {
		return c.dispatchBook("order", fqOrderCreated, fqOrderCreated, fqOrderCreated)
	})
	ctx.Step(`^an EventBook mixing OrderCreated and OrderCompleted is dispatched$`, func() error {
		return c.dispatchBook("order", fqOrderCreated, fqOrderCompleted, fqOrderCreated)
	})
	ctx.Step(`^an EventBook in domain "([^"]*)" is dispatched$`, func(domain string) error {
		return c.dispatchBook(domain, fqOrderCreated)
	})
	ctx.Step(`^an EventBook with five OrderCreated events is dispatched$`, func() error {
		return c.dispatchBook("order", fqOrderCreated, fqOrderCreated, fqOrderCreated, fqOrderCreated, fqOrderCreated)
	})

	// --- Thens ---
	ctx.Step(`^the write log contains (\d+) entries$`, func(n int) error {
		if got := len(c.folded.entries); got != n {
			return fmt.Errorf("write log has %d entries, want %d", got, n)
		}
		return nil
	})
	ctx.Step(`^the write log contains only OrderCreated entries$`, func() error {
		for _, entry := range c.folded.entries {
			if !strings.HasSuffix(entry, ":OrderCreated") {
				return fmt.Errorf("unexpected entry %q", entry)
			}
		}
		if len(c.folded.entries) != 2 {
			return fmt.Errorf("entries = %d, want the 2 OrderCreated folds", len(c.folded.entries))
		}
		return nil
	})
	ctx.Step(`^the write log remains empty$`, func() error {
		if got := len(c.folded.entries); got != 0 {
			return fmt.Errorf("write log has %d entries, want 0 (undeclared domain)", got)
		}
		return nil
	})
	ctx.Step(`^every entry was appended by the same projector instance$`, func() error {
		if len(c.folded.entries) == 0 {
			return fmt.Errorf("no entries to compare")
		}
		first := strings.SplitN(c.folded.entries[0], ":", 2)[0]
		for _, entry := range c.folded.entries {
			if strings.SplitN(entry, ":", 2)[0] != first {
				return fmt.Errorf("entries from different instances (C-0086)")
			}
		}
		return nil
	})
}
