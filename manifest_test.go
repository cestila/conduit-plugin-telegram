package main

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/cestila/ConduitOmniChat/sdk/conduitv1"
	"github.com/cestila/ConduitOmniChat/sdk/pluginsdk"
)

func TestGetManifestDescribesPlugin(t *testing.T) {
	m, err := NewTelegramPlugin().GetManifest(context.Background(), &conduitv1.GetManifestRequest{})
	if err != nil {
		t.Fatalf("GetManifest: %v", err)
	}
	if err := pluginsdk.ValidateManifest(m); err != nil {
		t.Fatalf("invalid manifest: %v", err)
	}
	if m.GetId() != "telegram" || m.GetProvider() != conduitv1.Provider_PROVIDER_TELEGRAM {
		t.Fatalf("id/provider = %q/%v", m.GetId(), m.GetProvider())
	}
	if m.GetVersion() == "" || m.GetColor() == "" || m.GetIcon() == "" {
		t.Fatalf("version, color and icon are required: %+v", m)
	}
	flows := m.GetLoginFlows()
	if len(flows) == 0 || flows[0] != conduitv1.LoginFlow_LOGIN_FLOW_QR {
		t.Fatalf("login flows = %v", flows)
	}
	var keys []string
	for _, f := range m.GetCredentialFields() {
		keys = append(keys, f.GetKey())
	}
	if got := strings.Join(keys, ","); got != "api_id,api_hash,password" {
		t.Fatalf("credential field keys = %q, want %q", got, "api_id,api_hash,password")
	}
}

func TestPluginMetadataMatchesSDKProtocol(t *testing.T) {
	data, err := os.ReadFile("plugin-metadata.json")
	if err != nil {
		t.Fatalf("reading plugin-metadata.json: %v", err)
	}
	var meta struct {
		MinProtocolVersion uint32 `json:"min_protocol_version"`
		MaxProtocolVersion uint32 `json:"max_protocol_version"`
		MinCoreVersion     string `json:"min_core_version"`
		Dependencies       []any  `json:"dependencies"`
	}
	if err := json.Unmarshal(data, &meta); err != nil {
		t.Fatalf("decoding plugin-metadata.json: %v", err)
	}
	if meta.MinProtocolVersion == 0 {
		t.Fatal("min_protocol_version must be > 0")
	}
	if meta.MinProtocolVersion > pluginsdk.ProtocolVersion || meta.MaxProtocolVersion < pluginsdk.ProtocolVersion {
		t.Fatalf("protocol bounds [%d, %d] do not cover sdk ProtocolVersion %d",
			meta.MinProtocolVersion, meta.MaxProtocolVersion, pluginsdk.ProtocolVersion)
	}
	if strings.TrimSpace(meta.MinCoreVersion) == "" {
		t.Fatal("min_core_version must not be empty")
	}
}
