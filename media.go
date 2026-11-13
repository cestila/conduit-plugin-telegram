package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math/rand"
	"mime"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/uploader"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"

	conduitv1 "github.com/cestila/ConduitOmniChat/sdk/conduitv1"
)

const (
	// incomingDownloadTimeout acota la descarga de un adjunto a demanda.
	incomingDownloadTimeout = 2 * time.Minute
)

// validateOutgoing valida y normaliza el cuerpo de un mensaje saliente.
// Un mensaje sin texto es válido mientras lleve adjuntos.
func validateOutgoing(body string, attachments []*conduitv1.OutgoingAttachment) (string, error) {
	body = strings.TrimSpace(body)
	if body == "" && len(attachments) == 0 {
		return "", errors.New("el mensaje no puede estar vacío: escribí texto o adjuntá un archivo")
	}
	return body, nil
}

// outgoingFileName devuelve el nombre visible del adjunto a enviar.
func outgoingFileName(att *conduitv1.OutgoingAttachment) string {
	if name := strings.TrimSpace(att.GetFileName()); name != "" {
		return name
	}
	return filepath.Base(att.GetFilePath())
}

// outgoingMimeType resuelve el MIME del adjunto; el core ya lo infiere, pero se
// mantiene un fallback por extensión para no romper con clientes viejos.
func outgoingMimeType(att *conduitv1.OutgoingAttachment) string {
	if mimeType := strings.TrimSpace(att.GetMimeType()); mimeType != "" {
		return mimeType
	}
	if mimeType := mime.TypeByExtension(strings.ToLower(filepath.Ext(att.GetFilePath()))); mimeType != "" {
		return mimeType
	}
	return "application/octet-stream"
}

// buildInputMedia traduce un archivo subido al media TL que corresponde a su MIME.
func buildInputMedia(uploaded tg.InputFileClass, mimeType, fileName string) tg.InputMediaClass {
	lower := strings.ToLower(strings.TrimSpace(mimeType))
	filename := &tg.DocumentAttributeFilename{FileName: fileName}

	switch {
	case strings.HasPrefix(lower, "image/") && lower != "image/gif":
		return &tg.InputMediaUploadedPhoto{File: uploaded}
	case lower == "image/gif":
		// Sin DocumentAttributeAnimated Telegram guarda el GIF como imagen fija.
		return &tg.InputMediaUploadedDocument{
			File:     uploaded,
			MimeType: mimeType,
			Attributes: []tg.DocumentAttributeClass{
				&tg.DocumentAttributeAnimated{},
				filename,
			},
		}
	case strings.HasPrefix(lower, "video/"):
		return &tg.InputMediaUploadedDocument{
			File:     uploaded,
			MimeType: mimeType,
			Attributes: []tg.DocumentAttributeClass{
				&tg.DocumentAttributeVideo{SupportsStreaming: true},
				filename,
			},
		}
	case strings.HasPrefix(lower, "audio/"):
		return &tg.InputMediaUploadedDocument{
			File:     uploaded,
			MimeType: mimeType,
			Attributes: []tg.DocumentAttributeClass{
				&tg.DocumentAttributeAudio{
					Title: fileName,
					Voice: isVoiceNoteMIME(mimeType),
				},
				filename,
			},
		}
	default:
		return &tg.InputMediaUploadedDocument{
			File:       uploaded,
			MimeType:   mimeType,
			ForceFile:  true,
			Attributes: []tg.DocumentAttributeClass{filename},
		}
	}
}

// isVoiceNoteMIME detecta si un MIME de audio corresponde a nota de voz.
// Telegram requiere contenedor Ogg con codec Opus (audio/ogg) para notas de voz nativas.
func isVoiceNoteMIME(mimeType string) bool {
	lower := strings.ToLower(strings.TrimSpace(mimeType))
	if lower == "" {
		return false
	}
	base := lower
	if i := strings.Index(base, ";"); i >= 0 {
		base = strings.TrimSpace(base[:i])
	}
	return base == "audio/ogg" || base == "audio/opus"
}

