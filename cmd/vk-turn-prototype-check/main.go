package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"

	box "github.com/sagernet/sing-box"
	"github.com/sagernet/sing-box/experimental/libbox"
	"github.com/sagernet/sing-box/include"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/service/filemanager"
)

func run() error {
	if len(os.Args) != 2 {
		return fmt.Errorf("usage: vk-turn-prototype-check private-input.json")
	}
	input, err := os.ReadFile(os.Args[1])
	if err != nil {
		return err
	}
	session, err := libbox.StartVKTurnPrototype(string(input))
	if err != nil {
		return err
	}
	defer libbox.StopVKTurnPrototype(session.ID)
	var config map[string]json.RawMessage
	if err := json.Unmarshal([]byte(session.Configuration), &config); err != nil {
		return err
	}
	config["inbounds"] = json.RawMessage(`[{"type":"mixed","tag":"probe","listen":"127.0.0.1","listen_port":20880}]`)
	data, err := json.Marshal(config)
	if err != nil {
		return err
	}
	ctx := include.Context(context.Background())
	ctx = filemanager.WithDefault(ctx, filepath.Dir(os.Args[1]), filepath.Dir(os.Args[1]), os.Getuid(), os.Getgid())
	var options option.Options
	if err := options.UnmarshalJSONContext(ctx, data); err != nil {
		return err
	}
	// No TUN or system routes on the development Mac: only loopback SOCKS.
	options.Log.Output = filepath.Join(filepath.Dir(os.Args[1]), "sing-box-probe.log")
	options.Route.DefaultDomainResolver = &option.DomainResolveOptions{Server: "dns"}
	instance, err := box.New(box.Options{Context: ctx, Options: options})
	if err != nil {
		return err
	}
	defer instance.Close()
	if err := instance.Start(); err != nil {
		return err
	}
	proxyURL, _ := url.Parse("socks5://127.0.0.1:20880")
	transport := &http.Transport{Proxy: http.ProxyURL(proxyURL)}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 30 * time.Second}
	for _, target := range []struct{ name, address string }{{"routed-youtube", "https://www.youtube.com/generate_204"}, {"direct-apple", "https://captive.apple.com/hotspot-detect.html"}} {
		response, err := client.Get(target.address)
		if err != nil {
			return fmt.Errorf("%s: %w", target.name, err)
		}
		response.Body.Close()
		fmt.Printf("PROBE %s HTTP %d\n", target.name, response.StatusCode)
		if response.StatusCode >= 400 {
			return fmt.Errorf("%s failed", target.name)
		}
	}
	return nil
}
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "PROBE FAILED:", err)
		os.Exit(1)
	}
}
