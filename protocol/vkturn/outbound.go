package vkturn

import (
	"context"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cacggghp/vk-turn-proxy/pkg/proxy"
	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/adapter/outbound"
	"github.com/sagernet/sing-box/log"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

var sessionsMu sync.Mutex
var sessions = make(map[int64]*proxy.Proxy)
var nextSession atomic.Int64
var generation int64

func BeginSession() int64 {
	sessionsMu.Lock()
	defer sessionsMu.Unlock()
	generation++
	return generation
}

func AddSession(p *proxy.Proxy, expectedGeneration int64) (int64, error) {
	sessionsMu.Lock()
	if expectedGeneration != generation {
		sessionsMu.Unlock()
		return 0, fmt.Errorf("VK TURN startup cancelled")
	}
	id := nextSession.Add(1)
	sessions[id] = p
	sessionsMu.Unlock()
	return id, nil
}
func StopSession(id int64) {
	sessionsMu.Lock()
	p := sessions[id]
	delete(sessions, id)
	sessionsMu.Unlock()
	if p != nil {
		p.StopWithTimeout(3 * time.Second)
	}
}

type Options struct {
	Session int64 `json:"session"`
}
type Outbound struct {
	outbound.Adapter
	proxy *proxy.Proxy
}

func RegisterOutbound(registry *outbound.Registry) {
	outbound.Register[Options](registry, "vk-turn-transport", NewOutbound)
}
func NewOutbound(_ context.Context, _ adapter.Router, _ log.ContextLogger, tag string, options Options) (adapter.Outbound, error) {
	sessionsMu.Lock()
	p := sessions[options.Session]
	sessionsMu.Unlock()
	if p == nil {
		return nil, fmt.Errorf("VK TURN session is not prepared")
	}
	return &Outbound{Adapter: outbound.NewAdapter("vk-turn-transport", tag, []string{N.NetworkUDP}, nil), proxy: p}, nil
}
func (o *Outbound) DialContext(_ context.Context, network string, _ M.Socksaddr) (net.Conn, error) {
	if network != N.NetworkUDP {
		return nil, fmt.Errorf("VK TURN transport accepts WireGuard UDP packets only")
	}
	return newPacketConn(o.proxy), nil
}
func (o *Outbound) ListenPacket(_ context.Context, _ M.Socksaddr) (net.PacketConn, error) {
	return newPacketConn(o.proxy), nil
}

// CancelSessions also cancels bootstrap before its handle has reached Swift.
func CancelSessions() {
	sessionsMu.Lock()
	generation++
	ids := make([]int64, 0, len(sessions))
	for id := range sessions {
		ids = append(ids, id)
	}
	sessionsMu.Unlock()
	for _, id := range ids {
		StopSession(id)
	}
}

func SessionStats(id int64) proxy.Stats {
	sessionsMu.Lock()
	p := sessions[id]
	sessionsMu.Unlock()
	if p == nil {
		return proxy.Stats{}
	}
	return p.GetStats()
}