// sendMedia sube cada adjunto y lo envía con messages.sendMedia.
// El cuerpo del mensaje viaja como caption solo en el primer adjunto y el id
// remoto devuelto es el del primer envío.
func (p *TelegramPlugin) sendMedia(ctx context.Context, client *telegram.Client, peer *peerInfo, body string, attachments []*conduitv1.OutgoingAttachment, replyToMsgID int) (string, error) {
	upload := uploader.NewUploader(client.API())

	remoteID := ""
	for index, att := range attachments {
		name := outgoingFileName(att)
		uploadPath := att.GetFilePath()
		mimeType := outgoingMimeType(att)

		convertedPath, newMime, newName, cleanup := prepareTelegramAudio(uploadPath, mimeType, name)
		if cleanup != nil {
			defer cleanup()
		}
		if convertedPath != "" {
			uploadPath = convertedPath
			mimeType = newMime
			name = newName
		}

		file, err := upload.FromPath(ctx, uploadPath)
		if err != nil {
			return "", fmt.Errorf("subiendo %s: %w", name, err)
		}

		caption := ""
		if index == 0 {
			caption = body
		}

		randomID := rand.Int63()
		mediaReq := &tg.MessagesSendMediaRequest{
			Peer:     peer.peer,
			Media:    buildInputMedia(file, mimeType, name),
			Message:  caption,
			RandomID: randomID,
		}
		replyTo := inputReplyTo(peer.topicID, replyToMsgID)
		if replyTo != nil {
			mediaReq.ReplyTo = replyTo
		}
		updates, err := client.API().MessagesSendMedia(ctx, mediaReq)
		if err != nil && isMediaRejected(err) {
			// Algunos formatos exigen metadata que no podemos calcular sin decodificar
			// (dimensiones de video, por ejemplo). Reintentar como documento plano
			// entrega el archivo igual en vez de perder el envío.
			log.Printf("ℹ️ Telegram rechazó %s como media, se reintenta como documento: %v", name, err)
			fallbackReq := &tg.MessagesSendMediaRequest{
				Peer: peer.peer,
				Media: &tg.InputMediaUploadedDocument{
					File:       file,
					MimeType:   mimeType,
					ForceFile:  true,
					Attributes: []tg.DocumentAttributeClass{&tg.DocumentAttributeFilename{FileName: name}},
				},
				Message:  caption,
				RandomID: randomID,
			}
			if replyTo != nil {
				fallbackReq.ReplyTo = replyTo
			}
			updates, err = client.API().MessagesSendMedia(ctx, fallbackReq)
		}
		if err != nil {
			return "", fmt.Errorf("enviando %s: %w", name, err)
		}

		if index == 0 {
			remoteID = extractMessageID(updates)
			if remoteID == "" {
				remoteID = strconv.FormatInt(randomID, 10)
			}
		}
	}

	return remoteID, nil
}

// isMediaRejected informa si Telegram rechazó el media por formato o metadata,
// caso en el que conviene reintentar como documento plano.
func isMediaRejected(err error) bool {
	return tgerr.Is(err,
		"MEDIA_INVALID",
		"MEDIA_EMPTY",
		"PHOTO_EXT_INVALID",
		"IMAGE_PROCESS_FAILED",
		"VIDEO_CONTENT_TYPE_INVALID",
		"DOCUMENT_INVALID",
	)
}

// incomingMedia describe el adjunto detectado en un mensaje entrante.
type incomingMedia struct {
	fileName    string // nombre visible del adjunto
	storageName string // nombre único en disco, derivado del id del mensaje
	mimeType    string
	contentType string
	size        int64
}

// found informa si el mensaje trae un adjunto reconocido.
func (m incomingMedia) found() bool { return m.fileName != "" }

// describeIncomingMedia inspecciona el media de un mensaje y devuelve su
// metadata junto con la ubicación de descarga de Telegram.
func describeIncomingMedia(msg *tg.Message) (incomingMedia, tg.InputFileLocationClass) {
	if msg == nil || msg.Media == nil {
		return incomingMedia{}, nil
	}

	switch media := msg.Media.(type) {
	case *tg.MessageMediaPhoto:
		if media == nil {
			return incomingMedia{}, nil
		}
		return describeIncomingPhoto(msg.ID, media)
	case *tg.MessageMediaDocument:
		if media == nil {
			return incomingMedia{}, nil
		}
		return describeIncomingDocument(msg.ID, media)
	default:
		// Ubicaciones, encuestas y web pages se siguen tratando como texto.
		return incomingMedia{}, nil
	}
}

