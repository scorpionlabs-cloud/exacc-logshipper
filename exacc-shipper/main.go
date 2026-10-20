// exacc-shipper retrieves committed manifests and forwards validated events.
package main

import (
	"bytes"
	"compress/gzip"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"example.com/exacc-log-shipper/internal/config"
	"example.com/exacc-log-shipper/internal/landing"
	"example.com/exacc-log-shipper/internal/model"
	"example.com/exacc-log-shipper/internal/securefile"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type app struct {
	cfg           config.Shipper
	store         landing.Store
	client        *http.Client
	secret, token string
}
type receipt struct {
	BatchID     string    `json:"batch_id"`
	ManifestKey string    `json:"manifest_key"`
	ProcessedAt time.Time `json:"processed_at"`
}
type failure struct {
	BatchID   string    `json:"batch_id"`
	Attempts  int       `json:"attempts"`
	LastError string    `json:"last_error"`
	UpdatedAt time.Time `json:"updated_at"`
}

func main() {
	configPath := flag.String("config", "configs/local/shipper.json", "shipper JSON config")
	once := flag.Bool("once", false, "poll exactly once then exit")
	flag.Parse()
	var cfg config.Shipper
	if err := config.Load(*configPath, &cfg); err != nil {
		log.Fatal(err)
	}
	if err := cfg.Validate(); err != nil {
		log.Fatal(err)
	}
	secret := os.Getenv("EXACC_MANIFEST_HMAC_KEY")
	token := os.Getenv("EXACC_RECEIVER_TOKEN")
	if len(secret) < 16 || len(token) < 16 {
		log.Fatal("manifest HMAC key and receiver bearer token must each have at least 16 characters")
	}
	store, err := landing.Open(cfg.Storage)
	if err != nil {
		log.Fatal(err)
	}
	client, err := httpClient(cfg.Receiver)
	if err != nil {
		log.Fatal(err)
	}
	a := &app{cfg: cfg, store: store, client: client, secret: secret, token: token}
	for _, dir := range []string{"processed", "failed", "quarantined"} {
		if err = os.MkdirAll(filepath.Join(cfg.StateDir, dir), 0700); err != nil {
			log.Fatal(err)
		}
	}
	for {
		if err = a.poll(); err != nil {
			log.Printf("poll failed: %v", err)
			if *once {
				os.Exit(1)
			}
		}
		if *once {
			return
		}
		time.Sleep(time.Duration(cfg.PollSeconds) * time.Second)
	}
}
func httpClient(r config.Receiver) (*http.Client, error) {
	u, err := url.Parse(r.URL)
	if err != nil {
		return nil, err
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1")) {
		return nil, fmt.Errorf("receiver must use HTTPS or local loopback HTTP")
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	if r.CACertFile != "" {
		pem, err := os.ReadFile(r.CACertFile)
		if err != nil {
			return nil, err
		}
		pool, err := x509.SystemCertPool()
		if err != nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("invalid receiver CA PEM")
		}
		tr.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: pool}
	}
	return &http.Client{Timeout: 30 * time.Second, Transport: tr}, nil
}
func (a *app) stateFile(which, id string) string {
	return filepath.Join(a.cfg.StateDir, which, model.Hash(id)+".json")
}
func (a *app) poll() error {
	keys, err := a.store.List("v1/")
	if err != nil {
		return err
	}
	processed, failed := 0, 0
	for _, key := range keys {
		if !strings.HasSuffix(key, ".manifest.json") {
			continue
		}
		id := key // Before parsing, use object key as durable processing identifier.
		if _, err = os.Stat(a.stateFile("processed", id)); err == nil {
			continue
		}
		if _, err = os.Stat(a.stateFile("quarantined", id)); err == nil {
			continue
		}
		if err = a.process(key); err != nil {
			failed++
			log.Printf("manifest=%s ERROR: %v", key, err)
			a.recordFailure(key, err)
		} else {
			processed++
		}
	}
	log.Printf("poll complete processed=%d errors=%d objects=%d", processed, failed, len(keys))
	if failed > 0 {
		return fmt.Errorf("%d manifests could not be processed", failed)
	}
	return nil
}
func (a *app) recordFailure(key string, processErr error) {
	path := a.stateFile("failed", key)
	var record failure
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, &record)
	}
	record.BatchID = key
	record.Attempts++
	record.LastError = processErr.Error()
	record.UpdatedAt = time.Now().UTC()
	if record.Attempts >= 5 {
		if err := securefile.JSON(a.stateFile("quarantined", key), record); err != nil {
			log.Printf("quarantine write error: %v", err)
		} else {
			log.Printf("quarantined %s after %d failures; requires operator review", key, record.Attempts)
		}
	} else if err := securefile.JSON(path, record); err != nil {
		log.Printf("failure state write error: %v", err)
	}
}
func (a *app) process(manifestKey string) error {
	raw, err := a.store.Get(manifestKey)
	if err != nil {
		return err
	}
	var m model.Manifest
	if err = json.Unmarshal(raw, &m); err != nil {
		return err
	}
	if m.SchemaVersion != model.SchemaVersion || m.BatchID == "" || m.EventCount <= 0 {
		return fmt.Errorf("invalid manifest schema or batch metadata")
	}
	if err = m.Verify(a.secret); err != nil {
		return err
	}
	expected := strings.TrimSuffix(m.PayloadKey, ".jsonl.gz") + ".manifest.json"
	if !strings.HasSuffix(m.PayloadKey, ".jsonl.gz") || manifestKey != expected {
		return fmt.Errorf("manifest key does not match referenced payload")
	}
	if err = landing.ValidKey(m.PayloadKey); err != nil {
		return err
	}
	allowed := len(a.cfg.AllowedOrganizations) == 0
	for _, org := range a.cfg.AllowedOrganizations {
		if org == m.Identity.OrganizationID {
			allowed = true
			break
		}
	}
	if !allowed {
		return fmt.Errorf("unauthorized organization in manifest: %q", m.Identity.OrganizationID)
	}
	if m.PayloadBytes < 1 || m.PayloadBytes > 32<<20 {
		return fmt.Errorf("compressed payload exceeds prototype 32 MiB cap")
	}
	payload, err := a.store.Get(m.PayloadKey)
	if err != nil {
		return err
	}
	if int64(len(payload)) != m.PayloadBytes || model.SHA(payload) != m.PayloadSHA256 {
		return fmt.Errorf("payload length or SHA-256 mismatch")
	}
	gz, err := gzip.NewReader(bytes.NewReader(payload))
	if err != nil {
		return err
	}
	decompressed, err := io.ReadAll(io.LimitReader(gz, a.cfg.MaxUncompressedBytes+1))
	if err != nil {
		gz.Close()
		return err
	}
	if err = gz.Close(); err != nil {
		return err
	}
	if int64(len(decompressed)) > a.cfg.MaxUncompressedBytes {
		return fmt.Errorf("decompression exceeded allowed size")
	}
	var events []model.Event
	for _, line := range bytes.Split(bytes.TrimSuffix(decompressed, []byte("\n")), []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		if len(line) > 256<<10 {
			return fmt.Errorf("event exceeds 256 KiB")
		}
		var e model.Event
		if err = json.Unmarshal(line, &e); err != nil {
			return err
		}
		if e.SchemaVersion != model.SchemaVersion || e.EventID == "" || e.SourceID != m.SourceID || e.Identity.OrganizationID != m.Identity.OrganizationID || e.Identity.NodeID != m.Identity.NodeID {
			return fmt.Errorf("event and manifest identity mismatch")
		}
		events = append(events, e)
	}
	if len(events) != m.EventCount {
		return fmt.Errorf("manifest count=%d but got %d", m.EventCount, len(events))
	}
	out, err := json.Marshal(model.Batch{BatchID: m.BatchID, Events: events})
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, a.cfg.Receiver.URL, bytes.NewReader(out))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+a.token)
	resp, err := a.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	response, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("receiver HTTP %d: %s", resp.StatusCode, string(response))
	}
	// Receiver has durably acknowledged the batch. Only then persist shipper checkpoint.
	if err = securefile.JSON(a.stateFile("processed", manifestKey), receipt{BatchID: m.BatchID, ManifestKey: manifestKey, ProcessedAt: time.Now().UTC()}); err != nil {
		return err
	}
	if err = os.Remove(a.stateFile("failed", manifestKey)); err != nil && !errors.Is(err, os.ErrNotExist) {
		log.Printf("cannot remove failure record: %v", err)
	}
	log.Printf("committed batch=%s count=%d source=%s", m.BatchID, len(events), m.SourceID)
	return nil
}
