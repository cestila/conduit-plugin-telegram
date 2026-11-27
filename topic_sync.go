package main

import (
	"context"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gotd/td/tg"

	conduitv1 "github.com/cestila/ConduitOmniChat/sdk/conduitv1"
)

var (
	// topicResyncDebounce agrupa ráfagas de updates de un foro en un solo resync.
	topicResyncDebounce = 3 * time.Second
	// topicResyncInterval es el resync de seguridad de todos los foros conocidos:
	// borrar un tema no siempre produce un update, así que esto es lo que lo detecta.
	topicResyncInterval = 5 * time.Minute
)

const (
	// forumTopicsPageLimit es el tamaño de página de messages.getForumTopics.
	forumTopicsPageLimit = 100
	// maxForumTopicPages acota la paginación de temas para no iterar sin fin.
	maxForumTopicPages = 50
	// generalTopicID es el tema General: todo foro lo tiene y no se puede eliminar.
	generalTopicID = 1
)

// forumTopicList es el resultado de paginar los temas de un foro.
type forumTopicList struct {
	topics   []tg.ForumTopicClass // únicos, en el orden en que llegaron (incluye eliminados)
	expected int                  // mayor Count informado por Telegram
	deleted  int                  // stubs ForumTopicDeleted vistos en las páginas
	ended    bool                 // la paginación llegó al final de la lista
	complete bool                 // cubre Count con seguridad y llegó al final
}

// fetchForumTopicPass pagina messages.getForumTopics una sola vez hasta el final.
// Siempre devuelve lo recolectado, aun con error, y cuenta los stubs eliminados.
func fetchForumTopicPass(ctx context.Context, api dialogsAPI, peer tg.InputPeerClass) (forumTopicList, error) {
	var out forumTopicList
	seen := map[int]bool{}
	req := &tg.MessagesGetForumTopicsRequest{Peer: peer, Limit: forumTopicsPageLimit}

	for page := 0; page < maxForumTopicPages; page++ {
		res, err := api.MessagesGetForumTopics(ctx, req)
		if err != nil {
			return out, err
		}
		if res == nil || len(res.Topics) == 0 {
			out.ended = true // página vacía: se llegó al final
			break
		}
		if res.Count > out.expected {
			out.expected = res.Count
		}
		fresh := 0
		for _, tc := range res.Topics {
			id := forumTopicID(tc)
			if id <= 0 || seen[id] {
				continue
			}
			seen[id] = true
			out.topics = append(out.topics, tc)
			if _, ok := tc.(*tg.ForumTopicDeleted); ok {
				out.deleted++
			}
			fresh++
		}
		if len(res.Topics) < forumTopicsPageLimit {
			out.ended = true // página corta: se llegó al final
			break
		}
		if fresh == 0 {
			break // sin progreso: evita iterar sobre la misma página
		}
		next := nextForumTopicsRequest(peer, res)
		if next == nil {
			break
		}
		req = next
	}
	return out, nil
}

// fetchForumTopics decide de forma conservadora si la lista cubre el foro. Una
// lista dudosa nunca es complete: el core borra de forma irreversible lo ausente.
// Count puede no incluir los temas eliminados que llegan como ForumTopicDeleted y
// el orden puede variar entre pasadas, así que con stubs eliminados se exige una
// segunda pasada con exactamente el mismo conjunto de temas vivos.
func fetchForumTopics(ctx context.Context, api dialogsAPI, peer tg.InputPeerClass) (forumTopicList, error) {
	first, err := fetchForumTopicPass(ctx, api, peer)
	if err != nil || !first.ended {
		return first, err
	}
	if len(first.topics) == 0 {
		first.complete = true // un foro sin temas en la API solo tiene General
		return first, nil
	}
	if len(first.topics) < first.expected {
		return first, nil
	}
	if first.deleted == 0 {
		first.complete = true
		return first, nil
	}
	second, err := fetchForumTopicPass(ctx, api, peer)
	if err != nil {
		return first, err
	}
	first.complete = second.ended && sameLiveTopics(first.topics, second.topics)
	return first, nil
}

