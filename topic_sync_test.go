package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/gotd/td/tg"

	conduitv1 "github.com/cestila/ConduitOmniChat/sdk/conduitv1"
)

func testForum(id int64) *tg.Channel {
	return &tg.Channel{ID: id, AccessHash: 1, Title: "Mi Foro", Forum: true, Megagroup: true}
}

func drainFull(p *TelegramPlugin) []*conduitv1.PluginEvent {
	var out []*conduitv1.PluginEvent
	for len(p.eventCh) > 0 {
		out = append(out, <-p.eventCh)
	}
	return out
}

func eventTypes(events []*conduitv1.PluginEvent) []string {
	var out []string
	for _, ev := range events {
		out = append(out, ev.EventType+"|"+ev.RemoteSenderId)
	}
	return out
}

func reconcilesOf(events []*conduitv1.PluginEvent) []*conduitv1.PluginEvent {
	var out []*conduitv1.PluginEvent
	for _, ev := range events {
		if ev.EventType == "channel.reconcile" {
			out = append(out, ev)
		}
	}
	return out
}

func liveTopic(id int) *tg.ForumTopic {
	return &tg.ForumTopic{ID: id, Title: fmt.Sprintf("Tema %d", id), Date: 1_700_000_000 + id, TopMessage: 1000 + id}
}

func topicMessages(ids ...int) []tg.MessageClass {
	var out []tg.MessageClass
	for _, id := range ids {
		out = append(out, &tg.Message{ID: 1000 + id, Date: 1_800_000_000 + id, PeerID: &tg.PeerChannel{ChannelID: 555}})
	}
	return out
}

func TestSyncForumTopicsCompleteListEmitsSyncsThenOneReconcile(t *testing.T) {
	p := NewTelegramPlugin()
	// General no viene en la respuesta: igualmente debe estar en la lista viva.
	api := &fakeDialogsAPI{topics: map[int64]*tg.MessagesForumTopics{555: {
		Count:  2,
		Topics: []tg.ForumTopicClass{liveTopic(7), liveTopic(5)},
	}}}

	before := nowUnix()
	if _, err := p.syncForumTopics(context.Background(), api, testForum(555)); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	events := drainFull(p)
	want := []string{
		"channel.sync|c555_topic_1",
		"channel.sync|c555_topic_7",
		"channel.sync|c555_topic_5",
		"channel.reconcile|",
	}
	got := eventTypes(events)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("orden de eventos inesperado:\n got %v\nwant %v", got, want)
	}
	rec := events[len(events)-1]
	if rec.ServerId != "c555" {
		t.Errorf("ServerId = %q, se esperaba c555", rec.ServerId)
	}
	if rec.Body != "c555_topic_1\nc555_topic_5\nc555_topic_7" {
		t.Errorf("Body = %q", rec.Body)
	}
	api.mu.Lock()
	firstReq := api.firstReqAt
	api.mu.Unlock()
	if rec.SentAtUnix < before || rec.SentAtUnix > firstReq {
		t.Errorf("SentAtUnix = %d, debe ser la hora de inicio del fetch (entre %d y %d)", rec.SentAtUnix, before, firstReq)
	}
}

