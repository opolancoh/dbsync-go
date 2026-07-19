package config

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

type Source struct {
	Engine string `yaml:"engine"`
	// Schemas selects which schemas to migrate, in migration order. Empty means
	// every non-system schema, alphabetically.
	Schemas []string `yaml:"schemas"`
}

type Output struct {
	Directory string `yaml:"directory"`
}

type Config struct {
	Source        Source   `yaml:"source"`
	Output        Output   `yaml:"output"`
	IgnoredTables []string `yaml:"ignored_tables"`
	SourceConn    string
	TargetConn    string
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config file: %w", err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing config file: %w", err)
	}

	if cfg.Source.Engine == "" {
		return nil, fmt.Errorf("source.engine is required in config file")
	}
	if cfg.Output.Directory == "" {
		return nil, fmt.Errorf("output.directory is required in config file")
	}

	cfg.SourceConn = os.Getenv("DBSYNC_SOURCE_CONN")
	cfg.TargetConn = os.Getenv("DBSYNC_TARGET_CONN")

	return &cfg, nil
}

func (c *Config) Validate(requireSource, requireTarget bool) error {
	var errs []string
	if requireSource && c.SourceConn == "" {
		errs = append(errs, "DBSYNC_SOURCE_CONN environment variable is not set")
	}
	if requireTarget && c.TargetConn == "" {
		errs = append(errs, "DBSYNC_TARGET_CONN environment variable is not set")
	}
	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "\n"))
	}
	return nil
}

