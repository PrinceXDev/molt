package main

import (
	"fmt"
	"path/filepath"

	homedir "github.com/mitchellh/go-homedir"
	"github.com/pkg/errors"
	"golang.org/x/exp/slices"
	"golang.org/x/net/context"
)

// Config locates and describes an on-disk configuration.
type Config struct {
	Path  string
	Hosts []string
}

// Load resolves the config path and normalises the host list.
func Load(ctx context.Context, hosts []string) (*Config, error) {
	if len(hosts) == 0 {
		return nil, errors.New("at least one host is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, errors.Errorf("context already done: %v", err)
	}

	dir, err := homedir.Dir()
	if err != nil {
		return nil, errors.Errorf("cannot resolve home directory: %v", err)
	}

	slices.Sort(hosts)
	hosts = slices.Compact(hosts)

	return &Config{
		Path:  filepath.Join(dir, ".tidy", "config.json"),
		Hosts: hosts,
	}, nil
}

// Knows reports whether a host is configured.
func (c *Config) Knows(host string) bool {
	return slices.Contains(c.Hosts, host)
}

func main() {
	cfg, err := Load(context.Background(), []string{"b.example", "a.example", "a.example"})
	if err != nil {
		fmt.Println("error:", err)
		return
	}
	fmt.Println(cfg.Path, cfg.Hosts, cfg.Knows("a.example"))
}
