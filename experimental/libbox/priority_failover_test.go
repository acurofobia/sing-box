package libbox

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const priorityConfig = `{
 // Supported extended JSON comments remain valid.
 "outbounds":[
 {"type":"socks","tag":"p","server":"127.0.0.1","server_port":9},
 {"type":"socks","tag":"b1","server":"127.0.0.1","server_port":9},
 {"type":"socks","tag":"b2","server":"127.0.0.1","server_port":9},
 {"type":"socks","tag":"b3","server":"127.0.0.1","server_port":9},
 {"type":"direct","tag":"direct"},
 {"type":"selector","tag":"proxy","outbounds":["p","b1","b2","b3"],"default":"p"}
 ]}`
const priorityPolicy = `{"selector":"proxy","primary":"p","backups":["b1","b2","b3"],"intervalMilliseconds":10000,"timeoutMilliseconds":5000,"failureThreshold":2,"recoveryThreshold":3}`

func TestPriorityMetadataAndValidation(t *testing.T) {
	data, err := PriorityFailoverMetadata(priorityConfig)
	if err != nil {
		t.Fatal(err)
	}
	var m struct {
		Selectors []struct {
			Tag        string
			Candidates []string
		}
	}
	if err = json.Unmarshal([]byte(data), &m); err != nil {
		t.Fatal(err)
	}
	if len(m.Selectors) != 1 || len(m.Selectors[0].Candidates) != 4 || strings.Contains(data, "server_port") {
		t.Fatal(data)
	}
	if err = ValidatePriorityFailover(priorityConfig, priorityPolicy); err != nil {
		t.Fatal(err)
	}
	for _, config := range []string{strings.Replace(priorityConfig, `"tag":"b3"`, `"tag":"gone"`, 1), strings.Replace(priorityConfig, `"type":"socks","tag":"b3","server":"127.0.0.1","server_port":9`, `"type":"direct","tag":"b3"`, 1), strings.Replace(priorityConfig, `["p","b1","b2","b3"]`, `["p","b1","b2","b3","direct"]`, 1)} {
		if ValidatePriorityFailover(config, priorityPolicy) == nil {
			t.Fatal("invalid membership accepted")
		}
	}
}

func TestPriorityPrivateConfig(t *testing.T) {
	directory := os.Getenv("SING_PRIORITY_PRIVATE_DIR")
	if directory == "" {
		t.Skip("private fixtures not requested")
	}
	config, err := os.ReadFile(filepath.Join(directory, "four-servers.json"))
	if err != nil {
		t.Fatal("private config not readable")
	}
	policy, err := os.ReadFile(filepath.Join(directory, "policy.json"))
	if err != nil {
		t.Fatal("private policy not readable")
	}
	if ValidatePriorityFailover(string(config), string(policy)) != nil {
		t.Fatal("private policy validation failed (details suppressed)")
	}
	if CheckConfig(string(config)) != nil {
		t.Fatal("private configuration validation failed (details suppressed)")
	}
	baseline, err := os.ReadFile(filepath.Join(directory, "one-server-baseline.json"))
	if err != nil {
		t.Fatal("baseline not readable")
	}
	if CheckConfig(string(baseline)) != nil {
		t.Fatal("private baseline validation failed (details suppressed)")
	}
}
