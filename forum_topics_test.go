package main

import (
	"testing"

	"github.com/gotd/td/tg"
)

func TestParseTopicRemoteID(t *testing.T) {
	cases := []struct {
		input       string
		wantChan    string
		wantTopicID int
		wantIsTopic bool
	}{
		{"-1001234567890_topic_42", "-1001234567890", 42, true},
		{"-1001234567890_topic_1", "-1001234567890", 1, true},
		{"-1001234567890", "-1001234567890", 0, false},
		{"u123456", "u123456", 0, false},
		{"g789012", "g789012", 0, false},
		{"-100123_topic_invalid", "-100123_topic_invalid", 0, false},
		{"-1001_topic_0", "-1001_topic_0", 0, false},
		{"-1001_topic_-3", "-1001_topic_-3", 0, false},
	}

	for _, tc := range cases {
		gotChan, gotTopicID, gotIsTopic := parseTopicRemoteID(tc.input)
		if gotChan != tc.wantChan || gotTopicID != tc.wantTopicID || gotIsTopic != tc.wantIsTopic {
			t.Errorf("parseTopicRemoteID(%q) = (%q, %d, %v); want (%q, %d, %v)",
				tc.input, gotChan, gotTopicID, gotIsTopic, tc.wantChan, tc.wantTopicID, tc.wantIsTopic)
		}
	}
}

func TestLookupPeerPlainChatUnaffected(t *testing.T) {
	p := NewTelegramPlugin()
	p.rememberPeer(&peerInfo{
		remoteID:    "-100123",
		kind:        kindChannel,
		id:          123,
		title:       "Canal Plano",
		channelType: "megagroup",
		peer:        &tg.InputPeerChannel{ChannelID: 123},
	})

	peer := p.lookupPeer("-100123")
	if peer == nil || peer.topicID != 0 {
		t.Fatalf("se esperaba chat plano con topicID=0, se obtuvo %+v", peer)
	}
}

func TestMessageEventForumParentWithoutTopicStaysOnParent(t *testing.T) {
	p := NewTelegramPlugin()
	p.rememberPeer(&peerInfo{
		remoteID:    "-100555",
		kind:        kindChannel,
		id:          555,
		title:       "Foro sin tema 1",
		channelType: "forum",
		peer:        &tg.InputPeerChannel{ChannelID: 555},
	})

	msg := &tg.Message{
		ID:      300,
		PeerID:  &tg.PeerChannel{ChannelID: 555},
		FromID:  &tg.PeerUser{UserID: 888},
		Date:    1_700_000_300,
		Message: "Mensaje sin tema",
	}

	ev := p.messageEvent(msg, "-100555", kindChannel)
	if ev == nil || ev.RemoteSenderId != "-100555" {
		t.Fatalf("se esperaba RemoteSenderId=-100555, se obtuvo %q", ev.RemoteSenderId)
	}
}

func TestMessageEventNonForumChannelIgnoresTopID(t *testing.T) {
	p := NewTelegramPlugin()
	p.rememberPeer(&peerInfo{
		remoteID:    "-100777",
		kind:        kindChannel,
		id:          777,
		title:       "Canal común",
		channelType: "channel",
		peer:        &tg.InputPeerChannel{ChannelID: 777},
	})

	msg := &tg.Message{
		ID:      301,
		PeerID:  &tg.PeerChannel{ChannelID: 777},
		FromID:  &tg.PeerUser{UserID: 888},
		Date:    1_700_000_310,
		Message: "Mensaje citando",
		ReplyTo: &tg.MessageReplyHeader{
			ReplyToTopID: 5,
			ReplyToMsgID: 10,
		},
	}

	ev := p.messageEvent(msg, "-100777", kindChannel)
	if ev == nil || ev.RemoteSenderId != "-100777" || ev.ReplyToRemoteId != "10" {
		t.Fatalf("canal común no debe reescribir RemoteSenderId ni perder ReplyTo: %+v", ev)
	}
}

