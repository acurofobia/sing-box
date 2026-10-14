package daemon

import (
	"context"
	"errors"
	"slices"
	"sync"
	"time"
)

type PriorityFailoverPolicy struct {
	Selector             string   `json:"selector"`
	Primary              string   `json:"primary"`
	Backups              []string `json:"backups"`
	IntervalMilliseconds int32    `json:"intervalMilliseconds"`
	TimeoutMilliseconds  int32    `json:"timeoutMilliseconds"`
	FailureThreshold     int32    `json:"failureThreshold"`
	RecoveryThreshold    int32    `json:"recoveryThreshold"`
}

func (p PriorityFailoverPolicy) Validate() error {
	if p.Selector == "" || p.Primary == "" || len(p.Backups) != 3 {
		return errors.New("priority failover requires a selector, primary and three backups")
	}
	tags := append([]string{p.Selector, p.Primary}, p.Backups...)
	seen := make(map[string]bool, len(tags))
	for _, tag := range tags {
		if tag == "" || seen[tag] {
			return errors.New("priority failover tags must be nonempty and distinct")
		}
		seen[tag] = true
	}
	if p.IntervalMilliseconds < 1000 || p.IntervalMilliseconds > 3600000 || p.TimeoutMilliseconds < 1000 || p.TimeoutMilliseconds > 10000 || p.FailureThreshold < 1 || p.FailureThreshold > 10 || p.RecoveryThreshold < 1 || p.RecoveryThreshold > 10 {
		return errors.New("priority failover timing or thresholds outside supported range")
	}
	return nil
}

type PriorityFailoverState struct {
	Mode          string `json:"mode"`
	Selector      string `json:"selector,omitempty"`
	Selected      string `json:"selected,omitempty"`
	PrimaryHealth string `json:"primaryHealth,omitempty"`
	AllDown       bool   `json:"allDown"`
}
type priorityFailoverBackend interface {
	Probe(context.Context, string) (bool, error)
	Selected() string
	Select(string) error
}

// cycleAccess serializes probes; access makes generation validation and selection atomic.
type priorityFailoverController struct {
	ctx                    context.Context
	cancel                 context.CancelFunc
	policy                 PriorityFailoverPolicy
	backend                priorityFailoverBackend
	cycleAccess            sync.Mutex
	access                 sync.Mutex
	generation             uint64
	operationCancel        context.CancelFunc
	manual, paused, closed bool
	failures, recoveries   int32
	state                  PriorityFailoverState
	wake                   chan struct{}
	done                   chan struct{}
	started                bool
	transition             func(PriorityFailoverState)
}

