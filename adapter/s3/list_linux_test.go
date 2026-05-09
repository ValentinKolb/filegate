//go:build linux

package s3

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestListObjectsV2Basic puts a few objects and lists them. The
// expected order is lexical on relPath.
func TestListObjectsV2Basic(t *testing.T) {
	_, handler, mount, cleanup := newTestServer(t)
	defer cleanup()

	keys := []string{"a.txt", "b.txt", "photos/2024/cat.jpg", "photos/2024/dog.jpg", "z.bin"}
	for _, k := range keys {
		body := []byte("body-" + k)
		req := httptest.NewRequest(http.MethodPut, "/"+mount+"/"+k, bytes.NewReader(body))
		req.Host = "example.com"
		signRequestPayload(req, body)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("PUT %q status=%d body=%s", k, rec.Code, rec.Body.String())
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/"+mount+"?list-type=2", nil)
	req.Host = "example.com"
	signRequestPayload(req, nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("LIST status=%d body=%s", rec.Code, rec.Body.String())
	}

	var res listBucketResultV2
	if err := xml.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("XML decode: %v\nbody=%s", err, rec.Body.String())
	}
	if res.Name != mount {
		t.Errorf("Name=%q, want %q", res.Name, mount)
	}
	if res.IsTruncated {
		t.Errorf("IsTruncated should be false for small result")
	}
	if res.KeyCount != len(keys) {
		t.Fatalf("KeyCount=%d, want %d", res.KeyCount, len(keys))
	}
	for i, want := range keys {
		if res.Contents[i].Key != want {
			t.Errorf("Contents[%d].Key=%q, want %q", i, res.Contents[i].Key, want)
		}
		if res.Contents[i].Size <= 0 {
			t.Errorf("Contents[%d].Size=%d", i, res.Contents[i].Size)
		}
		if res.Contents[i].ETag == "" {
			t.Errorf("Contents[%d].ETag empty", i)
		}
	}
}

// TestListObjectsV2Prefix scopes to a prefix and verifies only
// matching keys appear, in order.
func TestListObjectsV2Prefix(t *testing.T) {
	_, handler, mount, cleanup := newTestServer(t)
	defer cleanup()

	keys := []string{"a.txt", "photos/2023/cat.jpg", "photos/2024/cat.jpg", "photos/2024/dog.jpg", "videos/v.mp4"}
	for _, k := range keys {
		body := []byte("x")
		req := httptest.NewRequest(http.MethodPut, "/"+mount+"/"+k, bytes.NewReader(body))
		req.Host = "example.com"
		signRequestPayload(req, body)
		handler.ServeHTTP(httptest.NewRecorder(), req)
	}

	req := httptest.NewRequest(http.MethodGet, "/"+mount+"?list-type=2&prefix=photos/", nil)
	req.Host = "example.com"
	signRequestPayload(req, nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("LIST status=%d body=%s", rec.Code, rec.Body.String())
	}
	var res listBucketResultV2
	_ = xml.Unmarshal(rec.Body.Bytes(), &res)
	want := []string{"photos/2023/cat.jpg", "photos/2024/cat.jpg", "photos/2024/dog.jpg"}
	if res.KeyCount != len(want) {
		t.Fatalf("KeyCount=%d, want %d, contents=%v", res.KeyCount, len(want), keysOf(res.Contents))
	}
	for i, w := range want {
		if res.Contents[i].Key != w {
			t.Errorf("Contents[%d]=%q, want %q", i, res.Contents[i].Key, w)
		}
	}
	if res.Prefix != "photos/" {
		t.Errorf("Prefix=%q, want photos/", res.Prefix)
	}
}