func TestLookupPeerWithTopicID(t *testing.T) {
	p := NewTelegramPlugin()
	parentPeer := &tg.InputPeerChannel{ChannelID: 9999, AccessHash: 12345}
	p.rememberPeer(&peerInfo{
		remoteID:    "-1009999",
		kind:        kindChannel,
		id:          9999,
		title:       "Supergrupo Foro",
		channelType: "forum",
		peer:        parentPeer,
	})

	// Resolución dinámica a partir del supergrupo padre
	peer := p.lookupPeer("-1009999_topic_5")
	if peer == nil {
		t.Fatal("se esperaba resolver el peer del tema a partir del canal padre")
	}
	if peer.topicID != 5 {
		t.Fatalf("se esperaba topicID=5, se obtuvo %d", peer.topicID)
	}
	if peer.remoteID != "-1009999_topic_5" {
		t.Fatalf("se esperaba remoteID=-1009999_topic_5, se obtuvo %q", peer.remoteID)
	}
	if peer.peer != parentPeer {
		t.Fatalf("el InputPeerClass debe ser el del canal padre")
	}
}

func TestApplyForumTopicsEmitsChannelSync(t *testing.T) {
	p := NewTelegramPlugin()
	serverRemoteID := "-1001234567890"
	serverTitle := "Comunidad Devs"
	parentPeer := &tg.InputPeerChannel{ChannelID: 1234567890, AccessHash: 987654}

	topics := []tg.ForumTopicClass{
		&tg.ForumTopic{
			ID:    1,
			Title: "", // Tópico General por defecto
			Date:  1_700_000_000,
		},
		&tg.ForumTopic{
			ID:    42,
			Title: "Backend Go",
			Date:  1_700_001_000,
		},
	}

	p.applyForumTopics(serverRemoteID, serverTitle, parentPeer, topics)

	var events []*tgTopicEvent
	for len(p.eventCh) > 0 {
		ev := <-p.eventCh
		events = append(events, &tgTopicEvent{
			eventType:       ev.EventType,
			remoteSenderId:  ev.RemoteSenderId,
			serverId:        ev.ServerId,
			parentChannelId: ev.ParentChannelId,
			channelName:     ev.ChannelName,
			channelType:     ev.ChannelType,
		})
	}

	if len(events) != 2 {
		t.Fatalf("se esperaban 2 eventos channel.sync para los temas, se obtuvieron %d", len(events))
	}

	// Tópico 1 (General)
	t1 := events[0]
	if t1.eventType != "channel.sync" || t1.remoteSenderId != "-1001234567890_topic_1" || t1.channelName != "General" {
		t.Errorf("evento tema 1 inválido: %+v", t1)
	}
	if t1.serverId != serverRemoteID || t1.parentChannelId != serverRemoteID || t1.channelType != "forum" {
		t.Errorf("jerarquía tema 1 inválida: %+v", t1)
	}

	// Tópico 42 (Backend Go)
	t2 := events[1]
	if t2.eventType != "channel.sync" || t2.remoteSenderId != "-1001234567890_topic_42" || t2.channelName != "Backend Go" {
		t.Errorf("evento tema 42 inválido: %+v", t2)
	}

	// Ambos temas deben estar indexados en p.peers
	peer1 := p.lookupPeer("-1001234567890_topic_1")
	if peer1 == nil || peer1.topicID != 1 {
		t.Errorf("peer1 no encontrado o topicID inválido: %+v", peer1)
	}

	peer42 := p.lookupPeer("-1001234567890_topic_42")
	if peer42 == nil || peer42.topicID != 42 {
		t.Errorf("peer42 no encontrado o topicID inválido: %+v", peer42)
	}
}

func TestApplyForumTopicsGuaranteesGeneralTopic(t *testing.T) {
	p := NewTelegramPlugin()
	serverRemoteID := "-10098765"
	serverTitle := "Grupo Sin General Explícito"
	parentPeer := &tg.InputPeerChannel{ChannelID: 98765, AccessHash: 111}

	// Telegram solo devuelve temas creados manualmente (por ej. ID 42 y 43), sin el 1
	topics := []tg.ForumTopicClass{
		&tg.ForumTopic{ID: 42, Title: "Backend Go", Date: 1_700_000_000},
		&tg.ForumTopic{ID: 43, Title: "Frontend React", Date: 1_700_001_000},
	}

	count := p.applyForumTopics(serverRemoteID, serverTitle, parentPeer, topics)
	if count != 3 {
		t.Fatalf("se esperaban 3 temas (General inyectado + 2 temas), se obtuvieron %d", count)
	}

	peer1 := p.lookupPeer("-10098765_topic_1")
	if peer1 == nil || peer1.title != "General" || peer1.topicID != 1 {
		t.Fatalf("el tema General (ID 1) debe existir y llamarse 'General', se obtuvo: %+v", peer1)
	}
}

