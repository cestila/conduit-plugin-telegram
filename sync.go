package main

import (
	"context"
	"fmt"
	"log"
	"math/rand"
	"strconv"
	"strings"
	"time"

	"github.com/gotd/td/telegram"
	"github.com/gotd/td/tg"

	conduitv1 "github.com/cestila/ConduitOmniChat/sdk/conduitv1"
)

// peerKind distingue usuarios, grupos básicos y canales/supergrupos.
type peerKind string

const (
	kindUser    peerKind = "user"
	kindGroup   peerKind = "group"
	kindChannel peerKind = "channel"
)

// peerInfo guarda lo necesario para volver a hablarle a un chat de Telegram.
type peerInfo struct {
	remoteID    string
	kind        peerKind
	id          int64
	title       string
	username    string
	phone       string
	channelType string
	peer        tg.InputPeerClass
	topicID     int
}

// rememberPeer registra un chat para poder enviarle mensajes después.
func (p *TelegramPlugin) rememberPeer(info *peerInfo) {
	if info == nil || info.remoteID == "" || info.peer == nil {
		return
	}
	p.mu.Lock()
	p.peers[info.remoteID] = info
	if digits := digitsOf(info.remoteID); digits != "" {
		p.peersByID[digits] = info
	}
	p.mu.Unlock()
}

// lookupPeer devuelve el chat registrado con ese identificador remoto.
//
// El core normaliza los identificadores como si fueran teléfonos, así que un
// chat "u123" puede volver como "+123" y perderse. Se busca por el
// identificador exacto y, si no aparece, por sus dígitos.
func (p *TelegramPlugin) lookupPeer(remoteID string) *peerInfo {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if info, ok := p.peers[remoteID]; ok {
		return info
	}
	if chanRemoteID, topicID, isTopic := parseTopicRemoteID(remoteID); isTopic {
		var parent *peerInfo
		if pChan, ok := p.peers[chanRemoteID]; ok {
			parent = pChan
		} else if digits := digitsOf(chanRemoteID); digits != "" {
			parent = p.peersByID[digits]
		}
		if parent != nil {
			return &peerInfo{
				remoteID:    remoteID,
				kind:        parent.kind,
				id:          parent.id,
				title:       parent.title,
				username:    parent.username,
				phone:       parent.phone,
				channelType: "forum",
				peer:        parent.peer,
				topicID:     topicID,
			}
		}
	}
	return p.peersByID[digitsOf(remoteID)]
}

// topicRemoteID construye el identificador remoto de un tema dentro de un canal.
func topicRemoteID(channelRemoteID string, topicID int) string {
	return fmt.Sprintf("%s_topic_%d", channelRemoteID, topicID)
}

// parseTopicRemoteID extrae el identificador del canal padre y el ID del tema.
func parseTopicRemoteID(remoteID string) (string, int, bool) {
	idx := strings.Index(remoteID, "_topic_")
	if idx == -1 {
		return remoteID, 0, false
	}
	tID, err := strconv.Atoi(remoteID[idx+len("_topic_"):])
	if err != nil || tID <= 0 {
		return remoteID, 0, false
	}
	return remoteID[:idx], tID, true
}

// hasRegisteredPeer informa si un identificador fue registrado explícitamente en memoria.
func (p *TelegramPlugin) hasRegisteredPeer(remoteID string) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	_, ok := p.peers[remoteID]
	return ok
}

