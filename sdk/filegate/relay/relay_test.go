package relay

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCopyResponseMirrorsStatusHeadersAndBody(t *testing.T) {
	src := &http.Response{
		StatusCode: http.StatusTeapot,
		Header:     http.Header{"X-Custom": {"a", "b"}, "Content-Type": {"text/plain"}},
		Body:       io.NopCloser(strings.NewReader("relayed body")),
	}
	dst := httptest.NewRecorder()

	n, err := CopyResponse(dst, src)
	if err != nil {
		t.Fatalf("CopyResponse: %v", err)
	}
	if n != int64(len("relayed body")) {
		t.Fatalf("copied n=%d, want %d", n, len("relayed body"))
	}
	res := dst.Result()
	if res.StatusCode != http.StatusTeapot {
		t.Fatalf("status=%d, want %d", res.StatusCode, http.StatusTeapot)
	}
	if got := res.Header.Get("Content-Type"); got != "text/plain" {
		t.Fatalf("content-type=%q", got)
	}
	if got := res.Header.Values("X-Custom"); len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("X-Custom values=%v", got)
	}
	body, _ := io.ReadAll(res.Body)
	if string(body) != "relayed body" {
		t.Fatalf("body=%q", body)
	}
}

type closingReader struct {
	io.Reader
	closed bool
}

func (c *closingReader) Close() error {
	c.closed = true
	return nil
}

func TestCopyResponseClosesUpstreamBody(t *testing.T) {
	body := &closingReader{Reader: strings.NewReader("x")}
	src := &http.Response{StatusCode: 200, Header: http.Header{}, Body: body}
	if _, err := CopyResponse(httptest.NewRecorder(), src); err != nil {
		t.Fatalf("CopyResponse: %v", err)
	}
	if !body.closed {
		t.Fatal("upstream body was not closed")
	}
}

func TestCopyResponseRejectsNil(t *testing.T) {
	if _, err := CopyResponse(nil, &http.Response{Body: http.NoBody}); err == nil {
		t.Fatal("nil dst should error")
	}
	if _, err := CopyResponse(httptest.NewRecorder(), nil); err == nil {
		t.Fatal("nil src should error")
	}
}