func TestInputReplyTo(t *testing.T) {
	// 1. Chat plano sin cita -> nil
	if got := inputReplyTo(0, 0); got != nil {
		t.Errorf("se esperaba nil para chat plano sin cita, se obtuvo %+v", got)
	}

	// 2. Chat plano con cita
	replyNormal, ok := inputReplyTo(0, 100).(*tg.InputReplyToMessage)
	if !ok || replyNormal == nil || replyNormal.ReplyToMsgID != 100 || replyNormal.TopMsgID != 0 {
		t.Errorf("cita en chat plano inválida: %+v", replyNormal)
	}

	// 3. Tema sin cita -> ReplyToMsgID = topicID
	replyTopicOnly, ok := inputReplyTo(42, 0).(*tg.InputReplyToMessage)
	if !ok || replyTopicOnly == nil || replyTopicOnly.ReplyToMsgID != 42 || replyTopicOnly.TopMsgID != 0 {
		t.Errorf("envío directo a tema inválido: %+v", replyTopicOnly)
	}

	// 4. Tema con cita -> ReplyToMsgID = replyToMsgID, TopMsgID = topicID
	replyTopicQuote, ok := inputReplyTo(42, 100).(*tg.InputReplyToMessage)
	if !ok || replyTopicQuote == nil || replyTopicQuote.ReplyToMsgID != 100 || replyTopicQuote.TopMsgID != 42 {
		t.Errorf("cita dentro de un tema inválida: %+v", replyTopicQuote)
	}

	// 5. Cita al mismo mensaje raíz del tema -> ReplyToMsgID = 42, sin TopMsgID redundante
	replyTopicRoot, ok := inputReplyTo(42, 42).(*tg.InputReplyToMessage)
	if !ok || replyTopicRoot == nil || replyTopicRoot.ReplyToMsgID != 42 || replyTopicRoot.TopMsgID != 0 {
		t.Errorf("cita a mensaje raíz del tema inválida: %+v", replyTopicRoot)
	}
}

func TestMessageEventInForumTopicWithoutQuote(t *testing.T) {
	p := NewTelegramPlugin()
	parentPeer := &tg.InputPeerChannel{ChannelID: 12345, AccessHash: 999}
	p.rememberPeer(&peerInfo{
		remoteID:    "-10012345",
		kind:        kindChannel,
		id:          12345,
		title:       "Foro Devs",
		channelType: "forum",
		peer:        parentPeer,
	})

	msg := &tg.Message{
		ID:      200,
		PeerID:  &tg.PeerChannel{ChannelID: 12345},
		FromID:  &tg.PeerUser{UserID: 888},
		Date:    1_700_000_200,
		Message: "Mensaje dentro del tema 42",
		ReplyTo: &tg.MessageReplyHeader{
			ForumTopic:   true,
			ReplyToMsgID: 42,
		},
	}

	ev := p.messageEvent(msg, "-10012345", kindChannel)
	if ev == nil {
		t.Fatal("se esperaba un evento de mensaje")
	}
	if ev.RemoteSenderId != "-10012345_topic_42" {
		t.Errorf("RemoteSenderId esperado -10012345_topic_42, se obtuvo %q", ev.RemoteSenderId)
	}
	if ev.ChatJid != "-10012345_topic_42" {
		t.Errorf("ChatJid esperado -10012345_topic_42, se obtuvo %q", ev.ChatJid)
	}
	if ev.ReplyToRemoteId != "" {
		t.Errorf("ReplyToRemoteId no debe contener el ID del tema como cita, se obtuvo %q", ev.ReplyToRemoteId)
	}
}

