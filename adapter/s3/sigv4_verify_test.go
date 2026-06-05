package s3

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// signRequest signs an http.Request the same way an AWS-SDK client
// would, so verifyRequest can be exercised end-to-end. Used only by
// tests in this package — no external SDK dependency.
//
// payloadHash MUST be the value to embed in x-amz-content-sha256:
//   - sigUnsignedBody for an unsigned body
//   - sigChunkAlgo for streaming chunks (caller frames the body)
//   - lowercase hex SHA-256 of the body otherwise
func signRequest(req *http.Request, accessKey, secretKey, region string, payloadHash string, t time.Time) {
	timestamp := t.UTC().Format("20060102T150405Z")
	date := timestamp[:8]
	req.Header.Set("X-Amz-Date", timestamp)
	req.Header.Set("X-Amz-Content-Sha256", payloadHash)
	if req.Header.Get("Host") == "" && req.Host != "" {
		// net/http strips Host into req.Host on serve. For client-
		// side signing we still need it in the headers.
		req.Header.Set("Host", req.Host)
	}

	signedHeaders := []string{"host", "x-amz-content-sha256", "x-amz-date"}
	canonical, signedJoined := canonicalRequest(req.Method, req.URL.Path, req.URL.RawQuery, headersAsMap(req), signedHeaders, payloadHash)
	scopeStr := scope(date, region, sigService)
	sts := stringToSign(timestamp, scopeStr, canonical)
	signingKey := derivedSigningKey(secretKey, date, region, sigService)
	sig := signature(signingKey, sts)

	authValue := fmt.Sprintf("%s Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		sigAlgorithm,
		accessKey, scopeStr,
		signedJoined,
		sig,
	)
	req.Header.Set("Authorization", authValue)
}

func newTestAuthConfig() authConfig {
	return authConfig{
		Region: "us-east-1",
		SecretForKeyID: func(keyID string) (string, bool) {
			if keyID == "AKIAIOSFODNN7EXAMPLE" {
				return "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY", true
			}
			return "", false
		},
	}
}

// TestVerifyRequestHeaderModeRoundTrip: sign, verify, succeed.
// Catches any deviation from canonical-request construction.
func TestVerifyRequestHeaderModeRoundTrip(t *testing.T) {
	body := []byte("hello world")
	hash := sha256.Sum256(body)
	hashHex := hex.EncodeToString(hash[:])

	req := httptest.NewRequest(http.MethodPut, "/bucket/key", bytes.NewReader(body))
	req.Host = "example.com"
	signRequest(req, "AKIAIOSFODNN7EXAMPLE", "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY", "us-east-1", hashHex, time.Now())

	res, sigErr := verifyRequest(req, newTestAuthConfig())
	if sigErr != nil {
		t.Fatalf("verifyRequest: %s", sigErr.Error())
	}
	if res.AccessKeyID != "AKIAIOSFODNN7EXAMPLE" {
		t.Errorf("AccessKeyID = %q", res.AccessKeyID)
	}
	got, err := io.ReadAll(res.BodyReader)
	if err != nil {
		t.Fatalf("body read: %v", err)
	}
	if !bytes.Equal(got, body) {
		t.Errorf("body = %q, want %q", got, body)
	}
}

// TestVerifyRejectsUnknownAccessKey: lookup returns false → error.
func TestVerifyRejectsUnknownAccessKey(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = "example.com"
	signRequest(req, "AKIAUNKNOWN", "doesnotmatter", "us-east-1", sigEmptyBodyHash, time.Now())

	_, sigErr := verifyRequest(req, newTestAuthConfig())
	if sigErr == nil || sigErr.Code != errInvalidAccessKeyID {
		t.Fatalf("want %s, got %v", errInvalidAccessKeyID, sigErr)
	}
}

