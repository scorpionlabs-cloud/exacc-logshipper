package landing

// Minimal AWS Signature V4 S3 path-style client. Supports only PUT/GET/ListV2
// needed by the reference pipeline; not a general AWS S3 SDK replacement.

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/xml"
	"example.com/exacc-log-shipper/internal/config"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"
)

type s3 struct {
	endpoint                              *url.URL
	bucket, region, access, secret, token string
	client                                *http.Client
}

func openS3(c config.Storage) (Store, error) {
	endpoint, err := url.Parse(c.Endpoint)
	if err != nil {
		return nil, err
	}
	if endpoint.Scheme != "https" || endpoint.Host == "" || endpoint.User != nil {
		return nil, fmt.Errorf("S3 endpoint must be HTTPS without userinfo")
	}
	access, secret := os.Getenv("AWS_ACCESS_KEY_ID"), os.Getenv("AWS_SECRET_ACCESS_KEY")
	if access == "" || secret == "" {
		return nil, fmt.Errorf("AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY must be set")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if c.CACertFile != "" {
		data, err := os.ReadFile(c.CACertFile)
		if err != nil {
			return nil, err
		}
		pool, err := x509.SystemCertPool()
		if err != nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM(data) {
			return nil, fmt.Errorf("CA bundle contains no certificates")
		}
		transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: pool}
	}
	region := c.Region
	if region == "" {
		region = "us-east-1"
	}
	return &s3{endpoint: endpoint, bucket: c.Bucket, region: region, access: access, secret: secret, token: os.Getenv("AWS_SESSION_TOKEN"), client: &http.Client{Timeout: 90 * time.Second, Transport: transport}}, nil
}
func hmacSHA(key []byte, msg string) []byte {
	h := hmac.New(sha256.New, key)
	_, _ = h.Write([]byte(msg))
	return h.Sum(nil)
}
func hashSHA(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }
func (s *s3) request(method, key string, query url.Values, body []byte) ([]byte, error) {
	if key != "" {
		if err := ValidKey(key); err != nil {
			return nil, err
		}
	}
	u := *s.endpoint
	p := strings.TrimRight(u.Path, "/") + "/" + s.bucket
	if key != "" {
		p += "/" + key
	}
	u.Path = p
	// url.URL.EscapedPath encodes spaces and UTF-8 but retains '/' separators.
	u.RawPath = ""
	if query == nil {
		query = url.Values{}
	}
	canonicalQuery := strings.ReplaceAll(query.Encode(), "+", "%20")
	u.RawQuery = canonicalQuery
	now := time.Now().UTC()
	stamp := now.Format("20060102T150405Z")
	day := now.Format("20060102")
	digest := hashSHA(body)
	hdr := "host:" + u.Host + "\n" + "x-amz-content-sha256:" + digest + "\n" + "x-amz-date:" + stamp + "\n"
	signedHeaders := "host;x-amz-content-sha256;x-amz-date"
	if s.token != "" {
		hdr += "x-amz-security-token:" + s.token + "\n"
		signedHeaders += ";x-amz-security-token"
	}
	canonical := method + "\n" + u.EscapedPath() + "\n" + canonicalQuery + "\n" + hdr + "\n" + signedHeaders + "\n" + digest
	scope := day + "/" + s.region + "/s3/aws4_request"
	toSign := "AWS4-HMAC-SHA256\n" + stamp + "\n" + scope + "\n" + hashSHA([]byte(canonical))
	kDate := hmacSHA([]byte("AWS4"+s.secret), day)
	kRegion := hmacSHA(kDate, s.region)
	kService := hmacSHA(kRegion, "s3")
	signature := hex.EncodeToString(hmacSHA(hmacSHA(kService, "aws4_request"), toSign))
	req, err := http.NewRequest(method, u.String(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("x-amz-date", stamp)
	req.Header.Set("x-amz-content-sha256", digest)
	if s.token != "" {
		req.Header.Set("x-amz-security-token", s.token)
	}
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+s.access+"/"+scope+", SignedHeaders="+signedHeaders+", Signature="+signature)
	if method == http.MethodPut {
		req.Header.Set("Content-Type", "application/octet-stream")
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("S3 %s %s HTTP %d: %s", method, u.Path, resp.StatusCode, string(data[:min(len(data), 512)]))
	}
	return data, nil
}
func (s *s3) Put(key string, data []byte) error {
	_, err := s.request(http.MethodPut, key, nil, data)
	return err
}
func (s *s3) Get(key string) ([]byte, error) { return s.request(http.MethodGet, key, nil, nil) }

type s3List struct {
	Contents []struct {
		Key string `xml:"Key"`
	} `xml:"Contents"`
	Truncated bool   `xml:"IsTruncated"`
	Token     string `xml:"NextContinuationToken"`
}

func (s *s3) List(prefix string) ([]string, error) {
	var keys []string
	var token string
	for page := 0; page < 1000; page++ {
		q := url.Values{"list-type": {"2"}, "prefix": {prefix}, "max-keys": {"1000"}}
		if token != "" {
			q.Set("continuation-token", token)
		}
		data, err := s.request(http.MethodGet, "", q, nil)
		if err != nil {
			return nil, err
		}
		var listing s3List
		if err = xml.Unmarshal(data, &listing); err != nil {
			return nil, fmt.Errorf("decode S3 ListObjectsV2 XML: %w", err)
		}
		for _, obj := range listing.Contents {
			keys = append(keys, obj.Key)
		}
		if !listing.Truncated {
			sort.Strings(keys)
			return keys, nil
		}
		if listing.Token == "" || listing.Token == token {
			return nil, fmt.Errorf("invalid S3 pagination token")
		}
		token = listing.Token
	}
	return nil, fmt.Errorf("S3 ListObjectsV2 exceeded 1000 pages; restrict prefix")
}
