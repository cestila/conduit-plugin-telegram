package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gotd/td/session"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/auth/qrlogin"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"

	conduitv1 "github.com/cestila/ConduitOmniChat/sdk/conduitv1"
)

const (
	systemSender = "telegram-system"
)

// errPasswordNeeded indica que la cuenta exige contraseña de nube (2FA).
var errPasswordNeeded = errors.New("la cuenta de Telegram tiene contraseña de nube (2FA)")

// parseAPIID valida y convierte el api_id de Telegram.
func parseAPIID(raw string) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, errors.New("faltan credenciales de Telegram: api_id")
	}
	id, err := strconv.Atoi(raw)
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("api_id de Telegram inválido: %q", raw)
	}
	return id, nil
}

// sessionPath devuelve la ruta del archivo de sesión MTProto de la cuenta.
func (p *TelegramPlugin) sessionPath() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		home = "."
	}
	dir := filepath.Join(home, ".local", "share", "conduit", "telegram")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		log.Printf("⚠️ no se pudo crear el directorio de sesión de Telegram %s: %v", dir, err)
	}
	name := sanitizeAccountName(p.accountID)
	return filepath.Join(dir, "session-"+name+".json")
}

// hasSavedSession informa si existe un archivo de sesión con contenido.
func hasSavedSession(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Size() > 0
}

// sanitizeAccountName convierte un identificador de cuenta en un nombre de archivo seguro.
func sanitizeAccountName(raw string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(raw) {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "default"
	}
	return b.String()
}

// run mantiene vivo el cliente MTProto hasta que se cancele el contexto.
func (p *TelegramPlugin) run(ctx context.Context) {
	defer func() {
		p.mu.Lock()
		p.running = false
		p.connected = false
		p.pairing = false
		p.client = nil
		p.mu.Unlock()
	}()

	dispatcher := tg.NewUpdateDispatcher()
	loggedIn := qrlogin.OnLoginToken(dispatcher)
	dispatcher.OnNewMessage(func(ctx context.Context, e tg.Entities, u *tg.UpdateNewMessage) error {
		p.handleMessage(&e, u.Message)
		return nil
	})
	dispatcher.OnNewChannelMessage(func(ctx context.Context, e tg.Entities, u *tg.UpdateNewChannelMessage) error {
		p.handleMessage(&e, u.Message)
		return nil
	})
	dispatcher.OnMessageReactions(func(ctx context.Context, e tg.Entities, u *tg.UpdateMessageReactions) error {
		p.handleReactionUpdate(u)
		return nil
	})
	dispatcher.OnMessagePoll(func(ctx context.Context, e tg.Entities, u *tg.UpdateMessagePoll) error {
		p.handlePollUpdate(u)
		return nil
	})
	dispatcher.OnEditMessage(func(ctx context.Context, e tg.Entities, u *tg.UpdateEditMessage) error {
		p.handleEditMessage(&e, u.Message)
		return nil
	})
	dispatcher.OnEditChannelMessage(func(ctx context.Context, e tg.Entities, u *tg.UpdateEditChannelMessage) error {
		p.handleEditMessage(&e, u.Message)
		return nil
	})
	dispatcher.OnDeleteMessages(func(ctx context.Context, e tg.Entities, u *tg.UpdateDeleteMessages) error {
		p.handleDeleteMessages(u.Messages)
		return nil
	})
	dispatcher.OnDeleteChannelMessages(func(ctx context.Context, e tg.Entities, u *tg.UpdateDeleteChannelMessages) error {
		p.handleDeleteChannelMessages(u.ChannelID, u.Messages)
		return nil
	})
	dispatcher.OnReadHistoryInbox(func(ctx context.Context, e tg.Entities, u *tg.UpdateReadHistoryInbox) error {
		p.handleReadHistoryInbox(u)
		return nil
	})
	dispatcher.OnReadChannelInbox(func(ctx context.Context, e tg.Entities, u *tg.UpdateReadChannelInbox) error {
		p.handleReadChannelInbox(u)
		return nil
	})

	client := telegram.NewClient(p.apiID, p.apiHash, telegram.Options{
		SessionStorage: &session.FileStorage{Path: p.sessionPath()},
		UpdateHandler:  dispatcher,
		Device: telegram.DeviceConfig{
			DeviceModel:    "ConduitOmniChat",
			SystemVersion:  "Linux",
			AppVersion:     "0.1.0",
			SystemLangCode: "en",
			LangCode:       "en",
		},
	})

	p.mu.Lock()
	p.client = client
	p.mu.Unlock()

	err := client.Run(ctx, func(ctx context.Context) error {
		return p.authenticate(ctx, client, loggedIn)
	})
	if err != nil && ctx.Err() == nil {
		log.Printf("❌ sesión de Telegram terminada: %v", err)
		p.emitAuthError(err)
	}
}

