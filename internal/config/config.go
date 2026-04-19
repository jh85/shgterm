// Package config loads shgterm configuration files.
//
// The on-disk schema intentionally mirrors ShogiHome's CSAGameSettingsForCLI
// (see shogihome/src/common/settings/csa.ts and usi.ts) so YAML or JSON
// exports from ShogiHome's CSA dialog load unchanged.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

type ProtocolVersion string

const (
	ProtocolV121          ProtocolVersion = "v121"
	ProtocolV121Floodgate ProtocolVersion = "v121_floodgate"
)

type RecordFormat string

const (
	RecordFormatKIF  RecordFormat = ".kif"
	RecordFormatKIFU RecordFormat = ".kifu"
	RecordFormatKI2  RecordFormat = ".ki2"
	RecordFormatKI2U RecordFormat = ".ki2u"
	RecordFormatCSA  RecordFormat = ".csa"
	RecordFormatJKF  RecordFormat = ".jkf"
)

// USIOption is a typed USI option value. Its JSON/YAML shape is
// {"type": "<usi option type>", "value": <check|spin|string>}.
type USIOption struct {
	Type  string `yaml:"type" json:"type"`
	Value any    `yaml:"value" json:"value"`
}

type USIEngine struct {
	Name              string               `yaml:"name" json:"name"`
	Path              string               `yaml:"path" json:"path"`
	Options           map[string]USIOption `yaml:"options" json:"options"`
	EnableEarlyPonder bool                 `yaml:"enableEarlyPonder" json:"enableEarlyPonder"`
}

type TCPKeepalive struct {
	InitialDelay int `yaml:"initialDelay" json:"initialDelay"`
}

type BlankLinePing struct {
	InitialDelay int `yaml:"initialDelay" json:"initialDelay"`
	Interval     int `yaml:"interval" json:"interval"`
}

type Server struct {
	ProtocolVersion ProtocolVersion `yaml:"protocolVersion" json:"protocolVersion"`
	Host            string          `yaml:"host" json:"host"`
	Port            int             `yaml:"port" json:"port"`
	ID              string          `yaml:"id" json:"id"`
	Password        string          `yaml:"password" json:"password"`
	TCPKeepalive    TCPKeepalive    `yaml:"tcpKeepalive" json:"tcpKeepalive"`
	BlankLinePing   *BlankLinePing  `yaml:"blankLinePing,omitempty" json:"blankLinePing,omitempty"`
}

// Config is the root configuration. Field names follow ShogiHome's
// CSAGameSettingsForCLI exactly, with two shgterm-only additions for
// multi-server configs: Servers (map) and DefaultServer.
type Config struct {
	USI                    USIEngine    `yaml:"usi" json:"usi"`
	Server                 Server       `yaml:"server" json:"server"`
	Servers                map[string]Server `yaml:"servers,omitempty" json:"servers,omitempty"`
	DefaultServer          string       `yaml:"defaultServer,omitempty" json:"defaultServer,omitempty"`
	Repeat                 int          `yaml:"repeat" json:"repeat"`
	AutoRelogin            bool         `yaml:"autoRelogin" json:"autoRelogin"`
	RestartPlayerEveryGame bool         `yaml:"restartPlayerEveryGame" json:"restartPlayerEveryGame"`
	SaveRecordFile         bool         `yaml:"saveRecordFile" json:"saveRecordFile"`
	EnableComment          bool         `yaml:"enableComment" json:"enableComment"`
	RecordFileNameTemplate string       `yaml:"recordFileNameTemplate,omitempty" json:"recordFileNameTemplate,omitempty"`
	RecordFileFormat       RecordFormat `yaml:"recordFileFormat,omitempty" json:"recordFileFormat,omitempty"`
}

