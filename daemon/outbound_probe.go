package daemon

import (
	"context"
	"errors"
	"os"
	"time"
)

// ProbeOutbound checks the running transport itself, bypassing routing rules and
// cached URL-test results. Closing or replacing the instance cancels the request.
func (s *StartedService) ProbeOutbound(tag string, timeoutMilliseconds int32) (bool, error) {
	if tag == "" || timeoutMilliseconds <= 0 || timeoutMilliseconds > 10000 {
		return false, os.ErrInvalid
	}
	s.serviceAccess.RLock()
	if s.serviceStatus.Status != ServiceStatus_STARTED || s.instance == nil {
		s.serviceAccess.RUnlock()
		return false, os.ErrClosed
	}
	instance := s.instance
	s.serviceAccess.RUnlock()
	outbound, found := instance.outboundManager.Outbound(tag)
	if !found {
		return false, errors.New("health-check outbound missing")
	}
	ctx, cancel := context.WithTimeout(instance.ctx, time.Duration(timeoutMilliseconds)*time.Millisecond)
	defer cancel()
	healthy, err := probePriorityHTTP(ctx, outbound, priorityProbeURLs)
	s.serviceAccess.RLock()
	current := s.instance == instance && s.serviceStatus.Status == ServiceStatus_STARTED
	s.serviceAccess.RUnlock()
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if !current {
		return false, context.Canceled
	}
	return healthy, err
}