// describeIncomingPhoto elige el tamaño más grande de la foto para descargarla.
func describeIncomingPhoto(msgID int, media *tg.MessageMediaPhoto) (incomingMedia, tg.InputFileLocationClass) {
	photo, ok := media.Photo.(*tg.Photo)
	if !ok || photo == nil {
		return incomingMedia{}, nil
	}

	sizeType, size := largestPhotoSize(photo.Sizes)
	if sizeType == "" {
		return incomingMedia{}, nil
	}

	name := fmt.Sprintf("photo_%d.jpg", msgID)
	return incomingMedia{
		fileName:    name,
		storageName: name,
		mimeType:    "image/jpeg",
		contentType: "image",
		size:        int64(size),
	}, &tg.InputPhotoFileLocation{
		ID:            photo.ID,
		AccessHash:    photo.AccessHash,
		FileReference: photo.FileReference,
		ThumbSize:     sizeType,
	}
}

// largestPhotoSize devuelve el tipo de la variante con más píxeles y su tamaño.
func largestPhotoSize(sizes []tg.PhotoSizeClass) (string, int) {
	bestType, bestSize, bestPixels := "", 0, 0

	for _, size := range sizes {
		var (
			sizeType string
			width    int
			height   int
			bytes    int
		)

		switch s := size.(type) {
		case *tg.PhotoSize:
			sizeType, width, height, bytes = s.Type, s.W, s.H, s.Size
		case *tg.PhotoSizeProgressive:
			sizeType, width, height = s.Type, s.W, s.H
			bytes = progressiveSize(s.Sizes)
		default:
			continue
		}

		if pixels := width * height; sizeType != "" && pixels > bestPixels {
			bestType, bestSize, bestPixels = sizeType, bytes, pixels
		}
	}

	return bestType, bestSize
}

// progressiveSize toma el tamaño final de un JPEG progresivo.
func progressiveSize(sizes []int) int {
	if len(sizes) == 0 {
		return 0
	}
	return sizes[len(sizes)-1]
}

// describeIncomingDocument resuelve nombre, MIME y tipo de un documento.
func describeIncomingDocument(msgID int, media *tg.MessageMediaDocument) (incomingMedia, tg.InputFileLocationClass) {
	doc, ok := media.Document.(*tg.Document)
	if !ok || doc == nil {
		return incomingMedia{}, nil
	}

	name, voice := documentDetails(doc)
	mimeType := strings.TrimSpace(doc.MimeType)
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	if name == "" {
		name = documentFallbackName(msgID, mimeType)
	}

	contentType := contentTypeForMime(mimeType)
	if voice || media.Voice {
		contentType = "audio"
	}

	return incomingMedia{
		fileName:    name,
		storageName: storageFileName(msgID, name),
		mimeType:    mimeType,
		contentType: contentType,
		size:        doc.Size,
	}, &tg.InputDocumentFileLocation{
		ID:            doc.ID,
		AccessHash:    doc.AccessHash,
		FileReference: doc.FileReference,
	}
}

// documentDetails extrae el nombre visible y si el audio es una nota de voz.
func documentDetails(doc *tg.Document) (name string, voice bool) {
	for _, attribute := range doc.Attributes {
		switch a := attribute.(type) {
		case *tg.DocumentAttributeFilename:
			if name == "" && strings.TrimSpace(a.FileName) != "" {
				name = filepath.Base(strings.TrimSpace(a.FileName))
			}
		case *tg.DocumentAttributeAudio:
			if a.Voice {
				voice = true
			}
		}
	}
	return name, voice
}

// documentFallbackName arma un nombre con la extensión que corresponde al MIME.
func documentFallbackName(msgID int, mimeType string) string {
	return strconv.Itoa(msgID) + mediaExtension(mimeType)
}

// mediaExtension elige una extensión estable para el MIME: prefiere la que
// coincide con el subtipo (video/mp4 → .mp4) antes que el primer alias.
func mediaExtension(mimeType string) string {
	clean := strings.ToLower(strings.TrimSpace(mimeType))
	if idx := strings.Index(clean, ";"); idx >= 0 {
		clean = strings.TrimSpace(clean[:idx])
	}

	subtype := ""
	if idx := strings.Index(clean, "/"); idx >= 0 {
		subtype = strings.TrimPrefix(clean[idx+1:], "x-")
	}

	exts, err := mime.ExtensionsByType(clean)
	if err != nil || len(exts) == 0 {
		return ".bin"
	}
	for _, ext := range exts {
		if strings.TrimPrefix(ext, ".") == subtype {
			return ext
		}
	}
	return exts[0]
}

