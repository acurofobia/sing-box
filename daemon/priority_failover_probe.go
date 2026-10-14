package daemon

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/protocol/group"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/common/ntp"
)

var priorityProbeURLs = []string{"https://www.gstatic.com/generate_204", "https://cp.cloudflare.com/generate_204"}

// ProbePriorityFailover performs the app's one-shot provider check with the same
// cold selection as the extension. PreStart never starts inbounds or periodic work.
func (s *StartedService) ProbePriorityFailover(ctx context.Context, configContent, policyJSON string) (bool, error) {
	if policyJSON == "" {
		return false, errors.New("priority failover policy required")
	}
	instance, err := s.newInstance(ctx, configContent, &OverrideOptions{PriorityFailoverJSON: policyJSON})
	if err != nil {
		return false, err
	}
	defer instance.Close()
	if err = instance.instance.PreStart(); err != nil {
		return false, err
	}
	if err = ctx.Err(); err != nil {
		return false, err
	}
	state := instance.priorityFailover.Status()
	return state.PrimaryHealth != "unknown" && !state.AllDown, nil
}

type priorityInstanceBackend struct {
	instance *Instance
	selector *group.Selector
}

func (b *priorityInstanceBackend) Selected() string { return b.selector.Now() }
func (b *priorityInstanceBackend) Select(tag string) error {
	if !b.selector.SelectOutboundAutomatic(tag) {
		return errors.New("priority failover selection failed")
	}
	return nil
}
func (b *priorityInstanceBackend) Probe(ctx context.Context, tag string) (bool, error) {
	outbound, ok := b.instance.outboundManager.Outbound(tag)
	if !ok {
		return false, errors.New("priority failover outbound missing")
	}
	return probePriorityHTTP(ctx, outbound, priorityProbeURLs)
}

// Both endpoints use the caller's single deadline and the existing outbound dialer.
func probePriorityHTTP(ctx context.Context, dialer N.Dialer, urls []string) (bool, error) {
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			return dialer.DialContext(ctx, network, M.ParseSocksaddr(address))
		},
		TLSClientConfig:   &tls.Config{Time: ntp.TimeFuncFromContext(ctx), RootCAs: adapter.RootPoolFromContext(ctx)},
		DisableKeepAlives: true,
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	for index, url := range urls {
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		attemptCtx := ctx
		attemptCancel := func() {}
		if deadline, ok := ctx.Deadline(); ok {
			remaining := time.Until(deadline)
			attemptCtx, attemptCancel = context.WithTimeout(ctx, remaining/time.Duration(len(urls)-index))
		}
		request, err := http.NewRequestWithContext(attemptCtx, http.MethodHead, url, nil)
		if err != nil {
			attemptCancel()
			return false, err
		}
		response, err := client.Do(request)
		if err != nil {
			attemptCancel()
			continue
		}
		response.Body.Close()
		attemptCancel()
		if response.StatusCode == http.StatusNoContent {
			return true, nil
		}
	}
	return false, ctx.Err()
}
func (s *StartedService) PriorityFailoverStatus() PriorityFailoverState {
	s.serviceAccess.RLock()
	defer s.serviceAccess.RUnlock()
	if s.instance == nil || s.instance.priorityFailover == nil {
		return PriorityFailoverState{Mode: "disabled"}
	}
	return s.instance.priorityFailover.Status()
}
func (s *StartedService) ResumePriorityFailover() error {
	s.serviceAccess.RLock()
	defer s.serviceAccess.RUnlock()
	if s.serviceStatus.Status != ServiceStatus_STARTED || s.instance == nil || s.instance.priorityFailover == nil {
		return errors.New("priority failover is not running")
	}
	s.instance.priorityFailover.Resume()
	return nil
}
