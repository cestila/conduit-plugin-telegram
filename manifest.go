package main

import (
	"context"

	"github.com/cestila/ConduitOmniChat/sdk/conduitv1"
	"github.com/cestila/ConduitOmniChat/sdk/pluginsdk"
)

// version is set at release time with -ldflags "-X main.version=vX.Y.Z".
var version = "dev"

// GetManifest describes the Telegram plugin to conduitd and its clients.
func (p *TelegramPlugin) GetManifest(context.Context, *conduitv1.GetManifestRequest) (*conduitv1.PluginManifest, error) {
	return &conduitv1.PluginManifest{
		Id:          "telegram",
		DisplayName: "Telegram",
		Description: "Telegram MTProto (QR login) vía gotd/td",
		Version:     version,
		Author:      "Conduit contributors",
		Homepage:    "https://github.com/cestila/conduit-plugin-telegram",
		Provider:    conduitv1.Provider_PROVIDER_TELEGRAM,
		Icon:        "fa-brands fa-telegram",
		Color:       "#229ed9",
		CredentialFields: []*conduitv1.CredentialField{
			{Key: "api_id", Label: "api_id", Kind: conduitv1.CredentialFieldKind_CREDENTIAL_FIELD_KIND_NUMBER, Required: true, Placeholder: "1234567", Help: "https://my.telegram.org/apps"},
			{Key: "api_hash", Label: "api_hash", Kind: conduitv1.CredentialFieldKind_CREDENTIAL_FIELD_KIND_SECRET, Required: true, Placeholder: "0123456789abcdef", Help: "https://my.telegram.org/apps"},
			{Key: "password", Label: "Contraseña de nube (2FA, opcional)", Kind: conduitv1.CredentialFieldKind_CREDENTIAL_FIELD_KIND_SECRET, Placeholder: "Solo si tu cuenta tiene 2FA"},
		},
		LoginFlows:      []conduitv1.LoginFlow{conduitv1.LoginFlow_LOGIN_FLOW_QR},
		QrInstructions:  "Ajustes → Dispositivos → Vincular desktop",
		CredentialsHint: "Telegram se vincula escaneando un QR: solo necesitas api_id y api_hash de my.telegram.org. La contraseña de nube solo hace falta si tu cuenta tiene 2FA.",
		Capabilities:    []string{"reactions", "replies", "polls", "buttons", "edit", "delete", "pin", "read_receipts", "media", "voice", "forum_topics"},
		ProtocolVersion: pluginsdk.ProtocolVersion,
	}, nil
}
