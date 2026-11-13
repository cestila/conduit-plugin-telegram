package main

import (
	"context"
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
