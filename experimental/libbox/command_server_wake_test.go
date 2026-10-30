package libbox

import (
	"testing"
	"time"

	"github.com/sagernet/sing-box/daemon"
	"github.com/sagernet/sing-box/service/powerreport"
	"github.com/sagernet/sing/service/filemanager"
)

func TestCommandServerWakeImmediatelyResumesDeviceAndCancelsDelayedWake(t *testing.T) {
	ctx := filemanager.WithDefault(baseContext(nil), t.TempDir(), t.TempDir(), -1, -1)
	started := daemon.NewStartedService(daemon.ServiceOptions{Context: ctx})
	t.Cleanup(started.Close)
	if err := started.StartOrReloadService(ctx, `{"outbounds":[{"type":"direct","tag":"direct"}]}`, nil); err != nil {
		t.Fatal(err)
	}
	server := &CommandServer{StartedService: started, powerManager: powerreport.NewManager()}
	manager := started.Instance().PauseManager()
	for range 3 {
		server.Pause()
		if !manager.IsDevicePaused() {
			t.Fatal("sleep did not pause the device")
		}
		// Model the iOS fallback timer on the host platform as well.
		if server.endPauseTimer == nil {
			server.endPauseTimer = time.AfterFunc(time.Minute, server.endDevicePause)
		} else {
			server.endPauseTimer.Reset(time.Minute)
		}
		t.Cleanup(func() { server.endPauseTimer.Stop() })
		server.Wake()
		if manager.IsDevicePaused() {
			t.Fatal("wake left the device paused")
		}
		if server.endPauseTimer.Stop() {
			t.Fatal("wake left a delayed device wake scheduled")
		}
	}
	manager.NetworkPause()
	server.Pause()
	server.Wake()
	if manager.IsDevicePaused() || !manager.IsNetworkPaused() {
		t.Fatal("device wake changed the independent network pause")
	}
}
