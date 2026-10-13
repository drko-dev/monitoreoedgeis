package offlinecert

import (
	"errors"
	"sync"
)

// FaultPlan injects one deterministic error at the requested occurrence of an
// operation boundary. H3/H6 adapters can call Before before filesystem actions;
// this base has no provisioning adapter yet, so only the injector itself is
// certified here.
type FaultPlan struct {
	Operation  string
	Occurrence int
	Err        error

	mu   sync.Mutex
	hits int
}

func (p *FaultPlan) Before(operation string) error {
	if p == nil || operation != p.Operation {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.hits++
	if p.Occurrence < 1 || p.hits != p.Occurrence {
		return nil
	}
	if p.Err != nil {
		return p.Err
	}
	return errors.New("injected filesystem fault")
}
