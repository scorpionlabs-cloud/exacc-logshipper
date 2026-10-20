// exacc-collect: scheduled incremental read-only file collector and durable uploader.
package main

import (
	"bufio"
	"bytes"
	"compress/gzip"
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
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"
)

type cursor struct {
	Dev    uint64 `json:"dev"`
	Inode  uint64 `json:"inode"`
	Offset int64  `json:"offset"`
}
type pending struct {
	SourceID    string         `json:"source_id"`
	Next        cursor         `json:"next"`
	Manifest    model.Manifest `json:"manifest"`
	PayloadPath string         `json:"payload_path"`
}
type app struct {
	cfg     config.Collector
	store   landing.Store
	redact  []*regexp.Regexp
	signKey string
}

var idFormat = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}$`)

func main() {
	configPath := flag.String("config", "configs/local/collector.json", "collector JSON config")
	validate := flag.Bool("validate", false, "validate only, do not read or upload")
	flag.Parse()
	var cfg config.Collector
	if err := config.Load(*configPath, &cfg); err != nil {
		log.Fatal(err)
	}
	if err := cfg.Validate(); err != nil {
		log.Fatal(err)
	}
	for _, v := range []string{cfg.Identity.OrganizationID, cfg.Identity.SiteID, cfg.Identity.ExaccID, cfg.Identity.VMClusterID, cfg.Identity.NodeID} {
		if !idFormat.MatchString(v) {
			log.Fatalf("invalid identity component %q", v)
		}
	}
	for _, v := range cfg.Sources {
		if !idFormat.MatchString(v.ID) {
			log.Fatalf("invalid source id %q", v.ID)
		}
	}
	for _, v := range cfg.RedactPatterns {
		if _, err := regexp.Compile(v); err != nil {
			log.Fatalf("invalid redaction regex %q: %v", v, err)
		}
	}
	if *validate {
		fmt.Println("collector configuration valid (runtime connectivity not tested)")
		return
	}
	key := os.Getenv("EXACC_MANIFEST_HMAC_KEY")
	if len(key) < 16 {
		log.Fatal("EXACC_MANIFEST_HMAC_KEY must contain at least 16 characters")
	}
	store, err := landing.Open(cfg.Storage)
	if err != nil {
		log.Fatal(err)
	}
	a := &app{cfg: cfg, store: store, signKey: key}
	for _, pattern := range cfg.RedactPatterns {
		a.redact = append(a.redact, regexp.MustCompile(pattern))
	}
	if err = os.MkdirAll(filepath.Join(cfg.StateDir, "cursors"), 0700); err != nil {
		log.Fatal(err)
	}
	if err = os.MkdirAll(cfg.SpoolDir, 0700); err != nil {
		log.Fatal(err)
	}
	lock, err := os.OpenFile(filepath.Join(cfg.StateDir, "collector.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		log.Fatal(err)
	}
	defer lock.Close()
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		log.Fatal("another collector run is active")
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	if err = a.retryPending(); err != nil {
		log.Fatalf("pending batch retry failed, preserving local spool: %v", err)
	}
	var failed bool
	for _, source := range cfg.Sources {
		if err := a.collect(source); err != nil {
			log.Printf("source=%s failed: %v", source.ID, err)
			failed = true
		}
	}
	if failed {
		os.Exit(1)
	}
}
func (a *app) cursorPath(id string) string {
	return filepath.Join(a.cfg.StateDir, "cursors", id+".json")
}
func (a *app) loadCursor(id string) (cursor, error) {
	var c cursor
	b, err := os.ReadFile(a.cursorPath(id))
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	return c, json.Unmarshal(b, &c)
}
func (a *app) retryPending() error {
	entries, err := filepath.Glob(filepath.Join(a.cfg.SpoolDir, "*.pending.json"))
	if err != nil {
		return err
	}
	for _, name := range entries {
		b, err := os.ReadFile(name)
		if err != nil {
			return err
		}
		var p pending
		if err = json.Unmarshal(b, &p); err != nil {
			return fmt.Errorf("corrupt spool metadata %s: %w", name, err)
		}
		if err = a.uploadAndCommit(p, name); err != nil {
			return err
		}
	}
	return nil
}
func (a *app) uploadAndCommit(p pending, metaPath string) error {
	payload, err := os.ReadFile(p.PayloadPath)
	if err != nil {
		return fmt.Errorf("spooled payload unavailable: %w", err)
	}
	if model.SHA(payload) != p.Manifest.PayloadSHA256 {
		return fmt.Errorf("spool checksum mismatch: %s", p.PayloadPath)
	}
	if err = a.store.Put(p.Manifest.PayloadKey, payload); err != nil {
		return fmt.Errorf("payload S3 PUT: %w", err)
	}
	raw, err := json.Marshal(p.Manifest)
	if err != nil {
		return err
	}
	key := strings.TrimSuffix(p.Manifest.PayloadKey, ".jsonl.gz") + ".manifest.json"
	if err = a.store.Put(key, raw); err != nil {
		return fmt.Errorf("manifest S3 PUT: %w", err)
	}
	current, err := a.loadCursor(p.SourceID)
	if err != nil {
		return err
	}
	if current.Dev == p.Next.Dev && current.Inode == p.Next.Inode && current.Offset > p.Next.Offset {
		// Never move a cursor backwards after a replay.
	} else if err = securefile.JSON(a.cursorPath(p.SourceID), p.Next); err != nil {
		return err
	}
	// Metadata is removed first. Orphaned payloads are harmless and can be cleaned.
	if err = os.Remove(metaPath); err != nil {
		return err
	}
	if err = os.Remove(p.PayloadPath); err != nil {
		log.Printf("remove orphan payload %s: %v", p.PayloadPath, err)
	}
	log.Printf("uploaded batch=%s source=%s events=%d", p.Manifest.BatchID, p.SourceID, p.Manifest.EventCount)
	return nil
}
func (a *app) spoolSize() (int64, error) {
	var total int64
	err := filepath.WalkDir(a.cfg.SpoolDir, func(p string, d os.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		total += info.Size()
		return nil
	})
	return total, err
}
func (a *app) collect(source config.Source) error {
	f, err := os.Open(source.Path)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	sys, ok := st.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("inode metadata not available on Linux")
	}
	dev, inode := uint64(sys.Dev), uint64(sys.Ino)
	saved, err := a.loadCursor(source.ID)
	if err != nil {
		return err
	}
	from := saved.Offset
	if saved.Dev != dev || saved.Inode != inode || from > st.Size() {
		if saved.Inode != 0 {
			log.Printf("source=%s rotated/truncated; reading current file from offset 0 (rotated file requires separate coverage)", source.ID)
		}
		from = 0
	}
	if _, err = f.Seek(from, io.SeekStart); err != nil {
		return err
	}
	r := bufio.NewReader(f)
	var events []model.Event
	offset := from
	max := a.cfg.BatchMaxBytes
	for offset-from < max {
		start := offset
		line, err := r.ReadBytes('\n')
		if err == io.EOF {
			break
		} // Never commit an unfinished log line.
		if err != nil {
			return err
		}
		if int64(len(line)) > max {
			return fmt.Errorf("source=%s log record size exceeds batch maximum; increase limit", source.ID)
		}
		if offset-from+int64(len(line)) > max {
			break
		}
		offset += int64(len(line))
		message := strings.TrimRight(string(line), "\r\n")
		for _, pattern := range a.redact {
			message = pattern.ReplaceAllString(message, "[REDACTED]")
		}
		sev, code := model.Classify(message)
		id := model.Hash(fmt.Sprintf("%s|%s|%d|%d|%d", a.cfg.Identity.OrganizationID, source.ID, dev, inode, start))
		events = append(events, model.Event{SchemaVersion: model.SchemaVersion, EventID: id, Identity: a.cfg.Identity, SourceID: source.ID, SourceOffset: start, ObservedAt: time.Now().UTC(), Severity: sev, ErrorCode: code, Message: message})
	}
	if len(events) == 0 {
		return nil
	}
	var b bytes.Buffer
	zw := gzip.NewWriter(&b)
	for _, event := range events {
		encoded, err := json.Marshal(event)
		if err != nil {
			return err
		}
		if _, err = zw.Write(append(encoded, '\n')); err != nil {
			return err
		}
	}
	if err = zw.Close(); err != nil {
		return err
	}
	size, err := a.spoolSize()
	if err != nil {
		return err
	}
	if size+int64(b.Len())+8192 > a.cfg.SpoolMaxBytes {
		return fmt.Errorf("spool high-water limit reached: %d / %d", size, a.cfg.SpoolMaxBytes)
	}
	payloadHash := model.SHA(b.Bytes())
	batchID := model.Hash(fmt.Sprintf("%s|%s|%d|%d|%d|%s", a.cfg.Identity.OrganizationID, source.ID, dev, inode, from, payloadHash))
	now := time.Now().UTC()
	ident := a.cfg.Identity
	key := fmt.Sprintf("v1/org=%s/site=%s/exacc=%s/cluster=%s/node=%s/source=%s/%s/%s.jsonl.gz", ident.OrganizationID, ident.SiteID, ident.ExaccID, ident.VMClusterID, ident.NodeID, source.ID, now.Format("2006/01/02"), batchID)
	m := model.Manifest{SchemaVersion: model.SchemaVersion, BatchID: batchID, Identity: ident, SourceID: source.ID, PayloadKey: key, PayloadSHA256: payloadHash, PayloadBytes: int64(b.Len()), EventCount: len(events), CreatedAt: now}
	if err = m.Sign(a.signKey); err != nil {
		return err
	}
	payloadPath := filepath.Join(a.cfg.SpoolDir, batchID+".jsonl.gz")
	metaPath := filepath.Join(a.cfg.SpoolDir, batchID+".pending.json")
	if err = securefile.Write(payloadPath, b.Bytes(), 0600); err != nil {
		return err
	}
	p := pending{SourceID: source.ID, Next: cursor{Dev: dev, Inode: inode, Offset: offset}, Manifest: m, PayloadPath: payloadPath}
	if err = securefile.JSON(metaPath, p); err != nil {
		return err
	}
	return a.uploadAndCommit(p, metaPath)
}
