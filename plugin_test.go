package main

import (
	"testing"

	conduitv1 "github.com/cestila/ConduitOmniChat/sdk/conduitv1"
	"github.com/gotd/td/tg"
)

func TestParseAPIID(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    int
		wantErr bool
	}{
		{name: "válido", raw: "1234567", want: 1234567},
		{name: "con espacios", raw: "  42  ", want: 42},
		{name: "vacío", raw: "", wantErr: true},
		{name: "no numérico", raw: "abc", wantErr: true},
		{name: "negativo", raw: "-5", wantErr: true},
		{name: "cero", raw: "0", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseAPIID(tt.raw)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("se esperaba error para %q", tt.raw)
				}
				return
			}
			if err != nil {
				t.Fatalf("error inesperado: %v", err)
			}
			if got != tt.want {
				t.Fatalf("got %d, want %d", got, tt.want)
			}
		})
	}
}

func TestSanitizeAccountName(t *testing.T) {
	tests := map[string]string{
		"personal":           "personal",
		"Mi Cuenta/Personal": "MiCuentaPersonal",
		"":                   "default",
		"...":                "default",
		"a-b_c":              "a-b_c",
	}

	for raw, want := range tests {
		if got := sanitizeAccountName(raw); got != want {
			t.Errorf("sanitizeAccountName(%q) = %q, want %q", raw, got, want)
		}
	}
}

// El teléfono que Telegram expone del usuario debe viajar en el contacto para
// que el core una la misma persona con su cuenta de WhatsApp.
func TestApplyUsersEmitsPhoneIdentity(t *testing.T) {
	p := NewTelegramPlugin()
	p.applyUsers([]tg.UserClass{
		&tg.User{ID: 42, FirstName: "Ana", Phone: "+5491100007777"},
	})

	var events []*conduitv1.PluginEvent
	for len(p.eventCh) > 0 {
		events = append(events, <-p.eventCh)
	}
	if len(events) != 1 {
		t.Fatalf("se esperaba un contact.sync, llegaron %d eventos", len(events))
	}
	if events[0].IdentityPhone != "+5491100007777" {
		t.Fatalf("el contacto debió llevar el teléfono, obtuvo %q", events[0].IdentityPhone)
	}
}

// Un mensaje 1 a 1 también lleva el teléfono, para unir el contacto aunque el
// contact.sync todavía no haya llegado.
func TestMessageEventCarriesPhoneHintForDM(t *testing.T) {
	p := NewTelegramPlugin()
	p.rememberEntities(&tg.Entities{Users: map[int64]*tg.User{
		42: {ID: 42, FirstName: "Ana", Phone: "+5491100007777", AccessHash: 1},
	}})

	event := p.messageEvent(&tg.Message{
		ID:      7,
		PeerID:  &tg.PeerUser{UserID: 42},
		FromID:  &tg.PeerUser{UserID: 42},
		Date:    1_700_000_000,
		Message: "hola",
	}, "u42", kindUser)

	if event == nil {
		t.Fatal("se esperaba un evento de mensaje")
	}
	if event.IdentityPhone != "+5491100007777" {
		t.Fatalf("el mensaje debió llevar el teléfono, obtuvo %q", event.IdentityPhone)
	}
}

func TestRemoteIDForPeer(t *testing.T) {
	tests := []struct {
		peer     tg.PeerClass
		wantID   string
		wantKind peerKind
	}{
		{&tg.PeerUser{UserID: 7}, "u7", kindUser},
		{&tg.PeerChat{ChatID: 8}, "g8", kindGroup},
		{&tg.PeerChannel{ChannelID: 9}, "c9", kindChannel},
	}

	for _, tt := range tests {
		id, kind, numeric := remoteIDForPeer(tt.peer)
		if id != tt.wantID || kind != tt.wantKind {
			t.Errorf("remoteIDForPeer(%T) = (%q,%q), want (%q,%q)", tt.peer, id, kind, tt.wantID, tt.wantKind)
		}
		if numeric == 0 {
			t.Errorf("remoteIDForPeer(%T) perdió el id numérico", tt.peer)
		}
	}
}

