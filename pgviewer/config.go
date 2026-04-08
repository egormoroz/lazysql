package main

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Connection holds a single Postgres connection definition.
type Connection struct {
	Name string `yaml:"name"`
	URL  string `yaml:"url"` // postgres://user:pass@host:port/db?sslmode=disable
}

// Config is the top-level configuration file structure.
type Config struct {
	Connections []Connection `yaml:"connections"`
	PageSize    int          `yaml:"page_size"` // rows per page, default 100
	LogFile     string       `yaml:"log_file"`  // log file path, default /tmp/pgviewer.log
}

func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	if cfg.PageSize <= 0 {
		cfg.PageSize = 100
	}
	if len(cfg.Connections) == 0 {
		return nil, fmt.Errorf("config: no connections defined")
	}
	// Expand environment variables in URLs.
	for i := range cfg.Connections {
		cfg.Connections[i].URL = os.ExpandEnv(cfg.Connections[i].URL)
		if cfg.Connections[i].Name == "" {
			cfg.Connections[i].Name = fmt.Sprintf("connection-%d", i+1)
		}
	}
	return &cfg, nil
}