// TestVerifyRejectsRegionMismatch: credential scope region != configured.
func TestVerifyRejectsRegionMismatch(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = "example.com"
	signRequest(req, "AKIAIOSFODNN7EXAMPLE", "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY", "eu-west-1", sigEmptyBodyHash, time.Now())

	_, sigErr := verifyRequest(req, newTestAuthConfig())
	if sigErr == nil || sigErr.Code != errAuthorizationHeaderError {
		t.Fatalf("want %s, got %v", errAuthorizationHeaderError, sigErr)
	}
}

// TestVerifyRejectsClockSkew: sign with timestamp 30 min in past.
func TestVerifyRejectsClockSkew(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = "example.com"
	signRequest(req, "AKIAIOSFODNN7EXAMPLE", "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY", "us-east-1",
		sigEmptyBodyHash, time.Now().Add(-30*time.Minute))

	_, sigErr := verifyRequest(req, newTestAuthConfig())
	if sigErr == nil || sigErr.Code != errRequestTimeTooSkewed {
		t.Fatalf("want %s, got %v", errRequestTimeTooSkewed, sigErr)
	}
}

// TestVerifyRejectsTamperedSignature: flip one byte of the signature
// — must fail with errSignatureDoesNotMatch.
func TestVerifyRejectsTamperedSignature(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = "example.com"
	signRequest(req, "AKIAIOSFODNN7EXAMPLE", "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY", "us-east-1", sigEmptyBodyHash, time.Now())

	auth := req.Header.Get("Authorization")
	tampered := strings.Replace(auth, "Signature=", "Signature=ff", 1)
	req.Header.Set("Authorization", tampered)

	_, sigErr := verifyRequest(req, newTestAuthConfig())
	if sigErr == nil || sigErr.Code != errSignatureDoesNotMatch {
		t.Fatalf("want %s, got %v", errSignatureDoesNotMatch, sigErr)
	}
}

// TestVerifyRejectsMissingHost: SignedHeaders must include host.
// We construct a request with NO Authorization header (so we don't
// trigger the required-headers check pre-Authorization parse) and
// craft a header with malformed SignedHeaders.
func TestVerifyRejectsMissingHostInSignedHeaders(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = "example.com"
	req.Header.Set("X-Amz-Date", time.Now().UTC().Format("20060102T150405Z"))
	req.Header.Set("X-Amz-Content-Sha256", sigEmptyBodyHash)
	req.Header.Set("Authorization",
		fmt.Sprintf("%s Credential=%s/%s/us-east-1/s3/aws4_request, SignedHeaders=x-amz-date;x-amz-content-sha256, Signature=00",
			sigAlgorithm,
			"AKIAIOSFODNN7EXAMPLE",
			time.Now().UTC().Format("20060102")))

	_, sigErr := verifyRequest(req, newTestAuthConfig())
	if sigErr == nil || sigErr.Code != errAuthorizationHeaderError {
		t.Fatalf("want %s, got %v", errAuthorizationHeaderError, sigErr)
	}
}

// TestVerifyRejectsBodyHashMismatch: payload hash header doesn't match
// actual body bytes.
func TestVerifyRejectsBodyHashMismatch(t *testing.T) {
	body := []byte("real body")
	wrongHash := sha256.Sum256([]byte("DIFFERENT"))

	req := httptest.NewRequest(http.MethodPut, "/bucket/key", bytes.NewReader(body))
	req.Host = "example.com"
	signRequest(req, "AKIAIOSFODNN7EXAMPLE", "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY", "us-east-1",
		hex.EncodeToString(wrongHash[:]), time.Now())

	_, sigErr := verifyRequest(req, newTestAuthConfig())
	if sigErr == nil || sigErr.Code != errBadDigest {
		t.Fatalf("want %s, got %v", errBadDigest, sigErr)
	}
}

