package vkturn

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// WireGuardConfig carries only the keys and address provisioned for native sing-box WireGuard.
type WireGuardConfig struct {
	PrivateKey   string `json:"private_key"`
	PublicKey    string `json:"public_key"`
	PresharedKey string `json:"preshared_key,omitempty"`
	Address      string `json:"address"`
	MTU          int    `json:"mtu"`
}

func BuildConfiguration(route json.RawMessage, wg *WireGuardConfig, session int64) (string, error) {
	if !json.Valid(route) {
		return "", fmt.Errorf("invalid routing JSON")
	}
	type outbound struct {
		Type    string `json:"type"`
		Tag     string `json:"tag"`
		Session int64  `json:"session,omitempty"`
	}
	type peer struct {
		Address      string   `json:"address"`
		Port         int      `json:"port"`
		PublicKey    string   `json:"public_key"`
		PresharedKey string   `json:"pre_shared_key,omitempty"`
		AllowedIPs   []string `json:"allowed_ips"`
		Keepalive    int      `json:"persistent_keepalive_interval"`
	}
	type endpoint struct {
		Type       string   `json:"type"`
		Tag        string   `json:"tag"`
		System     bool     `json:"system"`
		Address    []string `json:"address"`
		PrivateKey string   `json:"private_key"`
		MTU        int      `json:"mtu"`
		Workers    int      `json:"workers"`
		Peers      []peer   `json:"peers"`
		Detour     string   `json:"detour"`
	}
	c := struct {
		Log          json.RawMessage `json:"log"`
		DNS          json.RawMessage `json:"dns"`
		Inbounds     json.RawMessage `json:"inbounds"`
		Outbounds    []outbound      `json:"outbounds"`
		Endpoints    []endpoint      `json:"endpoints,omitempty"`
		Route        json.RawMessage `json:"route"`
		Experimental json.RawMessage `json:"experimental"`
	}{
		Log:          json.RawMessage(`{"level":"info","timestamp":true}`),
		DNS:          json.RawMessage(`{"servers":[{"type":"udp","tag":"dns","server":"1.1.1.1","detour":"vk-wg"}],"final":"dns","strategy":"ipv4_only"}`),
		Inbounds:     json.RawMessage(`[{"type":"tun","tag":"tun","address":["172.19.0.1/30"],"mtu":1280,"auto_route":true,"stack":"gvisor"}]`),
		Outbounds:    []outbound{{Type: "direct", Tag: "direct"}},
		Route:        route,
		Experimental: json.RawMessage(`{"cache_file":{"enabled":true,"path":"prototype-cache.db"}}`),
	}
	if wg == nil {
		c.DNS = json.RawMessage(`{"servers":[{"type":"udp","tag":"dns","server":"1.1.1.1"}],"final":"dns","strategy":"ipv4_only"}`)
		c.Outbounds = append(c.Outbounds, outbound{Type: "direct", Tag: "vk-wg"})
	} else {
		if session <= 0 || wg.PrivateKey == "" || wg.PublicKey == "" || wg.Address == "" {
			return "", fmt.Errorf("missing WireGuard session parameters")
		}
		c.Outbounds = append(c.Outbounds, outbound{Type: "vk-turn-transport", Tag: "vk-transport", Session: session})
		c.Endpoints = []endpoint{{Type: "wireguard", Tag: "vk-wg", Address: []string{wg.Address}, PrivateKey: wg.PrivateKey, MTU: wg.MTU, Workers: 1,
			Peers: []peer{{Address: "127.0.0.1", Port: 51820, PublicKey: wg.PublicKey, PresharedKey: wg.PresharedKey, AllowedIPs: []string{"0.0.0.0/0"}, Keepalive: 25}}, Detour: "vk-transport"}}
	}
	b, err := json.Marshal(c)
	return string(b), err
}

// BuildVLESSConfiguration keeps the prototype route target but replaces the
// baseline direct alias with the supplied VLESS node. No TURN session is needed.
func BuildVLESSConfiguration(route, node json.RawMessage) (string, error) {
	var fields struct {
		Type   string `json:"type"`
		Server string `json:"server"`
		Port   int    `json:"server_port"`
		UUID   string `json:"uuid"`
		Detour string `json:"detour"`
	}
	if err := json.Unmarshal(node, &fields); err != nil || fields.Type != "vless" ||
		fields.Server == "" || fields.Port < 1 || fields.Port > 65535 || fields.UUID == "" || fields.Detour != "" {
		return "", fmt.Errorf("invalid standalone VLESS outbound")
	}
	baseline, err := BuildConfiguration(route, nil, 0)
	if err != nil {
		return "", err
	}
	var config map[string]json.RawMessage
	if err := json.Unmarshal([]byte(baseline), &config); err != nil {
		return "", err
	}
	var outbound map[string]json.RawMessage
	if err := json.Unmarshal(node, &outbound); err != nil {
		return "", fmt.Errorf("invalid VLESS outbound")
	}
	outbound["tag"] = json.RawMessage(`"vk-wg"`)
	// Resolve the proxy server without depending on its own tunnel DNS.
	outbound["domain_resolver"] = json.RawMessage(`{"server":"bootstrap","strategy":"ipv4_only"}`)
	outbounds, err := json.Marshal([]any{map[string]string{"type": "direct", "tag": "direct"}, outbound})
	if err != nil {
		return "", err
	}
	config["outbounds"] = outbounds
	config["dns"] = json.RawMessage(`{"servers":[{"type":"udp","tag":"dns","server":"1.1.1.1","detour":"vk-wg"},{"type":"udp","tag":"bootstrap","server":"1.1.1.1"}],"final":"dns","strategy":"ipv4_only"}`)
	data, err := json.Marshal(config)
	return string(data), err
}

