package main

import (
	"context"
	"errors"
	"sync"
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

// fakeDialogsAPI es un doble de dialogsAPI: sirve páginas de diálogos en orden
// y temas de foro por canal, y registra las peticiones recibidas.
//
// topicPages sirve una página distinta por cada petición de temas del canal
// (la última se repite); topics es la respuesta fija de un canal sin páginas.
type fakeDialogsAPI struct {
	pages      []tg.MessagesDialogsClass
	dialogReqs []*tg.MessagesGetDialogsRequest
	topics     map[int64]*tg.MessagesForumTopics
	topicPages map[int64][]*tg.MessagesForumTopics
	topicErrs  map[int64]error

	// topicPageErrs devuelve un error a partir de la petición N (base 0) del canal.
	topicPageErrs map[int64]int

	mu          sync.Mutex
	topicReqs   []*tg.MessagesGetForumTopicsRequest
	firstReqAt  int64 // unix en que llegó la primera petición de temas
	inflight    int
	maxInflight int
	// block, si no es nil, retiene cada petición de temas hasta que se cierre.
	block chan struct{}
	// started avisa (sin bloquear) cada vez que llega una petición de temas.
	started chan struct{}
}

func (f *fakeDialogsAPI) MessagesGetDialogs(_ context.Context, req *tg.MessagesGetDialogsRequest) (tg.MessagesDialogsClass, error) {
	f.dialogReqs = append(f.dialogReqs, req)
	idx := len(f.dialogReqs) - 1
	if idx >= len(f.pages) {
		return &tg.MessagesDialogsSlice{}, nil
	}
	return f.pages[idx], nil
}

func (f *fakeDialogsAPI) MessagesGetForumTopics(_ context.Context, req *tg.MessagesGetForumTopicsRequest) (*tg.MessagesForumTopics, error) {
	id := req.Peer.(*tg.InputPeerChannel).ChannelID
	f.mu.Lock()
	if f.firstReqAt == 0 {
		f.firstReqAt = nowUnix()
	}
	n := 0
	for _, r := range f.topicReqs {
		if r.Peer.(*tg.InputPeerChannel).ChannelID == id {
			n++
		}
	}
	f.topicReqs = append(f.topicReqs, req)
	f.inflight++
	if f.inflight > f.maxInflight {
		f.maxInflight = f.inflight
	}
	block, started := f.block, f.started
	f.mu.Unlock()
	defer func() {
		f.mu.Lock()
		f.inflight--
		f.mu.Unlock()
	}()

	if started != nil {
		select {
		case started <- struct{}{}:
		default:
		}
	}
	if block != nil {
		<-block
	}
	if err := f.topicErrs[id]; err != nil {
		return nil, err
	}
	if from, ok := f.topicPageErrs[id]; ok && n >= from {
		return nil, errors.New("page boom")
	}
	if pages := f.topicPages[id]; len(pages) > 0 {
		if n >= len(pages) {
			n = len(pages) - 1
		}
		return pages[n], nil
	}
	return f.topics[id], nil
}

func (f *fakeDialogsAPI) topicRequests() []*tg.MessagesGetForumTopicsRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*tg.MessagesGetForumTopicsRequest(nil), f.topicReqs...)
}

func drainEvents(p *TelegramPlugin) []string {
	var out []string
	for len(p.eventCh) > 0 {
		ev := <-p.eventCh
		out = append(out, ev.EventType+"|"+ev.RemoteSenderId+"|"+ev.ChannelName)
	}
	return out
}

func containsEvent(events []string, want string) bool {
	for _, e := range events {
		if e == want {
			return true
		}
	}
	return false
}

func TestApplyChatsForumTopicsErrorDoesNotPublishParent(t *testing.T) {
	p := NewTelegramPlugin()
	api := &fakeDialogsAPI{topicErrs: map[int64]error{555: errors.New("boom")}}

	p.applyChats(context.Background(), api, []tg.ChatClass{
		&tg.Channel{ID: 555, AccessHash: 1, Title: "Mi Foro", Forum: true, Megagroup: true},
	})

	events := drainEvents(p)
	if len(events) != 0 {
		t.Fatalf("con error de API no se debe emitir nada para el foro, se obtuvo %v", events)
	}
}

func TestApplyChatsForumEmptyTopicsPublishesGeneralAndReconciles(t *testing.T) {
	p := NewTelegramPlugin()
	api := &fakeDialogsAPI{topics: map[int64]*tg.MessagesForumTopics{555: {}}}

	p.applyChats(context.Background(), api, []tg.ChatClass{
		&tg.Channel{ID: 555, AccessHash: 1, Title: "Mi Foro", Forum: true, Megagroup: true},
	})

	events := drainFull(p)
	if len(events) != 2 {
		t.Fatalf("se esperaban General y un reconcile, se obtuvo %v", eventTypes(events))
	}
	if ev := events[0]; ev.EventType != "channel.sync" || ev.RemoteSenderId != "c555_topic_1" || ev.ChannelName != "General" {
		t.Fatalf("se esperaba el channel.sync de General, se obtuvo %+v", ev)
	}
	if ev := events[1]; ev.EventType != "channel.reconcile" || ev.Body != "c555_topic_1" {
		t.Fatalf("se esperaba el reconcile de General, se obtuvo %+v", ev)
	}
	for _, ev := range events {
		if ev.RemoteSenderId == "c555" {
			t.Fatalf("el foro no se publica como canal con el nombre del grupo: %+v", ev)
		}
	}
}

