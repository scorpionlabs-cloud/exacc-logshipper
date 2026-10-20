// exacc-receiver: minimal durable batch ingestion service; NOT a full RCA engine.
package main

import (
	"crypto/hmac"
	"encoding/json"
	"errors"
	"example.com/exacc-log-shipper/internal/config"
	"example.com/exacc-log-shipper/internal/model"
	"example.com/exacc-log-shipper/internal/securefile"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"
)

var idPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

type receiver struct {
	cfg   config.Server
	token string
	mu    sync.Mutex
}

func main() {
	configPath := flag.String("config", "configs/local/receiver.json", "receiver JSON config")
	flag.Parse()
	var cfg config.Server
	if err := config.Load(*configPath, &cfg); err != nil {
		log.Fatal(err)
	}
	if cfg.Listen == "" || cfg.DataDir == "" {
		log.Fatal("listen and data_dir required")
	}
	if (cfg.TLSCertFile == "") != (cfg.TLSKeyFile == "") {
		log.Fatal("TLS cert and key must be configured together")
	}
	host, _, err := net.SplitHostPort(cfg.Listen)
	if err != nil {
		log.Fatal(err)
	}
	if cfg.TLSCertFile == "" && host != "127.0.0.1" && host != "localhost" {
		log.Fatal("unencrypted receiver is permitted only on loopback")
	}
	token := os.Getenv("EXACC_RECEIVER_TOKEN")
	if len(token) < 16 {
		log.Fatal("EXACC_RECEIVER_TOKEN must have at least 16 characters")
	}
	if err = os.MkdirAll(filepath.Join(cfg.DataDir, "batches"), 0700); err != nil {
		log.Fatal(err)
	}
	r := &receiver{cfg: cfg, token: token}
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, req *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("/v1/telemetry/events:batch", r.ingest)
	mux.HandleFunc("/v1/telemetry/stats", r.stats)
	server := &http.Server{Addr: cfg.Listen, Handler: mux, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 20 * time.Second, WriteTimeout: 20 * time.Second, IdleTimeout: 30 * time.Second}
	log.Printf("receiver listening on %s (TLS=%t)", cfg.Listen, cfg.TLSCertFile != "")
	if cfg.TLSCertFile != "" {
		log.Fatal(server.ListenAndServeTLS(cfg.TLSCertFile, cfg.TLSKeyFile))
	}
	log.Fatal(server.ListenAndServe())
}
func (r *receiver) auth(req *http.Request) bool {
	candidate := req.Header.Get("Authorization")
	return hmac.Equal([]byte(candidate), []byte("Bearer "+r.token))
}
func (r *receiver) ingest(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	if !r.auth(req) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	req.Body = http.MaxBytesReader(w, req.Body, 16<<20)
	raw, err := io.ReadAll(req.Body)
	if err != nil {
		http.Error(w, "request too large or unreadable", http.StatusRequestEntityTooLarge)
		return
	}
	var batch model.Batch
	if err = json.Unmarshal(raw, &batch); err != nil || !idPattern.MatchString(batch.BatchID) || len(batch.Events) == 0 {
		http.Error(w, "invalid batch request", http.StatusBadRequest)
		return
	}
	for _, event := range batch.Events {
		if event.EventID == "" || event.SchemaVersion != model.SchemaVersion {
			http.Error(w, "invalid event", http.StatusBadRequest)
			return
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	path := filepath.Join(r.cfg.DataDir, "batches", batch.BatchID+".json")
	prev, err := os.ReadFile(path)
	if err == nil {
		// Compare canonical JSON, rather than whitespace, for idempotent retries.
		var stored model.Batch
		if e := json.Unmarshal(prev, &stored); e == nil {
			canon1, _ := json.Marshal(stored)
			canon2, _ := json.Marshal(batch)
			if hmac.Equal(canon1, canon2) {
				respondJSON(w, http.StatusOK, map[string]any{"status": "duplicate_acknowledged", "batch_id": batch.BatchID})
				return
			}
		}
		http.Error(w, "batch id conflict", http.StatusConflict)
		return
	}
	if !errors.Is(err, os.ErrNotExist) {
		http.Error(w, "storage unavailable", http.StatusInternalServerError)
		return
	}
	durable, err := json.Marshal(batch)
	if err != nil {
		http.Error(w, "encoding failed", http.StatusInternalServerError)
		return
	}
	if err = securefile.Write(path, durable, 0600); err != nil {
		log.Printf("persist batch error: %v", err)
		http.Error(w, "durable write failed", http.StatusInternalServerError)
		return
	}
	respondJSON(w, http.StatusAccepted, map[string]any{"status": "committed", "batch_id": batch.BatchID, "events": len(batch.Events), "received_at": time.Now().UTC()})
}
func (r *receiver) stats(w http.ResponseWriter, req *http.Request) {
	if !r.auth(req) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	entries, err := os.ReadDir(filepath.Join(r.cfg.DataDir, "batches"))
	if err != nil {
		http.Error(w, fmt.Sprintf("read store: %v", err), http.StatusInternalServerError)
		return
	}
	count := 0
	for _, e := range entries {
		if !e.IsDir() {
			count++
		}
	}
	respondJSON(w, http.StatusOK, map[string]any{"committed_batches": count})
}
func respondJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}
