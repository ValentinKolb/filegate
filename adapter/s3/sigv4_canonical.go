package s3

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
	"sort"
	"strings"
)

// AWS Signature Version 4 — canonical request construction and
// signing-key derivation.
//
// Reference (ground truth for this file):
//   https://docs.aws.amazon.com/AmazonS3/latest/API/sig-v4-header-based-auth.html
//   https://docs.aws.amazon.com/AmazonS3/latest/API/sigv4-streaming.html
//   https://docs.aws.amazon.com/AmazonS3/latest/API/sigv4-query-string-auth.html
//
// Implementation notes:
//
//   * We DO NOT normalize URI paths (S3 spec deviation — for S3 the
//     canonical URI is the verbatim path with each segment URI-
//     encoded once, except for the "/" separator). Other AWS services
//     run a normalize-then-encode that collapses ".." segments —
//     S3's signing rule is opposite, because object keys may contain
//     literal dots.
//   * The query-string canonicalization sorts by key (and then by
//     value for repeated keys) and URI-encodes both sides.
//   * The signed-headers list is the lower-cased, semicolon-joined
//     set of headers the client claims to have signed; the canonical
//     headers section emits each in alphabetical order with values
//     trimmed of leading/trailing whitespace and collapsed inner
//     whitespace.

// canonicalRequest computes the AWS-shaped canonical request string.
// payloadHash MUST be the value the client put in x-amz-content-sha256
// (which can be a literal SHA-256 hex digest, "UNSIGNED-PAYLOAD", a
// chunked-streaming sentinel, etc.) — verifying the digest matches
// the actual body is the caller's job; canonicalRequest just embeds
// the value verbatim.
func canonicalRequest(method, rawPath, rawQuery string, headers map[string][]string, signedHeaders []string, payloadHash string) (canonical, signedHeadersJoined string) {
	// 1. HTTPMethod
	// 2. CanonicalURI
	// 3. CanonicalQueryString
	// 4. CanonicalHeaders
	// 5. SignedHeaders
	// 6. HashedPayload
	uri := canonicalURIForS3(rawPath)
	query := canonicalQueryString(rawQuery)
	hdrs, signedJoined := canonicalHeadersAndSigned(headers, signedHeaders)
	parts := []string{
		method,
		uri,
		query,
		hdrs,
		signedJoined,
		payloadHash,
	}
	return strings.Join(parts, "\n"), signedJoined
}

// canonicalURIForS3 encodes the path per the S3-specific rule: each
// path segment is URI-encoded once (RFC 3986 unreserved characters
// pass through; other bytes become %XX), but the "/" separator is
// preserved literally and there is NO "..", "." normalization. An
// empty path becomes "/".
func canonicalURIForS3(rawPath string) string {
	if rawPath == "" {
		return "/"
	}
	// rawPath comes from r.URL.Path which is already percent-decoded
	// by net/http. Re-encode for the canonical form.
	segments := strings.Split(rawPath, "/")
	for i, seg := range segments {
		segments[i] = uriEncode(seg, false)
	}
	return strings.Join(segments, "/")
}

