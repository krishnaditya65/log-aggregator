package coldstore

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// S3Blobstore is a Blobstore backed by any S3-compatible HTTP API, using
// a hand-implemented AWS Signature Version 4 signer (crypto/hmac +
// crypto/sha256) — no AWS SDK involved.
type S3Blobstore struct {
	Endpoint  string // e.g. "https://s3.us-east-1.amazonaws.com" or a MinIO endpoint
	Bucket    string
	AccessKey string
	SecretKey string
	Region    string

	Client *http.Client
	// Now, when set, overrides time.Now for deterministic tests.
	Now func() time.Time
}

var _ Blobstore = (*S3Blobstore)(nil)

func (s *S3Blobstore) client() *http.Client {
	if s.Client != nil {
		return s.Client
	}
	return http.DefaultClient
}

func (s *S3Blobstore) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now().UTC()
}

func (s *S3Blobstore) objectURL(key string) string {
	base := strings.TrimRight(s.Endpoint, "/")
	return fmt.Sprintf("%s/%s/%s", base, s.Bucket, strings.TrimLeft(key, "/"))
}

func (s *S3Blobstore) Put(ctx context.Context, key string, data io.Reader) error {
	body, err := io.ReadAll(data)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, s.objectURL(key), bytes.NewReader(body))
	if err != nil {
		return err
	}
	if err := s.sign(req, body); err != nil {
		return err
	}
	resp, err := s.client().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("s3: put %s failed: %s: %s", key, resp.Status, string(b))
	}
	return nil
}

func (s *S3Blobstore) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.objectURL(key), nil)
	if err != nil {
		return nil, err
	}
	if err := s.sign(req, nil); err != nil {
		return nil, err
	}
	resp, err := s.client().Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 {
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("s3: get %s failed: %s: %s", key, resp.Status, string(b))
	}
	return resp.Body, nil
}

type listBucketResult struct {
	Contents []struct {
		Key string `xml:"Key"`
	} `xml:"Contents"`
}

func (s *S3Blobstore) List(ctx context.Context, prefix string) ([]string, error) {
	base := strings.TrimRight(s.Endpoint, "/") + "/" + s.Bucket
	q := url.Values{}
	q.Set("list-type", "2")
	q.Set("prefix", prefix)
	reqURL := base + "?" + q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
	}
	if err := s.sign(req, nil); err != nil {
		return nil, err
	}
	resp, err := s.client().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("s3: list %s failed: %s: %s", prefix, resp.Status, string(b))
	}
	var parsed listBucketResult
	if err := xml.Unmarshal(b, &parsed); err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(parsed.Contents))
	for _, c := range parsed.Contents {
		keys = append(keys, c.Key)
	}
	sort.Strings(keys)
	return keys, nil
}

// sign implements AWS Signature Version 4 for a single request: it
// computes the payload hash, sets the required x-amz-* headers, builds
// the canonical request / string-to-sign, derives the signing key via
// the HMAC-SHA256 chain, and sets the Authorization header.
func (s *S3Blobstore) sign(req *http.Request, body []byte) error {
	t := s.now()
	amzDate := t.Format("20060102T150405Z")
	dateStamp := t.Format("20060102")

	payloadHash := sha256Hex(body)

	req.Header.Set("x-amz-date", amzDate)
	req.Header.Set("x-amz-content-sha256", payloadHash)
	if req.Host == "" {
		req.Host = req.URL.Host
	}

	canonicalURI := canonicalURIPath(req.URL.Path)
	canonicalQuery := canonicalQueryString(req.URL)

	headerNames, canonicalHeaders := canonicalHeaders(req)
	signedHeaders := strings.Join(headerNames, ";")

	canonicalRequest := strings.Join([]string{
		req.Method,
		canonicalURI,
		canonicalQuery,
		canonicalHeaders,
		signedHeaders,
		payloadHash,
	}, "\n")

	credentialScope := fmt.Sprintf("%s/%s/s3/aws4_request", dateStamp, s.Region)
	stringToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256",
		amzDate,
		credentialScope,
		sha256Hex([]byte(canonicalRequest)),
	}, "\n")

	signingKey := deriveSigningKey(s.SecretKey, dateStamp, s.Region, "s3")
	signature := hex.EncodeToString(hmacSHA256(signingKey, stringToSign))

	auth := fmt.Sprintf(
		"AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		s.AccessKey, credentialScope, signedHeaders, signature,
	)
	req.Header.Set("Authorization", auth)
	return nil
}

func canonicalURIPath(p string) string {
	if p == "" {
		return "/"
	}
	segments := strings.Split(p, "/")
	for i, seg := range segments {
		segments[i] = url.PathEscape(seg)
	}
	out := strings.Join(segments, "/")
	if !strings.HasPrefix(out, "/") {
		out = "/" + out
	}
	return out
}

func canonicalQueryString(u *url.URL) string {
	vals := u.Query()
	keys := make([]string, 0, len(vals))
	for k := range vals {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		vs := vals[k]
		sort.Strings(vs)
		for _, v := range vs {
			parts = append(parts, url.QueryEscape(k)+"="+url.QueryEscape(v))
		}
	}
	return strings.Join(parts, "&")
}

// canonicalHeaders returns the sorted, signed header names and the
// canonical-headers block (name:value\n for each, sorted).
func canonicalHeaders(req *http.Request) ([]string, string) {
	host := req.Host
	if host == "" {
		host = req.URL.Host
	}
	set := map[string]string{
		"host": host,
	}
	for name, vals := range req.Header {
		lname := strings.ToLower(name)
		if lname == "x-amz-date" || lname == "x-amz-content-sha256" {
			set[lname] = strings.TrimSpace(vals[0])
		}
		if lname == "range" {
			set[lname] = strings.TrimSpace(vals[0])
		}
	}
	names := make([]string, 0, len(set))
	for k := range set {
		names = append(names, k)
	}
	sort.Strings(names)
	var b strings.Builder
	for _, n := range names {
		b.WriteString(n)
		b.WriteByte(':')
		b.WriteString(set[n])
		b.WriteByte('\n')
	}
	return names, b.String()
}

func sha256Hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func hmacSHA256(key []byte, data string) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(data))
	return mac.Sum(nil)
}

func deriveSigningKey(secretKey, dateStamp, region, service string) []byte {
	kDate := hmacSHA256([]byte("AWS4"+secretKey), dateStamp)
	kRegion := hmacSHA256(kDate, region)
	kService := hmacSHA256(kRegion, service)
	kSigning := hmacSHA256(kService, "aws4_request")
	return kSigning
}
