package libbox

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/cacggghp/vk-turn-proxy/pkg/proxy"
	"github.com/sagernet/sing-box/experimental/libbox/internal/oomprofile"
	"github.com/sagernet/sing-box/protocol/vkturn"
)

type VKTurnPrototypeSession struct {
	ID            int64
	Configuration string
}

// StartVKTurnPrototype bootstraps only the VK transport. WireGuard is created
// by sing-box from the returned configuration, within the same Go runtime.
func StartVKTurnPrototype(input string) (*VKTurnPrototypeSession, error) {
	var settings struct {
		Route          json.RawMessage        `json:"route"`
		Generation     int64                  `json:"generation"`
		Baseline       bool                   `json:"baseline"`
		Mode           string                 `json:"mode"`
		VLESS          json.RawMessage        `json:"vless"`
		VLESSOutbounds []json.RawMessage      `json:"vless_outbounds"`
		VLESSPrimary   string                 `json:"vless_primary"`
		WireGuard      vkturn.WireGuardConfig `json:"wireguard"`
		Proxy          struct {
			VKLink     string `json:"vk_link"`
			PeerAddr   string `json:"peer_addr"`
			UseUDP     bool   `json:"use_udp"`
			UseWrapA   bool   `json:"use_wrap_a"`
			Password   string `json:"wrap_a_password"`
			DeviceID   string `json:"device_id"`
			NumConns   int    `json:"num_conns"`
			TurnServer string `json:"turn_server"`
			TurnPort   string `json:"turn_port"`
		} `json:"proxy"`
		CachePath string `json:"cache_path"`
	}
	if err := json.Unmarshal([]byte(input), &settings); err != nil {
		return nil, fmt.Errorf("invalid prototype settings")
	}
	if settings.Baseline {
		text, err := vkturn.BuildConfiguration(settings.Route, nil, 0)
		return &VKTurnPrototypeSession{Configuration: text}, err
	}
	if settings.Mode == "vless" {
		if settings.VLESSOutbounds != nil || settings.VLESSPrimary != "" {
			if len(settings.VLESS) != 0 {
				return nil, fmt.Errorf("conflicting VLESS inputs")
			}
			text, err := vkturn.BuildVLESSFailoverConfiguration(settings.Route, settings.VLESSOutbounds, settings.VLESSPrimary)
			return &VKTurnPrototypeSession{Configuration: text}, err
		}
		text, err := vkturn.BuildVLESSConfiguration(settings.Route, settings.VLESS)
		return &VKTurnPrototypeSession{Configuration: text}, err
	}
	if settings.Mode != "" && settings.Mode != "vk-turn" {
		return nil, fmt.Errorf("unknown prototype connection mode")
	}
	if settings.Proxy.NumConns < 1 || settings.Proxy.NumConns > 50 {
		return nil, fmt.Errorf("TURN connection count must be 1...50")
	}
	pc := settings.Proxy
	prototypeLogs.reset([]string{pc.VKLink, pc.Password, settings.WireGuard.PrivateKey, settings.WireGuard.PresharedKey})
	p := proxy.NewProxy(proxy.Config{VKLink: pc.VKLink, PeerAddr: pc.PeerAddr, UseUDP: pc.UseUDP, UseSrtp: !pc.UseWrapA, UseDTLS: !pc.UseWrapA,
		UseWrapA: pc.UseWrapA, WrapAPassword: pc.Password, DeviceID: pc.DeviceID, NumConns: pc.NumConns,
		TurnServer: pc.TurnServer, TurnPort: pc.TurnPort, CredCachePath: filepath.Join(settings.CachePath, "prototype-turn-credentials.json")})
	if settings.Generation == 0 {
		settings.Generation = vkturn.BeginSession()
	}
	id, err := vkturn.AddSession(p, settings.Generation)
	if err != nil {
		return nil, err
	}
	success := false
	defer func() {
		if !success {
			vkturn.StopSession(id)
		}
	}()
	go p.Start()
	if err := p.WaitBootstrap(90 * time.Second); err != nil {
		return nil, fmt.Errorf("VK TURN bootstrap failed: %w", err)
	}
	wg := settings.WireGuard
	if pc.UseWrapA {
		provision, err := p.WaitWrapAProvision(30 * time.Second)
		if err != nil {
			return nil, fmt.Errorf("WRAP-A provisioning failed: %w", err)
		}
		privateKey, err := hex.DecodeString(provision.PrivateKeyHex)
		if err != nil || len(privateKey) != 32 {
			return nil, fmt.Errorf("invalid provisioned private key")
		}
		publicKey, err := hex.DecodeString(provision.PeerPublicKeyHex)
		if err != nil || len(publicKey) != 32 {
			return nil, fmt.Errorf("invalid provisioned public key")
		}
		wg = vkturn.WireGuardConfig{PrivateKey: base64.StdEncoding.EncodeToString(privateKey), PublicKey: base64.StdEncoding.EncodeToString(publicKey), Address: provision.Address, MTU: provision.MTU}
	}
	if wg.MTU < 1280 || wg.MTU > 1500 {
		wg.MTU = 1280
	}
	text, err := vkturn.BuildConfiguration(settings.Route, &wg, id)
	if err != nil {
		return nil, err
	}
	success = true
	return &VKTurnPrototypeSession{ID: id, Configuration: text}, nil
}

func StopVKTurnPrototype(id int64) { vkturn.StopSession(id) }

func CancelVKTurnPrototype() { vkturn.CancelSessions() }

// BeginVKTurnPrototype must run before asynchronous bootstrap dispatch.
func BeginVKTurnPrototype() int64 {
	beginPrototypeLogs()
	return vkturn.BeginSession()
}

type VKTurnPrototypeStats struct {
	ActiveConnections int32
	TxBytes           int64
	RxBytes           int64
}

func GetVKTurnPrototypeStats(id int64) *VKTurnPrototypeStats {
	s := vkturn.SessionStats(id)
	return &VKTurnPrototypeStats{ActiveConnections: s.ActiveConns, TxBytes: s.TxBytes, RxBytes: s.RxBytes}
}

// VerifyVKTurnPrototypeOOMProfiles exercises the former crash path on the device
// before the steady measurement. It does not synthesize a system pressure event.
func VerifyVKTurnPrototypeOOMProfiles(directory string) error {
	if err := os.MkdirAll(directory, 0700); err != nil {
		return err
	}
	for _, name := range oomReportProfiles {
		if err := oomprofile.WriteFile(filepath.Join(directory, name+".pb"), name); err != nil {
			return err
		}
	}
	status, err := json.Marshal(struct {
		OK              bool   `json:"ok"`
		GoVersion       string `json:"go_version"`
		ProfilesWritten int    `json:"profiles_written"`
	}{true, runtime.Version(), len(oomReportProfiles)})
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(directory, "verified.json"), status, 0600)
}
