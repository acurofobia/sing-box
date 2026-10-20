package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/sagernet/sing-box/experimental/cachefile"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	box "github.com/sagernet/sing-box"
	"github.com/sagernet/sing-box/adapter"
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
	M "github.com/sagernet/sing/common/metadata"
	"github.com/sagernet/sing/service"
	"github.com/sagernet/sing/service/filemanager"
)

type priorityNetDialer struct{ net.Dialer }

func (d priorityNetDialer) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	return d.Dialer.DialContext(ctx, network, destination.String())
}
func (d priorityNetDialer) ListenPacket(ctx context.Context, destination M.Socksaddr) (net.PacketConn, error) {
	return nil, fmt.Errorf("unused")
}
func TestPriorityHTTPProbe(t *testing.T) {
	var connections atomic.Int32
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ok" {
			w.WriteHeader(204)
		} else {
			w.WriteHeader(503)
		}
	}))
	server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			connections.Add(1)
		}
		if state == http.StateClosed {
			connections.Add(-1)
		}
	}
	server.Start()
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if ok, err := probePriorityHTTP(ctx, priorityNetDialer{}, []string{server.URL + "/bad", server.URL + "/ok"}); err != nil || !ok {
		t.Fatal(ok, err)
	}
	if ok, _ := probePriorityHTTP(ctx, priorityNetDialer{}, []string{server.URL + "/bad"}); ok {
		t.Fatal("503 is not healthy")
	}
	server.CloseClientConnections()
}
func TestPriorityHTTPDeadline(t *testing.T) {
	entered := make(chan struct{})
	exited := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(entered); <-r.Context().Done(); close(exited) }))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		ok, _ := probePriorityHTTP(ctx, priorityNetDialer{}, []string{server.URL, server.URL})
		if ok {
			t.Error("cancelled probe succeeded")
		}
	}()
	<-entered
	cancel()
	<-done
	select {
	case <-exited:
	case <-time.After(time.Second):
		t.Fatal("connection retained")
	}
}

