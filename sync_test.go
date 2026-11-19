package main

import (
	"context"
	"testing"

	conduitv1 "github.com/cestila/ConduitOmniChat/sdk/conduitv1"
	"github.com/gotd/td/tg"
)

func TestSyncContactsExcludesBotsChannelsAndIncludesPhone(t *testing.T) {
	ctx := context.Background()
	p := NewTelegramPlugin()
	p.accountID = "tg-acc-1"

	// Seed users: 1 regular person with phone, 1 bot, 1 deleted, 1 self.
	p.applyUsers([]tg.UserClass{
		&tg.User{
			ID:        1001,
			FirstName: "Ada",
			LastName:  "Lovelace",
			Username:  "adalovelace",
			Phone:     "5491122334455",
		},
		&tg.User{
			ID:        1002,
			FirstName: "HelperBot",
			Username:  "helper_bot",
			Bot:       true,
		},
		&tg.User{
			ID:        1003,
			FirstName: "Deleted",
			Deleted:   true,
		},
		&tg.User{
			ID:        1004,
			FirstName: "Me",
			Self:      true,
		},
	})

	// Seed a group and a channel in peers cache.
	p.rememberPeer(&peerInfo{
		remoteID: "g2001",
		kind:     kindGroup,
		id:       2001,
		title:    "Dev Group",
		peer:     &tg.InputPeerChat{ChatID: 2001},
	})
	p.rememberPeer(&peerInfo{
		remoteID: "c3001",
		kind:     kindChannel,
		id:       3001,
		title:    "News Channel",
		peer:     &tg.InputPeerChannel{ChannelID: 3001},
	})

	resp, err := p.SyncContacts(ctx, &conduitv1.SyncContactsRequest{
		Provider:  "telegram",
		AccountId: "tg-acc-1",
	})
	if err != nil {
		t.Fatalf("SyncContacts returned error: %v", err)
	}
	if resp.GetError() != "" {
		t.Fatalf("SyncContacts returned response error: %s", resp.GetError())
	}

	if len(resp.GetContacts()) != 1 {
		t.Fatalf("expected 1 contact, got %d: %+v", len(resp.GetContacts()), resp.GetContacts())
	}

	c := resp.GetContacts()[0]
	if c.GetAccountId() != "tg-acc-1" {
		t.Errorf("AccountId = %q, want %q", c.GetAccountId(), "tg-acc-1")
	}
	if c.GetRemoteId() != "u1001" {
		t.Errorf("RemoteId = %q, want %q", c.GetRemoteId(), "u1001")
	}
	if c.GetDisplayName() != "Ada Lovelace" {
		t.Errorf("DisplayName = %q, want %q", c.GetDisplayName(), "Ada Lovelace")
	}
	if c.GetPhone() != "5491122334455" {
		t.Errorf("Phone = %q, want %q", c.GetPhone(), "5491122334455")
	}
	if c.GetHandle() != "adalovelace" {
		t.Errorf("Handle = %q, want %q", c.GetHandle(), "adalovelace")
	}
}

func TestSyncContactsExcludesBotsAndSelfFromRememberEntities(t *testing.T) {
	ctx := context.Background()
	p := NewTelegramPlugin()
	p.accountID = "tg-acc-1"

	p.rememberEntities(&tg.Entities{
		Users: map[int64]*tg.User{
			2001: {
				ID:        2001,
				FirstName: "Grace",
				LastName:  "Hopper",
				Username:  "grace",
				Phone:     "5491133445566",
			},
			2002: {
				ID:        2002,
				FirstName: "SpamBot",
				Username:  "spam_bot",
				Bot:       true,
			},
			2003: {
				ID:        2003,
				FirstName: "SelfUser",
				Username:  "me",
				Self:      true,
			},
		},
	})

	resp, err := p.SyncContacts(ctx, &conduitv1.SyncContactsRequest{
		Provider:  "telegram",
		AccountId: "tg-acc-1",
	})
	if err != nil {
		t.Fatalf("SyncContacts returned error: %v", err)
	}
	if len(resp.GetContacts()) != 1 {
		t.Fatalf("expected 1 contact, got %d: %+v", len(resp.GetContacts()), resp.GetContacts())
	}
	if resp.GetContacts()[0].GetRemoteId() != "u2001" {
		t.Fatalf("expected u2001, got %q", resp.GetContacts()[0].GetRemoteId())
	}
}

func TestSyncContactsDoesNotFallbackToIdentifierAccountID(t *testing.T) {
	ctx := context.Background()
	p := NewTelegramPlugin()
	p.accountID = "default"

	p.applyUsers([]tg.UserClass{
		&tg.User{
			ID:        3001,
			FirstName: "Linus",
			Phone:     "5491144556677",
		},
	})

	resp, err := p.SyncContacts(ctx, &conduitv1.SyncContactsRequest{
		Provider: "telegram",
	})
	if err != nil {
		t.Fatalf("SyncContacts returned error: %v", err)
	}
	if len(resp.GetContacts()) != 1 {
		t.Fatalf("expected 1 contact, got %d", len(resp.GetContacts()))
	}
	if resp.GetContacts()[0].GetAccountId() != "" {
		t.Fatalf("expected empty AccountId when request omits it, got %q", resp.GetContacts()[0].GetAccountId())
	}
}