func TestMessageEventExtractsReplyTo(t *testing.T) {
	p := NewTelegramPlugin()
	msg := &tg.Message{
		ID:      10,
		PeerID:  &tg.PeerUser{UserID: 42},
		FromID:  &tg.PeerUser{UserID: 42},
		Date:    1_700_000_000,
		Message: "Respuesta con cita",
		ReplyTo: &tg.MessageReplyHeader{
			ReplyToMsgID: 5,
		},
	}

	event := p.messageEvent(msg, "u42", kindUser)
	if event == nil {
		t.Fatal("se esperaba un evento de mensaje")
	}
	if event.ReplyToRemoteId != "5" {
		t.Fatalf("se esperaba ReplyToRemoteId='5', se obtuvo %q", event.ReplyToRemoteId)
	}
}

func TestHandleReactionUpdate(t *testing.T) {
	p := NewTelegramPlugin()
	p.handleReactionUpdate(&tg.UpdateMessageReactions{
		MsgID: 10,
		Peer:  &tg.PeerUser{UserID: 42},
		Reactions: tg.MessageReactions{
			Results: []tg.ReactionCount{
				{
					Reaction: &tg.ReactionEmoji{Emoticon: "🎉"},
					Count:    2,
				},
			},
		},
	})

	if len(p.eventCh) != 1 {
		t.Fatalf("se esperaba 1 evento en eventCh, hay %d", len(p.eventCh))
	}
	ev := <-p.eventCh
	if ev.EventType != "message.reaction" {
		t.Errorf("EventType esperado 'message.reaction', obtenido %q", ev.EventType)
	}
	if ev.RemoteMessageId != "10" {
		t.Errorf("RemoteMessageId esperado '10', obtenido %q", ev.RemoteMessageId)
	}
	if ev.ReactionEmoji != "🎉" {
		t.Errorf("ReactionEmoji esperado '🎉', obtenido %q", ev.ReactionEmoji)
	}
}

func TestMessageEventParsesPoll(t *testing.T) {
	p := NewTelegramPlugin()
	msg := &tg.Message{
		ID:     15,
		PeerID: &tg.PeerUser{UserID: 42},
		FromID: &tg.PeerUser{UserID: 42},
		Date:   1_700_000_000,
		Media: &tg.MessageMediaPoll{
			Poll: tg.Poll{
				ID:       1001,
				Question: tg.TextWithEntities{Text: "¿Qué comemos hoy?"},
				Answers: []tg.PollAnswerClass{
					&tg.PollAnswer{Text: tg.TextWithEntities{Text: "Asado"}, Option: []byte{0x01}},
					&tg.PollAnswer{Text: tg.TextWithEntities{Text: "Pastas"}, Option: []byte{0x02}},
				},
				Closed: false,
			},
			Results: tg.PollResults{
				TotalVoters: 5,
				Results: []tg.PollAnswerVoters{
					{Option: []byte{0x01}, Voters: 3, Chosen: true},
					{Option: []byte{0x02}, Voters: 2, Chosen: false},
				},
			},
		},
	}

	event := p.messageEvent(msg, "u42", kindUser)
	if event == nil {
		t.Fatal("se esperaba un evento de mensaje")
	}
	if event.ContentType != "poll" {
		t.Fatalf("ContentType esperado 'poll', obtenido %q", event.ContentType)
	}
	if event.Poll == nil {
		t.Fatal("se esperaba event.Poll no nulo")
	}
	if event.Poll.Question != "¿Qué comemos hoy?" {
		t.Errorf("Pregunta esperada '¿Qué comemos hoy?', obtenida %q", event.Poll.Question)
	}
	if len(event.Poll.Options) != 2 {
		t.Fatalf("se esperaban 2 opciones, obtenidas %d", len(event.Poll.Options))
	}
	if event.Poll.Options[0].Text != "Asado" || event.Poll.Options[0].VoteCount != 3 || !event.Poll.Options[0].Chosen {
		t.Errorf("opción 0 inesperada: %+v", event.Poll.Options[0])
	}
	if event.Poll.Options[1].Text != "Pastas" || event.Poll.Options[1].VoteCount != 2 || event.Poll.Options[1].Chosen {
		t.Errorf("opción 1 inesperada: %+v", event.Poll.Options[1])
	}
}