// sameLiveTopics compara los conjuntos de ids de temas vivos de dos pasadas.
func sameLiveTopics(a, b []tg.ForumTopicClass) bool {
	aIDs, bIDs := map[int]bool{}, map[int]bool{}
	for _, tc := range a {
		if t, ok := tc.(*tg.ForumTopic); ok && t != nil && t.ID > 0 {
			aIDs[t.ID] = true
		}
	}
	for _, tc := range b {
		if t, ok := tc.(*tg.ForumTopic); ok && t != nil && t.ID > 0 {
			bIDs[t.ID] = true
		}
	}
	if len(aIDs) != len(bIDs) {
		return false
	}
	for id := range aIDs {
		if !bIDs[id] {
			return false
		}
	}
	return true
}

func forumTopicID(tc tg.ForumTopicClass) int {
	switch t := tc.(type) {
	case *tg.ForumTopic:
		if t != nil {
			return t.ID
		}
	case *tg.ForumTopicDeleted:
		if t != nil {
			return t.ID
		}
	}
	return 0
}

// nextForumTopicsRequest arma la página siguiente desde el último tema vivo de la
// respuesta (offset_topic / offset_id / offset_date del último mensaje del tema).
// Devuelve nil si no puede resolverse.
func nextForumTopicsRequest(peer tg.InputPeerClass, res *tg.MessagesForumTopics) *tg.MessagesGetForumTopicsRequest {
	for i := len(res.Topics) - 1; i >= 0; i-- {
		topic, ok := res.Topics[i].(*tg.ForumTopic)
		if !ok || topic == nil {
			continue
		}
		date := 0
		for _, mc := range res.Messages {
			switch m := mc.(type) {
			case *tg.Message:
				if m != nil && m.ID == topic.TopMessage {
					date = m.Date
				}
			case *tg.MessageService:
				if m != nil && m.ID == topic.TopMessage {
					date = m.Date
				}
			}
		}
		if date == 0 {
			return nil
		}
		return &tg.MessagesGetForumTopicsRequest{
			Peer:        peer,
			OffsetDate:  date,
			OffsetID:    topic.TopMessage,
			OffsetTopic: topic.ID,
			Limit:       forumTopicsPageLimit,
		}
	}
	return nil
}

// syncForumTopics consulta todos los temas de un supergrupo con foro, los publica
// como channel.sync y, solo si la lista es completa, emite un channel.reconcile con
// los temas vivos. Con error de API o lista incompleta se publica lo obtenido y no
// se reconcilia: el core borra de forma irreversible lo que falte en la lista.
func (p *TelegramPlugin) syncForumTopics(ctx context.Context, api dialogsAPI, chat *tg.Channel) (int, error) {
	if api == nil || chat == nil || !chat.Forum {
		return 0, nil
	}
	serverRemoteID := channelRemoteID(chat.ID)
	inputPeer := &tg.InputPeerChannel{ChannelID: chat.ID, AccessHash: chat.AccessHash}

	// Hora del snapshot: antes de la primera petición, para que el core proteja
	// los temas con actividad posterior (creados mientras se paginaba).
	fetchedAt := nowUnix()
	list, err := fetchForumTopics(ctx, api, inputPeer)
	if err != nil {
		log.Printf("⚠️ no se pudieron obtener los temas del foro %s: %v", chat.Title, err)
		if len(list.topics) == 0 {
			return 0, err
		}
	}
	count := p.applyForumTopics(serverRemoteID, chat.Title, inputPeer, list.topics)
	if err != nil {
		return count, err
	}
	if !list.complete {
		if list.ended || len(list.topics) == 0 {
			log.Printf("⚠️ lista de temas de %s incompleta (%d de %d): sin reconcile", chat.Title, len(list.topics), list.expected)
		}
		return count, nil
	}

	live := liveTopicIDs(list.topics)
	p.forgetTopicsAbsentFrom(serverRemoteID, live)
	p.emit(&conduitv1.PluginEvent{
		EventType:  "channel.reconcile",
		ServerId:   serverRemoteID,
		ServerName: chat.Title,
		Body:       reconcileBody(serverRemoteID, live),
		SentAtUnix: fetchedAt,
	})
	return count, nil
}

// liveTopicIDs devuelve los ids de los temas vivos; General siempre está.
func liveTopicIDs(topics []tg.ForumTopicClass) map[int]bool {
	live := map[int]bool{generalTopicID: true}
	for _, tc := range topics {
		if t, ok := tc.(*tg.ForumTopic); ok && t != nil && t.ID > 0 {
			live[t.ID] = true
		}
	}
	return live
}

