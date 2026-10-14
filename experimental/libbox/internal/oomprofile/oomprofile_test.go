//go:build darwin || linux || windows

package oomprofile

import (
	"google.golang.org/protobuf/encoding/protowire"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

var sampledAllocation []byte

func TestAllOOMProfilesReadCurrentRuntimeRecords(t *testing.T) {
	previousRate := runtime.MemProfileRate
	runtime.MemProfileRate = 1
	defer func() { runtime.MemProfileRate = previousRate }()
	sampledAllocation = make([]byte, 4096)
	defer func() { sampledAllocation = nil }()
	runtime.GC()
	for _, name := range []string{"allocs", "heap", "block", "mutex", "goroutine", "threadcreate"} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), name+".pb.gz")
			if err := WriteFile(path, name); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(path)
			if err != nil || len(data) == 0 {
				t.Fatalf("empty or missing profile: %v", err)
			}
			samples := 0
			for len(data) > 0 {
				number, typ, count := protowire.ConsumeTag(data)
				if count < 0 {
					t.Fatal("invalid protobuf tag")
				}
				data = data[count:]
				count = protowire.ConsumeFieldValue(number, typ, data)
				if count < 0 {
					t.Fatal("invalid protobuf field")
				}
				if int(number) == tagProfile_Sample {
					samples++
				}
				data = data[count:]
			}
			if (name == "heap" || name == "allocs") && samples == 0 {
				t.Fatal("allocation profile lost all samples")
			}
		})
	}
}