func TestMessageEventParsesReplyInlineMarkup(t *testing.T) {
	p := NewTelegramPlugin()
	msg := &tg.Message{
		ID:      20,
		PeerID:  &tg.PeerUser{UserID: 42},
		FromID:  &tg.PeerUser{UserID: 42},
		Date:    1_700_000_000,
		Message: "Elige una opción",
		ReplyMarkup: &tg.ReplyInlineMarkup{
			Rows: []tg.KeyboardInlineButtonRow{
				{
					Buttons: []tg.KeyboardInlineButton{
						{Text: "Aceptar", Type: &tg.InlineButtonTypeCallback{Data: []byte("btn_accept")}},
						{Text: "Sitio Web", Type: &tg.InlineButtonTypeURL{URL: "https://conduit.chat"}},
					},
				},
			},
		},
	}

	event := p.messageEvent(msg, "u42", kindUser)
	if event == nil {
		t.Fatal("se esperaba un evento de mensaje")
	}
	if len(event.ButtonRows) != 1 {
		t.Fatalf("se esperaba 1 fila de botones, obtenidas %d", len(event.ButtonRows))
	}
	row := event.ButtonRows[0]
	if len(row.Buttons) != 2 {
		t.Fatalf("se esperaban 2 botones, obtenidos %d", len(row.Buttons))
	}
	if row.Buttons[0].Text != "Aceptar" || row.Buttons[0].Id != "btn_accept" || row.Buttons[0].Type != "callback" {
		t.Errorf("botón 0 inesperado: %+v", row.Buttons[0])
	}
	if row.Buttons[1].Text != "Sitio Web" || row.Buttons[1].Url != "https://conduit.chat" || row.Buttons[1].Type != "url" {
		t.Errorf("botón 1 inesperado: %+v", row.Buttons[1])
	}
}

func TestMessageEventParsesGeoLocation(t *testing.T) {
	p := NewTelegramPlugin()
	msg := &tg.Message{
		ID:     25,
		PeerID: &tg.PeerUser{UserID: 42},
		FromID: &tg.PeerUser{UserID: 42},
		Date:   1_700_000_000,
		Media: &tg.MessageMediaGeo{
			Geo: &tg.GeoPoint{
				Lat:  -34.6037,
				Long: -58.3816,
			},
		},
	}

	event := p.messageEvent(msg, "u42", kindUser)
	if event == nil {
		t.Fatal("se esperaba un evento de mensaje")
	}
	if event.ContentType != "location" {
		t.Fatalf("ContentType esperado 'location', obtenido %q", event.ContentType)
	}
	if event.Location == nil || event.Location.Latitude != -34.6037 || event.Location.Longitude != -58.3816 {
		t.Fatalf("Location inesperado: %+v", event.Location)
	}
}

func TestMessageEventParsesSharedContact(t *testing.T) {
	p := NewTelegramPlugin()
	msg := &tg.Message{
		ID:     26,
		PeerID: &tg.PeerUser{UserID: 42},
		FromID: &tg.PeerUser{UserID: 42},
		Date:   1_700_000_000,
		Media: &tg.MessageMediaContact{
			PhoneNumber: "+5491199887766",
			FirstName:   "Ana",
			LastName:    "Ejemplo",
			Vcard:       "BEGIN:VCARD...",
		},
	}

	event := p.messageEvent(msg, "u42", kindUser)
	if event == nil {
		t.Fatal("se esperaba un evento de mensaje")
	}
	if event.ContentType != "contact" {
		t.Fatalf("ContentType esperado 'contact', obtenido %q", event.ContentType)
	}
	if event.SharedContact == nil || event.SharedContact.DisplayName != "Ana Ejemplo" || event.SharedContact.PhoneNumber != "+5491199887766" {
		t.Fatalf("SharedContact inesperado: %+v", event.SharedContact)
	}
}