// TestVerifyAcceptsUnsignedPayload: x-amz-content-sha256=UNSIGNED-PAYLOAD
// + a body must succeed without consuming the body.
func TestVerifyAcceptsUnsignedPayload(t *testing.T) {
	body := []byte("payload")
	req := httptest.NewRequest(http.MethodPut, "/bucket/key", bytes.NewReader(body))
	req.Host = "example.com"
	signRequest(req, "AKIAIOSFODNN7EXAMPLE", "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY", "us-east-1", sigUnsignedBody, time.Now())

	res, sigErr := verifyRequest(req, newTestAuthConfig())
	if sigErr != nil {
		t.Fatalf("verifyRequest: %s", sigErr.Error())
	}
	got, err := io.ReadAll(res.BodyReader)
	if err != nil {
		t.Fatalf("body read: %v", err)
	}
	if !bytes.Equal(got, body) {
		t.Errorf("body = %q, want %q", got, body)
	}
}

func TestVerifyAcceptsStreamingUnsignedPayloadTrailer(t *testing.T) {
	body := []byte("hello world")
	encoded := encodeUnsignedChunks([][]byte{[]byte("hello "), []byte("world")}, "x-amz-checksum-crc32:AAAAAA==")
	req := httptest.NewRequest(http.MethodPut, "/bucket/key", bytes.NewReader(encoded))
	req.Host = "example.com"
	req.Header.Set("Content-Encoding", "aws-chunked")
	req.Header.Set("x-amz-trailer", "x-amz-checksum-crc32")
	req.Header.Set("x-amz-decoded-content-length", fmt.Sprintf("%d", len(body)))
	signRequest(req, "AKIAIOSFODNN7EXAMPLE", "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY", "us-east-1", sigUnsignedTrailer, time.Now())

	res, sigErr := verifyRequest(req, newTestAuthConfig())
	if sigErr != nil {
		t.Fatalf("verifyRequest: %s", sigErr.Error())
	}
	got, err := io.ReadAll(res.BodyReader)
	if err != nil {
		t.Fatalf("body read: %v", err)
	}
	if !bytes.Equal(got, body) {
		t.Fatalf("body = %q, want %q", got, body)
	}
}

func TestAcceptedPayloadHashSentinels(t *testing.T) {
	hexHash := strings.Repeat("a", 64)
	for _, payloadHash := range []string{
		sigUnsignedBody,
		sigUnsignedTrailer,
		sigChunkAlgo,
		sigChunkAlgoTrailer,
		hexHash,
	} {
		if !isAcceptedPayloadHash(payloadHash) {
			t.Errorf("isAcceptedPayloadHash(%q) = false, want true", payloadHash)
		}
	}

	if isAcceptedPayloadHash("STREAMING-UNSIGNED-PAYLOAD") {
		t.Errorf("unsupported streaming sentinel was accepted")
	}
}

// TestVerifyMissingAuthorization: no Authorization header AND no
// X-Amz-Algorithm query param → AccessDenied.
func TestVerifyMissingAuthorization(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = "example.com"
	_, sigErr := verifyRequest(req, newTestAuthConfig())
	if sigErr == nil || sigErr.Code != errAccessDenied {
		t.Fatalf("want %s, got %v", errAccessDenied, sigErr)
	}
}

// TestSplitAuthHeaderTolerantOrder: AWS allows the three fields in
// any order within the header value.
func TestSplitAuthHeaderTolerantOrder(t *testing.T) {
	body := "SignedHeaders=host;x-amz-date, Credential=AKIA/20230101/us-east-1/s3/aws4_request, Signature=abc"
	cred, sh, sig, ok := splitAuthHeader(body)
	if !ok {
		t.Fatalf("splitAuthHeader rejected: %q", body)
	}
	if cred != "AKIA/20230101/us-east-1/s3/aws4_request" {
		t.Errorf("credential = %q", cred)
	}
	if sh != "host;x-amz-date" {
		t.Errorf("signed-headers = %q", sh)
	}
	if sig != "abc" {
		t.Errorf("signature = %q", sig)
	}
}