func newPriorityFailoverController(ctx context.Context, p PriorityFailoverPolicy, b priorityFailoverBackend) (*priorityFailoverController, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	p.Backups = slices.Clone(p.Backups)
	ctx, cancel := context.WithCancel(ctx)
	return &priorityFailoverController{ctx: ctx, cancel: cancel, policy: p, backend: b, wake: make(chan struct{}, 1), done: make(chan struct{}), state: PriorityFailoverState{Mode: "automatic", Selector: p.Selector, PrimaryHealth: "unknown"}}, nil
}
func (c *priorityFailoverController) Status() PriorityFailoverState {
	c.access.Lock()
	defer c.access.Unlock()
	s := c.state
	s.Selected = c.backend.Selected()
	return s
}
func (c *priorityFailoverController) invalidateLocked() {
	c.generation++
	c.failures = 0
	c.recoveries = 0
	if c.operationCancel != nil {
		c.operationCancel()
	}
}
func (c *priorityFailoverController) Manual(tag string) error {
	c.access.Lock()
	defer c.access.Unlock()
	if c.closed {
		return errors.New("priority failover is closed")
	}
	if tag != c.policy.Primary && !slices.Contains(c.policy.Backups, tag) {
		return errors.New("outbound is not a priority failover candidate")
	}
	c.invalidateLocked()
	c.manual = true
	c.state.Mode = "manual"
	c.state.PrimaryHealth = "unknown"
	c.state.AllDown = false
	if err := c.backend.Select(tag); err != nil {
		return err
	}
	c.state.Selected = c.backend.Selected()
	if c.transition != nil {
		c.transition(c.state)
	}
	return nil
}
func (c *priorityFailoverController) Resume() {
	c.access.Lock()
	if !c.closed {
		c.invalidateLocked()
		c.manual = false
		c.state.Mode = "automatic"
	}
	c.access.Unlock()
	c.signal()
}
func (c *priorityFailoverController) SetPaused(paused bool) {
	c.access.Lock()
	changed := c.paused != paused
	if changed {
		c.invalidateLocked()
		c.paused = paused
	}
	c.access.Unlock()
	if changed && !paused {
		c.signal()
	}
}
func (c *priorityFailoverController) signal() {
	select {
	case c.wake <- struct{}{}:
	default:
	}
}
func (c *priorityFailoverController) Start() {
	c.access.Lock()
	defer c.access.Unlock()
	if c.started || c.closed {
		return
	}
	c.started = true
	go c.loop()
}
func (c *priorityFailoverController) loop() {
	defer close(c.done)
	timer := time.NewTimer(time.Duration(c.policy.IntervalMilliseconds) * time.Millisecond)
	defer timer.Stop()
	for {
		select {
		case <-c.ctx.Done():
			return
		case <-c.wake:
		case <-timer.C:
		}
		c.cycle(false)
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(time.Duration(c.policy.IntervalMilliseconds) * time.Millisecond)
	}
}
func (c *priorityFailoverController) Close() {
	c.access.Lock()
	c.closed = true
	c.invalidateLocked()
	c.cancel()
	started := c.started
	c.access.Unlock()
	if started {
		<-c.done
	}
	c.cycleAccess.Lock()
	c.cycleAccess.Unlock()
}
func (c *priorityFailoverController) cycle(initial bool) {
	c.cycleAccess.Lock()
	defer c.cycleAccess.Unlock()
	c.access.Lock()
	if c.closed || c.paused || c.manual || c.ctx.Err() != nil {
		c.access.Unlock()
		return
	}
	generation := c.generation
	ctx, cancel := context.WithCancel(c.ctx)
	c.operationCancel = cancel
	active := c.backend.Selected()
	failures, recoveries := c.failures, c.recoveries
	allDown := c.state.AllDown
	oldState := c.state
	c.access.Unlock()
	defer cancel()
	known := make(map[string]bool, 4)
	probe := func(tag string) bool {
		if value, ok := known[tag]; ok {
			return value
		}
		probeCtx, probeCancel := context.WithTimeout(ctx, time.Duration(c.policy.TimeoutMilliseconds)*time.Millisecond)
		ok, err := c.backend.Probe(probeCtx, tag)
		probeCancel()
		known[tag] = ok && err == nil
		return known[tag]
	}
	primaryOK := probe(c.policy.Primary)
	primaryHealth := "down"
	if primaryOK {
		primaryHealth = "up"
		if recoveries < c.policy.RecoveryThreshold {
			recoveries++
		}
	} else {
		recoveries = 0
	}
	target := ""
	if initial && primaryOK || active == c.policy.Primary && primaryOK || active != c.policy.Primary && recoveries >= c.policy.RecoveryThreshold {
		target = c.policy.Primary
		failures = 0
		allDown = false
	} else if initial || allDown || !probe(active) {
		if failures < c.policy.FailureThreshold {
			failures++
		}
		if initial || allDown || failures >= c.policy.FailureThreshold {
			allDown = true
			for _, tag := range append([]string{c.policy.Primary}, c.policy.Backups...) {
				if ctx.Err() != nil {
					break
				}
				if probe(tag) {
					target = tag
					allDown = false
					failures = 0
					break
				}
			}
		}
	} else {
		failures = 0
		allDown = false
	}
	c.access.Lock()
	defer c.access.Unlock()
	if c.generation != generation || c.closed || c.manual || c.paused || ctx.Err() != nil {
		return
	}
	if target != "" && target != c.backend.Selected() {
		if c.backend.Select(target) != nil {
			return
		}
	}
	c.failures = failures
	c.recoveries = recoveries
	c.state.PrimaryHealth = primaryHealth
	c.state.AllDown = allDown
	c.state.Selected = c.backend.Selected()
	if c.transition != nil && oldState != c.state {
		c.transition(c.state)
	}
}