func TestSyncForumTopicsPaginatesAndReconcilesAllTopics(t *testing.T) {
	p := NewTelegramPlugin()
	var page1, page2 []tg.ForumTopicClass
	for i := 1; i <= 100; i++ {
		page1 = append(page1, liveTopic(i))
	}
	for i := 101; i <= 150; i++ {
		page2 = append(page2, liveTopic(i))
	}
	ids1 := make([]int, 0, 100)
	for i := 1; i <= 100; i++ {
		ids1 = append(ids1, i)
	}
	api := &fakeDialogsAPI{topicPages: map[int64][]*tg.MessagesForumTopics{555: {
		{Count: 150, Topics: page1, Messages: topicMessages(ids1...)},
		{Count: 150, Topics: page2},
	}}}

	if _, err := p.syncForumTopics(context.Background(), api, testForum(555)); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	reqs := api.topicRequests()
	if len(reqs) != 2 {
		t.Fatalf("se esperaban 2 peticiones, hubo %d", len(reqs))
	}
	if reqs[0].Limit != 100 {
		t.Errorf("Limit = %d, se esperaba 100", reqs[0].Limit)
	}
	if reqs[1].OffsetTopic != 100 || reqs[1].OffsetID != 1100 || reqs[1].OffsetDate != 1_800_000_100 {
		t.Errorf("offsets de la 2ª página inválidos: topic=%d id=%d date=%d", reqs[1].OffsetTopic, reqs[1].OffsetID, reqs[1].OffsetDate)
	}

	events := drainFull(p)
	recs := reconcilesOf(events)
	if len(recs) != 1 {
		t.Fatalf("se esperaba exactamente un reconcile, hubo %d", len(recs))
	}
	if n := len(strings.Split(recs[0].Body, "\n")); n != 150 {
		t.Fatalf("el reconcile debe traer los 150 temas, trae %d", n)
	}
	if !strings.Contains(recs[0].Body, "c555_topic_150") {
		t.Errorf("falta un tema de la 2ª página en el reconcile")
	}
	if events[len(events)-1].EventType != "channel.reconcile" {
		t.Errorf("el reconcile debe ser el último evento")
	}
}

func TestSyncForumTopicsShorterThanCountDoesNotReconcile(t *testing.T) {
	p := NewTelegramPlugin()
	api := &fakeDialogsAPI{topics: map[int64]*tg.MessagesForumTopics{555: {
		Count:  5,
		Topics: []tg.ForumTopicClass{liveTopic(1), liveTopic(7)},
	}}}

	if _, err := p.syncForumTopics(context.Background(), api, testForum(555)); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	events := drainFull(p)
	if len(reconcilesOf(events)) != 0 {
		t.Fatalf("lista menor que Count no debe emitir reconcile: %v", eventTypes(events))
	}
	if !containsEvent(drainStrings(events), "channel.sync|c555_topic_7|Tema 7") {
		t.Fatalf("lo obtenido sí debe publicarse como channel.sync: %v", eventTypes(events))
	}
}

func drainStrings(events []*conduitv1.PluginEvent) []string {
	var out []string
	for _, ev := range events {
		out = append(out, ev.EventType+"|"+ev.RemoteSenderId+"|"+ev.ChannelName)
	}
	return out
}

func TestSyncForumTopicsAPIErrorDoesNotReconcile(t *testing.T) {
	t.Run("primera página", func(t *testing.T) {
		p := NewTelegramPlugin()
		api := &fakeDialogsAPI{topicErrs: map[int64]error{555: errors.New("boom")}}
		if _, err := p.syncForumTopics(context.Background(), api, testForum(555)); err == nil {
			t.Fatal("se esperaba el error de la API")
		}
		if events := drainFull(p); len(events) != 0 {
			t.Fatalf("no se debe emitir nada: %v", eventTypes(events))
		}
	})
	t.Run("segunda página", func(t *testing.T) {
		p := NewTelegramPlugin()
		var page1 []tg.ForumTopicClass
		ids := []int{}
		for i := 1; i <= 100; i++ {
			page1 = append(page1, liveTopic(i))
			ids = append(ids, i)
		}
		api := &fakeDialogsAPI{
			topicPages:    map[int64][]*tg.MessagesForumTopics{555: {{Count: 150, Topics: page1, Messages: topicMessages(ids...)}}},
			topicPageErrs: map[int64]int{555: 1},
		}
		if _, err := p.syncForumTopics(context.Background(), api, testForum(555)); err == nil {
			t.Fatal("se esperaba el error de la API")
		}
		events := drainFull(p)
		if len(reconcilesOf(events)) != 0 {
			t.Fatal("un error a mitad de paginación no debe emitir reconcile")
		}
		if len(events) != 100 {
			t.Fatalf("lo obtenido se publica como channel.sync: %d eventos", len(events))
		}
	})
}