// TestParseCredentialRejectsBadShape: explicit cases for each
// validation rule.
func TestParseCredentialRejectsBadShape(t *testing.T) {
	cases := []struct{ name, in string }{
		{"too-few-fields", "AKIA/20230101/us-east-1/s3"},
		{"bad-terminator", "AKIA/20230101/us-east-1/s3/aws3_request"},
		{"short-date", "AKIA/2023/us-east-1/s3/aws4_request"},
	}
	for _, tc := range cases {
		_, err := parseCredential(tc.in)
		if err == nil {
			t.Errorf("%s: parseCredential(%q) = nil error", tc.name, tc.in)
		}
	}
}

// TestVerifyQueryModeRoundTrip: sign a presigned URL, verify.
func TestVerifyQueryModeRoundTrip(t *testing.T) {
	now := time.Now().UTC()
	timestamp := now.Format("20060102T150405Z")
	date := timestamp[:8]
	scopeStr := scope(date, "us-east-1", sigService)

	// Build presigned URL by hand.
	q := fmt.Sprintf("X-Amz-Algorithm=%s&X-Amz-Credential=%s&X-Amz-Date=%s&X-Amz-Expires=%d&X-Amz-SignedHeaders=host",
		sigAlgorithm,
		urlEncode("AKIAIOSFODNN7EXAMPLE/"+scopeStr),
		timestamp,
		3600,
	)
	req := httptest.NewRequest(http.MethodGet, "/bucket/key?"+q, nil)
	req.Host = "example.com"

	canonical, _ := canonicalRequest(http.MethodGet, "/bucket/key", req.URL.RawQuery, headersAsMap(req), []string{"host"}, sigUnsignedBody)
	sts := stringToSign(timestamp, scopeStr, canonical)
	signingKey := derivedSigningKey("wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY", date, "us-east-1", sigService)
	sig := signature(signingKey, sts)
	req.URL.RawQuery += "&X-Amz-Signature=" + sig

	res, sigErr := verifyRequest(req, newTestAuthConfig())
	if sigErr != nil {
		t.Fatalf("verifyRequest: %s", sigErr.Error())
	}
	if res.AccessKeyID != "AKIAIOSFODNN7EXAMPLE" {
		t.Errorf("AccessKeyID = %q", res.AccessKeyID)
	}
}

// TestVerifyQueryModeRejectsExpired: presigned URL whose
// X-Amz-Date+X-Amz-Expires is in the past.
func TestVerifyQueryModeRejectsExpired(t *testing.T) {
	past := time.Now().UTC().Add(-2 * time.Hour)
	timestamp := past.Format("20060102T150405Z")
	date := timestamp[:8]
	scopeStr := scope(date, "us-east-1", sigService)

	q := fmt.Sprintf("X-Amz-Algorithm=%s&X-Amz-Credential=%s&X-Amz-Date=%s&X-Amz-Expires=%d&X-Amz-SignedHeaders=host&X-Amz-Signature=00",
		sigAlgorithm,
		urlEncode("AKIAIOSFODNN7EXAMPLE/"+scopeStr),
		timestamp,
		60, // expires in 60s, so 2h ago + 60s is way in the past
	)
	req := httptest.NewRequest(http.MethodGet, "/bucket/key?"+q, nil)
	req.Host = "example.com"

	_, sigErr := verifyRequest(req, newTestAuthConfig())
	if sigErr == nil || sigErr.Code != errAccessDenied {
		t.Fatalf("want %s, got %v", errAccessDenied, sigErr)
	}
}

