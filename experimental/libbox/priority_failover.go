package libbox

import (
	"context"
	"encoding/json"
	"os"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/daemon"
	"github.com/sagernet/sing/service"
)

func ProbePriorityFailover(configContent, policyJSON string, timeoutMilliseconds int32) (bool, error) {
	if timeoutMilliseconds <= 0 {
		return false, os.ErrInvalid
	}
	ctx, cancel := context.WithTimeout(baseContext(nil), time.Duration(timeoutMilliseconds)*time.Millisecond)
	defer cancel()
	ctx = service.ContextWith[adapter.PlatformInterface](ctx, &probePlatformInterface{})
	started := daemon.NewStartedService(daemon.ServiceOptions{Context: ctx, LogMaxLines: 32})
	defer started.Close()
	return started.ProbePriorityFailover(ctx, configContent, policyJSON)
}

func PriorityFailoverMetadata(configContent string) (string, error) {
	options, err := parseConfig(baseContext(nil), configContent)
	if err != nil {
		return "", err
	}
	data, err := json.Marshal(daemon.PriorityFailoverMetadata(options))
	return string(data), err
}
func ValidatePriorityFailover(configContent, policyJSON string) error {
	options, err := parseConfig(baseContext(nil), configContent)
	if err != nil {
		return err
	}
	_, err = daemon.ParsePriorityFailover(options, policyJSON)
	return err
}
func (s *CommandServer) PriorityFailoverStatus() (string, error) {
	data, err := json.Marshal(s.StartedService.PriorityFailoverStatus())
	return string(data), err
}
func (s *CommandServer) ResumePriorityFailover() error {
	return s.StartedService.ResumePriorityFailover()
}