func TestApplyChatsNonForumChannelStillPublishesParent(t *testing.T) {
	p := NewTelegramPlugin()
	api := &fakeDialogsAPI{}

	p.applyChats(context.Background(), api, []tg.ChatClass{
		&tg.Channel{ID: 777, AccessHash: 1, Title: "Canal Normal", Megagroup: true},
	})

	events := drainEvents(p)
	if len(events) != 1 || events[0] != "channel.sync|c777|Canal Normal" {
		t.Fatalf("un canal no foro debe publicarse como antes, se obtuvo %v", events)
	}
}

func channelDialogPage(slice bool, ids ...int64) tg.MessagesDialogsClass {
	var dialogs []tg.DialogClass
	var chats []tg.ChatClass
	var msgs []tg.MessageClass
	for _, id := range ids {
		dialogs = append(dialogs, &tg.Dialog{Peer: &tg.PeerChannel{ChannelID: id}, TopMessage: int(id)})
		chats = append(chats, &tg.Channel{ID: id, AccessHash: id * 10, Title: "Foro", Forum: true, Megagroup: true})
		msgs = append(msgs, &tg.Message{ID: int(id), PeerID: &tg.PeerChannel{ChannelID: id}, Date: int(1_700_000_000 + id)})
	}
	if slice {
		return &tg.MessagesDialogsSlice{Dialogs: dialogs, Chats: chats, Messages: msgs}
	}
	return &tg.MessagesDialogs{Dialogs: dialogs, Chats: chats, Messages: msgs}
}

func TestSyncDialogsPaginatesBeyondFirstPage(t *testing.T) {
	p := NewTelegramPlugin()

	first := make([]int64, dialogsPageLimit)
	for i := range first {
		first[i] = int64(1000 + i)
	}
	api := &fakeDialogsAPI{
		pages: []tg.MessagesDialogsClass{
			channelDialogPage(true, first...),
			channelDialogPage(false, 9001),
		},
		topics: map[int64]*tg.MessagesForumTopics{
			9001: {Topics: []tg.ForumTopicClass{&tg.ForumTopic{ID: 7, Title: "Segundo"}}},
		},
	}

	p.syncDialogsAPI(context.Background(), api)

	if len(api.dialogReqs) != 2 {
		t.Fatalf("se esperaban 2 peticiones de diálogos, se obtuvieron %d", len(api.dialogReqs))
	}
	second := api.dialogReqs[1]
	last := first[len(first)-1]
	peer, ok := second.OffsetPeer.(*tg.InputPeerChannel)
	if !ok || peer.ChannelID != last || peer.AccessHash != last*10 {
		t.Fatalf("OffsetPeer de la 2ª página inválido: %+v", second.OffsetPeer)
	}
	if second.OffsetID != int(last) || second.OffsetDate != int(1_700_000_000+last) {
		t.Fatalf("offsets de la 2ª página inválidos: id=%d date=%d", second.OffsetID, second.OffsetDate)
	}

	events := drainEvents(p)
	if !containsEvent(events, "channel.sync|c9001_topic_7|Segundo") {
		t.Fatalf("los temas del foro de la 2ª página no se publicaron: %v", events)
	}
}

func TestSyncDialogsStopsOnShortPage(t *testing.T) {
	p := NewTelegramPlugin()
	api := &fakeDialogsAPI{pages: []tg.MessagesDialogsClass{channelDialogPage(true, 1, 2)}}

	p.syncDialogsAPI(context.Background(), api)

	if len(api.dialogReqs) != 1 {
		t.Fatalf("una página corta debe terminar la paginación, peticiones=%d", len(api.dialogReqs))
	}
}

func TestSyncDialogsStopsWithoutProgressAndCapsPages(t *testing.T) {
	p := NewTelegramPlugin()
	full := make([]int64, dialogsPageLimit)
	for i := range full {
		full[i] = int64(1000 + i)
	}
	same := channelDialogPage(true, full...)
	pages := make([]tg.MessagesDialogsClass, maxDialogPages+5)
	for i := range pages {
		pages[i] = same
	}
	api := &fakeDialogsAPI{pages: pages}

	p.syncDialogsAPI(context.Background(), api)

	if len(api.dialogReqs) != 2 {
		t.Fatalf("la misma página repetida (sin progreso) debe cortar en la 2ª petición, peticiones=%d", len(api.dialogReqs))
	}
}
