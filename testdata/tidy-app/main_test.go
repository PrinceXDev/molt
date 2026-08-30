package main

import (
	"testing"

	"golang.org/x/net/context"
)

func TestLoadSortsAndDeduplicates(t *testing.T) {
	cfg, err := Load(context.Background(), []string{"b", "a", "a"})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Hosts) != 2 || cfg.Hosts[0] != "a" || cfg.Hosts[1] != "b" {
		t.Errorf("Hosts = %v, want [a b]", cfg.Hosts)
	}
	if !cfg.Knows("a") {
		t.Error("Knows(a) = false")
	}
	if cfg.Knows("z") {
		t.Error("Knows(z) = true")
	}
}

func TestLoadRejectsEmptyHosts(t *testing.T) {
	if _, err := Load(context.Background(), nil); err == nil {
		t.Fatal("Load accepted an empty host list")
	}
}
