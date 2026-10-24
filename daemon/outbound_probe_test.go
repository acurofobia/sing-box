package daemon

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	box "github.com/sagernet/sing-box"
	"github.com/sagernet/sing-box/adapter/certificate"
	"github.com/sagernet/sing-box/adapter/endpoint"
	"github.com/sagernet/sing-box/adapter/inbound"
	"github.com/sagernet/sing-box/adapter/outbound"
	boxservice "github.com/sagernet/sing-box/adapter/service"
	"github.com/sagernet/sing-box/dns"
	"github.com/sagernet/sing-box/dns/transport/local"
	"github.com/sagernet/sing-box/protocol/direct"
	"github.com/sagernet/sing-box/protocol/group"
	"github.com/sagernet/sing-box/protocol/socks"
	"github.com/sagernet/sing-box/protocol/vless"
	"github.com/sagernet/sing/service"
	"github.com/sagernet/sing/service/filemanager"
)

func outboundProbeService(t *testing.T, config string) *StartedService {
	t.Helper()
	registry := outbound.NewRegistry()
	direct.RegisterOutbound(registry)
	socks.RegisterOutbound(registry)
	group.RegisterSelector(registry)
	vless.RegisterOutbound(registry)
	dnsRegistry := dns.NewTransportRegistry()
	local.RegisterTransport(dnsRegistry)
	ctx := box.Context(context.Background(), inbound.NewRegistry(), registry, endpoint.NewRegistry(), dnsRegistry, boxservice.NewRegistry(), certificate.NewRegistry())
	ctx = filemanager.WithDefault(service.ExtendContext(ctx), t.TempDir(), t.TempDir(), -1, -1)
	started := NewStartedService(ServiceOptions{Context: ctx, LogMaxLines: 32})
	t.Cleanup(started.Close)
	if err := started.StartOrReloadService(ctx, config, nil); err != nil {
		t.Fatal(err)
	}
	return started
}

func outboundProbeURLs(t *testing.T, handler http.HandlerFunc) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	previous := priorityProbeURLs
	priorityProbeURLs = []string{server.URL, server.URL}
	t.Cleanup(func() { priorityProbeURLs = previous })
}

func TestOutboundProbeUsesSelectedTransportInsteadOfDirectRoute(t *testing.T) {
	outboundProbeURLs(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	port := prioritySOCKS(t)
	config := fmt.Sprintf(`{"outbounds":[{"type":"direct","tag":"direct"},{"type":"socks","tag":"vk-wg","server":"127.0.0.1","server_port":%d}],"route":{"final":"direct"}}`, port)
	started := outboundProbeService(t, config)
	if ok, err := started.ProbeOutbound("vk-wg", 1000); err != nil || !ok {
		t.Fatal("working outbound was rejected", ok, err)
	}
	// Keep direct internet available while replacing only the selected VPN server.
	config = `{"outbounds":[{"type":"direct","tag":"direct"},{"type":"vless","tag":"vk-wg","server":"127.0.0.1","server_port":1,"uuid":"bf000d23-0752-40b4-affe-68f7707a9661"}],"route":{"final":"direct"}}`
	if err := started.StartOrReloadService(context.Background(), config, nil); err != nil {
		t.Fatal(err)
	}
	if ok, _ := started.ProbeOutbound("direct", 1000); !ok {
		t.Fatal("direct control request failed")
	}
	if ok, _ := started.ProbeOutbound("vk-wg", 1000); ok {
		t.Fatal("fake VLESS passed because direct internet works")
	}
}

func TestOutboundProbeRejectsCaptivePortalAndHonorsDeadline(t *testing.T) {
	for _, hanging := range []bool{false, true} {
		t.Run(fmt.Sprint(hanging), func(t *testing.T) {
			outboundProbeURLs(t, func(w http.ResponseWriter, r *http.Request) {
				if hanging {
					<-r.Context().Done()
					return
				}
				w.WriteHeader(http.StatusOK)
			})
			started := outboundProbeService(t, `{"outbounds":[{"type":"direct","tag":"vk-wg"}]}`)
			before := time.Now()
			if ok, _ := started.ProbeOutbound("vk-wg", 100); ok {
				t.Fatal("unusable response passed health check")
			}
			if time.Since(before) > time.Second {
				t.Fatal("probe ignored its deadline")
			}
		})
	}
}

func TestOutboundProbeStopsWithTheRunningInstance(t *testing.T) {
	entered := make(chan struct{}, 1)
	outboundProbeURLs(t, func(_ http.ResponseWriter, r *http.Request) {
		entered <- struct{}{}
		<-r.Context().Done()
	})
	started := outboundProbeService(t, `{"outbounds":[{"type":"direct","tag":"vk-wg"}]}`)
	done := make(chan bool, 1)
	go func() { ok, _ := started.ProbeOutbound("vk-wg", 5000); done <- ok }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("probe did not start")
	}
	if err := started.CloseService(); err != nil {
		t.Fatal(err)
	}
	select {
	case ok := <-done:
		if ok {
			t.Fatal("stopped instance produced a successful result")
		}
	case <-time.After(time.Second):
		t.Fatal("stop waited for the health-check deadline")
	}
}

