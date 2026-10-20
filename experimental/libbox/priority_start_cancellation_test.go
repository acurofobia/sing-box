package libbox

import (
	"context"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/sagernet/sing-box/daemon"
	"github.com/sagernet/sing/service/filemanager"
)

func TestPriorityCancelServiceStartInterruptsProbeAndRejectsLaterStart(t *testing.T) {
	previous := sWorkingPath
	sWorkingPath = t.TempDir()
	defer func() { sWorkingPath = previous }()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	entered := make(chan struct{})
	peerClosed := make(chan struct{})
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		data := make([]byte, 256)
		if _, err = conn.Read(data); err != nil {
			return
		}
		close(entered)
		conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		for {
			if _, err = conn.Read(data); err != nil {
				close(peerClosed)
				return
			}
		}
	}()
	ctx, cancel := context.WithCancel(filemanager.WithDefault(baseContext(nil), sWorkingPath, t.TempDir(), -1, -1))
	defer cancel()
	started := daemon.NewStartedService(daemon.ServiceOptions{Context: ctx})
	defer started.Close()
	server := &CommandServer{ctx: ctx, cancel: cancel, StartedService: started}
	config := fmt.Sprintf(`{"outbounds":[{"type":"socks","tag":"p","server":"127.0.0.1","server_port":%d},{"type":"socks","tag":"b1","server":"127.0.0.1","server_port":9},{"type":"socks","tag":"b2","server":"127.0.0.1","server_port":9},{"type":"socks","tag":"b3","server":"127.0.0.1","server_port":9},{"type":"selector","tag":"proxy","outbounds":["p","b1","b2","b3"],"default":"p"}],"route":{"final":"proxy"}}`, listener.Addr().(*net.TCPAddr).Port)
	result := make(chan error, 1)
	go func() {
		result <- started.StartOrReloadService(ctx, config, &daemon.OverrideOptions{PriorityFailoverJSON: priorityPolicy})
	}()
	select {
	case <-entered:
	case err := <-result:
		t.Fatalf("startup ended before probe: %v", err)
	case <-time.After(time.Second):
		t.Fatal("cold probe not entered")
	}
	server.CancelServiceStart()
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("cancelled startup succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("probe did not cancel promptly")
	}
	select {
	case <-peerClosed:
	case <-time.After(time.Second):
		t.Fatal("probe connection retained")
	}
	if err := server.StartOrReloadService(config, &OverrideOptions{PriorityFailoverJSON: priorityPolicy}); err == nil {
		t.Fatal("later startup ignored cancellation latch")
	}
	started.CloseService()
}
