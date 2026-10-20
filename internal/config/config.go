package config

import (
	"encoding/json"
	"example.com/exacc-log-shipper/internal/model"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type Storage struct {
	Backend    string `json:"backend"` // filesystem or s3
	RootDir    string `json:"root_dir"`
	Endpoint   string `json:"endpoint"`
	Bucket     string `json:"bucket"`
	Region     string `json:"region"`
	CACertFile string `json:"ca_cert_file"`
}
type Source struct {
	ID   string `json:"id"`
	Path string `json:"path"`
}
type Collector struct {
	Identity       model.Identity `json:"identity"`
	StateDir       string         `json:"state_dir"`
	SpoolDir       string         `json:"spool_dir"`
	BatchMaxBytes  int64          `json:"batch_max_bytes"`
	SpoolMaxBytes  int64          `json:"spool_max_bytes"`
	Sources        []Source       `json:"sources"`
	Storage        Storage        `json:"storage"`
	RedactPatterns []string       `json:"redact_patterns"`
}
type Receiver struct {
	URL        string `json:"url"`
	CACertFile string `json:"ca_cert_file"`
}
type Shipper struct {
	StateDir             string   `json:"state_dir"`
	PollSeconds          int      `json:"poll_seconds"`
	MaxUncompressedBytes int64    `json:"max_uncompressed_bytes"`
	AllowedOrganizations []string `json:"allowed_organizations"`
	Storage              Storage  `json:"storage"`
	Receiver             Receiver `json:"receiver"`
}
type Server struct {
	Listen      string `json:"listen"`
	DataDir     string `json:"data_dir"`
	TLSCertFile string `json:"tls_cert_file"`
	TLSKeyFile  string `json:"tls_key_file"`
}

func Load(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err = json.Unmarshal(b, v); err != nil {
		return fmt.Errorf("decode %s: %w", path, err)
	}
	return nil
}
func (s Storage) Validate() error {
	switch s.Backend {
	case "filesystem":
		if s.RootDir == "" || !filepath.IsAbs(s.RootDir) {
			return fmt.Errorf("filesystem storage requires absolute root_dir")
		}
	case "s3":
		if !strings.HasPrefix(s.Endpoint, "https://") || s.Bucket == "" {
			return fmt.Errorf("s3 requires https endpoint and bucket")
		}
	default:
		return fmt.Errorf("unsupported storage backend: %q", s.Backend)
	}
	return nil
}
func (c Collector) Validate() error {
	if c.Identity.OrganizationID == "" || c.Identity.SiteID == "" || c.Identity.NodeID == "" {
		return fmt.Errorf("identity organization_id, site_id and node_id are required")
	}
	if c.StateDir == "" || c.SpoolDir == "" || len(c.Sources) == 0 {
		return fmt.Errorf("state_dir, spool_dir and sources required")
	}
	if c.BatchMaxBytes < 4096 || c.SpoolMaxBytes < c.BatchMaxBytes {
		return fmt.Errorf("batch max >=4096 and spool >= batch required")
	}
	if err := c.Storage.Validate(); err != nil {
		return err
	}
	for _, s := range c.Sources {
		if s.ID == "" || !filepath.IsAbs(s.Path) {
			return fmt.Errorf("source id and absolute path required")
		}
	}
	return nil
}
func (s Shipper) Validate() error {
	if s.StateDir == "" || s.PollSeconds < 1 || s.MaxUncompressedBytes < 4096 {
		return fmt.Errorf("state_dir, poll_seconds >= 1, max_uncompressed_bytes >= 4096 required")
	}
	if s.Receiver.URL == "" {
		return fmt.Errorf("receiver.url required")
	}
	return s.Storage.Validate()
}
