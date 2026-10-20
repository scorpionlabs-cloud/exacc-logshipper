// Package model defines the transport contract shared by the three services.
package model

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
)

const SchemaVersion = "1.0"

var oraCode = regexp.MustCompile(`\bORA-\d{5}\b`)

type Identity struct {
	OrganizationID string `json:"organization_id"`
	SiteID         string `json:"site_id"`
	ExaccID        string `json:"exacc_id"`
	VMClusterID    string `json:"vm_cluster_id"`
	NodeID         string `json:"node_id"`
}

type Event struct {
	SchemaVersion string    `json:"schema_version"`
	EventID       string    `json:"event_id"`
	Identity      Identity  `json:"identity"`
	SourceID      string    `json:"source_id"`
	SourceOffset  int64     `json:"source_offset"`
	ObservedAt    time.Time `json:"observed_at"`
	Severity      string    `json:"severity"`
	ErrorCode     string    `json:"error_code,omitempty"`
	Message       string    `json:"message"`
}

type Manifest struct {
	SchemaVersion string    `json:"schema_version"`
	BatchID       string    `json:"batch_id"`
	Identity      Identity  `json:"identity"`
	SourceID      string    `json:"source_id"`
	PayloadKey    string    `json:"payload_key"`
	PayloadSHA256 string    `json:"payload_sha256"`
	PayloadBytes  int64     `json:"payload_bytes"`
	EventCount    int       `json:"event_count"`
	CreatedAt     time.Time `json:"created_at"`
	Signature     string    `json:"signature"`
}

type Batch struct {
	BatchID string  `json:"batch_id"`
	Events  []Event `json:"events"`
}

func Hash(v string) string   { sum := sha256.Sum256([]byte(v)); return hex.EncodeToString(sum[:]) }
func SHA(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }
func Classify(message string) (severity, code string) {
	severity = "INFO"
	if m := oraCode.FindString(message); m != "" {
		code = m
		severity = "ERROR"
	}
	u := strings.ToUpper(message)
	if strings.Contains(u, "FATAL") || strings.Contains(u, "CRITICAL") {
		severity = "CRITICAL"
	} else if severity != "CRITICAL" && strings.Contains(u, "ERROR") {
		severity = "ERROR"
	} else if severity == "INFO" && strings.Contains(u, "WARN") {
		severity = "WARN"
	}
	return
}
func (m Manifest) signBytes() ([]byte, error) { m.Signature = ""; return json.Marshal(m) }
func (m *Manifest) Sign(key string) error {
	if len(key) < 16 {
		return fmt.Errorf("manifest signing key must have at least 16 characters")
	}
	data, err := m.signBytes()
	if err != nil {
		return err
	}
	h := hmac.New(sha256.New, []byte(key))
	h.Write(data)
	m.Signature = hex.EncodeToString(h.Sum(nil))
	return nil
}
func (m Manifest) Verify(key string) error {
	if key == "" {
		return fmt.Errorf("manifest signing key missing")
	}
	data, err := m.signBytes()
	if err != nil {
		return err
	}
	signed, err := hex.DecodeString(m.Signature)
	if err != nil {
		return fmt.Errorf("invalid manifest signature encoding: %w", err)
	}
	h := hmac.New(sha256.New, []byte(key))
	h.Write(data)
	if !hmac.Equal(signed, h.Sum(nil)) {
		return fmt.Errorf("manifest signature mismatch")
	}
	return nil
}
