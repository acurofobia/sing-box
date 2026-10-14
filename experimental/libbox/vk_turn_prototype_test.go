package libbox

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestVLESSPrototypeStartsWithoutTURNSettings(t *testing.T) {
	session, err := StartVKTurnPrototype(`{"mode":"vless","route":{"final":"vk-wg"},"vless":{"type":"vless","server":"127.0.0.1","server_port":443,"uuid":"bf000d23-0752-40b4-affe-68f7707a9661"}}`)
	if err != nil {
		t.Fatal(err)
	}
	if session.ID != 0 {
		t.Fatal("VLESS created a TURN session")
	}
	if !strings.Contains(session.Configuration, `"type":"vless"`) {
		t.Fatal("VLESS not selected")
	}
	if strings.Contains(session.Configuration, "wireguard") || strings.Contains(session.Configuration, "vk-turn-transport") {
		t.Fatal("VLESS contains TURN or WireGuard")
	}
	var config map[string]json.RawMessage
	if err := json.Unmarshal([]byte(session.Configuration), &config); err != nil {
		t.Fatal(err)
	}
}

func TestVLESSPrototypeRejectsMissingOutbound(t *testing.T) {
	if _, err := StartVKTurnPrototype(`{"mode":"vless","route":{"final":"vk-wg"}}`); err == nil {
		t.Fatal("accepted missing VLESS configuration")
	}
}
