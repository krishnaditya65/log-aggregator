package coldstore

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestSigV4KnownVector reproduces AWS's canonical published SigV4 worked
// example (S3 GET, bucket "examplebucket", key "test.txt", region
// us-east-1, date 2013-05-24) and checks our signer against the
// signature independently hand-computed (via Python's hashlib/hmac,
// outside of this codebase) for that exact example:
//
//	Access Key: AKIAIOSFODNN7EXAMPLE
//	Secret Key: wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY
//	Expected Signature: f0e8bdb87c964420e857bd35b5d6ed310bd44f0170aba48dd91039c6036bdb41
func TestSigV4KnownVector(t *testing.T) {
	const (
		accessKey = "AKIAIOSFODNN7EXAMPLE"
		secretKey = "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"
		region    = "us-east-1"
		wantSig   = "f0e8bdb87c964420e857bd35b5d6ed310bd44f0170aba48dd91039c6036bdb41"
	)
	fixedTime, err := time.Parse("20060102T150405Z", "20130524T000000Z")
	if err != nil {
		t.Fatal(err)
	}

	s := &S3Blobstore{
		Endpoint:  "https://examplebucket.s3.amazonaws.com",
		Bucket:    "", // path already includes the vhost-style bucket in this classic example
		AccessKey: accessKey,
		SecretKey: secretKey,
		Region:    region,
		Now:       func() time.Time { return fixedTime },
	}

	req, err := http.NewRequest(http.MethodGet, "https://examplebucket.s3.amazonaws.com/test.txt", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = "examplebucket.s3.amazonaws.com"
	req.Header.Set("Range", "bytes=0-9")

	if err := s.sign(req, nil); err != nil {
		t.Fatalf("sign: %v", err)
	}

	auth := req.Header.Get("Authorization")
	if !strings.Contains(auth, "Signature="+wantSig) {
		t.Fatalf("signature mismatch.\ngot Authorization header: %s\nwant signature: %s", auth, wantSig)
	}
	if !strings.Contains(auth, "Credential="+accessKey+"/20130524/us-east-1/s3/aws4_request") {
		t.Fatalf("unexpected credential scope in header: %s", auth)
	}
	if !strings.Contains(auth, "SignedHeaders=host;range;x-amz-content-sha256;x-amz-date") {
		t.Fatalf("unexpected signed headers in header: %s", auth)
	}
}

// TestS3BlobstorePutGetList runs Put/Get/List against a local
// httptest server that emulates just enough of the S3 REST API
// (including verifying the SigV4 Authorization header is present and
// well-formed) to prove the client wiring works end-to-end.
func TestS3BlobstorePutGetList(t *testing.T) {
	objects := map[string][]byte{}

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "AWS4-HMAC-SHA256 Credential=") {
			http.Error(w, "missing sigv4 auth", http.StatusForbidden)
			return
		}
		if r.Header.Get("x-amz-date") == "" || r.Header.Get("x-amz-content-sha256") == "" {
			http.Error(w, "missing amz headers", http.StatusForbidden)
			return
		}

		if r.URL.Query().Get("list-type") == "2" {
			prefix := r.URL.Query().Get("prefix")
			var buf bytes.Buffer
			buf.WriteString(`<ListBucketResult>`)
			for k := range objects {
				if strings.HasPrefix(k, prefix) {
					fmt.Fprintf(&buf, "<Contents><Key>%s</Key></Contents>", k)
				}
			}
			buf.WriteString(`</ListBucketResult>`)
			w.Header().Set("Content-Type", "application/xml")
			w.Write(buf.Bytes())
			return
		}

		// object path: /<bucket>/<key...>
		parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/"), "/", 2)
		if len(parts) != 2 {
			http.Error(w, "bad path", http.StatusBadRequest)
			return
		}
		key := parts[1]

		switch r.Method {
		case http.MethodPut:
			body, _ := io.ReadAll(r.Body)
			objects[key] = body
			w.WriteHeader(http.StatusOK)
		case http.MethodGet:
			data, ok := objects[key]
			if !ok {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			w.Write(data)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	s := &S3Blobstore{
		Endpoint:  srv.URL,
		Bucket:    "coldbucket",
		AccessKey: "testkey",
		SecretKey: "testsecret",
		Region:    "us-east-1",
		Client:    srv.Client(),
	}

	ctx := context.Background()
	key := "svc/2026/01/15/segment-1-100.col"
	if err := s.Put(ctx, key, bytes.NewReader([]byte("hello-columnar-data"))); err != nil {
		t.Fatalf("Put: %v", err)
	}

	rc, err := s.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	data, err := io.ReadAll(rc)
	rc.Close()
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "hello-columnar-data" {
		t.Fatalf("unexpected body: %s", data)
	}

	keys, err := s.List(ctx, "svc/2026/01/15/")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(keys) != 1 || keys[0] != key {
		t.Fatalf("unexpected list result: %v", keys)
	}
}
