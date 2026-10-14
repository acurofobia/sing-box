package libbox

import (
	"fmt"
	"strings"
	"testing"

	"github.com/sagernet/sing-box/daemon"
)

func prototypeTestLogServer() *CommandServer {
	return &CommandServer{StartedService: daemon.NewStartedService(daemon.ServiceOptions{LogMaxLines: 1000})}
}

func TestPrototypeTransportLogReplaysBootstrapAndStreams(t *testing.T) {
	writer := newPrototypeLogWriter()
	writer.reset([]string{"test-private-password"})
	fmt.Fprintln(writer, "proxy: bootstrap succeeded")
	server := prototypeTestLogServer()
	writer.attach(server)
	fmt.Fprintln(writer, "proxy: error: test-private-password")
	logs := server.SavedLog()
	if len(logs) != 2 || !strings.Contains(logs[0].Message, "bootstrap succeeded") || !strings.Contains(logs[1].Message, "[redacted]") {
		t.Fatal("transport bootstrap/stream records missing or credentials exposed")
	}
	if strings.Contains(logs[1].Message, "test-private-password") {
		t.Fatal("password exposed")
	}
}

func TestPrototypeTransportLogResetsBetweenModesAndBoundsBacklog(t *testing.T) {
	writer := newPrototypeLogWriter()
	for i := 0; i < 500; i++ {
		fmt.Fprintln(writer, "old TURN session")
	}
	server := prototypeTestLogServer()
	writer.attach(server)
	if count := len(server.SavedLog()); count != 100 {
		t.Fatalf("backlog count=%d, want 100", count)
	}
	writer.reset(nil)
	next := prototypeTestLogServer()
	writer.attach(next)
	if len(next.SavedLog()) != 0 {
		t.Fatal("old TURN logs leaked into new mode")
	}
	fmt.Fprintln(writer, "new session")
	if len(server.SavedLog()) != 100 || len(next.SavedLog()) != 1 {
		t.Fatal("logs delivered to previous mode")
	}
}
