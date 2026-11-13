package main

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gotd/td/telegram"
	"github.com/gotd/td/tg"

	conduitv1 "github.com/cestila/ConduitOmniChat/sdk/conduitv1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// TelegramPlugin implementa conduitv1.PluginServiceServer con un cliente MTProto real.
type TelegramPlugin struct {
	conduitv1.UnimplementedPluginServiceServer

	mu             sync.RWMutex
	connected      bool
	running        bool
	pairing        bool
	client         *telegram.Client
	cancel         context.CancelFunc
	apiID          int
	apiHash        string
	password       string
	accountID      string
	peers          map[string]*peerInfo
	peersByID      map[string]*peerInfo // índice por dígitos: el core normaliza los ids como teléfonos
	lastDialogSync time.Time
	eventCh        chan *conduitv1.PluginEvent
}

// NewTelegramPlugin inicializa el plugin de Telegram.
func NewTelegramPlugin() *TelegramPlugin {
	return &TelegramPlugin{
		peers:     make(map[string]*peerInfo),
		peersByID: make(map[string]*peerInfo),
		eventCh:   make(chan *conduitv1.PluginEvent, 512),
	}
}

// Connect inicia la sesión MTProto: reutiliza la sesión guardada o abre el login por QR.
func (p *TelegramPlugin) Connect(ctx context.Context, req *conduitv1.ConnectRequest) (*conduitv1.ConnectResponse, error) {
	creds := req.GetCredentials()

	apiID, err := parseAPIID(creds.GetApiId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	apiHash := strings.TrimSpace(creds.GetApiHash())
	if apiHash == "" {
		return nil, status.Error(codes.InvalidArgument, "faltan credenciales de Telegram: api_hash")
	}
	password := strings.TrimSpace(creds.GetPassword())
	accountID := req.GetAccountIdentifier()

	p.mu.Lock()

	// Si ya hay un intento en curso con las mismas credenciales, no reiniciamos el QR.
	if p.running && p.apiID == apiID && p.apiHash == apiHash && p.password == password {
		pairing := p.pairing
		connected := p.connected
		p.mu.Unlock()

		switch {
		case connected:
			return &conduitv1.ConnectResponse{Success: true, Message: "La sesión de Telegram ya está activa"}, nil
		case pairing:
			return &conduitv1.ConnectResponse{Success: true, Pairing: true, Message: "Esperando escaneo del código QR de Telegram"}, nil
		default:
			return &conduitv1.ConnectResponse{Success: true, Message: "Conectando con Telegram"}, nil
		}
	}

	if p.cancel != nil {
		p.cancel()
		p.cancel = nil
	}

	runCtx, cancel := context.WithCancel(context.Background())
	p.cancel = cancel
	p.apiID = apiID
	p.apiHash = apiHash
	p.password = password
	p.accountID = accountID
	p.running = true
	p.connected = false
	p.pairing = !hasSavedSession(p.sessionPath())
	pairing := p.pairing
	p.mu.Unlock()

	go p.run(runCtx)

	if pairing {
		return &conduitv1.ConnectResponse{
			Success: true,
			Pairing: true,
			Message: "Esperando escaneo del código QR de Telegram",
		}, nil
	}
	return &conduitv1.ConnectResponse{
		Success: true,
		Message: "Reconectando la sesión de Telegram guardada",
	}, nil
}

// Disconnect cancela la sesión y detiene el cliente MTProto.
func (p *TelegramPlugin) Disconnect(ctx context.Context, req *conduitv1.DisconnectRequest) (*conduitv1.DisconnectResponse, error) {
	p.mu.Lock()
	cancel := p.cancel
	p.cancel = nil
	p.connected = false
	p.running = false
	p.pairing = false
	p.client = nil
	p.peers = make(map[string]*peerInfo)
	p.peersByID = make(map[string]*peerInfo)
	p.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	return &conduitv1.DisconnectResponse{Success: true}, nil
}

// SendOutgoingMessage envía un mensaje real por MTProto.
func (p *TelegramPlugin) SendOutgoingMessage(ctx context.Context, req *conduitv1.OutgoingMessageRequest) (*conduitv1.OutgoingMessageResponse, error) {
	client := p.currentClient()
	if client == nil || !p.isConnected() {
		return nil, status.Error(codes.Unavailable, "el plugin de Telegram no está conectado")
	}

	attachments := req.GetAttachments()
	body, err := validateOutgoing(req.GetBody(), attachments)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	peer := p.lookupPeer(req.GetRemoteRecipientId())
	if peer == nil {
		return &conduitv1.OutgoingMessageResponse{
			Success:      false,
			ErrorMessage: fmt.Sprintf("chat de Telegram desconocido: %s (sincroniza los chats primero)", req.GetRemoteRecipientId()),
		}, nil
	}

	var remoteID string
	replyToMsgID, _ := strconv.Atoi(req.GetReplyToRemoteId())
	if len(attachments) > 0 {
		remoteID, err = p.sendMedia(ctx, client, peer, body, attachments, replyToMsgID)
	} else {
		remoteID, err = p.sendMessage(ctx, client, peer, body, replyToMsgID)
	}
	if err != nil {
		return &conduitv1.OutgoingMessageResponse{
			Success:      false,
			ErrorMessage: fmt.Sprintf("error enviando a Telegram: %v", err),
		}, nil
	}

	return &conduitv1.OutgoingMessageResponse{Success: true, RemoteMessageId: remoteID}, nil
}

// StreamIncomingEvents transmite los eventos del cliente MTProto hacia el core.
func (p *TelegramPlugin) StreamIncomingEvents(req *conduitv1.PluginStreamRequest, stream conduitv1.PluginService_StreamIncomingEventsServer) error {
	for {
		select {
		case <-stream.Context().Done():
			return nil
		case event := <-p.eventCh:
			if err := stream.Send(event); err != nil {
				return err
			}
		}
	}
}

// SendReaction envía o quita una reacción a un mensaje en Telegram.
func (p *TelegramPlugin) SendReaction(ctx context.Context, req *conduitv1.PluginReactionRequest) (*conduitv1.PluginReactionResponse, error) {
	client := p.currentClient()
	if client == nil || !p.isConnected() {
		return nil, status.Error(codes.Unavailable, "el plugin de Telegram no está conectado")
	}

	peer := p.lookupPeer(req.GetRemoteChatId())
	if peer == nil {
		return &conduitv1.PluginReactionResponse{
			Success:      false,
			ErrorMessage: fmt.Sprintf("chat de Telegram desconocido: %s", req.GetRemoteChatId()),
		}, nil
	}

	msgID, err := strconv.Atoi(req.GetRemoteMessageId())
	if err != nil || msgID <= 0 {
		return nil, status.Errorf(codes.InvalidArgument, "remote_message_id inválido: %s", req.GetRemoteMessageId())
	}

	var reactions []tg.ReactionClass
	if emoji := strings.TrimSpace(req.GetEmoji()); emoji != "" {
		reactions = append(reactions, &tg.ReactionEmoji{Emoticon: emoji})
	}

	_, err = client.API().MessagesSendReaction(ctx, &tg.MessagesSendReactionRequest{
		Peer:     peer.peer,
		MsgID:    msgID,
		Reaction: reactions,
	})
	if err != nil {
		return &conduitv1.PluginReactionResponse{
			Success:      false,
			ErrorMessage: fmt.Sprintf("error enviando reacción a Telegram: %v", err),
		}, nil
	}

	return &conduitv1.PluginReactionResponse{Success: true}, nil
}

// SendVote envía el voto de una encuesta a Telegram.
func (p *TelegramPlugin) SendVote(ctx context.Context, req *conduitv1.PluginVoteRequest) (*conduitv1.PluginVoteResponse, error) {
	client := p.currentClient()
	if client == nil || !p.isConnected() {
		return nil, status.Error(codes.Unavailable, "el plugin de Telegram no está conectado")
	}

	peer := p.lookupPeer(req.GetRemoteChatId())
	if peer == nil {
		return &conduitv1.PluginVoteResponse{
			Success:      false,
			ErrorMessage: fmt.Sprintf("chat de Telegram desconocido: %s", req.GetRemoteChatId()),
		}, nil
	}

	msgID, err := strconv.Atoi(req.GetRemoteMessageId())
	if err != nil || msgID <= 0 {
		return nil, status.Errorf(codes.InvalidArgument, "remote_message_id inválido: %s", req.GetRemoteMessageId())
	}

	var optionsBytes [][]byte
	for _, sel := range req.GetSelectedOptions() {
		optionsBytes = append(optionsBytes, []byte(sel))
	}

	_, err = client.API().MessagesSendVote(ctx, &tg.MessagesSendVoteRequest{
		Peer:    peer.peer,
		MsgID:   msgID,
		Options: optionsBytes,
	})
	if err != nil {
		return &conduitv1.PluginVoteResponse{
			Success:      false,
			ErrorMessage: fmt.Sprintf("error enviando voto a Telegram: %v", err),
		}, nil
	}

	return &conduitv1.PluginVoteResponse{Success: true}, nil
}

// ClickButton envía el callback de un botón presionado a Telegram.
func (p *TelegramPlugin) ClickButton(ctx context.Context, req *conduitv1.PluginClickButtonRequest) (*conduitv1.PluginClickButtonResponse, error) {
	client := p.currentClient()
	if client == nil || !p.isConnected() {
		return nil, status.Error(codes.Unavailable, "el plugin de Telegram no está conectado")
	}

	peer := p.lookupPeer(req.GetRemoteChatId())
	if peer == nil {
		return &conduitv1.PluginClickButtonResponse{
			Success:      false,
			ErrorMessage: fmt.Sprintf("chat de Telegram desconocido: %s", req.GetRemoteChatId()),
		}, nil
	}

	msgID, err := strconv.Atoi(req.GetRemoteMessageId())
	if err != nil || msgID <= 0 {
		return nil, status.Errorf(codes.InvalidArgument, "remote_message_id inválido: %s", req.GetRemoteMessageId())
	}

	res, err := client.API().MessagesGetBotCallbackAnswer(ctx, &tg.MessagesGetBotCallbackAnswerRequest{
		Peer:  peer.peer,
		MsgID: msgID,
		Data:  []byte(req.ButtonId),
	})
	if err != nil {
		return &conduitv1.PluginClickButtonResponse{
			Success:      false,
			ErrorMessage: fmt.Sprintf("error obteniendo respuesta del bot de Telegram: %v", err),
		}, nil
	}

	return &conduitv1.PluginClickButtonResponse{
		Success:         true,
		FeedbackMessage: res.Message,
		IsAlert:         res.Alert,
		Url:             res.URL,
	}, nil
}

// EditMessage edita el texto de un mensaje en Telegram.
func (p *TelegramPlugin) EditMessage(ctx context.Context, req *conduitv1.PluginEditMessageRequest) (*conduitv1.PluginEditMessageResponse, error) {
	client := p.currentClient()
	if client == nil || !p.isConnected() {
		return nil, status.Error(codes.Unavailable, "el plugin de Telegram no está conectado")
	}

	peer := p.lookupPeer(req.GetRemoteChatId())
	if peer == nil {
		return &conduitv1.PluginEditMessageResponse{
			Success:      false,
			ErrorMessage: fmt.Sprintf("chat de Telegram desconocido: %s", req.GetRemoteChatId()),
		}, nil
	}

	msgID, err := strconv.Atoi(req.GetRemoteMessageId())
	if err != nil || msgID <= 0 {
		return nil, status.Errorf(codes.InvalidArgument, "remote_message_id inválido: %s", req.GetRemoteMessageId())
	}

	_, err = client.API().MessagesEditMessage(ctx, &tg.MessagesEditMessageRequest{
		Peer:    peer.peer,
		ID:      msgID,
		Message: req.GetNewBody(),
	})
	if err != nil {
		return &conduitv1.PluginEditMessageResponse{
			Success:      false,
			ErrorMessage: fmt.Sprintf("error editando mensaje en Telegram: %v", err),
		}, nil
	}

	return &conduitv1.PluginEditMessageResponse{Success: true}, nil
}

// DeleteMessage revoca un mensaje para todos en Telegram.
func (p *TelegramPlugin) DeleteMessage(ctx context.Context, req *conduitv1.PluginDeleteMessageRequest) (*conduitv1.PluginDeleteMessageResponse, error) {
	client := p.currentClient()
	if client == nil || !p.isConnected() {
		return nil, status.Error(codes.Unavailable, "el plugin de Telegram no está conectado")
	}

	msgID, err := strconv.Atoi(req.GetRemoteMessageId())
	if err != nil || msgID <= 0 {
		return nil, status.Errorf(codes.InvalidArgument, "remote_message_id inválido: %s", req.GetRemoteMessageId())
	}

	_, err = client.API().MessagesDeleteMessages(ctx, &tg.MessagesDeleteMessagesRequest{
		ID:     []int{msgID},
		Revoke: true,
	})
	if err != nil {
		return &conduitv1.PluginDeleteMessageResponse{
			Success:      false,
			ErrorMessage: fmt.Sprintf("error eliminando mensaje en Telegram: %v", err),
		}, nil
	}

	return &conduitv1.PluginDeleteMessageResponse{Success: true}, nil
}

// PinMessage fija o desfija un mensaje en Telegram.
func (p *TelegramPlugin) PinMessage(ctx context.Context, req *conduitv1.PluginPinMessageRequest) (*conduitv1.PluginPinMessageResponse, error) {
	client := p.currentClient()
	if client == nil || !p.isConnected() {
		return nil, status.Error(codes.Unavailable, "el plugin de Telegram no está conectado")
	}

	peer := p.lookupPeer(req.GetRemoteChatId())
	if peer == nil {
		return &conduitv1.PluginPinMessageResponse{
			Success:      false,
			ErrorMessage: fmt.Sprintf("chat de Telegram desconocido: %s", req.GetRemoteChatId()),
		}, nil
	}

	msgID, err := strconv.Atoi(req.GetRemoteMessageId())
	if err != nil || msgID <= 0 {
		return nil, status.Errorf(codes.InvalidArgument, "remote_message_id inválido: %s", req.GetRemoteMessageId())
	}

	_, err = client.API().MessagesUpdatePinnedMessage(ctx, &tg.MessagesUpdatePinnedMessageRequest{
		Peer:  peer.peer,
		ID:    msgID,
		Unpin: !req.GetPinned(),
	})
	if err != nil {
		return &conduitv1.PluginPinMessageResponse{
			Success:      false,
			ErrorMessage: fmt.Sprintf("error actualizando mensaje fijado en Telegram: %v", err),
		}, nil
	}

	return &conduitv1.PluginPinMessageResponse{Success: true}, nil
}

// SyncChatHistory publica el historial reciente del chat pedido.
func (p *TelegramPlugin) SyncChatHistory(ctx context.Context, req *conduitv1.SyncChatHistoryRequest) (*conduitv1.SyncChatHistoryResponse, error) {
	client := p.currentClient()
	if client == nil || !p.isConnected() {
		return nil, status.Error(codes.Unavailable, "el plugin de Telegram no está conectado")
	}

	peer := p.lookupPeer(req.GetRemoteChatId())
	if peer == nil && p.dialogCacheIsStale() {
		// El core puede pedir historial antes de que termine la lista de chats, o
		// de un chat que quedó fuera de la primera página: se refresca y se reintenta.
		p.syncDialogs(ctx, client)
		peer = p.lookupPeer(req.GetRemoteChatId())
	}
	if peer == nil {
		return &conduitv1.SyncChatHistoryResponse{
			Success: false,
			Message: fmt.Sprintf("chat de Telegram desconocido: %s", req.GetRemoteChatId()),
		}, nil
	}

	offsetID := historyOffsetID(req.GetOldestRemoteMessageId())

	if err := p.syncHistory(ctx, client, peer, int(req.GetLimit()), offsetID); err != nil {
		return &conduitv1.SyncChatHistoryResponse{
			Success: false,
			Message: fmt.Sprintf("error leyendo el historial de Telegram: %v", err),
		}, nil
	}

	return &conduitv1.SyncChatHistoryResponse{Success: true, Message: "Historial de Telegram sincronizado"}, nil
}

// dialogCacheIsStale evita refrescar la lista de chats en cada consulta perdida.
func (p *TelegramPlugin) dialogCacheIsStale() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return time.Since(p.lastDialogSync) > 30*time.Second
}

