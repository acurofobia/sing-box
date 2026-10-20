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

func TestVLESSPrototypeFailoverValidatesPolicyAndRejectsMixedInput(t *testing.T) {
	nodes := make([]json.RawMessage, 0, 4)
	for _, tag := range []string{"p", "b1", "b2", "b3"} {
		nodes = append(nodes, json.RawMessage(`{"type":"vless","tag":"`+tag+`","server":"example.org","server_port":443,"uuid":"bf000d23-0752-40b4-affe-68f7707a9661","tls":{"enabled":true,"server_name":"example.org","utls":{"enabled":true,"fingerprint":"chrome"},"reality":{"enabled":true,"public_key":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA","short_id":""}}}`))
	}
	input := map[string]any{"mode": "vless", "route": map[string]string{"final": "vk-wg"}, "vless_outbounds": nodes, "vless_primary": "p"}
	data, _ := json.Marshal(input)
	session, err := StartVKTurnPrototype(string(data))
	if err != nil {
		t.Fatal(err)
	}
	if session.ID != 0 {
		t.Fatal("TURN started")
	}
	policy := `{"selector":"vk-wg","primary":"p","backups":["b1","b2","b3"],"intervalMilliseconds":10000,"timeoutMilliseconds":5000,"failureThreshold":2,"recoveryThreshold":3}`
	if err := ValidatePriorityFailover(session.Configuration, policy); err != nil {
		t.Fatal(err)
	}
	if err := ValidatePriorityFailover(session.Configuration, strings.Replace(policy, `"b3"`, `"missing"`, 1)); err == nil {
		t.Fatal("missing node accepted")
	}
	input["vless"] = nodes[0]
	data, _ = json.Marshal(input)
	if _, err := StartVKTurnPrototype(string(data)); err == nil {
		t.Fatal("mixed legacy and failover inputs accepted")
	}
}
