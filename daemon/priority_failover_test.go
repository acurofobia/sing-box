package daemon

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"
)

func TestPriorityLiveConnectionStatus(t *testing.T) {
	var c *priorityFailoverController
	b := &priorityTestBackend{selected: "p"}
	b.probe = func(ctx context.Context, tag string) (bool, error) {
		data, err := json.Marshal(c.Status())
		if err != nil {
			t.Fatal(err)
		}
		var state struct {
			Checking      string `json:"checking"`
			CheckingIndex int    `json:"checkingIndex"`
			Total         int    `json:"total"`
		}
		if err = json.Unmarshal(data, &state); err != nil {
			t.Fatal(err)
		}
		index := 1
		if tag == "b1" {
			index = 2
		}
		if state.Checking != tag || state.CheckingIndex != index || state.Total != 4 {
			t.Fatalf("live attempt missing: %s", data)
		}
		return tag == "b1", nil
	}
	c = testPriorityController(t, b)
	c.cycle(true)
	data, _ := json.Marshal(c.Status())
	var state struct {
		Selected       string `json:"selected"`
		SelectedHealth string `json:"selectedHealth"`
		SelectedIndex  int    `json:"selectedIndex"`
		Checking       string `json:"checking"`
	}
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatal(err)
	}
	if state.Selected != "b1" || state.SelectedHealth != "up" || state.SelectedIndex != 2 || state.Checking != "" {
		t.Fatalf("successful backup not reported: %s", data)
	}
}

func TestPriorityServerHealthSnapshot(t *testing.T) {
	b := &priorityTestBackend{selected: "p", results: map[string][]bool{"p": {false}, "b1": {true}}}
	c := testPriorityController(t, b)
	c.cycle(true)
	data, err := json.Marshal(c.Status())
	if err != nil {
		t.Fatal(err)
	}
	var snapshot struct {
		Servers []struct{ Tag, Health string }
	}
	if err = json.Unmarshal(data, &snapshot); err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Servers) != 4 {
		t.Fatalf("expected four ordered servers: %s", data)
	}
	for i, health := range []string{"down", "up", "unknown", "unknown"} {
		if snapshot.Servers[i].Tag != []string{"p", "b1", "b2", "b3"}[i] || snapshot.Servers[i].Health != health {
			t.Fatalf("incorrect server health: %s", data)
		}
	}
	if err = c.Manual("b2"); err != nil {
		t.Fatal(err)
	}
	data, _ = json.Marshal(c.Status())
	_ = json.Unmarshal(data, &snapshot)
	for _, server := range snapshot.Servers {
		if server.Health != "unknown" {
			t.Fatalf("manual selection retained old health: %s", data)
		}
	}
}

func TestPriorityServerHealthExpires(t *testing.T) {
	b := &priorityTestBackend{selected: "p", results: map[string][]bool{"p": {false}, "b1": {false}, "b2": {true}}}
	c := testPriorityController(t, b)
	c.cycle(true)
	c.observedAt[1] = time.Now().Add(-time.Hour)
	state := c.Status()
	if state.Servers[1].Health != "unknown" || state.Servers[0].Health != "down" || state.SelectedHealth != "up" {
		t.Fatalf("stale backup retained a live health claim: %+v", state)
	}
}

type priorityTestBackend struct {
	mu       sync.Mutex
	selected string
	results  map[string][]bool
	calls    []string
	probe    func(context.Context, string) (bool, error)
}

