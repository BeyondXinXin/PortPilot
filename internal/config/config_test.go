package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSaveLoad(t *testing.T) {
	base := t.TempDir()
	t.Setenv("LOCALAPPDATA", filepath.Join(base, "LocalAppData"))
	content := filepath.Join(base, "site")
	if err := os.Mkdir(content, 0755); err != nil {
		t.Fatal(err)
	}
	cfg := Defaults()
	cfg.Services = []Service{{Name: "Website", Type: ServiceStatic, Directory: content, Port: 8080, AutoStart: true}}
	if err := Save(base, cfg); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(base)
	if err != nil {
		t.Fatal(err)
	}
	if got := loaded.Services[0].LocalAddress; got != "http://127.0.0.1:8080" {
		t.Fatalf("unexpected local address %q", got)
	}
	if loaded.Services[0].ID == "" {
		t.Fatal("expected generated service ID")
	}
}