func reconcileBody(serverRemoteID string, live map[int]bool) string {
	ids := make([]int, 0, len(live))
	for id := range live {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	lines := make([]string, len(ids))
	for i, id := range ids {
		lines[i] = topicRemoteID(serverRemoteID, id)
	}
	return strings.Join(lines, "\n")
}

// markTopicDeleted registra un tema eliminado: se olvida su peer y lookupPeer
// deja de sintetizarlo. General nunca se marca.
func (p *TelegramPlugin) markTopicDeleted(remoteID string) {
	if _, id, ok := parseTopicRemoteID(remoteID); !ok || id == generalTopicID {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.markTopicDeletedLocked(remoteID)
}

func (p *TelegramPlugin) markTopicDeletedLocked(remoteID string) {
	if p.deletedTopics == nil {
		p.deletedTopics = make(map[string]struct{})
	}
	p.deletedTopics[remoteID] = struct{}{}
	if info, ok := p.peers[remoteID]; ok {
		delete(p.peers, remoteID)
		if digits := digitsOf(remoteID); digits != "" && p.peersByID[digits] == info {
			delete(p.peersByID, digits)
		}
	}
}

// clearTopicDeleted quita la marca de eliminado (el tema volvió a estar vivo).
func (p *TelegramPlugin) clearTopicDeleted(remoteID string) {
	p.mu.Lock()
	delete(p.deletedTopics, remoteID)
	p.mu.Unlock()
}

// forgetTopicsAbsentFrom olvida los temas registrados de un foro que no están en
// una lista COMPLETA de temas vivos. General nunca se olvida.
func (p *TelegramPlugin) forgetTopicsAbsentFrom(serverRemoteID string, live map[int]bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for remoteID := range p.peers {
		chanID, topicID, ok := parseTopicRemoteID(remoteID)
		if !ok || chanID != serverRemoteID || topicID == generalTopicID || live[topicID] {
			continue
		}
		p.markTopicDeletedLocked(remoteID)
	}
}

// --- resync continuo ---

// forumResyncer vuelve a sincronizar los temas de un foro ante updates de
// Telegram y periódicamente. Agrupa ráfagas por foro (debounce), nunca corre dos
// resyncs del mismo foro a la vez y termina con el contexto del cliente.
type forumResyncer struct {
	p        *TelegramPlugin
	ctx      context.Context
	api      dialogsAPI
	debounce time.Duration

	mu      sync.Mutex
	pending map[string]*time.Timer // resync agendado, esperando el debounce
	running map[string]bool        // resync en vuelo
	rerun   map[string]bool        // llegó un trigger durante el vuelo
	stopped bool                   // el resync fue reemplazado o detenido
	wg      sync.WaitGroup
}

// startTopicSync activa el resync continuo mientras viva ctx.
func (p *TelegramPlugin) startTopicSync(ctx context.Context, api dialogsAPI) {
	s := &forumResyncer{
		p:        p,
		ctx:      ctx,
		api:      api,
		debounce: topicResyncDebounce,
		pending:  make(map[string]*time.Timer),
		running:  make(map[string]bool),
		rerun:    make(map[string]bool),
	}
	p.mu.Lock()
	previous := p.topicSync
	p.topicSync = s
	p.mu.Unlock()
	// Un resync anterior no debe seguir barriendo el mismo foro en paralelo.
	if previous != nil {
		previous.stop()
	}

	interval := topicResyncInterval
	s.wg.Add(1)
	go s.loop(interval)
}

// stop detiene el resync: no agenda más y cancela los timers pendientes.
func (s *forumResyncer) stop() {
	s.mu.Lock()
	s.stopped = true
	s.mu.Unlock()
	s.stopTimers()
}

// loop dispara el resync periódico y limpia los timers al cancelarse el contexto.
func (s *forumResyncer) loop(interval time.Duration) {
	defer s.wg.Done()
	var tick <-chan time.Time
	if interval > 0 {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		tick = ticker.C
	}
	for {
		select {
		case <-s.ctx.Done():
			s.stopTimers()
			return
		case <-tick:
			for _, id := range s.p.knownForums() {
				s.schedule(id)
			}
		}
	}
}

func (s *forumResyncer) stopTimers() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, t := range s.pending {
		if t.Stop() {
			s.wg.Done()
		}
		delete(s.pending, id)
	}
}

// wait bloquea hasta que terminan todas las goroutines del resync (para tests).
func (s *forumResyncer) wait() { s.wg.Wait() }

// schedule agenda un resync del foro; los triggers repetidos se unen al ya agendado.
func (s *forumResyncer) schedule(remoteID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped || s.ctx.Err() != nil {
		return
	}
	if s.pending[remoteID] != nil {
		return
	}
	if s.running[remoteID] {
		s.rerun[remoteID] = true
		return
	}
	s.startTimerLocked(remoteID)
}

func (s *forumResyncer) startTimerLocked(remoteID string) {
	s.wg.Add(1)
	s.pending[remoteID] = time.AfterFunc(s.debounce, func() { s.fire(remoteID) })
}

func (s *forumResyncer) fire(remoteID string) {
	defer s.wg.Done()
	s.mu.Lock()
	delete(s.pending, remoteID)
	if s.stopped || s.ctx.Err() != nil {
		s.mu.Unlock()
		return
	}
	s.running[remoteID] = true
	s.mu.Unlock()

	s.p.resyncForum(s.ctx, s.api, remoteID)

	s.mu.Lock()
	delete(s.running, remoteID)
	if s.rerun[remoteID] {
		delete(s.rerun, remoteID)
		if s.ctx.Err() == nil {
			s.startTimerLocked(remoteID)
		}
	}
	s.mu.Unlock()
}

// knownForums lista los foros (no sus temas) registrados.
func (p *TelegramPlugin) knownForums() []string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	var ids []string
	for remoteID, info := range p.peers {
		if info != nil && info.kind == kindChannel && info.channelType == "forum" && info.topicID == 0 {
			ids = append(ids, remoteID)
		}
	}
	sort.Strings(ids)
	return ids
}