func (b *priorityTestBackend) Selected() string { b.mu.Lock(); defer b.mu.Unlock(); return b.selected }
func (b *priorityTestBackend) Select(tag string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.selected = tag
	return nil
}
func (b *priorityTestBackend) Probe(ctx context.Context, tag string) (bool, error) {
	if b.probe != nil {
		return b.probe(ctx, tag)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.calls = append(b.calls, tag)
	r := b.results[tag]
	if len(r) == 0 {
		return false, nil
	}
	b.results[tag] = r[1:]
	return r[0], nil
}
func testPriorityPolicy() PriorityFailoverPolicy {
	return PriorityFailoverPolicy{Selector: "proxy", Primary: "p", Backups: []string{"b1", "b2", "b3"}, IntervalMilliseconds: 10000, TimeoutMilliseconds: 5000, FailureThreshold: 2, RecoveryThreshold: 3}
}
func testPriorityController(t *testing.T, b *priorityTestBackend) *priorityFailoverController {
	t.Helper()
	c, err := newPriorityFailoverController(context.Background(), testPriorityPolicy(), b)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	return c
}
func TestPriorityHealthyPrimary(t *testing.T) {
	b := &priorityTestBackend{selected: "b2", results: map[string][]bool{"p": {true, true}}}
	c := testPriorityController(t, b)
	c.cycle(true)
	c.cycle(false)
	if b.Selected() != "p" || len(b.calls) != 2 {
		t.Fatalf("selection=%s calls=%v", b.Selected(), b.calls)
	}
}
func TestPriorityFailureThreshold(t *testing.T) {
	b := &priorityTestBackend{selected: "p", results: map[string][]bool{"p": {false, true, false}}}
	c := testPriorityController(t, b)
	for range 3 {
		c.cycle(false)
	}
	if b.Selected() != "p" || len(b.calls) != 3 {
		t.Fatal(b.calls)
	}
}
func TestPriorityOrderedBackups(t *testing.T) {
	b := &priorityTestBackend{selected: "p", results: map[string][]bool{"p": {false, false}, "b1": {false}, "b2": {true}}}
	c := testPriorityController(t, b)
	c.cycle(false)
	c.cycle(false)
	if b.Selected() != "b2" || len(b.calls) != 4 {
		t.Fatalf("%s %v", b.Selected(), b.calls)
	}
}
func TestPriorityRecoveryThreshold(t *testing.T) {
	b := &priorityTestBackend{selected: "b1", results: map[string][]bool{"p": {true, true, false, true, true, true}, "b1": {true, true, true, true, true}}}
	c := testPriorityController(t, b)
	for range 5 {
		c.cycle(false)
		if b.Selected() != "b1" {
			t.Fatal("early recovery")
		}
	}
	c.cycle(false)
	if b.Selected() != "p" {
		t.Fatal("no recovery")
	}
}
func TestPriorityAllDown(t *testing.T) {
	b := &priorityTestBackend{selected: "p", results: map[string][]bool{}}
	c := testPriorityController(t, b)
	c.cycle(true)
	if !c.Status().AllDown || b.Selected() != "p" || len(b.calls) != 4 {
		t.Fatal("all-down policy")
	}
}
func TestPriorityManualDuringProbe(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	done := make(chan struct{})
	b := &priorityTestBackend{selected: "p", probe: func(ctx context.Context, tag string) (bool, error) { close(entered); <-release; return true, nil }}
	c := testPriorityController(t, b)
	go func() { c.cycle(true); close(done) }()
	<-entered
	if err := c.Manual("b2"); err != nil {
		t.Fatal(err)
	}
	close(release)
	<-done
	if b.Selected() != "b2" || c.Status().Mode != "manual" {
		t.Fatal("stale probe won")
	}
	if state := c.Status(); state.Checking != "" || state.SelectedHealth != "unknown" {
		t.Fatal("manual selection retained stale connection state")
	}
}
func TestPriorityStopDuringProbe(t *testing.T) {
	entered := make(chan struct{})
	done := make(chan struct{})
	b := &priorityTestBackend{selected: "p", probe: func(ctx context.Context, tag string) (bool, error) {
		close(entered)
		<-ctx.Done()
		return false, ctx.Err()
	}}
	c := testPriorityController(t, b)
	go func() { c.cycle(true); close(done) }()
	<-entered
	c.Close()
	<-done
	if b.Selected() != "p" {
		t.Fatal("selected after close")
	}
}
func TestPriorityPauseAndResume(t *testing.T) {
	b := &priorityTestBackend{selected: "b1", results: map[string][]bool{"p": {true}}}
	c := testPriorityController(t, b)
	c.SetPaused(true)
	c.cycle(true)
	if len(b.calls) != 0 {
		t.Fatal("probe while paused")
	}
	c.SetPaused(false)
	c.cycle(true)
	if b.Selected() != "p" {
		t.Fatal("resume")
	}
}

func TestPriorityWakePreservesFailuresAndImmediatelySelectsBackup(t *testing.T) {
	b := &priorityTestBackend{selected: "p", results: map[string][]bool{"p": {false, false}, "b1": {false}, "b2": {true}}}
	c := testPriorityController(t, b)
	c.cycle(false)
	if b.Selected() != "p" {
		t.Fatal("switched before the failure threshold")
	}
	for range 3 {
		c.SetPaused(true)
		c.SetPaused(false)
	}
	c.SetPaused(true)
	// Only the final wake may trigger the loop, not a buffered earlier wake.
	select {
	case <-c.wake:
	default:
	}
	selected := make(chan PriorityFailoverState, 1)
	c.transition = func(state PriorityFailoverState) { selected <- state }
	c.Start()
	c.SetPaused(false)
	select {
	case state := <-selected:
		if state.Selected != "b2" || state.SelectedHealth != "up" {
			t.Fatalf("wake lost the previous failure: %+v", state)
		}
	case <-time.After(time.Second):
		t.Fatal("wake waited for the periodic interval")
	}
}

func TestPriorityPauseDiscardsInterruptedProbeWithoutLosingFailures(t *testing.T) {
	b := &priorityTestBackend{selected: "p", results: map[string][]bool{"p": {false, false}, "b1": {true}}}
	c := testPriorityController(t, b)
	c.cycle(false)
	entered := make(chan struct{})
	done := make(chan struct{})
	b.probe = func(ctx context.Context, tag string) (bool, error) {
		close(entered)
		<-ctx.Done()
		// Even a late success must not overwrite the completed failure.
		return true, nil
	}
	go func() { c.cycle(false); close(done) }()
	<-entered
	c.SetPaused(true)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("sleep did not cancel the probe")
	}
	b.probe = nil
	c.SetPaused(false)
	c.cycle(false)
	if b.Selected() != "b1" {
		t.Fatal("interrupted probe discarded the previous failure")
	}
}

