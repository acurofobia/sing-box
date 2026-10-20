package vkturn

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestVLESSConfigurationPreservesOutboundAndRouting(t *testing.T) {
	route := json.RawMessage(`{"rules":[{"domain":["example.org"],"outbound":"vk-wg"},{"ip_is_private":true,"outbound":"direct"},{"port":25,"action":"reject"}],"final":"vk-wg"}`)
	node := json.RawMessage(`{"type":"vless","tag":"R3-N1","server":"127.0.0.1","server_port":443,"uuid":"bf000d23-0752-40b4-affe-68f7707a9661","flow":"xtls-rprx-vision","tls":{"enabled":true,"server_name":"example.org","reality":{"enabled":true,"public_key":"test-key","short_id":""}}}`)
	text, err := BuildVLESSConfiguration(route, node)
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		Route     json.RawMessage   `json:"route"`
		Endpoints []json.RawMessage `json:"endpoints"`
		Outbounds []map[string]any  `json:"outbounds"`
		DNS       struct{ Servers []struct{ Detour string } }
	}
	if err := json.Unmarshal([]byte(text), &config); err != nil {
		t.Fatal(err)
	}
	if string(config.Route) != string(route) {
		t.Fatal("VLESS changed the original routing")
	}
	if len(config.Endpoints) != 0 || len(config.Outbounds) != 2 {
		t.Fatal("VLESS started extra engines")
	}
	var expected map[string]any
	if err := json.Unmarshal(node, &expected); err != nil {
		t.Fatal(err)
	}
	expected["tag"] = "vk-wg"
	expected["domain_resolver"] = map[string]any{"server": "bootstrap", "strategy": "ipv4_only"}
	if !reflect.DeepEqual(config.Outbounds[1], expected) {
		t.Fatal("VLESS parameters were changed")
	}
	if config.Outbounds[0]["type"] != "direct" {
		t.Fatal("direct outbound missing")
	}
	if len(config.DNS.Servers) != 2 || config.DNS.Servers[0].Detour != "vk-wg" {
		t.Fatal("DNS bypasses VLESS")
	}
}

func TestVLESSConfigurationRejectsInvalidOutbound(t *testing.T) {
	for _, node := range []string{`null`, `{}`, `{"type":"direct"}`, `{"type":"vless","detour":"vk-transport"}`, `[]`} {
		if _, err := BuildVLESSConfiguration(json.RawMessage(`{"final":"vk-wg"}`), json.RawMessage(node)); err == nil {
			t.Fatal("accepted invalid VLESS outbound")
		}
	}
}

func TestVLESSFailoverConfigurationPreservesFourTags(t *testing.T) {
	route := json.RawMessage(`{"final":"vk-wg","rule_set":[{"type":"remote","tag":"test","format":"binary","url":"https://example.org/rules.srs","download_detour":"vk-wg"}]}`)
	nodes := []json.RawMessage{}
	for _, tag := range []string{"p", "b1", "b2", "b3"} {
		nodes = append(nodes, json.RawMessage(`{"type":"vless","tag":"`+tag+`","server":"example.org","server_port":443,"uuid":"bf000d23-0752-40b4-affe-68f7707a9661","flow":"xtls-rprx-vision","tls":{"enabled":true,"server_name":"example.org","utls":{"enabled":true,"fingerprint":"chrome"},"reality":{"enabled":true,"public_key":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA","short_id":"abcd"}}}`))
	}
	text, err := BuildVLESSFailoverConfiguration(route, nodes, "b2")
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		Outbounds []map[string]any
		Inbounds  []any
		Endpoints []any
		DNS       struct{ Servers []map[string]any }
		Route     json.RawMessage
	}
	if err := json.Unmarshal([]byte(text), &config); err != nil {
		t.Fatal(err)
	}
	if len(config.Outbounds) != 6 || len(config.Inbounds) != 1 || len(config.Endpoints) != 0 {
		t.Fatal("extra/missing engine components")
	}
	selector := config.Outbounds[5]
	if selector["type"] != "selector" || selector["tag"] != "vk-wg" || selector["default"] != "b2" || selector["interrupt_exist_connections"] != true {
		t.Fatal(selector)
	}
	for i, tag := range []string{"p", "b1", "b2", "b3"} {
		if config.Outbounds[i+1]["tag"] != tag || config.Outbounds[i+1]["domain_resolver"] == nil {
			t.Fatal("lost node/bootstrap")
		}
	}
	if string(config.Route) != string(route) || config.DNS.Servers[0]["detour"] != "vk-wg" || config.DNS.Servers[1]["detour"] != nil {
		t.Fatal("routing/bootstrap changed")
	}
	for _, bad := range [][]json.RawMessage{nodes[:3], {nodes[0], nodes[0], nodes[2], nodes[3]}} {
		if _, err := BuildVLESSFailoverConfiguration(route, bad, "p"); err == nil {
			t.Fatal("invalid nodes accepted")
		}
	}
	if _, err := BuildVLESSFailoverConfiguration(route, nodes, "missing"); err == nil {
		t.Fatal("missing primary accepted")
	}
}
