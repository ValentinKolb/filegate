//go:build linux

package s3

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestPutObjectAcceptsStreamingUnsignedPayloadTrailer(t *testing.T) {
	_, handler, mount, cleanup := newTestServer(t)
	defer cleanup()

	body := []byte("stored through unsigned aws-chunked trailer")
	encoded := encodeUnsignedChunks([][]byte{[]byte("stored through "), []byte("unsigned aws-chunked trailer")}, "x-amz-checksum-crc32:AAAAAA==")
	req := httptest.NewRequest(http.MethodPut, "/"+mount+"/unsigned-trailer.txt", bytes.NewReader(encoded))
	req.Host = "example.com"
	req.Header.Set("Content-Encoding", "aws-chunked")
	req.Header.Set("x-amz-trailer", "x-amz-checksum-crc32")
	req.Header.Set("x-amz-decoded-content-length", fmt.Sprintf("%d", len(body)))
	signRequest(req, testAccessKey, testSecretKey, testRegion, sigUnsignedTrailer, time.Now())

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT status=%d body=%s", rec.Code, rec.Body.String())
	}

	gReq := httptest.NewRequest(http.MethodGet, "/"+mount+"/unsigned-trailer.txt", nil)
	gReq.Host = "example.com"
	signRequestPayload(gReq, nil)
	gRec := httptest.NewRecorder()
	handler.ServeHTTP(gRec, gReq)
	if gRec.Code != http.StatusOK {
		t.Fatalf("GET status=%d body=%s", gRec.Code, gRec.Body.String())
	}
	if !bytes.Equal(gRec.Body.Bytes(), body) {
		t.Fatalf("GET body=%q, want %q", gRec.Body.Bytes(), body)
	}
}
