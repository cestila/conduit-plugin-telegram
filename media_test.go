package main

import (
	"testing"

	conduitv1 "github.com/cestila/ConduitOmniChat/sdk/conduitv1"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
)

func TestValidateOutgoingAcceptsEmptyBodyWithAttachments(t *testing.T) {
	atts := []*conduitv1.OutgoingAttachment{{FilePath: "/tmp/foto.png", MimeType: "image/png"}}

	body, err := validateOutgoing("   ", atts)
	if err != nil {
		t.Fatalf("un mensaje con adjuntos puede ir sin texto: %v", err)
	}
	if body != "" {
		t.Fatalf("body = %q, se esperaba vacío", body)
	}
}

func TestValidateOutgoingRejectsMessageWithoutTextOrAttachments(t *testing.T) {
	_, err := validateOutgoing("  ", nil)
	if err == nil {
		t.Fatal("se esperaba error para un mensaje sin texto ni adjuntos")
	}
	if !contains(err.Error(), "texto") || !contains(err.Error(), "adjunt") {
		t.Fatalf("el error debe ser accionable, got %q", err.Error())
	}
}

func TestValidateOutgoingKeepsTextOnlyMessages(t *testing.T) {
	body, err := validateOutgoing("  hola  ", nil)
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if body != "hola" {
		t.Fatalf("body = %q, se esperaba hola", body)
	}
}

func TestBuildInputMediaForImageUsesPhoto(t *testing.T) {
	file := &tg.InputFile{ID: 1, Parts: 1, Name: "foto.png"}

	media := buildInputMedia(file, "image/png", "foto.png")
	photo, ok := media.(*tg.InputMediaUploadedPhoto)
	if !ok {
		t.Fatalf("media = %T, se esperaba *tg.InputMediaUploadedPhoto", media)
	}
	if photo.File != file {
		t.Fatalf("el archivo subido no se propagó a la foto")
	}
}

func TestBuildInputMediaForGIFKeepsAnimation(t *testing.T) {
	file := &tg.InputFile{ID: 2, Parts: 1, Name: "loop.gif"}

	media := buildInputMedia(file, "image/gif", "loop.gif")
	doc := requireUploadedDocument(t, media)
	if doc.MimeType != "image/gif" {
		t.Fatalf("mime = %q", doc.MimeType)
	}
	if _, ok := attributeOf[*tg.DocumentAttributeAnimated](doc.Attributes); !ok {
		t.Fatal("un GIF debe conservar DocumentAttributeAnimated")
	}
	requireFilenameAttribute(t, doc.Attributes, "loop.gif")
}

func TestBuildInputMediaForVideoStreams(t *testing.T) {
	file := &tg.InputFile{ID: 3, Parts: 1, Name: "clip.mp4"}

	media := buildInputMedia(file, "video/mp4", "clip.mp4")
	doc := requireUploadedDocument(t, media)
	video, ok := attributeOf[*tg.DocumentAttributeVideo](doc.Attributes)
	if !ok {
		t.Fatal("un video debe llevar DocumentAttributeVideo")
	}
	if !video.SupportsStreaming {
		t.Fatal("el video debe permitir streaming")
	}
	requireFilenameAttribute(t, doc.Attributes, "clip.mp4")
}