// TestListObjectsV2Pagination puts more keys than fit, verifies
// IsTruncated + NextContinuationToken + page-2 fetch picks up
// where page-1 left off.
func TestListObjectsV2Pagination(t *testing.T) {
	_, handler, mount, cleanup := newTestServer(t)
	defer cleanup()

	const n = 7
	for i := 0; i < n; i++ {
		k := fmt.Sprintf("k%02d.txt", i)
		body := []byte("v")
		req := httptest.NewRequest(http.MethodPut, "/"+mount+"/"+k, bytes.NewReader(body))
		req.Host = "example.com"
		signRequestPayload(req, body)
		handler.ServeHTTP(httptest.NewRecorder(), req)
	}

	// Page 1 — max-keys=3.
	req := httptest.NewRequest(http.MethodGet, "/"+mount+"?list-type=2&max-keys=3", nil)
	req.Host = "example.com"
	signRequestPayload(req, nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	var page1 listBucketResultV2
	_ = xml.Unmarshal(rec.Body.Bytes(), &page1)
	if page1.KeyCount != 3 {
		t.Fatalf("page1 KeyCount=%d, want 3", page1.KeyCount)
	}
	if !page1.IsTruncated {
		t.Fatalf("page1 IsTruncated=false, want true")
	}
	if page1.NextContinuationToken == "" {
		t.Fatalf("page1 NextContinuationToken empty")
	}
	gotKeys := keysOf(page1.Contents)
	wantKeys := []string{"k00.txt", "k01.txt", "k02.txt"}
	for i, w := range wantKeys {
		if gotKeys[i] != w {
			t.Errorf("page1 Contents[%d]=%q, want %q", i, gotKeys[i], w)
		}
	}

	// Page 2 — feed back the continuation-token.
	req = httptest.NewRequest(http.MethodGet, "/"+mount+"?list-type=2&max-keys=3&continuation-token="+page1.NextContinuationToken, nil)
	req.Host = "example.com"
	signRequestPayload(req, nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	var page2 listBucketResultV2
	_ = xml.Unmarshal(rec.Body.Bytes(), &page2)
	if page2.KeyCount != 3 {
		t.Fatalf("page2 KeyCount=%d, want 3", page2.KeyCount)
	}
	if !page2.IsTruncated {
		t.Fatalf("page2 IsTruncated=false, want true")
	}
	gotKeys2 := keysOf(page2.Contents)
	wantKeys2 := []string{"k03.txt", "k04.txt", "k05.txt"}
	for i, w := range wantKeys2 {
		if gotKeys2[i] != w {
			t.Errorf("page2 Contents[%d]=%q, want %q", i, gotKeys2[i], w)
		}
	}

	// Page 3 — final.
	req = httptest.NewRequest(http.MethodGet, "/"+mount+"?list-type=2&max-keys=3&continuation-token="+page2.NextContinuationToken, nil)
	req.Host = "example.com"
	signRequestPayload(req, nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	var page3 listBucketResultV2
	_ = xml.Unmarshal(rec.Body.Bytes(), &page3)
	if page3.KeyCount != 1 {
		t.Fatalf("page3 KeyCount=%d, want 1", page3.KeyCount)
	}
	if page3.IsTruncated {
		t.Fatalf("page3 IsTruncated=true, want false (final page)")
	}
	if page3.NextContinuationToken != "" {
		t.Errorf("page3 NextContinuationToken=%q, want empty", page3.NextContinuationToken)
	}
	if page3.Contents[0].Key != "k06.txt" {
		t.Errorf("page3 last key=%q, want k06.txt", page3.Contents[0].Key)
	}
}

// TestListObjectsV2StartAfter pins start-after as a strict-greater
// bound: passing "k02" should yield k03 first, not k02 itself.
func TestListObjectsV2StartAfter(t *testing.T) {
	_, handler, mount, cleanup := newTestServer(t)
	defer cleanup()

	for i := 0; i < 5; i++ {
		k := fmt.Sprintf("k%02d.txt", i)
		body := []byte("v")
		req := httptest.NewRequest(http.MethodPut, "/"+mount+"/"+k, bytes.NewReader(body))
		req.Host = "example.com"
		signRequestPayload(req, body)
		handler.ServeHTTP(httptest.NewRecorder(), req)
	}

	req := httptest.NewRequest(http.MethodGet, "/"+mount+"?list-type=2&start-after=k01.txt", nil)
	req.Host = "example.com"
	signRequestPayload(req, nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	var res listBucketResultV2
	_ = xml.Unmarshal(rec.Body.Bytes(), &res)
	wantFirst := "k02.txt"
	if len(res.Contents) == 0 || res.Contents[0].Key != wantFirst {
		t.Fatalf("first key=%q, want %q (start-after must be strict-greater)", firstKey(res.Contents), wantFirst)
	}
	if res.StartAfter != "k01.txt" {
		t.Errorf("StartAfter=%q, want k01.txt", res.StartAfter)
	}
}

// TestListObjectsV2RejectDelimiter: M1 doesn't support delimiter;
// requests including one are rejected with NotImplemented so the
// client fails loudly.
func TestListObjectsV2RejectDelimiter(t *testing.T) {
	_, handler, mount, cleanup := newTestServer(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodGet, "/"+mount+"?list-type=2&delimiter=/", nil)
	req.Host = "example.com"
	signRequestPayload(req, nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("delimiter status=%d, want 501", rec.Code)
	}
}

// TestListObjectsV2InvalidMaxKeys: negative or non-integer values
// surface as InvalidArgument.
func TestListObjectsV2InvalidMaxKeys(t *testing.T) {
	_, handler, mount, cleanup := newTestServer(t)
	defer cleanup()

	for _, v := range []string{"-1", "abc"} {
		req := httptest.NewRequest(http.MethodGet, "/"+mount+"?list-type=2&max-keys="+v, nil)
		req.Host = "example.com"
		signRequestPayload(req, nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("max-keys=%q status=%d, want 400", v, rec.Code)
		}
	}
}

// TestListObjectsV2EmptyBucket: a bucket with no objects yields
// KeyCount=0 and IsTruncated=false.
func TestListObjectsV2EmptyBucket(t *testing.T) {
	_, handler, mount, cleanup := newTestServer(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodGet, "/"+mount+"?list-type=2", nil)
	req.Host = "example.com"
	signRequestPayload(req, nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("empty LIST status=%d body=%s", rec.Code, rec.Body.String())
	}
	var res listBucketResultV2
	_ = xml.Unmarshal(rec.Body.Bytes(), &res)
	if res.KeyCount != 0 {
		t.Errorf("empty KeyCount=%d", res.KeyCount)
	}
	if res.IsTruncated {
		t.Errorf("empty IsTruncated=true")
	}
}

// TestListObjectsV2NoBucket: GET on a non-existent bucket returns
// NoSuchBucket (already covered by handleBucketOp; this catches a
// regression if list dispatch bypasses the bucket-existence check).
func TestListObjectsV2NoBucket(t *testing.T) {
	_, handler, _, cleanup := newTestServer(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodGet, "/no-such?list-type=2", nil)
	req.Host = "example.com"
	signRequestPayload(req, nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing bucket status=%d, want 404", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "NoSuchBucket") {
		t.Errorf("body should mention NoSuchBucket: %q", rec.Body.String())
	}
}

// TestListObjectsV2EncodingTypeURL: encoding-type=url percent-encodes
// the Key/Prefix/StartAfter response fields. Useful for clients that
// want binary-safe listing of keys with non-ASCII bytes.
func TestListObjectsV2EncodingTypeURL(t *testing.T) {
	_, handler, mount, cleanup := newTestServer(t)
	defer cleanup()

	body := []byte("x")
	req := httptest.NewRequest(http.MethodPut, "/"+mount+"/space%20key.txt", bytes.NewReader(body))
	req.Host = "example.com"
	signRequestPayload(req, body)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT status=%d body=%s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/"+mount+"?list-type=2&encoding-type=url", nil)
	req.Host = "example.com"
	signRequestPayload(req, nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("LIST status=%d body=%s", rec.Code, rec.Body.String())
	}
	var res listBucketResultV2
	_ = xml.Unmarshal(rec.Body.Bytes(), &res)
	if res.EncodingType != "url" {
		t.Errorf("EncodingType=%q, want url", res.EncodingType)
	}
	// The literal space in "space key.txt" (sent as %20 in URL,
	// which net/http decodes to literal space in r.URL.Path) ends
	// up encoded as %20 in the response key.
	if len(res.Contents) != 1 {
		t.Fatalf("KeyCount=%d, want 1", len(res.Contents))
	}
	if !strings.Contains(res.Contents[0].Key, "%20") {
		t.Errorf("Key should be url-encoded, got %q", res.Contents[0].Key)
	}
}

// TestListObjectsV2RejectFetchOwner: fetch-owner=true is not
// supported in M1 (no per-object owner model). The request is
// rejected with NotImplemented.
func TestListObjectsV2RejectFetchOwner(t *testing.T) {
	_, handler, mount, cleanup := newTestServer(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodGet, "/"+mount+"?list-type=2&fetch-owner=true", nil)
	req.Host = "example.com"
	signRequestPayload(req, nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("fetch-owner status=%d, want 501", rec.Code)
	}
}

// TestListObjectsV2MaxKeysZero: max-keys=0 returns an empty page,
// not an infinite loop.
func TestListObjectsV2MaxKeysZero(t *testing.T) {
	_, handler, mount, cleanup := newTestServer(t)
	defer cleanup()

	body := []byte("x")
	req := httptest.NewRequest(http.MethodPut, "/"+mount+"/k.txt", bytes.NewReader(body))
	req.Host = "example.com"
	signRequestPayload(req, body)
	handler.ServeHTTP(httptest.NewRecorder(), req)

	req = httptest.NewRequest(http.MethodGet, "/"+mount+"?list-type=2&max-keys=0", nil)
	req.Host = "example.com"
	signRequestPayload(req, nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("max-keys=0 status=%d body=%s", rec.Code, rec.Body.String())
	}
	var res listBucketResultV2
	_ = xml.Unmarshal(rec.Body.Bytes(), &res)
	if res.IsTruncated {
		t.Errorf("max-keys=0 IsTruncated=true; should be false (no progress possible)")
	}
	if res.NextContinuationToken != "" {
		t.Errorf("max-keys=0 NextContinuationToken=%q, should be empty", res.NextContinuationToken)
	}
	if res.KeyCount != 0 {
		t.Errorf("max-keys=0 KeyCount=%d, should be 0", res.KeyCount)
	}
}

// helpers

func keysOf(items []listObjectXML) []string {
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.Key
	}
	return out
}

func firstKey(items []listObjectXML) string {
	if len(items) == 0 {
		return ""
	}
	return items[0].Key
}