// digitsOf deja solo los dígitos de un identificador remoto.
func digitsOf(value string) string {
	var b strings.Builder
	for _, r := range value {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// remoteIDForPeer traduce un peer de Telegram al identificador remoto estable.
func remoteIDForPeer(peer tg.PeerClass) (string, peerKind, int64) {
	switch v := peer.(type) {
	case *tg.PeerUser:
		return userRemoteID(v.UserID), kindUser, v.UserID
	case *tg.PeerChat:
		return groupRemoteID(v.ChatID), kindGroup, v.ChatID
	case *tg.PeerChannel:
		return channelRemoteID(v.ChannelID), kindChannel, v.ChannelID
	}
	return "", "", 0
}

func userRemoteID(id int64) string    { return "u" + strconv.FormatInt(id, 10) }
func groupRemoteID(id int64) string   { return "g" + strconv.FormatInt(id, 10) }
func channelRemoteID(id int64) string { return "c" + strconv.FormatInt(id, 10) }

// syncDialogs lista los chats reales de la cuenta y los publica al core.
func (p *TelegramPlugin) syncDialogs(ctx context.Context, client *telegram.Client) {
	p.mu.Lock()
	p.lastDialogSync = time.Now()
	p.mu.Unlock()

	res, err := client.API().MessagesGetDialogs(ctx, &tg.MessagesGetDialogsRequest{
		OffsetPeer: &tg.InputPeerEmpty{},
		Limit:      100,
	})
	if err != nil {
		log.Printf("⚠️ no se pudieron listar los chats de Telegram: %v", err)
		return
	}

	var users []tg.UserClass
	var chats []tg.ChatClass
	var dialogs []tg.DialogClass
	var messages []tg.MessageClass
	switch d := res.(type) {
	case *tg.MessagesDialogs:
		dialogs, users, chats, messages = d.Dialogs, d.Users, d.Chats, d.Messages
	case *tg.MessagesDialogsSlice:
		dialogs, users, chats, messages = d.Dialogs, d.Users, d.Chats, d.Messages
	}

	p.applyUsers(users)
	p.applyChats(ctx, client, chats)
	p.publishUnreadDialogs(dialogs, messages)
}

// publishUnreadDialogs publica el último mensaje de los chats con no leídos.
// Sin esto, lo que llegó mientras el daemon estaba apagado no aparece: el chat
// se sincroniza como contacto, pero su contenido queda vacío hasta abrirlo.
func (p *TelegramPlugin) publishUnreadDialogs(dialogs []tg.DialogClass, messages []tg.MessageClass) {
	if len(dialogs) == 0 || len(messages) == 0 {
		return
	}

	byID := make(map[int]*tg.Message, len(messages))
	for _, mc := range messages {
		if msg, ok := mc.(*tg.Message); ok && msg != nil {
			byID[msg.ID] = msg
		}
	}

	for _, dc := range dialogs {
		dialog, ok := dc.(*tg.Dialog)
		if !ok || dialog == nil || dialog.UnreadCount == 0 {
			continue
		}
		msg, ok := byID[dialog.TopMessage]
		if !ok || msg.Out {
			continue
		}
		p.handleMessage(nil, msg)
	}
}

// applyUsers publica los contactos humanos de la cuenta.
func (p *TelegramPlugin) applyUsers(users []tg.UserClass) {
	for _, uc := range users {
		user, ok := uc.(*tg.User)
		if !ok || user == nil || user.Self || user.Deleted || user.Bot {
			continue
		}

		remoteID := userRemoteID(user.ID)
		displayName := strings.TrimSpace(strings.TrimSpace(user.FirstName) + " " + strings.TrimSpace(user.LastName))
		if displayName == "" {
			displayName = user.Username
		}
		if displayName == "" {
			displayName = remoteID
		}

		p.rememberPeer(&peerInfo{
			remoteID: remoteID,
			kind:     kindUser,
			id:       user.ID,
			title:    displayName,
			username: user.Username,
			phone:    user.Phone,
			peer:     &tg.InputPeerUser{UserID: user.ID, AccessHash: user.AccessHash},
		})

		p.emit(&conduitv1.PluginEvent{
			EventType:       "contact.sync",
			RemoteSenderId:  remoteID,
			SenderName:      displayName,
			Body:            user.Username,
			SentAtUnix:      nowUnix(),
			ContentType:     "text",
			RemoteMessageId: "contact-" + remoteID,
			IdentityPhone:   user.Phone,
		})
	}
}

// applyChats publica grupos y canales respetando la jerarquía del servidor.
func (p *TelegramPlugin) applyChats(ctx context.Context, client *telegram.Client, chats []tg.ChatClass) {
	for _, cc := range chats {
		switch chat := cc.(type) {
		case *tg.Chat:
			remoteID := groupRemoteID(chat.ID)
			p.rememberPeer(&peerInfo{
				remoteID: remoteID,
				kind:     kindGroup,
				id:       chat.ID,
				title:    chat.Title,
				peer:     &tg.InputPeerChat{ChatID: chat.ID},
			})

			p.emit(&conduitv1.PluginEvent{
				EventType:       "contact.sync",
				RemoteSenderId:  remoteID,
				SenderName:      chat.Title,
				Body:            "group",
				SentAtUnix:      nowUnix(),
				ChannelType:     "group",
				ChannelName:     chat.Title,
				RemoteMessageId: "group-" + remoteID,
			})

		case *tg.Channel:
			remoteID := channelRemoteID(chat.ID)
			channelType := channelTypeFor(chat)

			p.rememberPeer(&peerInfo{
				remoteID:    remoteID,
				kind:        kindChannel,
				id:          chat.ID,
				title:       chat.Title,
				username:    chat.Username,
				channelType: channelType,
				peer:        &tg.InputPeerChannel{ChannelID: chat.ID, AccessHash: chat.AccessHash},
			})

			if chat.Forum && client != nil {
				synced := p.syncForumTopics(ctx, client, chat)
				if synced > 0 {
					continue
				}
			}

			p.emit(&conduitv1.PluginEvent{
				EventType:       "channel.sync",
				RemoteSenderId:  remoteID,
				SenderName:      chat.Title,
				ServerId:        remoteID,
				ServerName:      chat.Title,
				ChannelName:     chat.Title,
				ChannelType:     channelType,
				SentAtUnix:      nowUnix(),
				RemoteMessageId: "channel-" + remoteID,
			})
		}
	}
}

// syncForumTopics consulta los temas de un supergrupo con foro en Telegram y los publica.
func (p *TelegramPlugin) syncForumTopics(ctx context.Context, client *telegram.Client, chat *tg.Channel) int {
	if client == nil || chat == nil || !chat.Forum {
		return 0
	}
	serverRemoteID := channelRemoteID(chat.ID)
	inputPeer := &tg.InputPeerChannel{ChannelID: chat.ID, AccessHash: chat.AccessHash}

	res, err := client.API().MessagesGetForumTopics(ctx, &tg.MessagesGetForumTopicsRequest{
		Peer:  inputPeer,
		Limit: 100,
	})
	if err != nil {
		log.Printf("⚠️ no se pudieron obtener los temas del foro %s: %v", chat.Title, err)
		return 0
	}
	if res == nil || len(res.Topics) == 0 {
		return 0
	}
	return p.applyForumTopics(serverRemoteID, chat.Title, inputPeer, res.Topics)
}

// applyForumTopics publica los temas de un foro como canales hijos y recuerda sus peers.
func (p *TelegramPlugin) applyForumTopics(serverRemoteID, serverTitle string, parentPeer tg.InputPeerClass, topics []tg.ForumTopicClass) int {
	hasTopic1 := false
	for _, tc := range topics {
		if t, ok := tc.(*tg.ForumTopic); ok && t != nil && t.ID == 1 {
			hasTopic1 = true
			break
		}
	}
	if !hasTopic1 {
		// En Telegram todo foro tiene siempre el tema 'General' (ID 1) no eliminable.
		// Si MessagesGetForumTopics no lo incluye en la lista de temas devueltos,
		// se añade explícitamente para que siempre esté disponible.
		topics = append([]tg.ForumTopicClass{&tg.ForumTopic{
			ID:    1,
			Title: "General",
			Date:  int(nowUnix()),
		}}, topics...)
	}

	count := 0
	for _, tc := range topics {
		topic, ok := tc.(*tg.ForumTopic)
		if !ok || topic == nil {
			continue
		}

		topicTitle := strings.TrimSpace(topic.Title)
		if topicTitle == "" {
			if topic.ID == 1 {
				topicTitle = "General"
			} else {
				topicTitle = fmt.Sprintf("Tema %d", topic.ID)
			}
		}

		remoteID := topicRemoteID(serverRemoteID, topic.ID)

		p.rememberPeer(&peerInfo{
			remoteID:    remoteID,
			kind:        kindChannel,
			id:          int64(topic.ID),
			title:       topicTitle,
			channelType: "forum",
			peer:        parentPeer,
			topicID:     topic.ID,
		})

		sentAt := int64(topic.Date)
		if sentAt == 0 {
			sentAt = nowUnix()
		}

		p.emit(&conduitv1.PluginEvent{
			EventType:       "channel.sync",
			RemoteSenderId:  remoteID,
			SenderName:      serverTitle,
			ServerId:        serverRemoteID,
			ServerName:      serverTitle,
			ParentChannelId: serverRemoteID,
			ChannelName:     topicTitle,
			ChannelType:     "forum",
			SentAtUnix:      sentAt,
			RemoteMessageId: "topic-" + remoteID,
		})
		count++
	}
	return count
}

// channelTypeFor clasifica un canal de Telegram para la navegación drill-down.
func channelTypeFor(channel *tg.Channel) string {
	switch {
	case channel.Forum:
		return "forum"
	case channel.Broadcast:
		return "announcement"
	default:
		return "megagroup"
	}
}

// rememberEntities guarda los usuarios y canales que acompañan a un update.
func (p *TelegramPlugin) rememberEntities(e *tg.Entities) {
	if e == nil {
		return
	}
	for _, user := range e.Users {
		if user == nil || user.Deleted {
			continue
		}
		displayName := strings.TrimSpace(strings.TrimSpace(user.FirstName) + " " + strings.TrimSpace(user.LastName))
		if displayName == "" {
			displayName = user.Username
		}
		title := displayName
		if title == "" {
			title = userRemoteID(user.ID)
		}
		p.rememberPeer(&peerInfo{
			remoteID: userRemoteID(user.ID),
			kind:     kindUser,
			id:       user.ID,
			title:    title,
			username: user.Username,
			phone:    user.Phone,
			peer:     &tg.InputPeerUser{UserID: user.ID, AccessHash: user.AccessHash},
		})
	}
	for _, channel := range e.Channels {
		if channel == nil {
			continue
		}
		p.rememberPeer(&peerInfo{
			remoteID:    channelRemoteID(channel.ID),
			kind:        kindChannel,
			id:          channel.ID,
			title:       channel.Title,
			username:    channel.Username,
			channelType: channelTypeFor(channel),
			peer:        &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		})
	}
	for _, chat := range e.Chats {
		if chat == nil {
			continue
		}
		p.rememberPeer(&peerInfo{
			remoteID: groupRemoteID(chat.ID),
			kind:     kindGroup,
			id:       chat.ID,
			title:    chat.Title,
			peer:     &tg.InputPeerChat{ChatID: chat.ID},
		})
	}
}

// handleMessage convierte un mensaje MTProto en un evento para el core.
func (p *TelegramPlugin) handleMessage(e *tg.Entities, m tg.MessageClass) {
	msg, ok := m.(*tg.Message)
	if !ok || msg == nil {
		return
	}

	p.rememberEntities(e)

	remoteID, kind, _ := remoteIDForPeer(msg.PeerID)
	if remoteID == "" {
		return
	}
	event := p.messageEvent(msg, remoteID, kind)
	if event == nil {
		return
	}
	p.emit(event)
}

// messageEvent arma el evento de un mensaje con el contrato que ya usa el core.
func (p *TelegramPlugin) messageEvent(msg *tg.Message, remoteID string, kind peerKind) *conduitv1.PluginEvent {
	body := msg.Message
	media, _ := describeIncomingMedia(msg)
	var pollProto *conduitv1.ProtoPoll
	if pollMedia, ok := msg.Media.(*tg.MessageMediaPoll); ok && pollMedia != nil {
		pollProto = parseTelegramPoll(pollMedia)
		if strings.TrimSpace(body) == "" && pollProto != nil {
			body = pollProto.Question
		}
	}

	var locProto *conduitv1.ProtoLocation
	var contactProto *conduitv1.ProtoSharedContact

	if geo, ok := msg.Media.(*tg.MessageMediaGeo); ok && geo != nil {
		if pt, ok := geo.Geo.(*tg.GeoPoint); ok && pt != nil {
			locProto = &conduitv1.ProtoLocation{
				Latitude:  pt.Lat,
				Longitude: pt.Long,
			}
			if strings.TrimSpace(body) == "" {
				body = fmt.Sprintf("Ubicación: %.6f, %.6f", pt.Lat, pt.Long)
			}
		}
	} else if venue, ok := msg.Media.(*tg.MessageMediaVenue); ok && venue != nil {
		var lat, long float64
		if pt, ok := venue.Geo.(*tg.GeoPoint); ok && pt != nil {
			lat = pt.Lat
			long = pt.Long
		}
		locProto = &conduitv1.ProtoLocation{
			Latitude:  lat,
			Longitude: long,
			Title:     venue.Title,
			Address:   venue.Address,
		}
		if strings.TrimSpace(body) == "" {
			body = venue.Title
		}
	} else if contact, ok := msg.Media.(*tg.MessageMediaContact); ok && contact != nil {
		name := strings.TrimSpace(contact.FirstName + " " + contact.LastName)
		if name == "" {
			name = contact.PhoneNumber
		}
		contactProto = &conduitv1.ProtoSharedContact{
			DisplayName: name,
			PhoneNumber: contact.PhoneNumber,
			Vcard:       contact.Vcard,
		}
		if strings.TrimSpace(body) == "" {
			body = name
		}
	}

	if strings.TrimSpace(body) == "" && !media.found() && pollProto == nil && locProto == nil && contactProto == nil {
		return nil
	}

	eventType := "message.received"
	senderName := ""
	if msg.Out {
		eventType = "message.sent"
		senderName = "me"
	} else if sender, _, senderID := remoteIDForPeer(msg.FromID); sender != "" {
		senderName = p.senderDisplayName(sender, senderID)
	}

	targetRemoteID := remoteID
	topicID := 0
	if _, tid, isTopic := parseTopicRemoteID(remoteID); isTopic {
		topicID = tid
	} else if peer := p.lookupPeer(remoteID); peer != nil && peer.channelType == "forum" {
		if header, ok := msg.ReplyTo.(*tg.MessageReplyHeader); ok && header != nil {
			if header.ReplyToTopID > 0 {
				topicID = header.ReplyToTopID
			} else if header.ForumTopic && header.ReplyToMsgID > 0 {
				topicID = header.ReplyToMsgID
			}
		}
		if topicID == 0 {
			if p.hasRegisteredPeer(topicRemoteID(remoteID, 1)) {
				topicID = 1
			}
		}
		if topicID > 0 {
			targetRemoteID = topicRemoteID(remoteID, topicID)
		}
	}

	event := &conduitv1.PluginEvent{
		EventType:       eventType,
		RemoteMessageId: strconv.Itoa(msg.ID),
		RemoteSenderId:  targetRemoteID,
		SenderName:      senderName,
		Body:            body,
		SentAtUnix:      int64(msg.Date),
		ChatJid:         targetRemoteID,
		ContentType:     "text",
	}

	if pollProto != nil {
		event.ContentType = "poll"
		event.Poll = pollProto
	} else if locProto != nil {
		event.ContentType = "location"
		event.Location = locProto
	} else if contactProto != nil {
		event.ContentType = "contact"
		event.SharedContact = contactProto
	}

	if msg.EditDate != 0 {
		event.IsEdited = true
		event.EditedAtUnix = int64(msg.EditDate)
	}
	if msg.Pinned {
		event.IsPinned = true
	}

	if header, ok := msg.ReplyTo.(*tg.MessageReplyHeader); ok && header != nil && header.ReplyToMsgID != 0 {
		if topicID == 0 || header.ReplyToMsgID != topicID {
			event.ReplyToRemoteId = strconv.Itoa(header.ReplyToMsgID)
		}
	}

	if buttonRows := parseTelegramReplyMarkup(msg.ReplyMarkup); len(buttonRows) > 0 {
		event.ButtonRows = buttonRows
	}

	if media.found() {
		event.ContentType = media.contentType
		event.Attachments = []*conduitv1.ProtoAttachment{
			// No se descarga al recibir: el archivo se baja a demanda con remote_ref.
			{
				Id:             fmt.Sprintf("att-%d", msg.ID),
				FileName:       media.fileName,
				MimeType:       media.mimeType,
				FileSize:       media.size,
				DownloadStatus: downloadStatusRemoteOnly,
				RemoteRef:      encodeMediaRef(remoteID, msg.ID),
			},
		}
	}

	if kind == kindUser {
		// El teléfono permite unir este chat con la misma persona en otra cuenta.
		if info := p.lookupPeer(remoteID); info != nil {
			event.IdentityPhone = info.phone
		}
	} else {
		p.applyServerIdentity(event, remoteID, topicID)
		if sender, _, _ := remoteIDForPeer(msg.FromID); sender != "" {
			event.ParticipantId = sender
		}
	}

	return event
}

// applyServerIdentity completa ServerId/ServerName/ChannelName/ChannelType de un chat grupal o canal.
func (p *TelegramPlugin) applyServerIdentity(event *conduitv1.PluginEvent, remoteID string, topicID int) {
	info := p.lookupPeer(remoteID)
	title := ""
	channelType := "group"
	if info != nil {
		title = info.title
		if info.channelType != "" {
			channelType = info.channelType
		}
	}
	event.ServerId = remoteID
	event.ServerName = title

	if topicID > 0 {
		event.ParentChannelId = remoteID
		event.ChannelType = "forum"

		topicRemoteID := topicRemoteID(remoteID, topicID)
		topicTitle := ""
		if tInfo := p.lookupPeer(topicRemoteID); tInfo != nil && tInfo.title != "" {
			topicTitle = tInfo.title
		} else if topicID == 1 {
			topicTitle = "General"
		} else {
			topicTitle = fmt.Sprintf("Tema %d", topicID)
		}
		event.ChannelName = topicTitle

		if info != nil && p.lookupPeer(topicRemoteID) == nil {
			p.rememberPeer(&peerInfo{
				remoteID:    topicRemoteID,
				kind:        kindChannel,
				id:          int64(topicID),
				title:       topicTitle,
				channelType: "forum",
				peer:        info.peer,
				topicID:     topicID,
			})
		}
	} else {
		event.ChannelName = title
		event.ChannelType = channelType
	}
}

// senderDisplayName busca el nombre visible del remitente en el registro de chats.
func (p *TelegramPlugin) senderDisplayName(remoteID string, senderID int64) string {
	if info := p.lookupPeer(remoteID); info != nil && info.title != "" {
		return info.title
	}
	return remoteID
}

// extractMessageID saca el id remoto del resultado de un envío.
func extractMessageID(updates tg.UpdatesClass) string {
	switch u := updates.(type) {
	case *tg.Updates:
		for _, update := range u.Updates {
			if id, ok := update.(*tg.UpdateMessageID); ok && id.ID != 0 {
				return strconv.Itoa(id.ID)
			}
		}
	case *tg.UpdateShortSentMessage:
		return strconv.Itoa(u.ID)
	}
	return ""
}

// inputReplyTo construye el encabezado de respuesta o hilo según si el destino es un tema.
func inputReplyTo(topicID, replyToMsgID int) tg.InputReplyToClass {
	if topicID > 0 {
		if replyToMsgID > 0 && replyToMsgID != topicID {
			return &tg.InputReplyToMessage{
				ReplyToMsgID: replyToMsgID,
				TopMsgID:     topicID,
			}
		}
		return &tg.InputReplyToMessage{
			ReplyToMsgID: topicID,
		}
	}
	if replyToMsgID > 0 {
		return &tg.InputReplyToMessage{
			ReplyToMsgID: replyToMsgID,
		}
	}
	return nil
}

// sendMessage envía texto a un chat registrado y devuelve el id remoto.
func (p *TelegramPlugin) sendMessage(ctx context.Context, client *telegram.Client, peer *peerInfo, body string, replyToMsgID int) (string, error) {
	randomID := rand.Int63()

	req := &tg.MessagesSendMessageRequest{
		Peer:     peer.peer,
		Message:  body,
		RandomID: randomID,
	}
	if replyTo := inputReplyTo(peer.topicID, replyToMsgID); replyTo != nil {
		req.ReplyTo = replyTo
	}

	updates, err := client.API().MessagesSendMessage(ctx, req)
	if err != nil {
		return "", err
	}

	remoteID := extractMessageID(updates)
	if remoteID == "" {
		remoteID = strconv.FormatInt(randomID, 10)
	}
	return remoteID, nil
}

// handleReactionUpdate traduce una actualización de reacciones al stream del core.
func (p *TelegramPlugin) handleReactionUpdate(ur *tg.UpdateMessageReactions) {
	if ur == nil {
		return
	}
	remoteID, _, _ := remoteIDForPeer(ur.Peer)
	if remoteID == "" {
		return
	}

	for _, rc := range ur.Reactions.Results {
		if emoji := reactionToEmoji(rc.Reaction); emoji != "" {
			p.emit(&conduitv1.PluginEvent{
				EventType:       "message.reaction",
				RemoteMessageId: strconv.Itoa(ur.MsgID),
				RemoteSenderId:  remoteID,
				ChatJid:         remoteID,
				ReactionEmoji:   emoji,
			})
		}
	}
}

func reactionToEmoji(rc tg.ReactionClass) string {
	if emoji, ok := rc.(*tg.ReactionEmoji); ok && emoji != nil {
		return emoji.Emoticon
	}
	return ""
}

// parseTelegramPoll convierte un MessageMediaPoll de Telegram a ProtoPoll unificado.
func parseTelegramPoll(m *tg.MessageMediaPoll) *conduitv1.ProtoPoll {
	if m == nil {
		return nil
	}
	poll := m.Poll
	results := m.Results

	resMap := make(map[string]tg.PollAnswerVoters)
	for _, r := range results.Results {
		resMap[string(r.Option)] = r
	}

	selectableCount := int32(1)
	if poll.MultipleChoice {
		selectableCount = int32(len(poll.Answers))
	}

	var options []*conduitv1.ProtoPollOption
	for idx, ansClass := range poll.Answers {
		ans, ok := ansClass.(*tg.PollAnswer)
		if !ok || ans == nil {
			continue
		}
		optID := strconv.Itoa(idx)
		voteCount := int32(0)
		chosen := false
		if r, ok := resMap[string(ans.Option)]; ok {
			voteCount = int32(r.Voters)
			chosen = r.Chosen
		}
		options = append(options, &conduitv1.ProtoPollOption{
			Id:        optID,
			Text:      ans.Text.Text,
			VoteCount: voteCount,
			Chosen:    chosen,
		})
	}

	return &conduitv1.ProtoPoll{
		Question:        poll.Question.Text,
		Options:         options,
		SelectableCount: selectableCount,
		IsClosed:        poll.Closed,
		TotalVotes:      int32(results.TotalVoters),
		IsQuiz:          poll.Quiz,
	}
}

// handlePollUpdate traduce una actualización de encuesta al stream del core.
func (p *TelegramPlugin) handlePollUpdate(u *tg.UpdateMessagePoll) {
	if u == nil {
		return
	}
	msgID := strconv.Itoa(u.MsgID)
	if u.MsgID == 0 {
		msgID = strconv.FormatInt(u.PollID, 10)
	}
	p.emit(&conduitv1.PluginEvent{
		EventType:       "message.poll_update",
		RemoteMessageId: msgID,
		Poll:            parseTelegramPoll(&tg.MessageMediaPoll{Poll: u.Poll, Results: u.Results}),
	})
}

// handleEditMessage procesa un mensaje editado en Telegram y lo emite como message.edited.
func (p *TelegramPlugin) handleEditMessage(e *tg.Entities, m tg.MessageClass) {
	msg, ok := m.(*tg.Message)
	if !ok || msg == nil {
		return
	}
	p.rememberEntities(e)
	remoteID, kind, _ := remoteIDForPeer(msg.PeerID)
	if remoteID == "" {
		return
	}
	event := p.messageEvent(msg, remoteID, kind)
	if event != nil {
		event.EventType = "message.edited"
		p.emit(event)
	}
}

// handleDeleteMessages procesa IDs de mensajes eliminados en Telegram y los emite como message.deleted.
func (p *TelegramPlugin) handleDeleteMessages(msgIDs []int) {
	for _, id := range msgIDs {
		p.emit(&conduitv1.PluginEvent{
			EventType:       "message.deleted",
			RemoteMessageId: strconv.Itoa(id),
		})
	}
}

// handleDeleteChannelMessages procesa IDs de mensajes eliminados en canales/supergrupos de Telegram con contexto de chat.
func (p *TelegramPlugin) handleDeleteChannelMessages(channelID int64, msgIDs []int) {
	remoteID := channelRemoteID(channelID)
	for _, id := range msgIDs {
		p.emit(&conduitv1.PluginEvent{
			EventType:       "message.deleted",
			RemoteMessageId: strconv.Itoa(id),
			RemoteSenderId:  remoteID,
			ChatJid:         remoteID,
		})
	}
}

// parseTelegramReplyMarkup convierte ReplyInlineMarkup o ReplyKeyboardMarkup de Telegram a ProtoButtonRow.
func parseTelegramReplyMarkup(rm tg.ReplyMarkupClass) []*conduitv1.ProtoButtonRow {
	if rm == nil {
		return nil
	}
	var result []*conduitv1.ProtoButtonRow

	switch markup := rm.(type) {
	case *tg.ReplyInlineMarkup:
		for _, row := range markup.Rows {
			var btns []*conduitv1.ProtoButton
			for _, btn := range row.Buttons {
				b := &conduitv1.ProtoButton{
					Text: btn.Text,
					Type: "callback",
				}
				switch t := btn.Type.(type) {
				case *tg.InlineButtonTypeCallback:
					b.Id = string(t.Data)
					b.Type = "callback"
				case *tg.InlineButtonTypeURL:
					b.Id = t.URL
					b.Url = t.URL
					b.Type = "url"
				case *tg.InlineButtonTypeWebView:
					b.Id = t.URL
					b.Url = t.URL
					b.Type = "url"
				case *tg.InlineButtonTypeSwitchInline:
					b.Id = t.Query
					b.Type = "quick_reply"
				default:
					b.Id = btn.Text
				}
				btns = append(btns, b)
			}
			if len(btns) > 0 {
				result = append(result, &conduitv1.ProtoButtonRow{Buttons: btns})
			}
		}
	case *tg.ReplyKeyboardMarkup:
		for _, row := range markup.Rows {
			var btns []*conduitv1.ProtoButton
			for _, btn := range row.Buttons {
				if btn.Text != "" {
					btns = append(btns, &conduitv1.ProtoButton{
						Id:   btn.Text,
						Text: btn.Text,
						Type: "quick_reply",
					})
				}
			}
			if len(btns) > 0 {
				result = append(result, &conduitv1.ProtoButtonRow{Buttons: btns})
			}
		}
	}

	return result
}

// historyOffsetID traduce el cursor remoto al offset_id de messages.getHistory.
// Un cursor vacío o inválido significa "última página".
func historyOffsetID(raw string) int {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0
	}
	id, err := strconv.Atoi(raw)
	if err != nil || id <= 0 {
		return 0
	}
	return id
}

// syncHistory publica una página de mensajes de un chat.
// offsetID = 0 trae la última página; con offsetID trae la anterior a ese mensaje.
func (p *TelegramPlugin) syncHistory(ctx context.Context, client *telegram.Client, peer *peerInfo, limit, offsetID int) error {
	if limit <= 0 || limit > 100 {
		limit = 50
	}

	var res tg.MessagesMessagesClass
	var err error

	if peer.topicID > 0 {
		res, err = client.API().MessagesGetReplies(ctx, &tg.MessagesGetRepliesRequest{
			Peer:     peer.peer,
			MsgID:    peer.topicID,
			Limit:    limit,
			OffsetID: offsetID,
		})
	} else {
		res, err = client.API().MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{
			Peer:     peer.peer,
			Limit:    limit,
			OffsetID: offsetID,
		})
	}
	if err != nil {
		return err
	}

	var messages []tg.MessageClass
	switch h := res.(type) {
	case *tg.MessagesMessages:
		messages = h.Messages
	case *tg.MessagesMessagesSlice:
		messages = h.Messages
	case *tg.MessagesChannelMessages:
		messages = h.Messages
	}

	for _, mc := range messages {
		msg, ok := mc.(*tg.Message)
		if !ok || msg == nil {
			continue
		}
		if event := p.messageEvent(msg, peer.remoteID, peer.kind); event != nil {
			p.emit(event)
		}
	}
	return nil
}