func TestBuildInputMediaForAudioNamesTrack(t *testing.T) {
	file := &tg.InputFile{ID: 4, Parts: 1, Name: "nota.ogg"}

	media := buildInputMedia(file, "audio/ogg", "nota.ogg")
	doc := requireUploadedDocument(t, media)
	audio, ok := attributeOf[*tg.DocumentAttributeAudio](doc.Attributes)
	if !ok {
		t.Fatal("un audio debe llevar DocumentAttributeAudio")
	}
	if audio.Title != "nota.ogg" {
		t.Fatalf("title = %q, se esperaba nota.ogg", audio.Title)
	}
	if !audio.Voice {
		t.Fatal("audio/ogg debe tener Voice = true")
	}
	requireFilenameAttribute(t, doc.Attributes, "nota.ogg")

	// audio/ogg; codecs=opus también debe marcarse como nota de voz
	opusMedia := buildInputMedia(file, "audio/ogg; codecs=opus", "nota.ogg")
	opusDoc := requireUploadedDocument(t, opusMedia)
	opusAudio, ok := attributeOf[*tg.DocumentAttributeAudio](opusDoc.Attributes)
	if !ok || !opusAudio.Voice {
		t.Fatal("audio/ogg; codecs=opus debe tener Voice = true")
	}

	// audio/webm sin convertir es un audio regular para que Telegram no falle
	webmMedia := buildInputMedia(file, "audio/webm; codecs=opus", "nota.webm")
	webmDoc := requireUploadedDocument(t, webmMedia)
	webmAudio, ok := attributeOf[*tg.DocumentAttributeAudio](webmDoc.Attributes)
	if !ok {
		t.Fatal("audio/webm debe llevar DocumentAttributeAudio")
	}
	if webmAudio.Voice {
		t.Fatal("audio/webm directo no debe tener Voice = true antes de convertirse a ogg")
	}

	// audio/mpeg (música / archivo regular) no debe marcarse como nota de voz
	mp3Media := buildInputMedia(file, "audio/mpeg", "cancion.mp3")
	mp3Doc := requireUploadedDocument(t, mp3Media)
	mp3Audio, ok := attributeOf[*tg.DocumentAttributeAudio](mp3Doc.Attributes)
	if !ok {
		t.Fatal("audio/mpeg debe llevar DocumentAttributeAudio")
	}
	if mp3Audio.Voice {
		t.Fatal("audio/mpeg NO debe tener Voice = true")
	}
}

func TestBuildInputMediaForDocumentForcesFile(t *testing.T) {
	file := &tg.InputFile{ID: 5, Parts: 1, Name: "informe.pdf"}

	media := buildInputMedia(file, "application/pdf", "informe.pdf")
	doc := requireUploadedDocument(t, media)
	if !doc.ForceFile {
		t.Fatal("un documento genérico debe forzar ForceFile")
	}
	requireFilenameAttribute(t, doc.Attributes, "informe.pdf")
}

func TestDescribeIncomingPhotoPicksLargestSize(t *testing.T) {
	msg := mediaMessage(77, &tg.MessageMediaPhoto{Photo: &tg.Photo{
		ID:            9,
		AccessHash:    10,
		FileReference: []byte{1, 2},
		Sizes: []tg.PhotoSizeClass{
			&tg.PhotoSize{Type: "s", W: 90, H: 90, Size: 1200},
			&tg.PhotoSize{Type: "x", W: 1280, H: 720, Size: 98000},
			&tg.PhotoStrippedSize{Type: "i", Bytes: []byte{3}},
		},
	}})

	media, location := describeIncomingMedia(msg)
	if media.fileName != "photo_77.jpg" {
		t.Fatalf("file_name = %q, se esperaba photo_77.jpg", media.fileName)
	}
	if media.mimeType != "image/jpeg" || media.contentType != "image" {
		t.Fatalf("mime/content_type = %q/%q", media.mimeType, media.contentType)
	}
	if media.size != 98000 {
		t.Fatalf("file_size = %d, se esperaba 98000", media.size)
	}

	photo, ok := location.(*tg.InputPhotoFileLocation)
	if !ok {
		t.Fatalf("location = %T, se esperaba *tg.InputPhotoFileLocation", location)
	}
	if photo.ThumbSize != "x" {
		t.Fatalf("thumb_size = %q, se esperaba el tamaño más grande", photo.ThumbSize)
	}
	if photo.ID != 9 || photo.AccessHash != 10 {
		t.Fatalf("la location perdió la identidad de la foto: %+v", photo)
	}
}