func TestMessageEventParsesEditAndPinned(t *testing.T) {
	p := NewTelegramPlugin()
	msg := &tg.Message{
		ID:       27,
		PeerID:   &tg.PeerUser{UserID: 42},
		FromID:   &tg.PeerUser{UserID: 42},
		Date:     1_700_000_000,
		Message:  "Mensaje editado y fijado",
		EditDate: 1_700_000_050,
		Pinned:   true,
	}

	event := p.messageEvent(msg, "u42", kindUser)
	if event == nil {
		t.Fatal("se esperaba un evento de mensaje")
	}
	if !event.IsEdited || event.EditedAtUnix != 1_700_000_050 {
		t.Errorf("IsEdited / EditedAtUnix inesperados: %v / %d", event.IsEdited, event.EditedAtUnix)
	}
	if !event.IsPinned {
		t.Errorf("se esperaba IsPinned = true")
	}
}

func TestChannelTypeFor(t *testing.T) {
	tests := []struct {
		name    string
		channel *tg.Channel
		want    string
	}{
		{name: "canal de difusión", channel: &tg.Channel{Broadcast: true}, want: "announcement"},
		{name: "supergrupo con temas", channel: &tg.Channel{Megagroup: true, Forum: true}, want: "forum"},
		{name: "supergrupo normal", channel: &tg.Channel{Megagroup: true}, want: "megagroup"},
	}

	for _, tt := range tests {
		if got := channelTypeFor(tt.channel); got != tt.want {
			t.Errorf("%s: got %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestConnectRejectsMissingCredentials(t *testing.T) {
	p := NewTelegramPlugin()

	cases := []struct {
		name  string
		creds *conduitv1.ProviderCredentials
		want  string
	}{
		{name: "sin api_id", creds: &conduitv1.ProviderCredentials{ApiHash: "hash"}, want: "api_id"},
		{name: "sin api_hash", creds: &conduitv1.ProviderCredentials{ApiId: "12345"}, want: "api_hash"},
		{name: "api_id no numérico", creds: &conduitv1.ProviderCredentials{ApiId: "x", ApiHash: "hash"}, want: "api_id"},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			_, err := p.Connect(t.Context(), &conduitv1.ConnectRequest{Credentials: tt.creds})
			if err == nil {
				t.Fatal("se esperaba error de credenciales")
			}
			if !contains(err.Error(), tt.want) {
				t.Fatalf("el error %q no menciona %q", err.Error(), tt.want)
			}
		})
	}
}

func TestMessageEventForGroupKeepsServerIdentity(t *testing.T) {
	p := NewTelegramPlugin()
	groupID := int64(-100123)
	remoteID := channelRemoteID(groupID)
	p.rememberPeer(&peerInfo{
		remoteID:    remoteID,
		kind:        kindChannel,
		id:          groupID,
		title:       "Comunidad Devs",
		channelType: "megagroup",
		peer:        &tg.InputPeerChannel{ChannelID: groupID, AccessHash: 1},
	})

	msg := &tg.Message{
		ID:      55,
		PeerID:  &tg.PeerChannel{ChannelID: groupID},
		FromID:  &tg.PeerUser{UserID: 42},
		Message: "hola",
		Date:    1700000000,
	}

	event := p.messageEvent(msg, remoteID, kindChannel)
	if event == nil {
		t.Fatal("no se generó evento")
	}
	if event.GetChatJid() != remoteID {
		t.Fatalf("chat_jid = %q, want %q", event.GetChatJid(), remoteID)
	}
	if event.GetServerId() != remoteID {
		t.Fatalf("server_id = %q, want %q (el core usa server_id para tratar el chat como grupo)", event.GetServerId(), remoteID)
	}
	if event.GetServerName() != "Comunidad Devs" {
		t.Fatalf("server_name = %q", event.GetServerName())
	}
	if event.GetParticipantId() != "u42" {
		t.Fatalf("participant_id = %q, want u42", event.GetParticipantId())
	}
	if event.GetEventType() != "message.received" {
		t.Fatalf("event_type = %q", event.GetEventType())
	}
}

func TestMessageEventForDMHasNoServer(t *testing.T) {
	p := NewTelegramPlugin()
	remoteID := userRemoteID(42)

	msg := &tg.Message{
		ID:      56,
		PeerID:  &tg.PeerUser{UserID: 42},
		FromID:  &tg.PeerUser{UserID: 42},
		Message: "hola",
		Date:    1700000001,
	}

	event := p.messageEvent(msg, remoteID, kindUser)
	if event == nil {
		t.Fatal("no se generó evento")
	}
	if event.GetServerId() != "" {
		t.Fatalf("un chat 1 a 1 no debe llevar server_id, got %q", event.GetServerId())
	}
}

func TestMessageEventSkipsEmptyBody(t *testing.T) {
	p := NewTelegramPlugin()
	msg := &tg.Message{ID: 1, PeerID: &tg.PeerUser{UserID: 2}, Message: "   ", Date: 1}
	if event := p.messageEvent(msg, userRemoteID(2), kindUser); event != nil {
		t.Fatal("un mensaje sin texto no debe emitirse todavía")
	}
}

func contains(haystack, needle string) bool {
	return len(needle) == 0 || (len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0)
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}

func TestHistoryOffsetID(t *testing.T) {
	tests := map[string]int{
		"":        0,
		"   ":     0,
		"125":     125,
		"0":       0,
		"-7":      0,
		"out-123": 0,
		"abc":     0,
	}

	for raw, want := range tests {
		if got := historyOffsetID(raw); got != want {
			t.Errorf("historyOffsetID(%q) = %d, want %d", raw, got, want)
		}
	}
}

// El core normaliza los identificadores como teléfonos: un chat "u123" puede
// volver a pedir historial como "+123". Debe resolverse igual.
func TestLookupPeerByDigits(t *testing.T) {
	p := NewTelegramPlugin()
	p.rememberPeer(&peerInfo{remoteID: "u123", id: 123, peer: &tg.InputPeerUser{UserID: 123}})

	if got := p.lookupPeer("u123"); got == nil || got.id != 123 {
		t.Fatalf("lookupPeer exacto falló: %+v", got)
	}
	if got := p.lookupPeer("+123"); got == nil || got.id != 123 {
		t.Fatalf("lookupPeer por dígitos falló: %+v", got)
	}
	if got := p.lookupPeer("999"); got != nil {
		t.Fatalf("lookupPeer inventó un peer: %+v", got)
	}
}

func TestTelegramDeleteMessagesEvents(t *testing.T) {
	p := NewTelegramPlugin()

	// 1. Delete en chat privado / normal
	p.handleDeleteMessages([]int{101, 102})

	select {
	case ev := <-p.eventCh:
		if ev.EventType != "message.deleted" || ev.RemoteMessageId != "101" {
			t.Errorf("primer evento inesperado: %+v", ev)
		}
	default:
		t.Fatal("se esperaba evento para mensaje 101")
	}

	select {
	case ev := <-p.eventCh:
		if ev.EventType != "message.deleted" || ev.RemoteMessageId != "102" {
			t.Errorf("segundo evento inesperado: %+v", ev)
		}
	default:
		t.Fatal("se esperaba evento para mensaje 102")
	}

	// 2. Delete en canal / supergrupo con ChannelID
	p.handleDeleteChannelMessages(777, []int{201})

	select {
	case ev := <-p.eventCh:
		if ev.EventType != "message.deleted" || ev.RemoteMessageId != "201" {
			t.Errorf("evento de canal inesperado: %+v", ev)
		}
		if ev.ChatJid != "c777" || ev.RemoteSenderId != "c777" {
			t.Errorf("chat context esperado c777, obtenido %q / %q", ev.ChatJid, ev.RemoteSenderId)
		}
	default:
		t.Fatal("se esperaba evento para mensaje 201 en canal 777")
	}
}