func TestSyncForumTopicsEmptyListReconcilesOnlyGeneral(t *testing.T) {
	p := NewTelegramPlugin()
	api := &fakeDialogsAPI{topics: map[int64]*tg.MessagesForumTopics{555: {}}}
	if _, err := p.syncForumTopics(context.Background(), api, testForum(555)); err != nil {
		t.Fatal(err)
	}
	recs := reconcilesOf(drainFull(p))
	if len(recs) != 1 {
		t.Fatalf("una lista vacía y completa solo tiene General: reconciles=%d", len(recs))
	}
	if recs[0].Body != "c555_topic_1" {
		t.Fatalf("Body = %q, se esperaba solo General", recs[0].Body)
	}
}

// Count puede no incluir los temas eliminados que llegan como ForumTopicDeleted.
// Con stubs eliminados hace falta una segunda pasada, y solo reconcilia si ambas
// pasadas coinciden en el conjunto de temas vivos.
func TestSyncForumTopicsDeletedStubsRequireMatchingSecondPass(t *testing.T) {
	p := NewTelegramPlugin()
	api := &fakeDialogsAPI{topicPages: map[int64][]*tg.MessagesForumTopics{555: {
		{Count: 2, Topics: []tg.ForumTopicClass{liveTopic(5), liveTopic(7), &tg.ForumTopicDeleted{ID: 9}}},
		{Count: 2, Topics: []tg.ForumTopicClass{liveTopic(5), liveTopic(8), &tg.ForumTopicDeleted{ID: 9}}},
	}}}
	if _, err := p.syncForumTopics(context.Background(), api, testForum(555)); err != nil {
		t.Fatal(err)
	}
	if recs := reconcilesOf(drainFull(p)); len(recs) != 0 {
		t.Fatalf("dos pasadas con temas vivos distintos no deben reconciliar: %d", len(recs))
	}
	if n := len(api.topicRequests()); n != 2 {
		t.Fatalf("se esperaban 2 pasadas, hubo %d peticiones", n)
	}
}

func TestSyncForumTopicsDeletedStubsIdenticalPassesReconcile(t *testing.T) {
	p := NewTelegramPlugin()
	page := &tg.MessagesForumTopics{Count: 2, Topics: []tg.ForumTopicClass{liveTopic(5), liveTopic(7), &tg.ForumTopicDeleted{ID: 9}}}
	api := &fakeDialogsAPI{topicPages: map[int64][]*tg.MessagesForumTopics{555: {page, page}}}
	if _, err := p.syncForumTopics(context.Background(), api, testForum(555)); err != nil {
		t.Fatal(err)
	}
	recs := reconcilesOf(drainFull(p))
	if len(recs) != 1 {
		t.Fatalf("dos pasadas idénticas deben reconciliar una vez, hubo %d", len(recs))
	}
	if recs[0].Body != "c555_topic_1\nc555_topic_5\nc555_topic_7" {
		t.Fatalf("Body = %q", recs[0].Body)
	}
	if n := len(api.topicRequests()); n != 2 {
		t.Fatalf("se esperaban exactamente 2 peticiones, hubo %d", n)
	}
}

