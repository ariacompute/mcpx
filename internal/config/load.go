package config

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// ErrNoUpstreams is returned when the file describes a gateway with nothing
// to serve. It is a startup failure, not an empty catalogue.
var ErrNoUpstreams = errors.New("config: no upstreams configured")

// Load reads, decodes and defaults one configuration file. Validation is
// separate so `check` can report the file it parsed before judging it.
func Load(path string) (*Config, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("config: %s: %w", path, err)
	}
	defer func() { _ = file.Close() }()

	cfg, err := Decode(file)
	if err != nil {
		return nil, fmt.Errorf("config: %s: %w", path, err)
	}
	return cfg, nil
}

// Decode parses YAML or JSON from a reader. Unknown keys are rejected: a typo
// in `allow_prefixes` must not silently produce an unrestricted gateway.
func Decode(r io.Reader) (*Config, error) {
	decoder := yaml.NewDecoder(r)
	decoder.KnownFields(true)
	cfg := &Config{}
	if err := decoder.Decode(cfg); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, errors.New("config: file is empty")
		}
		return nil, fmt.Errorf("config: decode: %w", err)
	}
	cfg.ApplyDefaults()
	return cfg, nil
}

// ApplyDefaults fills every optional field. It is idempotent so callers can
// build a Config in code and normalise it the same way.
func (c *Config) ApplyDefaults() {
	if strings.TrimSpace(c.Server.Listen) == "" {
		c.Server.Listen = DefaultListen
	}
	if strings.TrimSpace(c.Server.Path) == "" {
		c.Server.Path = DefaultPath
	}
	c.Server.Path = "/" + strings.Trim(strings.TrimSpace(c.Server.Path), "/")
	if len(c.Server.ProtocolVersions) == 0 {
		c.Server.ProtocolVersions = append([]string(nil), DefaultProtocolVersions...)
	}
	if c.Server.MaxRequestBytes <= 0 {
		c.Server.MaxRequestBytes = DefaultMaxRequestBytes
	}
	if c.Server.SessionTTL <= 0 {
		c.Server.SessionTTL = Duration(DefaultSessionTTL)
	}
	if c.Server.ShutdownGrace <= 0 {
		c.Server.ShutdownGrace = Duration(DefaultShutdownGrace)
	}
	if c.Server.Auth != nil && c.Server.Auth.Scheme == "" {
		c.Server.Auth.Scheme = SchemeNone
	}
	if c.Defaults.Timeout <= 0 {
		c.Defaults.Timeout = Duration(DefaultTimeout)
	}
	if c.Defaults.MaxResponseBytes <= 0 {
		c.Defaults.MaxResponseBytes = DefaultMaxResponseBytes
	}
	if c.Defaults.MaxBodyBytes <= 0 {
		c.Defaults.MaxBodyBytes = DefaultMaxBodyBytes
	}

	for i := range c.Upstreams {
		u := &c.Upstreams[i]
		u.Name = strings.TrimSpace(u.Name)
		u.BaseURL = strings.TrimRight(strings.TrimSpace(u.BaseURL), "/")
		if u.Timeout <= 0 {
			u.Timeout = c.Defaults.Timeout
		}
		if u.MaxResponseBytes <= 0 {
			u.MaxResponseBytes = c.Defaults.MaxResponseBytes
		}
		if u.Auth == nil {
			u.Auth = &Auth{Scheme: SchemeNone}
		}
		if u.Auth.Scheme == "" {
			u.Auth.Scheme = SchemeNone
		}
		if u.Passthrough != nil && u.Passthrough.Enabled {
			if strings.TrimSpace(u.Passthrough.Name) == "" {
				u.Passthrough.Name = u.Name + "_request"
			}
			if len(u.Passthrough.Methods) == 0 {
				u.Passthrough.Methods = []string{"GET"}
			}
			for j := range u.Passthrough.Methods {
				u.Passthrough.Methods[j] = strings.ToUpper(strings.TrimSpace(u.Passthrough.Methods[j]))
			}
			if len(u.Passthrough.AllowPrefixes) == 0 {
				u.Passthrough.AllowPrefixes = append([]string(nil), u.AllowPrefixes...)
			}
		}
		for j := range u.Operations {
			op := &u.Operations[j]
			op.Name = strings.TrimSpace(op.Name)
			if strings.TrimSpace(op.Method) == "" {
				op.Method = "GET"
			}
			op.Method = strings.ToUpper(strings.TrimSpace(op.Method))
			for k := range op.Params {
				p := &op.Params[k]
				p.Name = strings.TrimSpace(p.Name)
				if strings.TrimSpace(p.In) == "" {
					p.In = ParamInQuery
				}
				p.In = strings.ToLower(strings.TrimSpace(p.In))
			}
			if op.BodyParam == "" {
				for _, p := range op.Params {
					if p.In == ParamInBody {
						op.BodyParam = p.Name
						break
					}
				}
			}
		}
	}
}
