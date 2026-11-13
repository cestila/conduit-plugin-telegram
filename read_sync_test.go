package main

import (
	"testing"

	"github.com/gotd/td/tg"
)

func TestReadChannelInboxCarriesServerIdentity(t *testing.T) {
	p := NewTelegramPlugin()
	remoteID := channelRemoteID(777)
	p.rememberPeer(&peerInfo{remoteID: remoteID, kind: kindChannel, id: 777, title: "Comunidad", channelType: "megagroup",
		peer: &tg.InputPeerChannel{ChannelID: 777, AccessHash: 1}})

	p.handleReadChannelInbox(&tg.UpdateReadChannelInbox{ChannelID: 777, MaxID: 90})

	ev := <-p.eventCh
	if ev.GetEventType() != "messages.read" || ev.GetChatJid() != remoteID || ev.GetRemoteMessageId() != "90" {
		t.Fatalf("evento inesperado: %v", ev)
	}
	if ev.GetServerId() != remoteID || ev.GetServerName() != "Comunidad" || ev.GetChannelName() != "Comunidad" || ev.GetChannelType() != "megagroup" {
		t.Fatalf("falta identidad de servidor: %v", ev)
	}
}

func TestReadHistoryInboxBasicGroupCarriesServerIdentity(t *testing.T) {
	p := NewTelegramPlugin()
	p.handleReadHistoryInbox(&tg.UpdateReadHistoryInbox{Peer: &tg.PeerChat{ChatID: 55}, MaxID: 12})

	ev := <-p.eventCh
	if ev.GetServerId() != "g55" || ev.GetChannelType() != "group" || ev.GetChatJid() != "g55" {
		t.Fatalf("grupo básico sin identidad (default group): %v", ev)
	}
}

func TestReadHistoryInboxDMStaysWithoutServer(t *testing.T) {
	p := NewTelegramPlugin()
	p.rememberPeer(&peerInfo{remoteID: "u9", kind: kindUser, id: 9, phone: "+54911", peer: &tg.InputPeerUser{UserID: 9}})
	p.handleReadHistoryInbox(&tg.UpdateReadHistoryInbox{Peer: &tg.PeerUser{UserID: 9}, MaxID: 3})

	ev := <-p.eventCh
	if ev.GetServerId() != "" || ev.GetChannelType() != "" || ev.GetIdentityPhone() != "+54911" || ev.GetChatJid() != "u9" {
		t.Fatalf("DM alterado: %v", ev)
	}
}
