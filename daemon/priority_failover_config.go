package daemon

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"github.com/sagernet/sing-box/option"
)

type PrioritySelectorMetadata struct {
	Tag        string   `json:"tag"`
	Candidates []string `json:"candidates"`
	Default    string   `json:"default"`
}
type PriorityOutboundMetadata struct {
	Tag  string `json:"tag"`
	Type string `json:"type"`
}
type PriorityMetadata struct {
	Selectors []PrioritySelectorMetadata `json:"selectors"`
	Outbounds []PriorityOutboundMetadata `json:"outbounds"`
}

func priorityConcrete(outbound option.Outbound) bool {
	switch outbound.Type {
	case "socks", "http", "shadowsocks", "vmess", "trojan", "vless", "hysteria", "hysteria2", "tuic", "ssh", "shadowtls", "anytls", "naive":
		if options, ok := outbound.Options.(option.DialerOptionsWrapper); ok && options.TakeDialerOptions().Detour != "" {
			return false
		}
		return true
	default:
		return false
	}
}
func PriorityFailoverMetadata(options option.Options) PriorityMetadata {
	m := PriorityMetadata{Selectors: []PrioritySelectorMetadata{}, Outbounds: []PriorityOutboundMetadata{}}
	concrete := make(map[string]bool)
	for _, o := range options.Outbounds {
		if o.Tag != "" && priorityConcrete(o) {
			concrete[o.Tag] = true
			m.Outbounds = append(m.Outbounds, PriorityOutboundMetadata{o.Tag, o.Type})
		}
	}
	for _, o := range options.Outbounds {
		if s, ok := o.Options.(*option.SelectorOutboundOptions); ok {
			candidates := []string{}
			for _, tag := range s.Outbounds {
				if concrete[tag] {
					candidates = append(candidates, tag)
				}
			}
			m.Selectors = append(m.Selectors, PrioritySelectorMetadata{o.Tag, candidates, s.Default})
		}
	}
	return m
}
func ParsePriorityFailover(options option.Options, content string) (*PriorityFailoverPolicy, error) {
	if content == "" {
		return nil, nil
	}
	var p PriorityFailoverPolicy
	decoder := json.NewDecoder(strings.NewReader(content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&p); err != nil {
		return nil, errors.New("invalid priority failover policy JSON")
	}
	if err := p.Validate(); err != nil {
		return nil, err
	}
	candidates := append([]string{p.Primary}, p.Backups...)
	found := false
	for _, o := range options.Outbounds {
		if o.Tag == p.Selector {
			s, ok := o.Options.(*option.SelectorOutboundOptions)
			if !ok || len(s.Outbounds) != 4 {
				return nil, errors.New("priority failover selector must contain exactly four candidates")
			}
			for _, tag := range candidates {
				if !slices.Contains(s.Outbounds, tag) {
					return nil, errors.New("priority failover candidate missing from selector")
				}
			}
			found = true
		}
	}
	if !found {
		return nil, errors.New("priority failover selector missing")
	}
	for _, tag := range candidates {
		count := 0
		for _, o := range options.Outbounds {
			if o.Tag == tag && priorityConcrete(o) {
				count++
			}
		}
		if count != 1 {
			return nil, errors.New("priority failover candidate must be a unique concrete proxy outbound")
		}
	}
	return &p, nil
}