// canonicalQueryString builds the AWS canonical query string: key=value
// pairs sorted by key (then by value for duplicate keys), URI-encoded
// per the AWS rule (NOT form-encoded — '+' in input is a literal
// '+', not a space).
//
// We deliberately do NOT use net/url.ParseQuery / url.Values.Encode:
// those follow HTML form encoding semantics where '+' decodes to
// space. AWS SigV4 explicitly requires '%20' for spaces, treating
// '+' as just another character that encodes to '%2B'. Using
// form-encoding here makes the canonical string disagree with what
// the client signed, leading to spurious signature mismatches on
// any query parameter containing '+' literally.
func canonicalQueryString(rawQuery string) string {
	if rawQuery == "" {
		return ""
	}
	type pair struct{ key, value string }
	pairs := make([]pair, 0, 8)
	for _, kv := range strings.Split(rawQuery, "&") {
		if kv == "" {
			continue
		}
		eq := strings.IndexByte(kv, '=')
		var k, v string
		if eq < 0 {
			k = kv
		} else {
			k = kv[:eq]
			v = kv[eq+1:]
		}
		// Decode percent escapes, leaving every other byte (including
		// '+') as-is. We re-encode below per AWS rules.
		dk, ok := percentDecode(k)
		if !ok {
			// Malformed escape — keep raw, signature mismatch
			// will surface naturally.
			dk = k
		}
		dv, ok := percentDecode(v)
		if !ok {
			dv = v
		}
		pairs = append(pairs, pair{dk, dv})
	}
	sort.SliceStable(pairs, func(i, j int) bool {
		if pairs[i].key != pairs[j].key {
			return pairs[i].key < pairs[j].key
		}
		return pairs[i].value < pairs[j].value
	})
	var b strings.Builder
	for i, p := range pairs {
		if i > 0 {
			b.WriteByte('&')
		}
		b.WriteString(uriEncode(p.key, true))
		b.WriteByte('=')
		b.WriteString(uriEncode(p.value, true))
	}
	return b.String()
}

// percentDecode decodes "%XX" escapes. Unlike url.QueryUnescape it
// does NOT translate '+' to space — the AWS canonical form preserves
// '+' as a literal character. Returns (decoded, true) on success;
// (input, false) when an escape is malformed (caller decides whether
// to fall through with the raw string).
func percentDecode(s string) (string, bool) {
	if !strings.ContainsRune(s, '%') {
		return s, true
	}
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c != '%' {
			out = append(out, c)
			continue
		}
		if i+2 >= len(s) {
			return s, false
		}
		hi, ok1 := hexNibble(s[i+1])
		lo, ok2 := hexNibble(s[i+2])
		if !ok1 || !ok2 {
			return s, false
		}
		out = append(out, byte(hi<<4|lo))
		i += 2
	}
	return string(out), true
}

func hexNibble(c byte) (int, bool) {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0'), true
	case c >= 'a' && c <= 'f':
		return int(c-'a') + 10, true
	case c >= 'A' && c <= 'F':
		return int(c-'A') + 10, true
	default:
		return 0, false
	}
}

// stripQueryParam removes the named parameter from a raw query
// string (as r.URL.RawQuery presents it), keeping the rest of the
// byte-shape intact. Used when verifying presigned URLs: we must
// take X-Amz-Signature out of the canonical input without otherwise
// re-encoding what the client signed.
func stripQueryParam(rawQuery, name string) string {
	if rawQuery == "" {
		return ""
	}
	prefix := name + "="
	parts := strings.Split(rawQuery, "&")
	out := parts[:0]
	for _, p := range parts {
		if strings.HasPrefix(p, prefix) {
			continue
		}
		out = append(out, p)
	}
	return strings.Join(out, "&")
}

// canonicalHeadersAndSigned builds the canonical headers block and
// the signed-headers list. The signed list is the lower-cased
// alphabetic-order semicolon-joined names; the canonical block is
// "name:value\n" for each, with values whitespace-collapsed.
//
// signedHeaderNames is the list the client claims to have signed
// (parsed from the Authorization header's SignedHeaders field, or
// from the X-Amz-SignedHeaders query parameter). We honor it
// verbatim — the verification logic in this package additionally
// enforces that mandatory headers (host, x-amz-content-sha256,
// x-amz-date) are present in this list when applicable.
func canonicalHeadersAndSigned(headers map[string][]string, signedHeaderNames []string) (block, signedJoined string) {
	// Lowercase + sort.
	names := make([]string, len(signedHeaderNames))
	for i, n := range signedHeaderNames {
		names[i] = strings.ToLower(strings.TrimSpace(n))
	}
	sort.Strings(names)

	var b strings.Builder
	for _, name := range names {
		// Find the header value(s). HTTP header lookup is case-
		// insensitive by spec; iterate the raw map for portability.
		vals := lookupHeaderValues(headers, name)
		joined := joinHeaderValues(vals)
		b.WriteString(name)
		b.WriteByte(':')
		b.WriteString(joined)
		b.WriteByte('\n')
	}
	return b.String(), strings.Join(names, ";")
}