// Un stub eliminado en una lista obtenida marca el tema como borrado aunque no
// estuviera registrado: lookupPeer no debe sintetizarlo.
func TestSyncForumTopicsDeletedStubStopsSynthesis(t *testing.T) {
	p := NewTelegramPlugin()
	p.rememberPeer(&peerInfo{remoteID: "c555", kind: kindChannel, id: 555, channelType: "forum", peer: &tg.InputPeerChannel{ChannelID: 555, AccessHash: 1}})
	if p.lookupPeer("c555_topic_9") == nil {
		t.Fatal("precondición: un tema no registrado se sintetiza")
	}
	api := &fakeDialogsAPI{topics: map[int64]*tg.MessagesForumTopics{555: {
		Count:  1,
		Topics: []tg.ForumTopicClass{liveTopic(5), &tg.ForumTopicDeleted{ID: 9}},
	}}}
	if _, err := p.syncForumTopics(context.Background(), api, testForum(555)); err != nil {
		t.Fatal(err)
	}
	if p.lookupPeer("c555_topic_9") != nil {
		t.Fatal("un stub eliminado debe impedir que lookupPeer sintetice el tema")
	}
}

func TestSyncForumTopicsDeletedTopicLeavesLiveListAndStopsResolving(t *testing.T) {
	p := NewTelegramPlugin()
	p.rememberPeer(&peerInfo{remoteID: "c555", kind: kindChannel, id: 555, channelType: "forum", title: "Mi Foro",
		peer: &tg.InputPeerChannel{ChannelID: 555, AccessHash: 1}})
	p.rememberPeer(&peerInfo{remoteID: "c555_topic_9", kind: kindChannel, id: 9, channelType: "forum", topicID: 9,
		peer: &tg.InputPeerChannel{ChannelID: 555, AccessHash: 1}})
	if p.lookupPeer("c555_topic_9") == nil {
		t.Fatal("precondición: el tema 9 debe resolverse")
	}

	api := &fakeDialogsAPI{topics: map[int64]*tg.MessagesForumTopics{555: {
		Count:  3,
		Topics: []tg.ForumTopicClass{liveTopic(1), liveTopic(5), &tg.ForumTopicDeleted{ID: 9}},
	}}}
	if _, err := p.syncForumTopics(context.Background(), api, testForum(555)); err != nil {
		t.Fatal(err)
	}

	events := drainFull(p)
	for _, ev := range events {
		if ev.RemoteSenderId == "c555_topic_9" {
			t.Fatalf("un tema eliminado no se publica: %v", eventTypes(events))
		}
	}
	recs := reconcilesOf(events)
	if len(recs) != 1 || recs[0].Body != "c555_topic_1\nc555_topic_5" {
		t.Fatalf("reconcile inesperado: %+v", recs)
	}
	if p.lookupPeer("c555_topic_9") != nil {
		t.Fatal("el tema eliminado no debe resolverse (ni sintetizarse) en lookupPeer")
	}
	if got := p.lookupPeer("c555_topic_42"); got == nil || got.topicID != 42 {
		t.Fatalf("un tema desconocido pero no eliminado debe seguir resolviéndose, got %+v", got)
	}

	// El tema reaparece vivo: se limpia la marca.
	api.topics[555] = &tg.MessagesForumTopics{Count: 3, Topics: []tg.ForumTopicClass{liveTopic(1), liveTopic(5), liveTopic(9)}}
	if _, err := p.syncForumTopics(context.Background(), api, testForum(555)); err != nil {
		t.Fatal(err)
	}
	if p.lookupPeer("c555_topic_9") == nil {
		t.Fatal("un tema que vuelve a estar vivo debe resolverse de nuevo")
	}
}