// TestVerifyQueryModeRejectsExcessiveExpires: cap is 7 days.
func TestVerifyQueryModeRejectsExcessiveExpires(t *testing.T) {
	now := time.Now().UTC()
	timestamp := now.Format("20060102T150405Z")
	date := timestamp[:8]
	scopeStr := scope(date, "us-east-1", sigService)
	q := fmt.Sprintf("X-Amz-Algorithm=%s&X-Amz-Credential=%s&X-Amz-Date=%s&X-Amz-Expires=%d&X-Amz-SignedHeaders=host&X-Amz-Signature=00",
		sigAlgorithm,
		urlEncode("AKIAIOSFODNN7EXAMPLE/"+scopeStr),
		timestamp,
		maxPresignExpires+1,
	)
	req := httptest.NewRequest(http.MethodGet, "/bucket/key?"+q, nil)
	req.Host = "example.com"

	_, sigErr := verifyRequest(req, newTestAuthConfig())
	if sigErr == nil || sigErr.Code != errAuthorizationHeaderError {
		t.Fatalf("want %s, got %v", errAuthorizationHeaderError, sigErr)
	}
}

// TestChunkedDecoderRoundTrip: build a chunked body, decode, verify
// bytes match. Catches per-chunk-signature derivation bugs.
func TestChunkedDecoderRoundTrip(t *testing.T) {
	secret := "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY"
	region := "us-east-1"
	timestamp := "20230101T000000Z"
	date := "20230101"
	scopeStr := scope(date, region, sigService)
	signingKey := derivedSigningKey(secret, date, region, sigService)
	seedSig := "deadbeef"

	// Two non-empty chunks + the final 0-length chunk.
	chunks := [][]byte{[]byte("hello "), []byte("world")}
	encoded, lastSig := encodeChunks(t, chunks, signingKey, seedSig, timestamp, scopeStr)

	dec := newChunkedDecoder(io.NopCloser(bytes.NewReader(encoded)), signingKey, seedSig, timestamp, scopeStr)
	got, err := readAllChunked(dec)
	if err != nil {
		t.Fatalf("decoder: %v", err)
	}
	if string(got) != "hello world" {
		t.Fatalf("decoded = %q", got)
	}
	_ = lastSig
}

// TestChunkedDecoderRejectsBadSignature: tampering with one byte of
// a chunk body must surface as errChunkSignatureMismatch.
func TestChunkedDecoderRejectsBadSignature(t *testing.T) {
	secret := "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY"
	region := "us-east-1"
	timestamp := "20230101T000000Z"
	date := "20230101"
	scopeStr := scope(date, region, sigService)
	signingKey := derivedSigningKey(secret, date, region, sigService)
	seedSig := "deadbeef"

	encoded, _ := encodeChunks(t, [][]byte{[]byte("hello")}, signingKey, seedSig, timestamp, scopeStr)
	// Flip a byte INSIDE the chunk body. The chunk header (with
	// hex size + signature) sits in the first ~75 bytes; the body
	// follows after CRLF. Find the first CRLF and corrupt the byte
	// after.
	idx := bytes.Index(encoded, []byte("\r\n"))
	if idx < 0 {
		t.Fatal("encoded payload missing CRLF")
	}
	encoded[idx+2] ^= 0xff

	dec := newChunkedDecoder(io.NopCloser(bytes.NewReader(encoded)), signingKey, seedSig, timestamp, scopeStr)
	_, err := readAllChunked(dec)
	if !errors.Is(err, errChunkSignatureMismatch) {
		t.Fatalf("want errChunkSignatureMismatch, got %v", err)
	}
}

func TestUnsignedChunkedDecoderRoundTripWithTrailer(t *testing.T) {
	body := []byte("hello world")
	encoded := encodeUnsignedChunks([][]byte{[]byte("hello "), []byte("world")}, "x-amz-checksum-crc32:AAAAAA==")

	dec := newUnsignedChunkedDecoder(io.NopCloser(bytes.NewReader(encoded)))
	got, err := io.ReadAll(dec)
	if err != nil {
		t.Fatalf("decoder: %v", err)
	}
	if !bytes.Equal(got, body) {
		t.Fatalf("decoded = %q, want %q", got, body)
	}
}