func TestOutboundProbeRejectsInvalidOrMissingTargets(t *testing.T) {
	started := outboundProbeService(t, `{"outbounds":[{"type":"direct","tag":"vk-wg"}]}`)
	for _, timeout := range []int32{0, -1, 10001} {
		if ok, err := started.ProbeOutbound("vk-wg", timeout); err == nil || ok {
			t.Fatal("invalid timeout accepted", timeout)
		}
	}
	if ok, err := started.ProbeOutbound("missing", 100); err == nil || ok {
		t.Fatal("missing outbound accepted")
	}
	started.CloseService()
	if ok, err := started.ProbeOutbound("vk-wg", 100); err == nil || ok {
		t.Fatal("stopped service accepted")
	}
}

func TestOutboundProbeDiscardsSuccessAfterSelectionChanges(t *testing.T) {
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	outboundProbeURLs(t, func(w http.ResponseWriter, r *http.Request) {
		entered <- struct{}{}
		select {
		case <-release:
			w.WriteHeader(http.StatusNoContent)
		case <-r.Context().Done():
		}
	})
	config := fmt.Sprintf(`{"outbounds":[{"type":"socks","tag":"working","server":"127.0.0.1","server_port":%d},{"type":"socks","tag":"broken","server":"127.0.0.1","server_port":1},{"type":"selector","tag":"vk-wg","outbounds":["working","broken"],"default":"working"}]}`, prioritySOCKS(t))
	started := outboundProbeService(t, config)
	done := make(chan bool, 1)
	go func() { ok, _ := started.ProbeOutbound("vk-wg", 1000); done <- ok }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("probe did not start")
	}
	selected, _ := started.instance.outboundManager.Outbound("vk-wg")
	if !selected.(*group.Selector).SelectOutbound("broken") {
		t.Fatal("selection failed")
	}
	close(release)
	if <-done {
		t.Fatal("previous server's successful response marked the new server healthy")
	}
}

func TestOutboundProbeIsCancelledByServiceReload(t *testing.T) {
	entered := make(chan struct{}, 1)
	outboundProbeURLs(t, func(_ http.ResponseWriter, r *http.Request) {
		entered <- struct{}{}
		<-r.Context().Done()
	})
	started := outboundProbeService(t, `{"outbounds":[{"type":"direct","tag":"vk-wg"}]}`)
	done := make(chan bool, 1)
	go func() { ok, _ := started.ProbeOutbound("vk-wg", 5000); done <- ok }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("probe did not start")
	}
	if err := started.StartOrReloadService(context.Background(), `{"outbounds":[{"type":"socks","tag":"vk-wg","server":"127.0.0.1","server_port":1}]}`, nil); err != nil {
		t.Fatal(err)
	}
	select {
	case ok := <-done:
		if ok {
			t.Fatal("old instance marked the replacement healthy")
		}
	case <-time.After(time.Second):
		t.Fatal("reload did not cancel the old probe")
	}
}
