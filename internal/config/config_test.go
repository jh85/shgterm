package config

import (
	"os"
	"path/filepath"
	"testing"
)

func writeTempEngine(t *testing.T, dir string) string {
	t.Helper()
	p := filepath.Join(dir, "fake-engine")
	if err := os.WriteFile(p, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadYAMLWithRelativeEnginePath(t *testing.T) {
	dir := t.TempDir()
	writeTempEngine(t, dir)
	yaml := `
usi:
  name: test
  path: ./fake-engine
  options:
    USI_Hash: {type: spin, value: 1024}
    USI_Ponder: {type: check, value: true}
server:
  protocolVersion: v121_floodgate
  host: 127.0.0.1
  port: 4081
  id: testid
  password: testpass
  tcpKeepalive: {initialDelay: 10}
repeat: 3
autoRelogin: true
restartPlayerEveryGame: false
saveRecordFile: true
enableComment: true
`
	cfgPath := filepath.Join(dir, "c.yaml")
	if err := os.WriteFile(cfgPath, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Server.Host != "127.0.0.1" || cfg.Server.Port != 4081 {
		t.Fatalf("server mismatch: %+v", cfg.Server)
	}
	if cfg.Repeat != 3 {
		t.Fatalf("repeat=%d want 3", cfg.Repeat)
	}
	if !filepath.IsAbs(cfg.USI.Path) {
		t.Fatalf("engine path not resolved to absolute: %s", cfg.USI.Path)
	}
	if !cfg.IsFloodgateProtocol() {
		t.Fatal("expected floodgate protocol")
	}
}

func TestLoadJSON(t *testing.T) {
	dir := t.TempDir()
	writeTempEngine(t, dir)
	json := `{
	  "usi": {"name": "e", "path": "./fake-engine", "options": {}, "enableEarlyPonder": false},
	  "server": {"protocolVersion": "v121", "host": "h", "port": 4081, "id": "i", "password": "p", "tcpKeepalive": {"initialDelay": 10}},
	  "repeat": 1, "autoRelogin": true, "restartPlayerEveryGame": false, "saveRecordFile": false, "enableComment": false
	}`
	cfgPath := filepath.Join(dir, "c.json")
	if err := os.WriteFile(cfgPath, []byte(json), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Server.ProtocolVersion != ProtocolV121 {
		t.Fatalf("protocol mismatch: %s", cfg.Server.ProtocolVersion)
	}
}

func TestValidateAcceptsRepeatMinusOne(t *testing.T) {
	c := &Config{
		USI:    USIEngine{Path: "/bin/sh"},
		Server: Server{ProtocolVersion: ProtocolV121, Host: "h", Port: 4081, ID: "i", TCPKeepalive: TCPKeepalive{InitialDelay: 10}},
		Repeat: -1,
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("repeat=-1 should be valid: %v", err)
	}
}

func TestValidateRejectsRepeatZero(t *testing.T) {
	c := &Config{
		USI:    USIEngine{Path: "/bin/sh"},
		Server: Server{ProtocolVersion: ProtocolV121, Host: "h", Port: 4081, ID: "i", TCPKeepalive: TCPKeepalive{InitialDelay: 10}},
		Repeat: 0,
	}
	// repeat=0 should only appear before applyDefaults; validation requires
	// either >=1 or -1.
	if err := c.Validate(); err == nil {
		t.Fatal("repeat=0 should fail validation (must go through defaults first)")
	}
}

func TestValidateRejectsBadProtocol(t *testing.T) {
	c := &Config{
		USI:    USIEngine{Path: "/bin/sh"},
		Server: Server{ProtocolVersion: "v999", Host: "h", Port: 4081, ID: "i", TCPKeepalive: TCPKeepalive{InitialDelay: 10}},
		Repeat: 1,
	}
	if err := c.Validate(); err == nil {
		t.Fatal("expected error for bad protocolVersion")
	}
}

func TestValidateBlankLinePingFloor(t *testing.T) {
	c := &Config{
		USI:    USIEngine{Path: "/bin/sh"},
		Server: Server{ProtocolVersion: ProtocolV121, Host: "h", Port: 4081, ID: "i", TCPKeepalive: TCPKeepalive{InitialDelay: 10}, BlankLinePing: &BlankLinePing{InitialDelay: 20, Interval: 40}},
		Repeat: 1,
	}
	if err := c.Validate(); err == nil {
		t.Fatal("expected error for blankLinePing.initialDelay < 30")
	}
}

func TestLoadMultiServer(t *testing.T) {
	dir := t.TempDir()
	writeTempEngine(t, dir)
	yaml := `
usi:
  name: test
  path: ./fake-engine
  options: {}
servers:
  floodgate:
    protocolVersion: v121_floodgate
    host: wdoor.c.u-tokyo.ac.jp
    port: 4081
    id: alice
    password: secret
    tcpKeepalive: {initialDelay: 10}
    blankLinePing: {initialDelay: 40, interval: 40}
  local:
    protocolVersion: v121
    host: 127.0.0.1
    port: 4081
    id: test
    password: pw
    tcpKeepalive: {initialDelay: 10}
defaultServer: floodgate
repeat: 1
autoRelogin: false
restartPlayerEveryGame: false
saveRecordFile: false
enableComment: false
`
	cfgPath := filepath.Join(dir, "c.yaml")
	if err := os.WriteFile(cfgPath, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Servers) != 2 {
		t.Fatalf("servers size = %d, want 2", len(cfg.Servers))
	}

	// Explicit --server local wins.
	selected, err := cfg.SelectServer("local")
	if err != nil {
		t.Fatal(err)
	}
	if selected != "local" {
		t.Fatalf("selected = %q", selected)
	}
	if cfg.Server.Host != "127.0.0.1" {
		t.Fatalf("server.Host = %q", cfg.Server.Host)
	}

	// Reload for next scenario.
	cfg, err = Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	// No flag: defaultServer (floodgate).
	selected, err = cfg.SelectServer("")
	if err != nil {
		t.Fatal(err)
	}
	if selected != "floodgate" {
		t.Fatalf("default selected = %q", selected)
	}
	if cfg.Server.Host != "wdoor.c.u-tokyo.ac.jp" {
		t.Fatalf("default host mismatch: %q", cfg.Server.Host)
	}
}

func TestSelectServerUnknownName(t *testing.T) {
	dir := t.TempDir()
	writeTempEngine(t, dir)
	yaml := `
usi: {name: t, path: ./fake-engine, options: {}}
servers:
  only:
    protocolVersion: v121
    host: h
    port: 4081
    id: i
    password: p
    tcpKeepalive: {initialDelay: 10}
repeat: 1
autoRelogin: false
restartPlayerEveryGame: false
saveRecordFile: false
enableComment: false
`
	cfgPath := filepath.Join(dir, "c.yaml")
	if err := os.WriteFile(cfgPath, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cfg.SelectServer("bogus"); err == nil {
		t.Fatal("expected error for unknown server name")
	}
	// Also: no --server and no defaultServer → error telling the user to pick.
	if _, err := cfg.SelectServer(""); err == nil {
		t.Fatal("expected error when neither flag nor defaultServer is set")
	}
}

func TestLegacySingleServerStillLoads(t *testing.T) {
	dir := t.TempDir()
	writeTempEngine(t, dir)
	yaml := `
usi: {name: t, path: ./fake-engine, options: {}}
server:
  protocolVersion: v121
  host: h
  port: 4081
  id: i
  password: p
  tcpKeepalive: {initialDelay: 10}
repeat: 1
autoRelogin: false
restartPlayerEveryGame: false
saveRecordFile: false
enableComment: false
`
	cfgPath := filepath.Join(dir, "c.yaml")
	if err := os.WriteFile(cfgPath, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	selected, err := cfg.SelectServer("")
	if err != nil {
		t.Fatal(err)
	}
	if selected != "" {
		t.Fatalf("legacy: selected name should be empty, got %q", selected)
	}
	if cfg.Server.Host != "h" {
		t.Fatalf("host: %q", cfg.Server.Host)
	}
}
