package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	conduitv1 "github.com/cestila/ConduitOmniChat/sdk/conduitv1"
)

func TestMediaRefRoundTrip(t *testing.T) {
	for _, peer := range []string{"u2", "g15", "c900"} {
		raw := encodeMediaRef(peer, 55)
		ref, err := parseMediaRef(raw)
		if err != nil {
			t.Fatalf("parseMediaRef(%q): %v", raw, err)
		}
		if ref.Peer != peer || ref.MsgID != 55 {
			t.Fatalf("ref = %+v, se esperaba {%s 55}", ref, peer)
		}
	}
}

func TestParseMediaRefRejectsInvalid(t *testing.T) {
	for _, raw := range []string{"", "no es json", "{}", `{"peer":"u1"}`, `{"msg_id":3}`, `{"peer":"u1","msg_id":0}`, `{"peer":"u1","msg_id":-4}`} {
		if ref, err := parseMediaRef(raw); err == nil {
			t.Fatalf("parseMediaRef(%q) = %+v, se esperaba error", raw, ref)
		}
	}
}

func TestDownloadMediaInvalidRefFails(t *testing.T) {
	p := NewTelegramPlugin()
	resp, err := p.DownloadMedia(context.Background(), &conduitv1.PluginDownloadMediaRequest{RemoteRef: "basura"})
	if err != nil {
		t.Fatalf("un ref inválido responde success=false, no error: %v", err)
	}
	if resp.GetSuccess() || resp.GetErrorMessage() == "" || resp.GetLocalPath() != "" || resp.GetFileSize() != 0 {
		t.Fatalf("respuesta inesperada: %+v", resp)
	}
}

func TestDownloadMediaWithoutClientFails(t *testing.T) {
	p := NewTelegramPlugin()
	resp, err := p.DownloadMedia(context.Background(), &conduitv1.PluginDownloadMediaRequest{RemoteRef: encodeMediaRef("u2", 55)})
	if err != nil {
		t.Fatalf("sin conexión responde success=false, no error: %v", err)
	}
	if resp.GetSuccess() || resp.GetErrorMessage() == "" || resp.GetLocalPath() != "" {
		t.Fatalf("respuesta inesperada: %+v", resp)
	}
}

func TestSafeMediaPathStaysInsideDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "media")
	cases := map[string]string{
		"../../.bashrc": "7_.bashrc",
		"a/b.pdf":       "7_b.pdf",
		`a\b.pdf`:       "7_b.pdf",
		"":              "7_archivo",
		"..":            "7_archivo",
		".":             "7_archivo",
		"/":             "7_archivo",
		"informe.pdf":   "7_informe.pdf",
	}
	for name, want := range cases {
		got := safeMediaPath(dir, 7, name)
		if filepath.Dir(got) != dir {
			t.Fatalf("safeMediaPath(%q) = %q, debe quedar directamente en %q", name, got, dir)
		}
		if filepath.Base(got) != want {
			t.Fatalf("safeMediaPath(%q) base = %q, se esperaba %q", name, filepath.Base(got), want)
		}
		if strings.Contains(filepath.Base(got), "..") && want != "7_.bashrc" {
			t.Fatalf("nombre con '..': %q", got)
		}
	}
}