// authenticate espera a que la cuenta esté autorizada y luego sincroniza.
func (p *TelegramPlugin) authenticate(ctx context.Context, client *telegram.Client, loggedIn qrlogin.LoggedIn) error {
	authStatus, err := client.Auth().Status(ctx)
	if err != nil {
		return fmt.Errorf("consultando el estado de la sesión: %w", err)
	}

	if !authStatus.Authorized {
		if _, err := client.QR().Auth(ctx, loggedIn, p.showQR); err != nil {
			if tgerr.Is(err, "SESSION_PASSWORD_NEEDED") {
				if err := p.submitCloudPassword(ctx, client); err != nil {
					return err
				}
			} else {
				return fmt.Errorf("login por QR: %w", err)
			}
		}
	}

	p.mu.Lock()
	p.connected = true
	p.pairing = false
	p.mu.Unlock()

	p.emit(&conduitv1.PluginEvent{
		EventType:       "channel.ready",
		RemoteMessageId: "connected",
		RemoteSenderId:  systemSender,
		Body:            "Sesión de Telegram conectada",
		SentAtUnix:      time.Now().Unix(),
	})

	p.syncDialogs(ctx, client)

	<-ctx.Done()
	return nil
}

// submitCloudPassword completa el login cuando la cuenta exige 2FA.
func (p *TelegramPlugin) submitCloudPassword(ctx context.Context, client *telegram.Client) error {
	p.mu.RLock()
	password := p.password
	p.mu.RUnlock()

	if password == "" {
		return errPasswordNeeded
	}
	if _, err := client.Auth().Password(ctx, password); err != nil {
		return fmt.Errorf("contraseña de nube (2FA) rechazada: %w", err)
	}
	return nil
}

// showQR publica el enlace de login para que la GUI lo dibuje como QR.
// qrlogin.Auth vuelve a llamar a esta función con cada token renovado.
func (p *TelegramPlugin) showQR(ctx context.Context, token qrlogin.Token) error {
	url := token.URL()
	log.Printf("📷 Escanea con Telegram (Ajustes → Dispositivos → Vincular desktop): %s", url)
	p.emit(&conduitv1.PluginEvent{
		EventType:       "channel.auth_qr",
		RemoteMessageId: "qr-code",
		RemoteSenderId:  systemSender,
		Body:            url,
		SentAtUnix:      time.Now().Unix(),
	})
	return nil
}

// emitAuthError publica un fallo de autenticación para que la GUI explique qué falta.
func (p *TelegramPlugin) emitAuthError(err error) {
	message := err.Error()
	if errors.Is(err, errPasswordNeeded) {
		message = "La cuenta de Telegram pide contraseña de nube (2FA). Agrégala en las credenciales de la cuenta y vuelve a conectar."
	}
	p.emit(&conduitv1.PluginEvent{
		EventType:       "channel.auth_error",
		RemoteMessageId: "auth-error",
		RemoteSenderId:  systemSender,
		Body:            message,
		SentAtUnix:      time.Now().Unix(),
	})
}

// emit encola un evento hacia el core sin bloquear el hilo de red.
func (p *TelegramPlugin) emit(event *conduitv1.PluginEvent) {
	select {
	case p.eventCh <- event:
	default:
		log.Printf("⚠️ buffer de eventos de Telegram lleno, se descarta %s", event.GetEventType())
	}
}

// currentClient devuelve el cliente activo, si lo hay.
func (p *TelegramPlugin) currentClient() *telegram.Client {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.client
}

// handleReadHistoryInbox procesa lecturas en chats DM o grupos básicos.
func (p *TelegramPlugin) handleReadHistoryInbox(u *tg.UpdateReadHistoryInbox) {
	if u == nil || u.Peer == nil {
		return
	}
	remoteID, kind, _ := remoteIDForPeer(u.Peer)
	if remoteID == "" {
		return
	}
	phoneHint := ""
	if kind == kindUser {
		if info := p.lookupPeer(remoteID); info != nil {
			phoneHint = info.phone
		}
	}
	event := &conduitv1.PluginEvent{
		EventType:       "messages.read",
		RemoteSenderId:  remoteID,
		ChatJid:         remoteID,
		RemoteMessageId: strconv.Itoa(u.MaxID),
		SentAtUnix:      time.Now().Unix(),
		IdentityPhone:   phoneHint,
	}
	if kind != kindUser {
		p.applyServerIdentity(event, remoteID, 0)
	}
	p.emit(event)
}

// handleReadChannelInbox procesa lecturas en canales o supergrupos.
func (p *TelegramPlugin) handleReadChannelInbox(u *tg.UpdateReadChannelInbox) {
	if u == nil {
		return
	}
	remoteID := channelRemoteID(u.ChannelID)
	event := &conduitv1.PluginEvent{
		EventType:       "messages.read",
		RemoteSenderId:  remoteID,
		ChatJid:         remoteID,
		RemoteMessageId: strconv.Itoa(u.MaxID),
		SentAtUnix:      time.Now().Unix(),
	}
	p.applyServerIdentity(event, remoteID, 0)
	p.emit(event)
}