func lookupHeaderValues(headers map[string][]string, lowerName string) []string {
	for k, v := range headers {
		if strings.EqualFold(k, lowerName) {
			return v
		}
	}
	return nil
}

// joinHeaderValues joins multi-value header values with comma and
// collapses inner whitespace per the canonical headers rule. Leading
// and trailing whitespace are trimmed from each value before
// joining.
func joinHeaderValues(vals []string) string {
	if len(vals) == 0 {
		return ""
	}
	parts := make([]string, len(vals))
	for i, v := range vals {
		parts[i] = collapseWhitespace(strings.TrimSpace(v))
	}
	return strings.Join(parts, ",")
}

func collapseWhitespace(s string) string {
	if s == "" {
		return s
	}
	var b strings.Builder
	prevSpace := false
	for _, r := range s {
		if r == ' ' || r == '\t' {
			if !prevSpace {
				b.WriteByte(' ')
				prevSpace = true
			}
			continue
		}
		b.WriteRune(r)
		prevSpace = false
	}
	return b.String()
}

// uriEncode implements the AWS canonical encoding rule: RFC 3986
// unreserved characters pass through (A-Z a-z 0-9 - . _ ~), other
// bytes become %XX uppercase. The "/" character is special — passed
// through when encodeSlash is false, escaped when true. Used for
// path segments (slash literal) and for query keys/values (slash
// escaped).
func uriEncode(s string, encodeSlash bool) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z',
			c >= 'a' && c <= 'z',
			c >= '0' && c <= '9',
			c == '-' || c == '.' || c == '_' || c == '~':
			b.WriteByte(c)
		case c == '/' && !encodeSlash:
			b.WriteByte('/')
		default:
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

// stringToSign returns the "string to sign" component of SigV4. The
// AWS algorithm is:
//
//   "AWS4-HMAC-SHA256\n" +
//   <ISO8601-time>\n +
//   <date>/<region>/<service>/aws4_request\n +
//   hex(sha256(canonicalRequest))
func stringToSign(timestamp, scope, canonicalReq string) string {
	h := sha256.Sum256([]byte(canonicalReq))
	return strings.Join([]string{
		"AWS4-HMAC-SHA256",
		timestamp,
		scope,
		hex.EncodeToString(h[:]),
	}, "\n")
}

// scope returns the credential-scope string: "<date>/<region>/<service>/aws4_request".
// date is the YYYYMMDD form (the date portion of x-amz-date).
func scope(date, region, service string) string {
	return strings.Join([]string{date, region, service, "aws4_request"}, "/")
}

// derivedSigningKey computes the AWS-shaped signing key:
//
//   kDate    = HMAC-SHA256("AWS4" + secret, date)
//   kRegion  = HMAC-SHA256(kDate, region)
//   kService = HMAC-SHA256(kRegion, service)
//   kSign    = HMAC-SHA256(kService, "aws4_request")
//
// The result is fed into HMAC-SHA256 with the string-to-sign to
// produce the final signature.
func derivedSigningKey(secretAccessKey, date, region, service string) []byte {
	kDate := hmacSHA256([]byte("AWS4"+secretAccessKey), []byte(date))
	kRegion := hmacSHA256(kDate, []byte(region))
	kService := hmacSHA256(kRegion, []byte(service))
	return hmacSHA256(kService, []byte("aws4_request"))
}

func hmacSHA256(key, data []byte) []byte {
	h := hmac.New(func() hash.Hash { return sha256.New() }, key)
	h.Write(data)
	return h.Sum(nil)
}

// signature applies HMAC-SHA256 with the derived signing key over the
// string-to-sign and returns the lowercase hex digest.
func signature(signingKey []byte, stringToSign string) string {
	mac := hmacSHA256(signingKey, []byte(stringToSign))
	return hex.EncodeToString(mac)
}
