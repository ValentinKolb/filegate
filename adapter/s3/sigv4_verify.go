package s3

import (
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// sigV4Result is the parsed + verified context attached to a request
// after the verifier returns success. Handlers consult bodyReader for
// the (possibly chunk-decoded) request body — they must NOT read
// r.Body directly.
type sigV4Result struct {
	AccessKeyID string
	Region      string
	Service     string
	BodyReader  io.ReadCloser
}

// sigV4VerifyError is the typed error the verifier returns when a
// request fails authentication. The errorCode maps to an S3-shaped
// XML response via writeError.
type sigV4VerifyError struct {
	Code    errorCode
	Message string
}

func (e *sigV4VerifyError) Error() string { return string(e.Code) + ": " + e.Message }

func sigErr(code errorCode, msgFmt string, args ...any) *sigV4VerifyError {
	return &sigV4VerifyError{Code: code, Message: fmt.Sprintf(msgFmt, args...)}
}

// authConfig is what the verifier needs to know about the operator's
// configured credentials. M1 ships single-tenant: one access key, one
// secret. M3 swaps this for a multi-tenant lookup — the verifier
// itself only needs the secret-for-key callback, so it doesn't change.
type authConfig struct {
	Region        string
	SecretForKeyID func(keyID string) (string, bool)
}

// Constants AWS uses in the SigV4 wire format.
const (
	sigAlgorithm     = "AWS4-HMAC-SHA256"
	sigService       = "s3"
	sigChunkAlgo     = "STREAMING-AWS4-HMAC-SHA256-PAYLOAD"
	sigUnsignedBody  = "UNSIGNED-PAYLOAD"
	sigEmptyBodyHash = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" // sha256("")
	sigQueryAlgoKey  = "X-Amz-Algorithm"
	// AWS allows up to 7 days (604800 seconds) for query-mode expires.
	maxPresignExpires = 7 * 24 * 3600
	// Clock skew tolerance for x-amz-date (header & query mode). AWS
	// uses 15 minutes — same here.
	maxClockSkew = 15 * time.Minute
)

// verifyRequest is the entry-point. Returns (*sigV4Result, nil) on
// success. On failure returns (nil, *sigV4VerifyError) with a code
// the caller maps via writeError. Both modes (header + query) require
// a non-empty Host header — a request without one cannot meaningfully
// be signed, and AWS rejects it.
//
// Two modes are supported:
//   * header-mode:  Authorization: AWS4-HMAC-SHA256 Credential=...
//                                  SignedHeaders=...
//                                  Signature=...
//   * query-mode (presigned):       X-Amz-Algorithm=AWS4-HMAC-SHA256
//                                  &X-Amz-Credential=...
//                                  &X-Amz-Date=...
//                                  &X-Amz-Expires=...
//                                  &X-Amz-SignedHeaders=...
//                                  &X-Amz-Signature=...
//
// Streaming-chunked payload (header-mode with
// x-amz-content-sha256=STREAMING-AWS4-HMAC-SHA256-PAYLOAD) is
// detected here; the resulting BodyReader is a chunked-decoder
// that verifies per-chunk signatures as it streams.
func verifyRequest(r *http.Request, cfg authConfig) (*sigV4Result, *sigV4VerifyError) {
	if r.Host == "" {
		// SignedHeaders verification later catches "host" missing
		// from the SignedHeaders list, but if r.Host is itself
		// empty the canonical-headers block will sign an empty
		// Host value — reject up-front so the failure mode is
		// clearer than "signature mismatch".
		return nil, sigErr(errAuthorizationHeaderError, "missing Host header")
	}
	if r.URL.Query().Get(sigQueryAlgoKey) == sigAlgorithm {
		return verifyQueryMode(r, cfg)
	}
	auth := r.Header.Get("Authorization")
	if auth == "" {
		return nil, sigErr(errAccessDenied, "missing Authorization header")
	}
	if !strings.HasPrefix(auth, sigAlgorithm+" ") {
		return nil, sigErr(errAccessDenied, "unsupported authorization algorithm")
	}
	return verifyHeaderMode(r, cfg, strings.TrimPrefix(auth, sigAlgorithm+" "))
}

// peekAccessKey extracts the access key from a request WITHOUT
// running the full SigV4 verification. Used by the rate-limit
// pre-check so a throttled key short-circuits the request before
// the expensive body-binding step in verifyRequest. Returns "" if
// the request has no parseable access key (which means
// verifyRequest will reject it anyway, just a few microseconds
// later).
//
// Trust model: the access key is NOT authenticated at this point
// — anyone can spoof a request with any access key in the
// Authorization header. The implication is that a throttled
// key's bucket is consumable by anonymous traffic. That's an
// acceptable trade-off: real S3 throttles before signature
// verification too (you can't scale otherwise), and a key's
// existence can already be probed cheaply via the InvalidAccessKey
// vs. SignatureDoesNotMatch error split.
func peekAccessKey(r *http.Request) string {
	if v := r.URL.Query().Get("X-Amz-Credential"); v != "" {
		if cred, err := parseCredential(v); err == nil {
			return cred.AccessKey
		}
		return ""
	}
	auth := r.Header.Get("Authorization")
	if auth == "" {
		return ""
	}
	if !strings.HasPrefix(auth, sigAlgorithm+" ") {
		return ""
	}
	credentialRaw, _, _, ok := splitAuthHeader(strings.TrimPrefix(auth, sigAlgorithm+" "))
	if !ok {
		return ""
	}
	cred, err := parseCredential(credentialRaw)
	if err != nil {
		return ""
	}
	return cred.AccessKey
}

// verifyHeaderMode handles the standard Authorization-header path.
// The header value (after the algorithm prefix) is a comma-separated
// list of three fields: Credential, SignedHeaders, Signature.
func verifyHeaderMode(r *http.Request, cfg authConfig, authBody string) (*sigV4Result, *sigV4VerifyError) {
	credentialRaw, signedHeadersRaw, signatureRaw, ok := splitAuthHeader(authBody)
	if !ok {
		return nil, sigErr(errAuthorizationHeaderError, "malformed Authorization header")
	}
	cred, err := parseCredential(credentialRaw)
	if err != nil {
		return nil, sigErr(errAuthorizationHeaderError, "%s", err)
	}
	if cred.Service != sigService {
		return nil, sigErr(errAuthorizationHeaderError, "credential service must be %q, got %q", sigService, cred.Service)
	}
	if cred.Region != cfg.Region {
		return nil, sigErr(errAuthorizationHeaderError, "credential region %q does not match configured region %q", cred.Region, cfg.Region)
	}
	secret, ok := cfg.SecretForKeyID(cred.AccessKey)
	if !ok {
		return nil, sigErr(errInvalidAccessKeyID, "unknown access key %q", cred.AccessKey)
	}

	timestamp := r.Header.Get("X-Amz-Date")
	if timestamp == "" {
		return nil, sigErr(errAuthorizationHeaderError, "missing X-Amz-Date header")
	}
	if err := checkClockSkew(timestamp); err != nil {
		return nil, sigErr(errRequestTimeTooSkewed, "%s", err)
	}
	// Date in credential scope must match the date portion of the
	// timestamp (defends against scope/timestamp mismatch attacks).
	if !strings.HasPrefix(timestamp, cred.Date) {
		return nil, sigErr(errAuthorizationHeaderError, "X-Amz-Date %q doesn't match credential scope date %q", timestamp, cred.Date)
	}

	signedHeaders := strings.Split(signedHeadersRaw, ";")
	if err := requireMandatoryHeaders(signedHeaders, false); err != nil {
		return nil, sigErr(errAuthorizationHeaderError, "%s", err)
	}

	bodyHash := r.Header.Get("X-Amz-Content-Sha256")
	if bodyHash == "" {
		return nil, sigErr(errAuthorizationHeaderError, "missing X-Amz-Content-Sha256 header")
	}
	if !isAcceptedPayloadHash(bodyHash) {
		return nil, sigErr(errInvalidArgument, "X-Amz-Content-Sha256 must be UNSIGNED-PAYLOAD, STREAMING-AWS4-HMAC-SHA256-PAYLOAD, or a 64-char hex SHA-256")
	}

	// Verify the SIGNATURE first using the CLAIMED body hash. Only
	// after the signature checks out do we touch the body itself —
	// that ordering prevents a known-access-key but bad-signature
	// attacker from triggering an unbounded io.ReadAll DoS via the
	// hex-digest body-verification path.
	scopeStr := scope(cred.Date, cred.Region, cred.Service)
	// Embed the bodyHash value verbatim — AWS canonical request uses
	// exactly what the client put in x-amz-content-sha256 (case-
	// sensitive). Sentinels stay uppercase; hex digests stay
	// whatever case the client chose.
	canonical, _ := canonicalRequest(r.Method, r.URL.Path, r.URL.RawQuery, headersAsMap(r), signedHeaders, bodyHash)
	stringToSignVal := stringToSign(timestamp, scopeStr, canonical)
	signingKey := derivedSigningKey(secret, cred.Date, cred.Region, cred.Service)
	expected := signature(signingKey, stringToSignVal)
	if subtle.ConstantTimeCompare([]byte(expected), []byte(signatureRaw)) != 1 {
		return nil, sigErr(errSignatureDoesNotMatch, "computed signature does not match supplied")
	}

	// Signature OK. Now bind the body — UNSIGNED-PAYLOAD passes
	// through; the chunked sentinel wraps in a chunked decoder; a
	// hex-digest reads the body and compares.
	bodyReader, sigErrV := bindBody(r, bodyHash, signingKey, signatureRaw, timestamp, scopeStr)
	if sigErrV != nil {
		return nil, sigErrV
	}

	return &sigV4Result{
		AccessKeyID: cred.AccessKey,
		Region:      cred.Region,
		Service:     cred.Service,
		BodyReader:  bodyReader,
	}, nil
}

// isAcceptedPayloadHash returns true for the three valid forms of
// X-Amz-Content-Sha256: the UNSIGNED-PAYLOAD sentinel, the streaming-
// chunked sentinel, or a 64-char hex SHA-256 digest. Used as a
// pre-check before signature verification so the canonical-request
// embedding has a known shape.
func isAcceptedPayloadHash(s string) bool {
	if s == sigUnsignedBody || s == sigChunkAlgo {
		return true
	}
	if len(s) != 64 {
		return false
	}
	return isHex(s)
}

// verifyQueryMode handles presigned URLs where all SigV4 parameters
// are in the query string. Mandatory parameters: X-Amz-Algorithm,
// X-Amz-Credential, X-Amz-Date, X-Amz-Expires, X-Amz-SignedHeaders,
// X-Amz-Signature. The body is treated as UNSIGNED-PAYLOAD because
// presigned URLs sign metadata only.
func verifyQueryMode(r *http.Request, cfg authConfig) (*sigV4Result, *sigV4VerifyError) {
	q := r.URL.Query()
	credRaw := q.Get("X-Amz-Credential")
	timestamp := q.Get("X-Amz-Date")
	expiresRaw := q.Get("X-Amz-Expires")
	signedHeadersRaw := q.Get("X-Amz-SignedHeaders")
	signatureRaw := q.Get("X-Amz-Signature")
	if credRaw == "" || timestamp == "" || expiresRaw == "" || signedHeadersRaw == "" || signatureRaw == "" {
		return nil, sigErr(errAuthorizationHeaderError, "missing query SigV4 parameter")
	}

	cred, err := parseCredential(credRaw)
	if err != nil {
		return nil, sigErr(errAuthorizationHeaderError, "%s", err)
	}
	if cred.Service != sigService {
		return nil, sigErr(errAuthorizationHeaderError, "credential service must be %q, got %q", sigService, cred.Service)
	}
	if cred.Region != cfg.Region {
		return nil, sigErr(errAuthorizationHeaderError, "credential region %q does not match configured region %q", cred.Region, cfg.Region)
	}
	secret, ok := cfg.SecretForKeyID(cred.AccessKey)
	if !ok {
		return nil, sigErr(errInvalidAccessKeyID, "unknown access key %q", cred.AccessKey)
	}

	expires, err := strconv.Atoi(expiresRaw)
	if err != nil || expires <= 0 {
		return nil, sigErr(errAuthorizationHeaderError, "X-Amz-Expires must be a positive integer")
	}
	if expires > maxPresignExpires {
		return nil, sigErr(errAuthorizationHeaderError, "X-Amz-Expires must be <= %d (7 days)", maxPresignExpires)
	}

	signedAt, err := time.Parse("20060102T150405Z", timestamp)
	if err != nil {
		return nil, sigErr(errAuthorizationHeaderError, "X-Amz-Date %q is not in YYYYMMDDTHHMMSSZ form", timestamp)
	}
	// Date in credential scope must match the date portion of the
	// timestamp — same defense as header mode.
	if !strings.HasPrefix(timestamp, cred.Date) {
		return nil, sigErr(errAuthorizationHeaderError, "X-Amz-Date %q doesn't match credential scope date %q", timestamp, cred.Date)
	}
	now := time.Now().UTC()
	if now.Sub(signedAt) > time.Duration(expires)*time.Second {
		return nil, sigErr(errAccessDenied, "presigned URL has expired")
	}
	if signedAt.Sub(now) > maxClockSkew {
		return nil, sigErr(errRequestTimeTooSkewed, "request signed time is more than 15 minutes in the future")
	}

	signedHeaders := strings.Split(signedHeadersRaw, ";")
	if err := requireMandatoryHeaders(signedHeaders, true); err != nil {
		return nil, sigErr(errAuthorizationHeaderError, "%s", err)
	}

	// Drop X-Amz-Signature from the raw query string — it must not
	// be part of the input that produced itself. We strip it at the
	// raw-bytes level (not via url.Values.Encode) so the canonical
	// query encoding sees the client's original byte-shape, which
	// is what they signed against.
	rawQueryWithoutSig := stripQueryParam(r.URL.RawQuery, "X-Amz-Signature")

	canonical, _ := canonicalRequest(r.Method, r.URL.Path, rawQueryWithoutSig, headersAsMap(r), signedHeaders, sigUnsignedBody)
	scopeStr := scope(cred.Date, cred.Region, cred.Service)
	stringToSignVal := stringToSign(timestamp, scopeStr, canonical)
	signingKey := derivedSigningKey(secret, cred.Date, cred.Region, cred.Service)
	expected := signature(signingKey, stringToSignVal)
	if subtle.ConstantTimeCompare([]byte(expected), []byte(signatureRaw)) != 1 {
		return nil, sigErr(errSignatureDoesNotMatch, "computed signature does not match supplied")
	}

	return &sigV4Result{
		AccessKeyID: cred.AccessKey,
		Region:      cred.Region,
		Service:     cred.Service,
		BodyReader:  r.Body,
	}, nil
}

// splitAuthHeader splits the Authorization header body (everything
// after "AWS4-HMAC-SHA256 ") into the three named fields. AWS allows
// fields in any order; we tolerate any order and trim surrounding
// whitespace per field.
func splitAuthHeader(body string) (credential, signedHeaders, signature string, ok bool) {
	parts := strings.Split(body, ",")
	for _, p := range parts {
		p = strings.TrimSpace(p)
		switch {
		case strings.HasPrefix(p, "Credential="):
			credential = strings.TrimPrefix(p, "Credential=")
		case strings.HasPrefix(p, "SignedHeaders="):
			signedHeaders = strings.TrimPrefix(p, "SignedHeaders=")
		case strings.HasPrefix(p, "Signature="):
			signature = strings.TrimPrefix(p, "Signature=")
		}
	}
	return credential, signedHeaders, signature, credential != "" && signedHeaders != "" && signature != ""
}

// credentialFields is the parsed form of the Credential field:
//   <access-key>/<date>/<region>/<service>/aws4_request
type credentialFields struct {
	AccessKey string
	Date      string
	Region    string
	Service   string
}

func parseCredential(raw string) (credentialFields, error) {
	parts := strings.Split(raw, "/")
	if len(parts) != 5 {
		return credentialFields{}, errors.New("credential must have 5 slash-separated fields")
	}
	if parts[4] != "aws4_request" {
		return credentialFields{}, fmt.Errorf("credential terminator must be %q, got %q", "aws4_request", parts[4])
	}
	if len(parts[1]) != 8 {
		return credentialFields{}, fmt.Errorf("credential date %q is not YYYYMMDD", parts[1])
	}
	return credentialFields{
		AccessKey: parts[0],
		Date:      parts[1],
		Region:    parts[2],
		Service:   parts[3],
	}, nil
}

// requireMandatoryHeaders enforces that headers AWS requires be
// signed are actually in the SignedHeaders list. host is always
// required; x-amz-date and x-amz-content-sha256 are required for
// header-mode (NOT for query-mode where the body is unsigned).
func requireMandatoryHeaders(signedHeaders []string, queryMode bool) error {
	have := make(map[string]bool, len(signedHeaders))
	for _, h := range signedHeaders {
		have[strings.ToLower(strings.TrimSpace(h))] = true
	}
	if !have["host"] {
		return errors.New("SignedHeaders must include host")
	}
	if !queryMode {
		if !have["x-amz-content-sha256"] {
			return errors.New("SignedHeaders must include x-amz-content-sha256")
		}
		if !have["x-amz-date"] {
			return errors.New("SignedHeaders must include x-amz-date")
		}
	}
	return nil
}

// checkClockSkew rejects requests whose x-amz-date is more than
// maxClockSkew minutes from server time. The format is YYYYMMDDTHHMMSSZ.
func checkClockSkew(timestamp string) error {
	t, err := time.Parse("20060102T150405Z", timestamp)
	if err != nil {
		return fmt.Errorf("X-Amz-Date %q is not in YYYYMMDDTHHMMSSZ form", timestamp)
	}
	delta := time.Now().UTC().Sub(t)
	if delta < 0 {
		delta = -delta
	}
	if delta > maxClockSkew {
		return fmt.Errorf("request time differs from server time by %s (max %s)", delta, maxClockSkew)
	}
	return nil
}

// bindBody returns the body reader the handler should use, given a
// CALLED-AFTER-SIGNATURE-VERIFICATION assumption (the caller has
// already confirmed the bodyHash header value belongs to a valid
// signature, so an attacker can't trigger the body-bounded paths
// without first authenticating). Three cases:
//
//   1. UNSIGNED-PAYLOAD — pass body through.
//   2. STREAMING-AWS4-HMAC-SHA256-PAYLOAD — wrap r.Body in a
//      chunked-decoder that verifies per-chunk signatures.
//   3. Hex SHA-256 digest — bounded ReadAll + hash compare. The
//      bound is maxBodyForHashedPut.
//
// The hex-digest case buffers the full body in memory. Real S3
// PutObject from rclone uses STREAMING-… for large bodies and the
// digest for small ones (typical client cutoff is 64KB-1MB).
func bindBody(r *http.Request, bodyHash string, signingKey []byte, signature, timestamp, scope string) (io.ReadCloser, *sigV4VerifyError) {
	switch bodyHash {
	case sigUnsignedBody:
		return r.Body, nil
	case sigChunkAlgo:
		dec := newChunkedDecoder(r.Body, signingKey, signature, timestamp, scope)
		return dec, nil
	default:
		// Hex SHA-256 path. Bound the read so a malicious client
		// can't tie up arbitrary memory even after authenticating;
		// real-world hex-digest PUTs are tiny (CompleteMultipart's
		// XML body, single small writes).
		limited := io.LimitReader(r.Body, maxBodyForHashedPut+1)
		buf, err := io.ReadAll(limited)
		if err != nil {
			return nil, sigErr(errIncompleteBody, "could not read request body: %s", err)
		}
		if int64(len(buf)) > maxBodyForHashedPut {
			return nil, sigErr(errEntityTooLarge, "hex-digest payload exceeds %d bytes; use STREAMING-AWS4-HMAC-SHA256-PAYLOAD", maxBodyForHashedPut)
		}
		actual := sha256.Sum256(buf)
		if hex.EncodeToString(actual[:]) != strings.ToLower(bodyHash) {
			return nil, sigErr(errBadDigest, "request body SHA-256 does not match X-Amz-Content-Sha256")
		}
		return io.NopCloser(bytes.NewReader(buf)), nil
	}
}

// maxBodyForHashedPut caps how much memory the hex-digest body
// verification path can consume. AWS's recommended threshold for
// switching to streaming-chunked is 64 KiB; we leave headroom for
// small XML bodies (CompleteMultipart, DeleteObjects) up to 16 MiB.
const maxBodyForHashedPut = 16 << 20

// isHex reports whether s is all lowercase or uppercase hex digits.
func isHex(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= '0' && c <= '9':
		case c >= 'a' && c <= 'f':
		case c >= 'A' && c <= 'F':
		default:
			return false
		}
	}
	return true
}

// headersAsMap converts an http.Header (which is map[string][]string
// under the hood) into a fresh map. Keeps the canonical-request code
// dependency-free and avoids accidental mutation of r.Header.
//
// We also synthesise a "Host" entry from r.Host because net/http
// strips the Host header into r.Host but the canonical-request code
// expects it in the headers map.
func headersAsMap(r *http.Request) map[string][]string {
	out := make(map[string][]string, len(r.Header)+1)
	for k, v := range r.Header {
		out[k] = v
	}
	if r.Host != "" {
		out["Host"] = []string{r.Host}
	}
	return out
}
