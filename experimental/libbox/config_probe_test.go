package libbox

import (
	"encoding/json"
	"testing"
)

func TestProbeOutboundsDirectDefault(t *testing.T) {
	config := `{"outbounds":[{"type":"direct","tag":"direct"},{"type":"socks","tag":"proxy","server":"127.0.0.1","server_port":9}],"route":{"final":"direct"}}`
	result, err := ProbeOutbounds(config)
	if err != nil {
		t.Fatal(err)
	}
	var metadata struct {
		Default       string   `json:"default"`
		DefaultUsable bool     `json:"defaultUsable"`
		Candidates    []string `json:"candidates"`
	}
	if err := json.Unmarshal([]byte(result), &metadata); err != nil {
		t.Fatal(err)
	}
	if metadata.Default != "direct" || metadata.DefaultUsable || len(metadata.Candidates) != 1 || metadata.Candidates[0] != "proxy" {
		t.Fatalf("unexpected probe metadata: %+v", metadata)
	}
}

func TestProbeOutboundsProxyDefault(t *testing.T) {
	config := `{"outbounds":[{"type":"socks","tag":"proxy","server":"127.0.0.1","server_port":9}]}`
	result, err := ProbeOutbounds(config)
	if err != nil {
		t.Fatal(err)
	}
	var metadata struct {
		DefaultUsable bool `json:"defaultUsable"`
	}
	if err := json.Unmarshal([]byte(result), &metadata); err != nil {
		t.Fatal(err)
	}
	if !metadata.DefaultUsable {
		t.Fatal("proxy default must be usable")
	}
}

func TestProbeConfigUnavailableProxy(t *testing.T) {
	config := `{"outbounds":[{"type":"socks","tag":"proxy","server":"127.0.0.1","server_port":9}]}`
	works, err := ProbeConfig(config, "", 1000)
	if err != nil {
		t.Fatal(err)
	}
	if works {
		t.Fatal("unavailable proxy must fail both HTTPS checks")
	}
}

func TestProbeConfigRejectsDirect(t *testing.T) {
	config := `{"outbounds":[{"type":"direct","tag":"direct"}]}`
	works, err := ProbeConfig(config, "", 1000)
	if err == nil || works {
		t.Fatalf("direct outbound must require explicit selection: works=%v error=%v", works, err)
	}
}
