package daemon

import (
	"testing"

	"github.com/sagernet/sing-box/log"
)

type diagnosticLogHandler struct{ messages []string }

func (h *diagnosticLogHandler) WriteDebugMessage(message string) {
	h.messages = append(h.messages, message)
}
func (*diagnosticLogHandler) ConnectSSHAgent() (int32, error) { return 0, nil }

func TestDiagnosticLogForwardingWithoutDebug(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		handler := &diagnosticLogHandler{}
		server := NewStartedService(ServiceOptions{Handler: handler, DiagnosticLogEnabled: enabled, LogMaxLines: 10})
		server.WriteMessage(log.LevelError, "connection lost")
		if (len(handler.messages) == 1) != enabled {
			t.Fatalf("enabled=%v forwarded=%v", enabled, handler.messages)
		}
		if len(server.SavedLog()) != 1 {
			t.Fatal("diagnostics changed live log buffer")
		}
	}
}
