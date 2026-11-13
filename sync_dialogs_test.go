package main

import (
	"testing"

	"github.com/gotd/td/tg"
)

// Un mensaje que llegó mientras el daemon estaba apagado solo se ve si el chat
// trae no leídos: el diálogo se sincroniza como contacto, pero sin publicar su
// último mensaje la conversación queda vacía.
func TestPublishUnreadDialogsEmiteSoloChatsConNoLeidos(t *testing.T) {
	p := NewTelegramPlugin()
	p.rememberPeer(&peerInfo{
		remoteID: "u8746443280",
		kind:     kindUser,
		id:       8746443280,
		title:    "Santiago",
		peer:     &tg.InputPeerUser{UserID: 8746443280},
	})

	unread := &tg.Message{
		ID:      100,
		PeerID:  &tg.PeerUser{UserID: 8746443280},
		FromID:  &tg.PeerUser{UserID: 8746443280},
		Date:    1_700_000_000,
		Message: "hola, ¿todo bien?",
	}
	read := &tg.Message{
		ID:      101,
		PeerID:  &tg.PeerUser{UserID: 8746443280},
		Date:    1_700_000_100,
		Message: "mensaje ya leído",
	}

	dialogs := []tg.DialogClass{
		&tg.Dialog{Peer: &tg.PeerUser{UserID: 8746443280}, TopMessage: 100, UnreadCount: 1},
		&tg.Dialog{Peer: &tg.PeerUser{UserID: 8746443281}, TopMessage: 101, UnreadCount: 0},
	}
	messages := []tg.MessageClass{unread, read}

	p.publishUnreadDialogs(dialogs, messages)

	var events []string
	for len(p.eventCh) > 0 {
		ev := <-p.eventCh
		events = append(events, ev.EventType+":"+ev.RemoteMessageId)
	}
	if len(events) != 1 || events[0] != "message.received:100" {
		t.Fatalf("se esperaba solo el mensaje no leído, llegaron %v", events)
	}
}
