package vkturn_test

import (
	"context"
	"encoding/json"
	"testing"

	box "github.com/sagernet/sing-box"
	"github.com/sagernet/sing-box/include"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing-box/protocol/vkturn"
	"github.com/sagernet/sing/service/filemanager"
)

func TestBaselineDNSStarts(t *testing.T) {
	text, err := vkturn.BuildConfiguration(json.RawMessage(`{"final":"vk-wg"}`), nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	ctx := include.Context(context.Background())
	directory := t.TempDir()
	ctx = filemanager.WithDefault(ctx, directory, directory, -1, -1)
	var options option.Options
	if err := options.UnmarshalJSONContext(ctx, []byte(text)); err != nil {
		t.Fatal(err)
	}
	// Exercise the real DNS/outbound lifecycle, without changing host routes.
	options.Inbounds = nil
	instance, err := box.New(box.Options{Context: ctx, Options: options})
	if err != nil {
		t.Fatal(err)
	}
	defer instance.Close()
	if err := instance.Start(); err != nil {
		t.Fatal(err)
	}
}

func TestVLESSDNSStartsWithoutTURN(t *testing.T) {
	text, err := vkturn.BuildVLESSConfiguration(json.RawMessage(`{"final":"vk-wg"}`), json.RawMessage(`{"type":"vless","server":"example.org","server_port":443,"uuid":"bf000d23-0752-40b4-affe-68f7707a9661"}`))
	if err != nil {
		t.Fatal(err)
	}
	ctx := include.Context(context.Background())
	directory := t.TempDir()
	ctx = filemanager.WithDefault(ctx, directory, directory, -1, -1)
	var options option.Options
	if err := options.UnmarshalJSONContext(ctx, []byte(text)); err != nil {
		t.Fatal(err)
	}
	options.Inbounds = nil
	instance, err := box.New(box.Options{Context: ctx, Options: options})
	if err != nil {
		t.Fatal(err)
	}
	defer instance.Close()
	if err := instance.Start(); err != nil {
		t.Fatal(err)
	}
}