// A small local SOCKS5 transport exercises the concrete outbound, without a second box.
func prioritySOCKS(t *testing.T) uint16 {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				var err error
				defer conn.Close()
				conn.SetDeadline(time.Now().Add(5 * time.Second))
				header := make([]byte, 2)
				if _, err = io.ReadFull(conn, header); err != nil {
					return
				}
				methods := make([]byte, int(header[1]))
				if _, err = io.ReadFull(conn, methods); err != nil {
					return
				}
				conn.Write([]byte{5, 0})
				request := make([]byte, 4)
				if _, err = io.ReadFull(conn, request); err != nil {
					return
				}
				var host string
				switch request[3] {
				case 1:
					address := make([]byte, 4)
					if _, err = io.ReadFull(conn, address); err != nil {
						return
					}
					host = net.IP(address).String()
				case 3:
					var size [1]byte
					if _, err = io.ReadFull(conn, size[:]); err != nil {
						return
					}
					address := make([]byte, int(size[0]))
					if _, err = io.ReadFull(conn, address); err != nil {
						return
					}
					host = string(address)
				default:
					return
				}
				port := make([]byte, 2)
				if _, err = io.ReadFull(conn, port); err != nil {
					return
				}
				target, err := net.DialTimeout("tcp", net.JoinHostPort(host, strconv.Itoa(int(port[0])<<8|int(port[1]))), time.Second)
				if err != nil {
					return
				}
				defer target.Close()
				conn.Write([]byte{5, 0, 0, 1, 127, 0, 0, 1, 0, 0})
				finished := make(chan struct{})
				go func() { io.Copy(target, conn); target.Close(); close(finished) }()
				io.Copy(conn, target)
				conn.Close()
				<-finished
			}()
		}
	}()
	return uint16(listener.Addr().(*net.TCPAddr).Port)
}
func TestPriorityColdStartupBeforeRuleSet(t *testing.T)             { testPriorityColdStartup(t, "") }
func TestPriorityColdStartupOverridesCachedDeadBackup(t *testing.T) { testPriorityColdStartup(t, "b2") }
func testPriorityColdStartup(t *testing.T, cached string) {
	var instance *Instance
	var selectedAtFetch string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			w.WriteHeader(204)
			return
		}
		selectedAtFetch = instance.priorityFailover.Status().Selected
		io.WriteString(w, `{"version":4,"rules":[{"domain":["example.test"]}]}`)
	}))
	defer server.Close()
	oldURLs := priorityProbeURLs
	priorityProbeURLs = []string{server.URL + "/health", server.URL + "/health"}
	defer func() { priorityProbeURLs = oldURLs }()
	port := prioritySOCKS(t)
	config := fmt.Sprintf(`{"outbounds":[{"type":"socks","tag":"p","server":"127.0.0.1","server_port":1},{"type":"socks","tag":"b1","server":"127.0.0.1","server_port":%d},{"type":"socks","tag":"b2","server":"127.0.0.1","server_port":1},{"type":"socks","tag":"b3","server":"127.0.0.1","server_port":1},{"type":"selector","tag":"proxy","outbounds":["p","b1","b2","b3"],"default":"p"}],"route":{"final":"proxy","rule_set":[{"type":"remote","tag":"fixture","format":"source","url":%q,"download_detour":"proxy"}]}}`, port, server.URL+"/rules.json")
	outboundRegistry := outbound.NewRegistry()
	socks.RegisterOutbound(outboundRegistry)
	group.RegisterSelector(outboundRegistry)
	direct.RegisterOutbound(outboundRegistry)
	dnsRegistry := dns.NewTransportRegistry()
	local.RegisterTransport(dnsRegistry)
	ctx := box.Context(context.Background(), inbound.NewRegistry(), outboundRegistry, endpoint.NewRegistry(), dnsRegistry, boxservice.NewRegistry(), certificate.NewRegistry())
	ctx = filemanager.WithDefault(service.ExtendContext(ctx), t.TempDir(), t.TempDir(), -1, -1)
	if cached != "" {
		cachePath := filepath.Join(t.TempDir(), "cache.db")
		cache := cachefile.New(ctx, log.NewNOPFactory().Logger(), option.CacheFileOptions{Path: cachePath})
		if err := cache.Start(adapter.StartStateInitialize); err != nil {
			t.Fatal(err)
		}
		if err := cache.StoreSelected("proxy", cached); err != nil {
			t.Fatal(err)
		}
		if err := cache.Close(); err != nil {
			t.Fatal(err)
		}
		var raw map[string]any
		if err := json.Unmarshal([]byte(config), &raw); err != nil {
			t.Fatal(err)
		}
		raw["experimental"] = map[string]any{"cache_file": map[string]any{"enabled": true, "path": cachePath}}
		encoded, _ := json.Marshal(raw)
		config = string(encoded)
	}
	started := NewStartedService(ServiceOptions{Context: ctx})
	defer started.Close()
	policy, _ := json.Marshal(testPriorityPolicy())
	var err error
	instance, err = started.newInstance(ctx, config, &OverrideOptions{PriorityFailoverJSON: string(policy)})
	if err != nil {
		t.Fatal(err)
	}
	defer instance.Close()
	if err = instance.Start(); err != nil {
		t.Fatal(err)
	}
	if selectedAtFetch != "b1" || instance.priorityFailover.Status().Selected != "b1" {
		t.Fatal("rule-set fetch preceded failover", selectedAtFetch)
	}
}

func TestPriorityHangingEndpointFallback(t *testing.T) {
	var fallbackCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/hang" {
			<-r.Context().Done()
			return
		}
		fallbackCalls.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	ok, err := probePriorityHTTP(ctx, priorityNetDialer{}, []string{server.URL + "/hang", server.URL + "/ok"})
	if !ok || err != nil || fallbackCalls.Load() != 1 {
		t.Fatal("healthy second endpoint was starved", ok, err)
	}
}