func TestSyncForumTopicsForgetsCachedTopicsAbsentFromCompleteList(t *testing.T) {
	p := NewTelegramPlugin()
	parent := &tg.InputPeerChannel{ChannelID: 555, AccessHash: 1}
	p.rememberPeer(&peerInfo{remoteID: "c555", kind: kindChannel, id: 555, channelType: "forum", peer: parent})
	for _, id := range []int{1, 5, 8} {
		p.rememberPeer(&peerInfo{remoteID: topicRemoteID("c555", id), kind: kindChannel, id: int64(id), channelType: "forum", topicID: id, peer: parent})
	}

	// Lista incompleta: no se olvida nada.
	api := &fakeDialogsAPI{topics: map[int64]*tg.MessagesForumTopics{555: {Count: 9, Topics: []tg.ForumTopicClass{liveTopic(5)}}}}
	if _, err := p.syncForumTopics(context.Background(), api, testForum(555)); err != nil {
		t.Fatal(err)
	}
	if !p.hasRegisteredPeer("c555_topic_8") {
		t.Fatal("con lista incompleta no se debe olvidar ningún tema")
	}

	// Lista completa sin General ni el tema 8.
	api.topics[555] = &tg.MessagesForumTopics{Count: 1, Topics: []tg.ForumTopicClass{liveTopic(5)}}
	if _, err := p.syncForumTopics(context.Background(), api, testForum(555)); err != nil {
		t.Fatal(err)
	}
	if p.hasRegisteredPeer("c555_topic_8") || p.lookupPeer("c555_topic_8") != nil {
		t.Fatal("el tema 8 ausente de una lista completa debe olvidarse")
	}
	if !p.hasRegisteredPeer("c555_topic_5") {
		t.Fatal("el tema 5 sigue vivo")
	}
	if !p.hasRegisteredPeer("c555_topic_1") || p.lookupPeer("c555_topic_1") == nil {
		t.Fatal("General nunca se olvida")
	}
}

// --- resync continuo ---

func setTopicSyncTimings(t *testing.T, debounce, interval time.Duration) {
	t.Helper()
	oldD, oldI := topicResyncDebounce, topicResyncInterval
	topicResyncDebounce, topicResyncInterval = debounce, interval
	t.Cleanup(func() { topicResyncDebounce, topicResyncInterval = oldD, oldI })
}

func rememberForum(p *TelegramPlugin, id int64, forum bool) {
	chType := "megagroup"
	if forum {
		chType = "forum"
	}
	p.rememberPeer(&peerInfo{remoteID: channelRemoteID(id), kind: kindChannel, id: id, title: "Foro", channelType: chType,
		peer: &tg.InputPeerChannel{ChannelID: id, AccessHash: 1}})
}

func okTopics(id int64) map[int64]*tg.MessagesForumTopics {
	return map[int64]*tg.MessagesForumTopics{id: {Count: 1, Topics: []tg.ForumTopicClass{liveTopic(1)}}}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timeout esperando: %s", what)
}