func TestMessageEventInForumTopicWithQuote(t *testing.T) {
	p := NewTelegramPlugin()
	parentPeer := &tg.InputPeerChannel{ChannelID: 12345, AccessHash: 999}
	p.rememberPeer(&peerInfo{
		remoteID:    "-10012345",
		kind:        kindChannel,
		id:          12345,
		title:       "Foro Devs",
		channelType: "forum",
		peer:        parentPeer,
	})

	msg := &tg.Message{
		ID:      201,
		PeerID:  &tg.PeerChannel{ChannelID: 12345},
		FromID:  &tg.PeerUser{UserID: 888},
		Date:    1_700_000_210,
		Message: "Respondiendo a mensaje 105 dentro del tema 42",
		ReplyTo: &tg.MessageReplyHeader{
			ForumTopic:   true,
			ReplyToTopID: 42,
			ReplyToMsgID: 105,
		},
	}

	ev := p.messageEvent(msg, "-10012345", kindChannel)
	if ev == nil {
		t.Fatal("se esperaba un evento de mensaje")
	}
	if ev.RemoteSenderId != "-10012345_topic_42" {
		t.Errorf("RemoteSenderId esperado -10012345_topic_42, se obtuvo %q", ev.RemoteSenderId)
	}
	if ev.ReplyToRemoteId != "105" {
		t.Errorf("ReplyToRemoteId esperado 105, se obtuvo %q", ev.ReplyToRemoteId)
	}
}

func TestMessageEventInForumTopicGeneralNoHeader(t *testing.T) {
	p := NewTelegramPlugin()
	parentPeer := &tg.InputPeerChannel{ChannelID: 12345, AccessHash: 999}
	p.rememberPeer(&peerInfo{
		remoteID:    "-10012345",
		kind:        kindChannel,
		id:          12345,
		title:       "Foro Devs",
		channelType: "forum",
		peer:        parentPeer,
	})
	// Registrar tema General (1)
	p.rememberPeer(&peerInfo{
		remoteID:    "-10012345_topic_1",
		kind:        kindChannel,
		id:          1,
		title:       "General",
		channelType: "forum",
		peer:        parentPeer,
		topicID:     1,
	})

	msg := &tg.Message{
		ID:      202,
		PeerID:  &tg.PeerChannel{ChannelID: 12345},
		FromID:  &tg.PeerUser{UserID: 888},
		Date:    1_700_000_220,
		Message: "Mensaje raíz en General",
	}

	ev := p.messageEvent(msg, "-10012345", kindChannel)
	if ev == nil {
		t.Fatal("se esperaba un evento de mensaje")
	}
	if ev.RemoteSenderId != "-10012345_topic_1" {
		t.Errorf("RemoteSenderId esperado -10012345_topic_1 para General sin cabecera, se obtuvo %q", ev.RemoteSenderId)
	}
}

func TestMessageEventInNormalChannelKeepsQuote(t *testing.T) {
	p := NewTelegramPlugin()
	parentPeer := &tg.InputPeerChannel{ChannelID: 12345, AccessHash: 999}
	p.rememberPeer(&peerInfo{
		remoteID:    "-10012345",
		kind:        kindChannel,
		id:          12345,
		title:       "Grupo Normal",
		channelType: "megagroup",
		peer:        parentPeer,
	})

	msg := &tg.Message{
		ID:      203,
		PeerID:  &tg.PeerChannel{ChannelID: 12345},
		FromID:  &tg.PeerUser{UserID: 888},
		Date:    1_700_000_230,
		Message: "Mensaje citando al 42 en grupo normal",
		ReplyTo: &tg.MessageReplyHeader{
			ReplyToMsgID: 42,
		},
	}

	ev := p.messageEvent(msg, "-10012345", kindChannel)
	if ev == nil {
		t.Fatal("se esperaba un evento de mensaje")
	}
	if ev.RemoteSenderId != "-10012345" {
		t.Errorf("RemoteSenderId esperado -10012345, se obtuvo %q", ev.RemoteSenderId)
	}
	if ev.ReplyToRemoteId != "42" {
		t.Errorf("ReplyToRemoteId esperado 42, se obtuvo %q", ev.ReplyToRemoteId)
	}
}

type tgTopicEvent struct {
	eventType       string
	remoteSenderId  string
	serverId        string
	parentChannelId string
	channelName     string
	channelType     string
}
