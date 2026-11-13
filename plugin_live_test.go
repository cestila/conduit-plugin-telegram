package main

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	conduitv1 "github.com/cestila/ConduitOmniChat/sdk/conduitv1"
)

// TestLiveQRPairing comprueba contra los servidores reales de Telegram que el
// plugin emite un enlace de login escaneable. No completa el login: sin escaneo
// no hay sesión autorizada.
//
// Se ejecuta solo con TELEGRAM_LIVE=1, TELEGRAM_API_ID y TELEGRAM_API_HASH.
func TestLiveQRPairing(t *testing.T) {
	if os.Getenv("TELEGRAM_LIVE") != "1" {
		t.Skip("definir TELEGRAM_LIVE=1 para probar contra los servidores de Telegram")
	}

	apiID := os.Getenv("TELEGRAM_API_ID")
	apiHash := os.Getenv("TELEGRAM_API_HASH")
	if apiID == "" || apiHash == "" {
		t.Fatal("TELEGRAM_API_ID y TELEGRAM_API_HASH son obligatorios en modo live")
	}

	// Aísla la sesión para no tocar la sesión real del usuario.
	t.Setenv("HOME", t.TempDir())

	p := NewTelegramPlugin()
	defer func() {
		_, _ = p.Disconnect(context.Background(), &conduitv1.DisconnectRequest{})
	}()

	resp, err := p.Connect(context.Background(), &conduitv1.ConnectRequest{
		AccountIdentifier: "live-test",
		Credentials: &conduitv1.ProviderCredentials{
			ApiId:   apiID,
			ApiHash: apiHash,
		},
	})
	if err != nil {
		t.Fatalf("Connect falló: %v", err)
	}
	if !resp.GetPairing() {
		t.Fatalf("se esperaba pairing=true, got %+v", resp)
	}

	deadline := time.After(45 * time.Second)
	for {
		select {
		case event := <-p.eventCh:
			switch event.GetEventType() {
			case "channel.auth_qr":
				if !strings.HasPrefix(event.GetBody(), "tg://login?token=") {
					t.Fatalf("el cuerpo del QR no es un enlace de login de Telegram: %q", event.GetBody())
				}
				t.Logf("QR real emitido: %s", event.GetBody())
				return
			case "channel.auth_error":
				t.Fatalf("Telegram rechazó la autenticación: %s", event.GetBody())
			}
		case <-deadline:
			t.Fatal("no se recibió ningún QR de Telegram en 45s")
		}
	}
}