// BuildVLESSFailoverConfiguration builds one selector around four provisioned
// nodes. Policy remains an external Libbox override, never a sing-box JSON key.
func BuildVLESSFailoverConfiguration(route json.RawMessage, nodes []json.RawMessage, primary string) (string, error) {
	if len(nodes) != 4 {
		return "", fmt.Errorf("four VLESS outbounds required")
	}
	baseline, err := BuildConfiguration(route, nil, 0)
	if err != nil {
		return "", err
	}
	var config map[string]json.RawMessage
	if err = json.Unmarshal([]byte(baseline), &config); err != nil {
		return "", err
	}
	tags := make([]string, 0, 4)
	seen := map[string]bool{"direct": true, "vk-wg": true}
	outbounds := []any{map[string]string{"type": "direct", "tag": "direct"}}
	for _, node := range nodes {
		outbound, tag, err := validateFailoverVLESS(node)
		if err != nil {
			return "", err
		}
		if seen[tag] {
			return "", fmt.Errorf("duplicate or reserved VLESS tag")
		}
		seen[tag] = true
		tags = append(tags, tag)
		outbound["domain_resolver"] = json.RawMessage(`{"server":"bootstrap","strategy":"ipv4_only"}`)
		outbounds = append(outbounds, outbound)
	}
	found := false
	for _, tag := range tags {
		if tag == primary {
			found = true
		}
	}
	if !found {
		return "", fmt.Errorf("VLESS primary missing")
	}
	outbounds = append(outbounds, map[string]any{"type": "selector", "tag": "vk-wg", "outbounds": tags, "default": primary, "interrupt_exist_connections": true})
	config["outbounds"], err = json.Marshal(outbounds)
	if err != nil {
		return "", err
	}
	config["dns"] = json.RawMessage(`{"servers":[{"type":"udp","tag":"dns","server":"1.1.1.1","detour":"vk-wg"},{"type":"udp","tag":"bootstrap","server":"1.1.1.1"}],"final":"dns","strategy":"ipv4_only"}`)
	data, err := json.Marshal(config)
	return string(data), err
}

func validateFailoverVLESS(node json.RawMessage) (map[string]json.RawMessage, string, error) {
	var fields struct {
		Type   string `json:"type"`
		Tag    string `json:"tag"`
		Server string `json:"server"`
		Port   int    `json:"server_port"`
		UUID   string `json:"uuid"`
		Flow   string `json:"flow"`
		TLS    struct {
			Enabled    bool   `json:"enabled"`
			ServerName string `json:"server_name"`
			UTLS       struct {
				Enabled     bool   `json:"enabled"`
				Fingerprint string `json:"fingerprint"`
			} `json:"utls"`
			Reality struct {
				Enabled   bool   `json:"enabled"`
				PublicKey string `json:"public_key"`
				ShortID   string `json:"short_id"`
			} `json:"reality"`
			ALPN []string `json:"alpn"`
		} `json:"tls"`
	}
	decoder := json.NewDecoder(bytes.NewReader(node))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&fields) != nil || fields.Type != "vless" || !failoverTag.MatchString(fields.Tag) ||
		fields.Server == "" || strings.ContainsAny(fields.Server, " \t\r\n") || fields.Port < 1 || fields.Port > 65535 ||
		!failoverUUID.MatchString(fields.UUID) || (fields.Flow != "" && fields.Flow != "xtls-rprx-vision") ||
		!fields.TLS.Enabled || fields.TLS.ServerName == "" || !fields.TLS.UTLS.Enabled ||
		!fields.TLS.Reality.Enabled || !failoverPublicKey.MatchString(fields.TLS.Reality.PublicKey) || !failoverShortID.MatchString(fields.TLS.Reality.ShortID) {
		return nil, "", fmt.Errorf("invalid failover VLESS outbound")
	}
	switch fields.TLS.UTLS.Fingerprint {
	case "chrome", "firefox", "edge", "safari", "ios", "android", "random", "randomized", "360", "qq":
	default:
		return nil, "", fmt.Errorf("invalid failover VLESS fingerprint")
	}
	for _, alpn := range fields.TLS.ALPN {
		if alpn == "" {
			return nil, "", fmt.Errorf("invalid failover VLESS ALPN")
		}
	}
	var outbound map[string]json.RawMessage
	if err := json.Unmarshal(node, &outbound); err != nil {
		return nil, "", fmt.Errorf("invalid failover VLESS outbound")
	}
	return outbound, fields.Tag, nil
}

var (
	failoverTag       = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`)
	failoverUUID      = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	failoverPublicKey = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)
	failoverShortID   = regexp.MustCompile(`^(?:[0-9a-fA-F]{2}){0,8}$`)
)