func TestPriorityProviderPreflight(t *testing.T) {
	var ruleFetches atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		ruleFetches.Add(1)
		io.WriteString(w, `{"version":4,"rules":[{"domain":["example.test"]}]}`)
	}))
	defer server.Close()
	oldURLs := priorityProbeURLs
	priorityProbeURLs = []string{server.URL + "/health", server.URL + "/health"}
	defer func() { priorityProbeURLs = oldURLs }()
	port := prioritySOCKS(t)
	config := fmt.Sprintf(`{"outbounds":[{"type":"socks","tag":"p","server":"127.0.0.1","server_port":1},{"type":"socks","tag":"b1","server":"127.0.0.1","server_port":%d},{"type":"socks","tag":"b2","server":"127.0.0.1","server_port":1},{"type":"socks","tag":"b3","server":"127.0.0.1","server_port":1},{"type":"selector","tag":"proxy","outbounds":["p","b1","b2","b3"],"default":"p"}],"route":{"final":"proxy","rule_set":[{"type":"remote","tag":"fixture","format":"source","url":%q,"download_detour":"proxy"}]}}`, port, server.URL+"/rules.json")
	outboundRegistry := outbound.NewRegistry()
	socks.RegisterOutbound(outboundRegistry)
	group.RegisterSelector(outboundRegistry)
	dnsRegistry := dns.NewTransportRegistry()
	local.RegisterTransport(dnsRegistry)
	ctx := box.Context(context.Background(), inbound.NewRegistry(), outboundRegistry, endpoint.NewRegistry(), dnsRegistry, boxservice.NewRegistry(), certificate.NewRegistry())
	ctx = filemanager.WithDefault(service.ExtendContext(ctx), t.TempDir(), t.TempDir(), -1, -1)
	started := NewStartedService(ServiceOptions{Context: ctx, LogMaxLines: 32})
	defer started.Close()
	policy, _ := json.Marshal(testPriorityPolicy())
	works, err := started.ProbePriorityFailover(ctx, config, string(policy))
	if err != nil || !works || ruleFetches.Load() != 1 {
		t.Fatal("healthy backup must pass provider preflight before fetching rule sets", works, err)
	}
	if started.instance != nil {
		t.Fatal("one-shot preflight retained an instance")
	}
	// A reachable rules endpoint must not turn an all-down policy into success.
	config = fmt.Sprintf(`{"outbounds":[{"type":"socks","tag":"p","server":"127.0.0.1","server_port":1},{"type":"socks","tag":"b1","server":"127.0.0.1","server_port":1},{"type":"socks","tag":"b2","server":"127.0.0.1","server_port":1},{"type":"socks","tag":"b3","server":"127.0.0.1","server_port":1},{"type":"selector","tag":"proxy","outbounds":["p","b1","b2","b3"],"default":"p"}],"route":{"final":"proxy"}}`)
	works, err = started.ProbePriorityFailover(ctx, config, string(policy))
	if err != nil || works {
		t.Fatal("all-down policy passed provider preflight", works, err)
	}
	if _, err = started.ProbePriorityFailover(ctx, config, ""); err == nil {
		t.Fatal("missing policy accepted")
	}
}

func TestPriorityExternalSelectorManual(t *testing.T) {
	outboundRegistry := outbound.NewRegistry()
	socks.RegisterOutbound(outboundRegistry)
	group.RegisterSelector(outboundRegistry)
	direct.RegisterOutbound(outboundRegistry)
	dnsRegistry := dns.NewTransportRegistry()
	local.RegisterTransport(dnsRegistry)
	ctx := box.Context(context.Background(), inbound.NewRegistry(), outboundRegistry, endpoint.NewRegistry(), dnsRegistry, boxservice.NewRegistry(), certificate.NewRegistry())
	ctx = filemanager.WithDefault(service.ExtendContext(ctx), t.TempDir(), t.TempDir(), -1, -1)
	config := `{"outbounds":[{"type":"socks","tag":"p","server":"127.0.0.1","server_port":1},{"type":"socks","tag":"b1","server":"127.0.0.1","server_port":1},{"type":"socks","tag":"b2","server":"127.0.0.1","server_port":1},{"type":"socks","tag":"b3","server":"127.0.0.1","server_port":1},{"type":"selector","tag":"proxy","outbounds":["p","b1","b2","b3"],"default":"p"}],"route":{"final":"proxy"}}`
	started := NewStartedService(ServiceOptions{Context: ctx, LogMaxLines: 32})
	defer started.Close()
	policy, _ := json.Marshal(testPriorityPolicy())
	instance, err := started.newInstance(ctx, config, &OverrideOptions{PriorityFailoverJSON: string(policy)})
	if err != nil {
		t.Fatal(err)
	}
	defer instance.Close()
	if err = instance.Start(); err != nil {
		t.Fatal(err)
	}
	selected, _ := instance.outboundManager.Outbound("proxy")
	if !selected.(*group.Selector).SelectOutbound("b2") {
		t.Fatal("manual selection failed")
	}
	if instance.priorityFailover.Status().Mode != "manual" {
		t.Fatal("external selector API bypassed manual mode")
	}
}
