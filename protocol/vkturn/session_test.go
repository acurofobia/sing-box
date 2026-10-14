package vkturn

import (
	"github.com/cacggghp/vk-turn-proxy/pkg/proxy"
	"testing"
)

func TestCancelledGenerationCannotRegisterLateBootstrap(t *testing.T) {
	generation := BeginSession()
	CancelSessions()
	p := proxy.NewProxy(proxy.Config{NumConns: 1})
	if _, err := AddSession(p, generation); err == nil {
		t.Fatal("bootstrap registered after cancellation")
	}
}
