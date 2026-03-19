package config

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

type Source struct {
	Engine string `yaml:"engine"`
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

func (c *Config) Print(host, dbName string, extras ...string) {
	fmt.Println("Parameters")
	fmt.Println("─────────────────────────────────────────────────────")
	fmt.Printf("  Engine:         %s\n", c.Source.Engine)
	fmt.Printf("  Host:           %s\n", host)
	fmt.Printf("  Database:       %s\n", dbName)
	fmt.Printf("  Output dir:     %s\n", c.Output.Directory)
	if len(c.IgnoredTables) > 0 {
		fmt.Printf("  Ignored tables: %s\n", strings.Join(c.IgnoredTables, ", "))
	}
	for i := 0; i+1 < len(extras); i += 2 {
		fmt.Printf("  %-16s%s\n", extras[i]+":", extras[i+1])
	}
	fmt.Println("─────────────────────────────────────────────────────")
	fmt.Println()
}