func TestForumResyncTriggersAreCoalescedPerBurst(t *testing.T) {
	setTopicSyncTimings(t, 30*time.Millisecond, 0)
	p := NewTelegramPlugin()
	rememberForum(p, 555, true)
	api := &fakeDialogsAPI{topics: okTopics(555)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p.startTopicSync(ctx, api)

	for i := 0; i < 5; i++ {
		p.handleChannelUpdate(555)
	}
	p.handlePinnedForumTopic(&tg.PeerChannel{ChannelID: 555})
	p.handlePinnedForumTopics(&tg.PeerChannel{ChannelID: 555})
	p.handleMessage(nil, &tg.MessageService{PeerID: &tg.PeerChannel{ChannelID: 555}, Action: &tg.MessageActionTopicCreate{Title: "x"}})
	p.handleMessage(nil, &tg.MessageService{PeerID: &tg.PeerChannel{ChannelID: 555}, Action: &tg.MessageActionTopicEdit{}})

	waitFor(t, "primer resync", func() bool { return len(api.topicRequests()) >= 1 })
	time.Sleep(150 * time.Millisecond)
	if n := len(api.topicRequests()); n != 1 {
		t.Fatalf("una ráfaga debe producir exactamente 1 resync, hubo %d", n)
	}
	cancel()
	p.topicSync.wait()
}

func TestForumResyncIgnoresNonForumAndUnrelatedMessages(t *testing.T) {
	setTopicSyncTimings(t, 10*time.Millisecond, 0)
	p := NewTelegramPlugin()
	rememberForum(p, 777, false)
	api := &fakeDialogsAPI{topics: okTopics(777)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p.startTopicSync(ctx, api)

	p.handleChannelUpdate(777)
	p.handleChannelUpdate(999) // desconocido
	p.handlePinnedForumTopic(&tg.PeerChannel{ChannelID: 777})
	p.handleMessage(nil, &tg.MessageService{PeerID: &tg.PeerChannel{ChannelID: 777}, Action: &tg.MessageActionTopicCreate{}})
	// Otra acción de servicio en un foro no dispara nada.
	rememberForum(p, 555, true)
	p.handleMessage(nil, &tg.MessageService{PeerID: &tg.PeerChannel{ChannelID: 555}, Action: &tg.MessageActionChatEditTitle{Title: "z"}})

	time.Sleep(120 * time.Millisecond)
	if n := len(api.topicRequests()); n != 0 {
		t.Fatalf("no debe haber resync, hubo %d peticiones", n)
	}
	cancel()
	p.topicSync.wait()
}

func TestForumResyncNeverRunsConcurrentlyForSameForum(t *testing.T) {
	setTopicSyncTimings(t, 5*time.Millisecond, 0)
	p := NewTelegramPlugin()
	rememberForum(p, 555, true)
	api := &fakeDialogsAPI{topics: okTopics(555), block: make(chan struct{}), started: make(chan struct{}, 8)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p.startTopicSync(ctx, api)

	p.handleChannelUpdate(555)
	<-api.started // el primer resync está en vuelo, retenido
	p.handleChannelUpdate(555)
	time.Sleep(60 * time.Millisecond) // ya vencería el debounce
	if n := len(api.topicRequests()); n != 1 {
		t.Fatalf("no debe empezar otro resync mientras corre el anterior, peticiones=%d", n)
	}
	close(api.block)

	// El trigger recibido durante el vuelo se atiende después (uno solo).
	waitFor(t, "resync pendiente", func() bool { return len(api.topicRequests()) >= 2 })
	time.Sleep(60 * time.Millisecond)
	api.mu.Lock()
	maxIn := api.maxInflight
	api.mu.Unlock()
	if maxIn != 1 || len(api.topicRequests()) != 2 {
		t.Fatalf("maxInflight=%d peticiones=%d, se esperaba 1 y 2", maxIn, len(api.topicRequests()))
	}
	cancel()
	p.topicSync.wait()
}

func TestForumResyncPeriodicTickerStopsOnCancel(t *testing.T) {
	setTopicSyncTimings(t, 2*time.Millisecond, 25*time.Millisecond)
	p := NewTelegramPlugin()
	rememberForum(p, 555, true)
	rememberForum(p, 777, false)
	api := &fakeDialogsAPI{topics: okTopics(555)}
	ctx, cancel := context.WithCancel(context.Background())
	p.startTopicSync(ctx, api)

	waitFor(t, "al menos 2 resyncs periódicos", func() bool { return len(api.topicRequests()) >= 2 })
	for _, r := range api.topicRequests() {
		if r.Peer.(*tg.InputPeerChannel).ChannelID != 555 {
			t.Fatalf("el resync periódico solo toca foros conocidos: %+v", r.Peer)
		}
	}

	cancel()
	done := make(chan struct{})
	go func() { p.topicSync.wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("las goroutines del resync no terminaron al cancelar el contexto")
	}
	n := len(api.topicRequests())
	time.Sleep(100 * time.Millisecond)
	if after := len(api.topicRequests()); after != n {
		t.Fatalf("el resync siguió tras cancelar: %d -> %d", n, after)
	}
	// Un trigger posterior a la cancelación no hace nada.
	p.handleChannelUpdate(555)
	time.Sleep(30 * time.Millisecond)
	if after := len(api.topicRequests()); after != n {
		t.Fatalf("un trigger tras cancelar no debe sincronizar")
	}
}

// El wrapper de updates debe seguir entregando todo al dispatcher: si traga un
// update, se pierden mensajes y reacciones.
func TestTopicUpdateHandlerDelegatesEveryUpdate(t *testing.T) {
	dispatcher := tg.NewUpdateDispatcher()
	var handled int
	dispatcher.OnChannel(func(ctx context.Context, e tg.Entities, u *tg.UpdateChannel) error {
		handled++
		return nil
	})
	handler := topicUpdateHandler{UpdateDispatcher: dispatcher, plugin: NewTelegramPlugin()}

	inner := &tg.UpdateChannel{ChannelID: 999}
	for _, u := range []tg.UpdatesClass{
		&tg.Updates{Updates: []tg.UpdateClass{inner}},
		&tg.UpdateShort{Update: inner},
		&tg.UpdatesCombined{Updates: []tg.UpdateClass{inner}},
	} {
		if err := handler.Handle(context.Background(), u); err != nil {
			t.Fatalf("Handle(%s) devolvió error: %v", typeName(u), err)
		}
	}
	if handled != 3 {
		t.Fatalf("el dispatcher debe recibir los 3 updates, recibió %d", handled)
	}
}

func TestInnerUpdatesFlattensContainersByTypeName(t *testing.T) {
	inner := &tg.UpdateChannel{ChannelID: 1}
	cases := []struct {
		name string
		u    tg.UpdatesClass
		want int
	}{
		{"updateShort", &tg.UpdateShort{Update: inner}, 1},
		{"updates", &tg.Updates{Updates: []tg.UpdateClass{inner, inner}}, 2},
		{"updatesCombined", &tg.UpdatesCombined{Updates: []tg.UpdateClass{inner}}, 1},
		{"sinUpdatesInternos", &tg.UpdateShortMessage{}, 0},
		{"nilShort", &tg.UpdateShort{}, 0},
	}
	for _, tc := range cases {
		if got := len(innerUpdates(tc.u)); got != tc.want {
			t.Errorf("%s: esperaba %d updates internos, obtuve %d", tc.name, tc.want, got)
		}
	}
	if got := typeName(inner); got != "UpdateChannel" {
		t.Errorf("typeName(*tg.UpdateChannel) = %q, esperaba UpdateChannel", got)
	}
	if _, ok := pinnedTopicPeer(inner); ok {
		t.Error("un update sin campo Peer no debe devolver peer")
	}
}

// Un Count que no cuenta los temas eliminados puede "cubrirse" con la primera
// página, pero la lista no está completa hasta llegar al final: una continuación
// con solo duplicados no debe reconciliar.
func TestSyncForumTopicsDuplicateOnlyPageDoesNotReconcile(t *testing.T) {
	p := NewTelegramPlugin()
	var page1, page2 []tg.ForumTopicClass
	ids := make([]int, 0, 100)
	for i := 1; i <= 100; i++ {
		page1 = append(page1, liveTopic(i))
		page2 = append(page2, liveTopic(i))
		ids = append(ids, i)
	}
	api := &fakeDialogsAPI{topicPages: map[int64][]*tg.MessagesForumTopics{555: {
		{Count: 150, Topics: page1, Messages: topicMessages(ids...)},
		{Count: 150, Topics: page2},
	}}}
	if _, err := p.syncForumTopics(context.Background(), api, testForum(555)); err != nil {
		t.Fatalf("syncForumTopics devolvió error: %v", err)
	}
	if recs := reconcilesOf(drainFull(p)); len(recs) != 0 {
		t.Fatalf("una continuación con solo duplicados no debe reconciliar, hubo %d", len(recs))
	}
	if n := len(api.topicRequests()); n != 2 {
		t.Fatalf("esperaba 2 peticiones, hubo %d", n)
	}
}

// Tocar el tope de páginas sin llegar al final nunca reconcilia.
func TestSyncForumTopicsPageCapDoesNotReconcile(t *testing.T) {
	p := NewTelegramPlugin()
	p.eventCh = make(chan *conduitv1.PluginEvent, 20000)
	pages := make([]*tg.MessagesForumTopics, 0, maxForumTopicPages+10)
	for pg := 0; pg < maxForumTopicPages+10; pg++ {
		var topics []tg.ForumTopicClass
		var msgs []tg.MessageClass
		for i := 1; i <= forumTopicsPageLimit; i++ {
			id := pg*forumTopicsPageLimit + i
			topics = append(topics, liveTopic(id))
			msgs = append(msgs, &tg.Message{ID: 1000 + id, Date: 1_800_000_000 + id, PeerID: &tg.PeerChannel{ChannelID: 555}})
		}
		pages = append(pages, &tg.MessagesForumTopics{Count: 10000, Topics: topics, Messages: msgs})
	}
	api := &fakeDialogsAPI{topicPages: map[int64][]*tg.MessagesForumTopics{555: pages}}
	if _, err := p.syncForumTopics(context.Background(), api, testForum(555)); err != nil {
		t.Fatal(err)
	}
	if recs := reconcilesOf(drainFull(p)); len(recs) != 0 {
		t.Fatalf("el tope de páginas no debe reconciliar")
	}
	if n := len(api.topicRequests()); n != maxForumTopicPages {
		t.Fatalf("esperaba exactamente %d peticiones, hubo %d", maxForumTopicPages, n)
	}
}

// La forma del reconcile que consume el core: una línea por tema vivo, General
// siempre, ids <server>_topic_<dígitos> y SentAtUnix positivo.
func TestSyncForumTopicsReconcileShape(t *testing.T) {
	p := NewTelegramPlugin()
	api := &fakeDialogsAPI{topics: map[int64]*tg.MessagesForumTopics{555: {
		Count:  2,
		Topics: []tg.ForumTopicClass{liveTopic(5), liveTopic(7)},
	}}}
	before := nowUnix()
	if _, err := p.syncForumTopics(context.Background(), api, testForum(555)); err != nil {
		t.Fatal(err)
	}
	recs := reconcilesOf(drainFull(p))
	if len(recs) != 1 {
		t.Fatalf("esperaba 1 reconcile, hubo %d", len(recs))
	}
	rec := recs[0]
	if rec.SentAtUnix <= 0 || rec.SentAtUnix < before {
		t.Fatalf("SentAtUnix=%d, before=%d", rec.SentAtUnix, before)
	}
	want := "c555_topic_1\nc555_topic_5\nc555_topic_7"
	if rec.Body != want {
		t.Fatalf("Body=%q, esperaba %q", rec.Body, want)
	}
	if rec.ServerId != "c555" {
		t.Fatalf("ServerId=%q, esperaba c555", rec.ServerId)
	}
}

// Un resync reemplazado no debe seguir barriendo foros.
func TestStartTopicSyncReplacesPreviousResyncer(t *testing.T) {
	setTopicSyncTimings(t, time.Millisecond, 0)
	p := NewTelegramPlugin()
	rememberForum(p, 555, true)
	api := &fakeDialogsAPI{topics: okTopics(555)}
	ctx1, cancel1 := context.WithCancel(context.Background())
	p.startTopicSync(ctx1, api)
	s1 := p.topicSync
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	p.startTopicSync(ctx2, api)
	if p.topicSync == s1 || p.topicSync == nil {
		t.Fatal("el segundo startTopicSync debe instalar un resyncer nuevo")
	}
	before := len(api.topicRequests())
	s1.schedule("c555")
	time.Sleep(50 * time.Millisecond)
	if after := len(api.topicRequests()); after != before {
		t.Fatalf("un resyncer reemplazado no debe sincronizar: %d -> %d", before, after)
	}
	cancel1()
	s1.wait()
	cancel2()
	p.topicSync.wait()
}