func TestUnsignedChunkedDecoderRejectsBadChunkCRLF(t *testing.T) {
	encoded := []byte("5\r\nhello\n0\r\n\r\n")

	dec := newUnsignedChunkedDecoder(io.NopCloser(bytes.NewReader(encoded)))
	_, err := io.ReadAll(dec)
	if !errors.Is(err, errChunkTrailerMismatch) {
		t.Fatalf("want errChunkTrailerMismatch, got %v", err)
	}
}

func TestChunkedDecoderConsumesTrailerHeaders(t *testing.T) {
	secret := "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY"
	region := "us-east-1"
	timestamp := "20230101T000000Z"
	date := "20230101"
	scopeStr := scope(date, region, sigService)
	signingKey := derivedSigningKey(secret, date, region, sigService)
	seedSig := "deadbeef"

	encoded, _ := encodeChunks(t, [][]byte{[]byte("hello")}, signingKey, seedSig, timestamp, scopeStr)
	encoded = bytes.TrimSuffix(encoded, []byte("\r\n"))
	encoded = append(encoded, []byte("x-amz-checksum-crc32:AAAAAA==\r\n\r\n")...)

	dec := newChunkedDecoder(io.NopCloser(bytes.NewReader(encoded)), signingKey, seedSig, timestamp, scopeStr)
	got, err := readAllChunked(dec)
	if err != nil {
		t.Fatalf("decoder: %v", err)
	}
	if string(got) != "hello" {
		t.Fatalf("decoded = %q", got)
	}
}

// urlEncode is a thin wrapper using net/url's QueryEscape so query
// parameters in tests are valid. Not exported — tests-only.
func urlEncode(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z',
			c >= 'a' && c <= 'z',
			c >= '0' && c <= '9',
			c == '-' || c == '.' || c == '_' || c == '~':
			out = append(out, c)
		default:
			out = append(out, '%')
			const hex = "0123456789ABCDEF"
			out = append(out, hex[c>>4])
			out = append(out, hex[c&0x0F])
		}
	}
	return string(out)
}

func encodeUnsignedChunks(chunks [][]byte, trailerLines ...string) []byte {
	var buf bytes.Buffer
	for _, body := range chunks {
		fmt.Fprintf(&buf, "%x\r\n", len(body))
		buf.Write(body)
		buf.WriteString("\r\n")
	}
	buf.WriteString("0\r\n")
	for _, line := range trailerLines {
		buf.WriteString(line)
		buf.WriteString("\r\n")
	}
	buf.WriteString("\r\n")
	return buf.Bytes()
}

// encodeChunks frames the AWS streaming-chunked payload format from
// a sequence of chunk payloads. Returns the encoded bytes plus the
// final chunk's signature (handy for chained tests).
func encodeChunks(t *testing.T, chunks [][]byte, signingKey []byte, seedSig, timestamp, scopeStr string) ([]byte, string) {
	t.Helper()
	var buf bytes.Buffer
	prevSig := seedSig
	for _, body := range chunks {
		chunkHash := sha256.Sum256(body)
		sts := strings.Join([]string{
			chunkPayloadAlgo,
			timestamp,
			scopeStr,
			prevSig,
			emptySHA256Hex,
			hex.EncodeToString(chunkHash[:]),
		}, "\n")
		sig := signature(signingKey, sts)
		fmt.Fprintf(&buf, "%x;chunk-signature=%s\r\n", len(body), sig)
		buf.Write(body)
		buf.WriteString("\r\n")
		prevSig = sig
	}
	// Final 0-length chunk.
	emptyHash := sha256.Sum256(nil)
	sts := strings.Join([]string{
		chunkPayloadAlgo,
		timestamp,
		scopeStr,
		prevSig,
		emptySHA256Hex,
		hex.EncodeToString(emptyHash[:]),
	}, "\n")
	sig := signature(signingKey, sts)
	fmt.Fprintf(&buf, "0;chunk-signature=%s\r\n\r\n", sig)
	return buf.Bytes(), sig
}
