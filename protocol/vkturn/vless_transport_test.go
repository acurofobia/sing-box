package vkturn_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"testing"
	"time"

	box "github.com/sagernet/sing-box"
	"github.com/sagernet/sing-box/include"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing-box/protocol/vkturn"
	M "github.com/sagernet/sing/common/metadata"
	"github.com/sagernet/sing/service/filemanager"
)

func TestVLESSPrototypeForwardsRealTCPWithoutTURN(t *testing.T) {
	echo, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer echo.Close()
	go func() {
		conn, err := echo.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = io.Copy(conn, conn)
	}()
	reservation, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := reservation.Addr().(*net.TCPAddr).Port
	reservation.Close()
	start := func(text string) *box.Box {
		t.Helper()
		ctx := include.Context(context.Background())
		directory := t.TempDir()
		ctx = filemanager.WithDefault(ctx, directory, directory, -1, -1)
		var options option.Options
		if err := options.UnmarshalJSONContext(ctx, []byte(text)); err != nil {
			t.Fatal(err)
		}
		options.Log = &option.LogOptions{Disabled: true}
		instance, err := box.New(box.Options{Context: ctx, Options: options})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = instance.Close() })
		if err := instance.Start(); err != nil {
			t.Fatal(err)
		}
		return instance
	}
	const id = "bf000d23-0752-40b4-affe-68f7707a9661"
	start(fmt.Sprintf(`{"inbounds":[{"type":"vless","tag":"server","listen":"127.0.0.1","listen_port":%d,"users":[{"uuid":%q}]}],"outbounds":[{"type":"direct","tag":"direct"}],"route":{"final":"direct"}}`, port, id))
	text, err := vkturn.BuildVLESSConfiguration(json.RawMessage(`{"final":"vk-wg"}`), json.RawMessage(fmt.Sprintf(`{"type":"vless","server":"127.0.0.1","server_port":%d,"uuid":%q}`, port, id)))
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]json.RawMessage
	if err := json.Unmarshal([]byte(text), &config); err != nil {
		t.Fatal(err)
	}
	delete(config, "inbounds") // No host TUN or system route changes.
	data, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	client := start(string(data))
	outbound, found := client.Outbound().Outbound("vk-wg")
	if !found || outbound.Type() != "vless" {
		t.Fatal("VLESS route target missing")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := outbound.DialContext(ctx, "tcp", M.ParseSocksaddr(echo.Addr().String()))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	const payload = "prototype VLESS transport"
	if _, err := io.WriteString(conn, payload); err != nil {
		t.Fatal(err)
	}
	reply := make([]byte, len(payload))
	if _, err := io.ReadFull(conn, reply); err != nil {
		t.Fatal(err)
	}
	if string(reply) != payload {
		t.Fatal("VLESS corrupted the TCP payload")
	}
}