// resyncForum vuelve a leer los temas de un foro conocido.
func (p *TelegramPlugin) resyncForum(ctx context.Context, api dialogsAPI, remoteID string) {
	p.mu.RLock()
	info := p.peers[remoteID]
	p.mu.RUnlock()
	if info == nil || info.kind != kindChannel || info.channelType != "forum" || info.topicID != 0 {
		return
	}
	input, ok := info.peer.(*tg.InputPeerChannel)
	if !ok {
		return
	}
	chat := &tg.Channel{ID: input.ChannelID, AccessHash: input.AccessHash, Title: info.title, Forum: true, Megagroup: true}
	if _, err := p.syncForumTopics(ctx, api, chat); err != nil {
		log.Printf("⚠️ resync de temas de %s falló: %v", remoteID, err)
	}
}

// scheduleForumResync pide un resync (con debounce) si el canal es un foro conocido.
func (p *TelegramPlugin) scheduleForumResync(remoteID string) {
	p.mu.RLock()
	s := p.topicSync
	info := p.peers[remoteID]
	p.mu.RUnlock()
	if s == nil || info == nil || info.kind != kindChannel || info.channelType != "forum" || info.topicID != 0 {
		return
	}
	s.schedule(remoteID)
}

// handleChannelUpdate reacciona a updateChannel (cambios del canal, p. ej. temas).
func (p *TelegramPlugin) handleChannelUpdate(channelID int64) {
	p.scheduleForumResync(channelRemoteID(channelID))
}

// handlePinnedForumTopic reacciona a cambios de temas fijados de un foro.
func (p *TelegramPlugin) handlePinnedForumTopic(peer tg.PeerClass) {
	p.schedulePeerResync(peer)
}

// handlePinnedForumTopics reacciona al reordenamiento de temas fijados.
func (p *TelegramPlugin) handlePinnedForumTopics(peer tg.PeerClass) {
	p.schedulePeerResync(peer)
}

func (p *TelegramPlugin) schedulePeerResync(peer tg.PeerClass) {
	if ch, ok := peer.(*tg.PeerChannel); ok && ch != nil {
		p.scheduleForumResync(channelRemoteID(ch.ChannelID))
	}
}

// handleServiceMessage reacciona a los mensajes de servicio de creación/edición de temas.
func (p *TelegramPlugin) handleServiceMessage(msg *tg.MessageService) {
	if msg == nil {
		return
	}
	switch msg.Action.(type) {
	case *tg.MessageActionTopicCreate, *tg.MessageActionTopicEdit:
		p.schedulePeerResync(msg.PeerID)
	}
}