// MarkRead marca el historial como leído hasta max_id o el último mensaje.
func (p *TelegramPlugin) MarkRead(ctx context.Context, req *conduitv1.PluginMarkReadRequest) (*conduitv1.PluginMarkReadResponse, error) {
	client := p.currentClient()
	if client == nil || !p.isConnected() {
		return nil, status.Error(codes.Unavailable, "el plugin de Telegram no está conectado")
	}

	peer := p.lookupPeer(req.GetRemoteChatId())
	if peer == nil && p.dialogCacheIsStale() {
		p.syncDialogs(ctx, client)
		peer = p.lookupPeer(req.GetRemoteChatId())
	}
	if peer == nil {
		return &conduitv1.PluginMarkReadResponse{
			Success:      false,
			ErrorMessage: fmt.Sprintf("chat de Telegram desconocido: %s", req.GetRemoteChatId()),
		}, nil
	}

	maxID, _ := strconv.Atoi(req.GetMaxRemoteId())
	if maxID <= 0 && len(req.GetRemoteMessageIds()) > 0 {
		for _, mid := range req.GetRemoteMessageIds() {
			if id, err := strconv.Atoi(mid); err == nil && id > maxID {
				maxID = id
			}
		}
	}

	api := client.API()
	switch {
	case peer.topicID > 0:
		_, err := api.MessagesReadDiscussion(ctx, &tg.MessagesReadDiscussionRequest{
			Peer:      peer.peer,
			MsgID:     peer.topicID,
			ReadMaxID: maxID,
		})
		if err != nil {
			return &conduitv1.PluginMarkReadResponse{
				Success:      false,
				ErrorMessage: fmt.Sprintf("error marcando tema como leído en Telegram: %v", err),
			}, nil
		}
	case peer.kind == kindChannel:
		var inputChan tg.InputChannelClass
		if chPeer, ok := peer.peer.(*tg.InputPeerChannel); ok {
			inputChan = &tg.InputChannel{
				ChannelID:  chPeer.ChannelID,
				AccessHash: chPeer.AccessHash,
			}
		} else {
			inputChan = &tg.InputChannelEmpty{}
		}
		_, err := api.ChannelsReadHistory(ctx, &tg.ChannelsReadHistoryRequest{
			Channel: inputChan,
			MaxID:   maxID,
		})
		if err != nil {
			return &conduitv1.PluginMarkReadResponse{
				Success:      false,
				ErrorMessage: fmt.Sprintf("error marcando canal como leído en Telegram: %v", err),
			}, nil
		}
	default:
		_, err := api.MessagesReadHistory(ctx, &tg.MessagesReadHistoryRequest{
			Peer:  peer.peer,
			MaxID: maxID,
		})
		if err != nil {
			return &conduitv1.PluginMarkReadResponse{
				Success:      false,
				ErrorMessage: fmt.Sprintf("error marcando chat como leído en Telegram: %v", err),
			}, nil
		}
	}

	return &conduitv1.PluginMarkReadResponse{Success: true}, nil
}

// isConnected informa si la sesión MTProto está autorizada y viva.
func (p *TelegramPlugin) isConnected() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.connected
}

// nowUnix devuelve la marca de tiempo actual en segundos.
func nowUnix() int64 {
	return time.Now().Unix()
}
