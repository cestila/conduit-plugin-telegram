package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/gotd/td/telegram/downloader"
	"github.com/gotd/td/tg"

	conduitv1 "github.com/cestila/ConduitOmniChat/sdk/conduitv1"
)

// downloadStatusRemoteOnly marca un adjunto que aún no se bajó del servidor.
const downloadStatusRemoteOnly = "remote_only"

// mediaRef es la receta opaca para volver a pedir un adjunto: el chat (mismo
// identificador que usa lookupPeer) y el id del mensaje que lo contiene.
type mediaRef struct {
	Peer  string `json:"peer"`
	MsgID int    `json:"msg_id"`
}

// encodeMediaRef serializa la receta que viaja en ProtoAttachment.remote_ref.
func encodeMediaRef(peer string, msgID int) string {
	raw, _ := json.Marshal(mediaRef{Peer: peer, MsgID: msgID})
	return string(raw)
}

// parseMediaRef valida y decodifica una receta emitida por este plugin.
func parseMediaRef(raw string) (mediaRef, error) {
	var ref mediaRef
	if err := json.Unmarshal([]byte(raw), &ref); err != nil {
		return mediaRef{}, fmt.Errorf("remote_ref inválido: %w", err)
	}
	if ref.Peer == "" || ref.MsgID <= 0 {
		return mediaRef{}, errors.New("remote_ref inválido: faltan peer o msg_id")
	}
	return ref, nil
}

// safeMediaPath arma la ruta de destino de un adjunto: siempre es un archivo
// directo de dir, con el id del mensaje de prefijo y solo el nombre base saneado.
func safeMediaPath(dir string, msgID int, name string) string {
	return filepath.Join(dir, storageFileName(msgID, name))
}

// DownloadMedia baja un adjunto de Telegram a demanda. Vuelve a pedir el
// mensaje por id porque el file_reference guardado puede haber vencido.
func (p *TelegramPlugin) DownloadMedia(ctx context.Context, req *conduitv1.PluginDownloadMediaRequest) (*conduitv1.PluginDownloadMediaResponse, error) {
	fail := func(format string, args ...any) (*conduitv1.PluginDownloadMediaResponse, error) {
		return &conduitv1.PluginDownloadMediaResponse{Success: false, ErrorMessage: fmt.Sprintf(format, args...)}, nil
	}

	ref, err := parseMediaRef(req.GetRemoteRef())
	if err != nil {
		return fail("%v", err)
	}

	client := p.currentClient()
	if client == nil || !p.isConnected() {
		return fail("el plugin de Telegram no está conectado")
	}

	peer := p.lookupPeer(ref.Peer)
	if peer == nil && p.dialogCacheIsStale() {
		p.syncDialogs(ctx, client)
		peer = p.lookupPeer(ref.Peer)
	}
	if peer == nil {
		return fail("chat de Telegram desconocido: %s", ref.Peer)
	}

	ctx, cancel := context.WithTimeout(ctx, incomingDownloadTimeout)
	defer cancel()

	api := client.API()
	messages, err := p.fetchMessage(ctx, api, peer, ref.MsgID)
	if err != nil {
		return fail("no se pudo pedir el mensaje a Telegram: %v", err)
	}

	var msg *tg.Message
	for _, mc := range messages {
		if m, ok := mc.(*tg.Message); ok && m != nil && m.ID == ref.MsgID {
			msg = m
			break
		}
	}
	if msg == nil {
		return fail("el mensaje %d ya no existe en Telegram", ref.MsgID)
	}
	media, location := describeIncomingMedia(msg)
	if !media.found() || location == nil {
		return fail("el mensaje %d no tiene un adjunto descargable", ref.MsgID)
	}

	dir := mediaDownloadDir()
	target := safeMediaPath(dir, msg.ID, media.fileName)
	size, err := downloadToFile(ctx, api, location, dir, target)
	if err != nil {
		return fail("no se pudo descargar %s: %v", media.fileName, err)
	}
	return &conduitv1.PluginDownloadMediaResponse{Success: true, LocalPath: target, FileSize: size}, nil
}

// fetchMessage pide un mensaje por id; los canales usan su propio método.
func (p *TelegramPlugin) fetchMessage(ctx context.Context, api *tg.Client, peer *peerInfo, msgID int) ([]tg.MessageClass, error) {
	ids := []tg.InputMessageClass{&tg.InputMessageID{ID: msgID}}

	var res tg.MessagesMessagesClass
	var err error
	if peer.kind == kindChannel {
		chPeer, ok := peer.peer.(*tg.InputPeerChannel)
		if !ok {
			return nil, errors.New("canal sin acceso válido")
		}
		res, err = api.ChannelsGetMessages(ctx, &tg.ChannelsGetMessagesRequest{
			Channel: &tg.InputChannel{ChannelID: chPeer.ChannelID, AccessHash: chPeer.AccessHash},
			ID:      ids,
		})
	} else {
		res, err = api.MessagesGetMessages(ctx, ids)
	}
	if err != nil {
		return nil, err
	}

	switch r := res.(type) {
	case *tg.MessagesMessages:
		return r.Messages, nil
	case *tg.MessagesMessagesSlice:
		return r.Messages, nil
	case *tg.MessagesChannelMessages:
		return r.Messages, nil
	}
	return nil, nil
}

// downloadToFile baja a un temporal dentro de dir y lo renombra a target, así
// una descarga cortada nunca deja un archivo parcial con el nombre final.
func downloadToFile(ctx context.Context, api *tg.Client, location tg.InputFileLocationClass, dir, target string) (int64, error) {
	tmp, err := os.CreateTemp(dir, ".descarga-*")
	if err != nil {
		return 0, err
	}
	tmpPath := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpPath) }

	if _, err := downloader.NewDownloader().Download(api, location).Stream(ctx, tmp); err != nil {
		_ = tmp.Close()
		cleanup()
		return 0, err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return 0, err
	}
	if err := os.Rename(tmpPath, target); err != nil {
		cleanup()
		return 0, err
	}
	info, err := os.Stat(target)
	if err != nil {
		return 0, err
	}
	return info.Size(), nil
}