// Load reads a config from path, dispatching on extension (.json vs anything
// else → YAML). After parsing, it resolves the engine path relative to the
// config file when the absolute path does not exist, applies defaults, and
// validates.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}

	cfg := &Config{}
	if strings.EqualFold(filepath.Ext(path), ".json") {
		if err := json.Unmarshal(data, cfg); err != nil {
			return nil, fmt.Errorf("parse json: %w", err)
		}
	} else {
		if err := yaml.Unmarshal(data, cfg); err != nil {
			return nil, fmt.Errorf("parse yaml: %w", err)
		}
	}

	cfg.applyDefaults()
	if err := cfg.resolveEnginePath(path); err != nil {
		return nil, err
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func (c *Config) applyDefaults() {
	applyServerDefaults(&c.Server)
	for name, s := range c.Servers {
		applyServerDefaults(&s)
		c.Servers[name] = s
	}
	if c.Repeat == 0 {
		c.Repeat = 1
	}
	if c.RecordFileFormat == "" {
		c.RecordFileFormat = RecordFormatCSA
	}
	if c.RecordFileNameTemplate == "" {
		c.RecordFileNameTemplate = "{datetime}{_title}{_sente}{_gote}"
	}
}

func applyServerDefaults(s *Server) {
	if s.ProtocolVersion == "" {
		s.ProtocolVersion = ProtocolV121Floodgate
	}
	if s.Port == 0 {
		s.Port = 4081
	}
	if s.TCPKeepalive.InitialDelay == 0 {
		s.TCPKeepalive.InitialDelay = 10
	}
}

func (c *Config) resolveEnginePath(configPath string) error {
	if c.USI.Path == "" {
		return errors.New("usi.path is empty")
	}
	if _, err := os.Stat(c.USI.Path); err == nil {
		return nil
	}
	rel := filepath.Join(filepath.Dir(configPath), c.USI.Path)
	if _, err := os.Stat(rel); err != nil {
		return fmt.Errorf("usi engine not found: %s (also tried %s)", c.USI.Path, rel)
	}
	c.USI.Path = rel
	return nil
}

func (c *Config) Validate() error {
	// Validate whichever server form is in use.
	if len(c.Servers) > 0 {
		for name, s := range c.Servers {
			if err := validateServer(s, "servers["+name+"]"); err != nil {
				return err
			}
		}
		if c.DefaultServer != "" {
			if _, ok := c.Servers[c.DefaultServer]; !ok {
				return fmt.Errorf("defaultServer %q not found in servers", c.DefaultServer)
			}
		}
	} else {
		if err := validateServer(c.Server, "server"); err != nil {
			return err
		}
	}
	if c.Repeat < 1 {
		return fmt.Errorf("repeat must be >= 1 (got %d)", c.Repeat)
	}
	for name, opt := range c.USI.Options {
		switch opt.Type {
		case "check", "spin", "string", "combo", "filename":
		default:
			return fmt.Errorf("usi.options[%q].type unknown: %q", name, opt.Type)
		}
	}
	return nil
}

func validateServer(s Server, label string) error {
	switch s.ProtocolVersion {
	case ProtocolV121, ProtocolV121Floodgate:
	default:
		return fmt.Errorf("%s.protocolVersion must be v121 or v121_floodgate (got %q)", label, s.ProtocolVersion)
	}
	if s.Host == "" {
		return fmt.Errorf("%s.host is empty", label)
	}
	if s.Port <= 0 || s.Port > 65535 {
		return fmt.Errorf("%s.port out of range: %d", label, s.Port)
	}
	if s.ID == "" {
		return fmt.Errorf("%s.id is empty", label)
	}
	if s.TCPKeepalive.InitialDelay <= 0 {
		return fmt.Errorf("%s.tcpKeepalive.initialDelay must be positive", label)
	}
	if bp := s.BlankLinePing; bp != nil {
		if bp.InitialDelay < 30 {
			return fmt.Errorf("%s.blankLinePing.initialDelay must be >= 30", label)
		}
		if bp.Interval < 30 {
			return fmt.Errorf("%s.blankLinePing.interval must be >= 30", label)
		}
	}
	return nil
}

// SelectServer chooses which Server to use for this run. Precedence:
//
//  1. If name is non-empty, it must match a key in c.Servers.
//  2. Otherwise, if c.Servers is populated and c.DefaultServer names a key,
//     that entry is used.
//  3. Otherwise, c.Server (the legacy single-server form) is used.
//
// On success the selected server is copied into c.Server so the rest of
// the program (CLI overrides, the bridge) sees a uniform c.Server field.
// The selected name is returned (empty when the legacy form is used).
func (c *Config) SelectServer(name string) (string, error) {
	if name != "" {
		if len(c.Servers) == 0 {
			return "", fmt.Errorf("--server %q given but config has no 'servers' map", name)
		}
		s, ok := c.Servers[name]
		if !ok {
			return "", fmt.Errorf("server %q not found; available: %s",
				name, strings.Join(c.ServerNames(), ", "))
		}
		c.Server = s
		return name, nil
	}
	if len(c.Servers) > 0 {
		if c.DefaultServer == "" {
			return "", fmt.Errorf("config has 'servers' map but neither --server nor defaultServer is set; available: %s",
				strings.Join(c.ServerNames(), ", "))
		}
		c.Server = c.Servers[c.DefaultServer]
		return c.DefaultServer, nil
	}
	// Legacy single-server form; c.Server is already validated & defaulted.
	return "", nil
}

// ServerNames returns the sorted names in c.Servers. Useful for listing.
func (c *Config) ServerNames() []string {
	names := make([]string, 0, len(c.Servers))
	for k := range c.Servers {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}

// IsFloodgateProtocol reports whether the configured protocol supports the
// Floodgate extensions (per-move eval/PV comments).
func (c *Config) IsFloodgateProtocol() bool {
	return c.Server.ProtocolVersion == ProtocolV121Floodgate
}