func TestDescribeIncomingDocumentUsesFilenameAndMime(t *testing.T) {
	msg := mediaMessage(88, &tg.MessageMediaDocument{Document: &tg.Document{
		ID:            3,
		AccessHash:    4,
		FileReference: []byte{5},
		MimeType:      "application/pdf",
		Size:          2048,
		Attributes: []tg.DocumentAttributeClass{
			&tg.DocumentAttributeFilename{FileName: "informe.pdf"},
		},
	}})

	media, location := describeIncomingMedia(msg)
	if media.fileName != "informe.pdf" || media.contentType != "document" {
		t.Fatalf("fileName/content_type = %q/%q", media.fileName, media.contentType)
	}
	if media.size != 2048 {
		t.Fatalf("file_size = %d", media.size)
	}
	if media.storageName != "88_informe.pdf" {
		t.Fatalf("storage name = %q, debe incluir el id del mensaje", media.storageName)
	}
	doc, ok := location.(*tg.InputDocumentFileLocation)
	if !ok {
		t.Fatalf("location = %T, se esperaba *tg.InputDocumentFileLocation", location)
	}
	if doc.ID != 3 || doc.AccessHash != 4 {
		t.Fatalf("la location perdió la identidad del documento: %+v", doc)
	}
}

func TestDescribeIncomingDocumentFallsBackToMessageIDAndExtension(t *testing.T) {
	msg := mediaMessage(99, &tg.MessageMediaDocument{Document: &tg.Document{
		ID:       6,
		MimeType: "video/mp4",
		Attributes: []tg.DocumentAttributeClass{
			&tg.DocumentAttributeVideo{SupportsStreaming: true},
		},
	}})

	media, _ := describeIncomingMedia(msg)
	if media.fileName != "99.mp4" {
		t.Fatalf("file_name = %q, se esperaba 99.mp4", media.fileName)
	}
	if media.contentType != "video" {
		t.Fatalf("content_type = %q, se esperaba video", media.contentType)
	}
}

func TestDescribeIncomingVoiceIsAudio(t *testing.T) {
	msg := mediaMessage(123, &tg.MessageMediaDocument{
		Voice: true,
		Document: &tg.Document{
			ID:       7,
			MimeType: "audio/ogg",
			Attributes: []tg.DocumentAttributeClass{
				&tg.DocumentAttributeAudio{Voice: true},
			},
		},
	})

	media, _ := describeIncomingMedia(msg)
	if media.contentType != "audio" {
		t.Fatalf("content_type = %q, se esperaba audio", media.contentType)
	}
}

func TestDescribeIncomingIgnoresUnsupportedMedia(t *testing.T) {
	msg := mediaMessage(1, &tg.MessageMediaGeo{})
	media, location := describeIncomingMedia(msg)
	if media.found() || location != nil {
		t.Fatalf("una ubicación no debe convertirse en adjunto: %+v", media)
	}
}

func TestMessageEventEmitsPhotoAttachmentWithoutNetwork(t *testing.T) {
	p := NewTelegramPlugin()
	msg := mediaMessage(55, &tg.MessageMediaPhoto{Photo: &tg.Photo{
		ID:            11,
		AccessHash:    12,
		FileReference: []byte{13},
		Sizes:         []tg.PhotoSizeClass{&tg.PhotoSize{Type: "y", W: 800, H: 600, Size: 4321}},
	}})
	msg.PeerID = &tg.PeerUser{UserID: 2}
	msg.FromID = &tg.PeerUser{UserID: 2}

	event := p.messageEvent(msg, userRemoteID(2), kindUser)
	if event == nil {
		t.Fatal("una foto sin texto debe emitir evento")
	}
	if event.GetContentType() != "image" {
		t.Fatalf("content_type = %q, se esperaba image", event.GetContentType())
	}
	atts := event.GetAttachments()
	if len(atts) != 1 {
		t.Fatalf("attachments = %d, se esperaba 1", len(atts))
	}
	att := atts[0]
	if att.GetId() != "att-55" || att.GetFileName() != "photo_55.jpg" || att.GetMimeType() != "image/jpeg" {
		t.Fatalf("adjunto inesperado: %+v", att)
	}
	if att.GetDownloadStatus() != "remote_only" || att.GetLocalPath() != "" {
		t.Fatalf("el adjunto no se baja al recibir: %+v", att)
	}
	if att.GetFileSize() != 4321 {
		t.Fatalf("file_size = %d, se esperaba 4321", att.GetFileSize())
	}
	ref, err := parseMediaRef(att.GetRemoteRef())
	if err != nil || ref.Peer != userRemoteID(2) || ref.MsgID != 55 {
		t.Fatalf("remote_ref inválido: %q (%+v, %v)", att.GetRemoteRef(), ref, err)
	}
}