func TestPriorityManualSelectionStillResetsFailuresAfterWake(t *testing.T) {
	b := &priorityTestBackend{selected: "p", results: map[string][]bool{"p": {false, false}, "b1": {false}, "b2": {true}}}
	c := testPriorityController(t, b)
	c.cycle(false)
	c.SetPaused(true)
	c.SetPaused(false)
	if err := c.Manual("b1"); err != nil {
		t.Fatal(err)
	}
	if c.failures != 0 {
		t.Fatal("manual selection retained failures from another server")
	}
	c.Resume()
	c.cycle(false)
	if b.Selected() != "b1" {
		t.Fatal("manual selection reused failures from another server")
	}
}
func TestPriorityPolicyValidation(t *testing.T) {
	p := testPriorityPolicy()
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	p.Backups[0] = "p"
	if p.Validate() == nil {
		t.Fatal("duplicate accepted")
	}
	p = testPriorityPolicy()
	p.TimeoutMilliseconds = 11000
	if p.Validate() == nil {
		t.Fatal("unbounded timeout")
	}
}

func TestPriorityRepeatedStart(t *testing.T) {
	entered := make(chan struct{}, 4)
	b := &priorityTestBackend{selected: "p", probe: func(ctx context.Context, tag string) (bool, error) {
		entered <- struct{}{}
		<-ctx.Done()
		return false, ctx.Err()
	}}
	for range 4 {
		c := testPriorityController(t, b)
		c.Start()
		c.Start()
		c.Resume()
		<-entered
		c.Close()
	}
	if len(entered) != 0 {
		t.Fatal("overlapping controllers")
	}
}
func TestPriorityProbeEndpointsDown(t *testing.T) {
	b := &priorityTestBackend{selected: "p", results: map[string][]bool{}}
	c := testPriorityController(t, b)
	c.cycle(false)
	if len(b.calls) != 1 || c.Status().AllDown {
		t.Fatal("threshold bypassed")
	}
	c.cycle(false)
	if len(b.calls) != 5 || !c.Status().AllDown {
		t.Fatal("unbounded search")
	}
	c.cycle(false)
	if len(b.calls) != 9 || b.Selected() != "p" {
		t.Fatal("all-down oscillation")
	}
}
func TestPrioritySerializedProbes(t *testing.T) {
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	done := make(chan struct{}, 2)
	b := &priorityTestBackend{selected: "p", probe: func(ctx context.Context, tag string) (bool, error) {
		entered <- struct{}{}
		<-release
		return true, nil
	}}
	c := testPriorityController(t, b)
	go func() { c.cycle(false); done <- struct{}{} }()
	<-entered
	go func() { c.cycle(false); done <- struct{}{} }()
	close(release)
	<-done
	<-done
	if len(entered) != 1 {
		t.Fatal("wrong probe count")
	}
}

func TestPriorityManualInvalidatesHealth(t *testing.T) {
	b := &priorityTestBackend{selected: "p", results: map[string][]bool{}}
	c := testPriorityController(t, b)
	c.cycle(true)
	if err := c.Manual("b1"); err != nil {
		t.Fatal(err)
	}
	if c.Status().AllDown || c.Status().PrimaryHealth != "unknown" {
		t.Fatal("manual status retains stale observations")
	}
}
func TestPriorityLongOutageCountersBounded(t *testing.T) {
	b := &priorityTestBackend{selected: "p", results: map[string][]bool{}}
	c := testPriorityController(t, b)
	for range 20 {
		c.cycle(false)
	}
	if c.failures > c.policy.FailureThreshold {
		t.Fatal("failure counter grows without bound")
	}
}

func TestPriorityInitialUnpausedDoesNotQueueCycle(t *testing.T) {
	b := &priorityTestBackend{selected: "p", results: map[string][]bool{}}
	c := testPriorityController(t, b)
	c.SetPaused(false)
	if len(c.wake) != 0 {
		t.Fatal("initial active state queued an extra startup cycle")
	}
}

func TestPriorityKeepsHealthyBackupWhenHigherBackupRecovers(t *testing.T) {
	b := &priorityTestBackend{selected: "b2", results: map[string][]bool{"p": {false, false}, "b2": {true, true}, "b1": {true, true}}}
	c := testPriorityController(t, b)
	c.cycle(false)
	c.cycle(false)
	if b.Selected() != "b2" || len(b.calls) != 4 {
		t.Fatalf("selected=%s calls=%v", b.Selected(), b.calls)
	}
	for _, tag := range b.calls {
		if tag == "b1" {
			t.Fatal("probed unnecessary backup")
		}
	}
}
