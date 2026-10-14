package vkturn

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestConfigurationPreservesRoutingAndUsesNativeWireGuard(t *testing.T) {
	route := json.RawMessage(`{"rules":[{"domain":["example.org"],"outbound":"vk-wg"},{"ip_is_private":true,"outbound":"direct"},{"port":25,"action":"reject"}],"final":"vk-wg"}`)
	wg := WireGuardConfig{PrivateKey: strings.Repeat("A", 43) + "=", PublicKey: strings.Repeat("A", 43) + "=", Address: "10.0.0.2/32", MTU: 1280}
	text, err := BuildConfiguration(route, &wg, 7)
	if err != nil {
		t.Fatal(err)
	}
	var c struct {
		Route     json.RawMessage `json:"route"`
		Endpoints []struct {
			Type, Tag, Detour string
			System            bool
		}
		Outbounds []struct {
			Type, Tag string
			Session   int64
		}
	}
	if err := json.Unmarshal([]byte(text), &c); err != nil {
		t.Fatal(err)
	}
	if string(c.Route) != string(route) {
		t.Fatalf("routing changed: %s", c.Route)
	}
	if len(c.Endpoints) != 1 || c.Endpoints[0].Type != "wireguard" || c.Endpoints[0].Detour != "vk-transport" || c.Endpoints[0].System {
		t.Fatalf("not native userspace WireGuard: %+v", c.Endpoints)
	}
	if len(c.Outbounds) != 2 || c.Outbounds[1].Type != "vk-turn-transport" || c.Outbounds[1].Session != 7 {
		t.Fatalf("missing transport: %+v", c.Outbounds)
	}
}

func TestBaselineHasNoWireGuardOrTURN(t *testing.T) {
	text, err := BuildConfiguration(json.RawMessage(`{"final":"vk-wg"}`), nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(text, "wireguard") || strings.Contains(text, "vk-turn-transport") {
		t.Fatal("baseline starts extra engines")
	}
	if !strings.Contains(text, `"tag":"vk-wg"`) {
		t.Fatal("baseline loses route target")
	}
}

func TestInvalidRoutingRejected(t *testing.T) {
	if _, err := BuildConfiguration(json.RawMessage(`{"final":`), nil, 0); err == nil {
		t.Fatal("accepted invalid routing")
	}
}