func TestMessageEventKeepsCaptionForMedia(t *testing.T) {
	p := NewTelegramPlugin()
	msg := mediaMessage(56, &tg.MessageMediaPhoto{Photo: &tg.Photo{
		Sizes: []tg.PhotoSizeClass{&tg.PhotoSize{Type: "y", W: 10, H: 10, Size: 1}},
	}})
	msg.Message = "mirá esto"

	event := p.messageEvent(msg, userRemoteID(2), kindUser)
	if event == nil || event.GetBody() != "mirá esto" {
		t.Fatalf("el caption debe conservarse: %+v", event)
	}
	if event.GetContentType() != "image" {
		t.Fatalf("content_type = %q, se esperaba image", event.GetContentType())
	}
}

func TestMessageEventKeepsUnsupportedMediaAsText(t *testing.T) {
	p := NewTelegramPlugin()
	msg := mediaMessage(57, &tg.MessageMediaGeo{})
	msg.Message = "mi ubicación"

	event := p.messageEvent(msg, userRemoteID(2), kindUser)
	if event == nil {
		t.Fatal("no se emitió el mensaje de texto con ubicación")
	}
	if event.GetContentType() != "text" || len(event.GetAttachments()) != 0 {
		t.Fatalf("la ubicación debe quedar como texto: %+v", event)
	}
}

func mediaMessage(id int, media tg.MessageMediaClass) *tg.Message {
	return &tg.Message{ID: id, Media: media, Date: 1700000000}
}

func requireUploadedDocument(t *testing.T, media tg.InputMediaClass) *tg.InputMediaUploadedDocument {
	t.Helper()
	doc, ok := media.(*tg.InputMediaUploadedDocument)
	if !ok {
		t.Fatalf("media = %T, se esperaba *tg.InputMediaUploadedDocument", media)
	}
	return doc
}

func requireFilenameAttribute(t *testing.T, attrs []tg.DocumentAttributeClass, name string) {
	t.Helper()
	filename, ok := attributeOf[*tg.DocumentAttributeFilename](attrs)
	if !ok {
		t.Fatal("falta DocumentAttributeFilename")
	}
	if filename.FileName != name {
		t.Fatalf("file_name = %q, se esperaba %q", filename.FileName, name)
	}
}

// attributeOf busca un atributo concreto dentro de la lista TL.
func attributeOf[T tg.DocumentAttributeClass](attrs []tg.DocumentAttributeClass) (T, bool) {
	for _, attr := range attrs {
		if match, ok := attr.(T); ok {
			return match, true
		}
	}
	var zero T
	return zero, false
}

// El fallback a documento plano solo debe dispararse ante rechazos de formato,
// no ante errores de permisos o de red.
func TestIsMediaRejected(t *testing.T) {
	if !isMediaRejected(tgerr.New(400, "MEDIA_INVALID")) {
		t.Fatal("MEDIA_INVALID debería disparar el fallback")
	}
	if !isMediaRejected(tgerr.New(400, "VIDEO_CONTENT_TYPE_INVALID")) {
		t.Fatal("VIDEO_CONTENT_TYPE_INVALID debería disparar el fallback")
	}
	if isMediaRejected(tgerr.New(403, "CHAT_WRITE_FORBIDDEN")) {
		t.Fatal("un error de permisos no debe reintentarse como documento")
	}
	if isMediaRejected(nil) {
		t.Fatal("nil no es un rechazo de media")
	}
}