// storageFileName garantiza un archivo único por mensaje aunque dos envíos
// compartan el mismo nombre original.
func storageFileName(msgID int, name string) string {
	// Un nombre remoto puede traer separadores de Windows o de Unix: se queda
	// solo con el último componente de cualquiera de los dos.
	base := strings.TrimSpace(name)
	if idx := strings.LastIndexAny(base, `/\`); idx >= 0 {
		base = base[idx+1:]
	}
	base = strings.TrimSpace(filepath.Base(base))
	if base == "" || base == "." || base == ".." || base == string(filepath.Separator) {
		base = "archivo"
	}
	return strconv.Itoa(msgID) + "_" + base
}

// contentTypeForMime clasifica un MIME en el content_type que entiende el core.
func contentTypeForMime(mimeType string) string {
	lower := strings.ToLower(strings.TrimSpace(mimeType))

	switch {
	case strings.HasPrefix(lower, "image/"):
		return "image"
	case strings.HasPrefix(lower, "audio/"):
		return "audio"
	case strings.HasPrefix(lower, "video/"):
		return "video"
	default:
		return "document"
	}
}

// mediaDownloadDir devuelve el directorio compartido de media de Conduit.
func mediaDownloadDir() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		home = "."
	}
	dir := filepath.Join(home, ".local", "share", "conduit", "media")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		log.Printf("⚠️ no se pudo crear el directorio de media %s: %v", dir, err)
	}
	return dir
}

// prepareTelegramAudio verifica si el adjunto de audio está en contenedor WebM u otro
// formato y lo convierte a Ogg Opus con ffmpeg para que Telegram lo reconozca como nota de voz nativa (Voice: true).
func prepareTelegramAudio(filePath, mimeType, fileName string) (string, string, string, func()) {
	lowerMime := strings.ToLower(strings.TrimSpace(mimeType))
	lowerFile := strings.ToLower(strings.TrimSpace(fileName))

	isAudio := strings.HasPrefix(lowerMime, "audio/") || strings.HasSuffix(lowerFile, ".webm") || strings.HasSuffix(lowerFile, ".ogg")
	if !isAudio {
		return "", mimeType, fileName, nil
	}

	if strings.Contains(lowerMime, "ogg") || strings.Contains(lowerMime, "opus") || strings.HasSuffix(lowerFile, ".ogg") {
		return "", mimeType, fileName, nil
	}

	if !strings.Contains(lowerMime, "webm") && !strings.HasSuffix(lowerFile, ".webm") {
		return "", mimeType, fileName, nil
	}

	ffmpegPath, err := exec.LookPath("ffmpeg")
	if err != nil {
		return "", mimeType, fileName, nil
	}

	tempDir, err := os.MkdirTemp("", "conduit-tg-transcode-*")
	if err != nil {
		return "", mimeType, fileName, nil
	}
	cleanup := func() { os.RemoveAll(tempDir) }

	outputPath := filepath.Join(tempDir, "voice.ogg")
	cmd := exec.Command(ffmpegPath, "-y", "-i", filePath, "-vn", "-c:a", "copy", outputPath)
	if err := cmd.Run(); err != nil || !isNonEmptyTelegramFile(outputPath) {
		cmd2 := exec.Command(ffmpegPath, "-y", "-i", filePath, "-vn", "-c:a", "libopus", "-b:a", "64k", outputPath)
		if err2 := cmd2.Run(); err2 != nil || !isNonEmptyTelegramFile(outputPath) {
			cleanup()
			return "", mimeType, fileName, nil
		}
	}

	baseName := strings.TrimSuffix(fileName, filepath.Ext(fileName))
	if baseName == "" {
		baseName = "voice-note"
	}
	return outputPath, "audio/ogg; codecs=opus", baseName + ".ogg", cleanup
}

func isNonEmptyTelegramFile(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && !fi.IsDir() && fi.Size() > 0
}
