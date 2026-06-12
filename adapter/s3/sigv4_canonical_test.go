package s3

import (
	"strings"
	"testing"
)

// TestCanonicalURIForS3 pins the S3-specific path-encoding rule:
// each segment is percent-encoded once, slashes pass through, no
// path normalization. Edge cases drawn from real client emissions.
func TestCanonicalURIForS3(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", "/"},
		{"root", "/", "/"},
		{"plain", "/bucket/key.txt", "/bucket/key.txt"},
		{"unicode", "/bucket/grüße.txt", "/bucket/gr%C3%BC%C3%9Fe.txt"},
		{"space", "/bucket/file name", "/bucket/file%20name"},
		{"plus-not-special", "/bucket/a+b", "/bucket/a%2Bb"},
		{"reserved-chars", "/bucket/!*'()", "/bucket/%21%2A%27%28%29"},
		{"unreserved-pass", "/bucket/A-Z.0-9_~", "/bucket/A-Z.0-9_~"},
		// AWS spec: S3 does NOT collapse ".." or ".".
		{"dots-preserved", "/bucket/../up/./same", "/bucket/../up/./same"},
	}
	for _, tc := range cases {
		got := canonicalURIForS3(tc.in)
		if got != tc.want {
			t.Errorf("%s: canonicalURIForS3(%q) = %q, want %q", tc.name, tc.in, got, tc.want)
		}
	}
}

// TestCanonicalQueryString pins: sorted by key, then by value,
// percent-encoded both sides, ampersand-joined.
func TestCanonicalQueryString(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"single", "foo=bar", "foo=bar"},
		{"sorted-by-key", "z=2&a=1", "a=1&z=2"},
		{"dup-keys-sorted-by-value", "a=2&a=1", "a=1&a=2"},
		{"encode-space-as-%20", "k=hello world", "k=hello%20world"},
		// AWS SigV4 treats "+" as a literal character (NOT space).
		// A client sending "k=a+b" canonicalizes to "k=a%2Bb".
		// Spaces in values must be %20-encoded by the client.
		{"plus-stays-literal", "k=a+b", "k=a%2Bb"},
		{"encode-key", "key with space=value", "key%20with%20space=value"},
		{"empty-value", "k=", "k="},
	}
	for _, tc := range cases {
		got := canonicalQueryString(tc.in)
		if got != tc.want {
			t.Errorf("%s: canonicalQueryString(%q) = %q, want %q", tc.name, tc.in, got, tc.want)
		}
	}
}

// TestCanonicalHeadersAndSigned: lowercase, alphabetically sorted,
// values trimmed and inner whitespace collapsed.
func TestCanonicalHeadersAndSigned(t *testing.T) {
	headers := map[string][]string{
		"Host":                 {"example.com"},
		"X-Amz-Date":           {"20230101T000000Z"},
		"X-Amz-Content-Sha256": {sigEmptyBodyHash},
		"Content-Type":         {"text/plain"},
		"X-Amz-Custom":         {"  spaced   value  "},
	}
	signed := []string{"host", "x-amz-content-sha256", "x-amz-custom", "x-amz-date"}
	block, joined := canonicalHeadersAndSigned(headers, signed)

	wantJoined := "host;x-amz-content-sha256;x-amz-custom;x-amz-date"
	if joined != wantJoined {
		t.Errorf("joined=%q, want %q", joined, wantJoined)
	}
	wantBlock := "host:example.com\n" +
		"x-amz-content-sha256:" + sigEmptyBodyHash + "\n" +
		"x-amz-custom:spaced value\n" +
		"x-amz-date:20230101T000000Z\n"
	if block != wantBlock {
		t.Errorf("block:\n%s\nwant:\n%s", block, wantBlock)
	}
}

// TestDerivedSigningKeyMatchesAWSExample verifies the signing-key
// derivation against the example AWS publishes in the SigV4 docs.
// The values are:
//
//	secret = "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY"
//	date   = "20150830"
//	region = "us-east-1"
//	service = "iam"
//
// Expected derived key (hex):
//
//	c4afb1cc5771d871763a393e44b703571b55cc28424d1a5e86da6ed3c154a4b9
//
// Source: https://docs.aws.amazon.com/general/latest/gr/sigv4-calculate-signature.html
func TestDerivedSigningKeyMatchesAWSExample(t *testing.T) {
	got := derivedSigningKey(
		"wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY",
		"20150830",
		"us-east-1",
		"iam",
	)
	want := "c4afb1cc5771d871763a393e44b703571b55cc28424d1a5e86da6ed3c154a4b9"
	gotHex := bytesToHex(got)
	if gotHex != want {
		t.Errorf("derived signing key = %s, want %s", gotHex, want)
	}
}

// TestStringToSignFormat pins the four-line shape of the
// string-to-sign produced from a canonical request.
func TestStringToSignFormat(t *testing.T) {
	canonical := "GET\n/\n\nhost:example.com\n\nhost\n" + sigEmptyBodyHash
	scopeStr := scope("20230101", "us-east-1", "s3")
	got := stringToSign("20230101T000000Z", scopeStr, canonical)
	lines := strings.Split(got, "\n")
	if len(lines) != 4 {
		t.Fatalf("string-to-sign has %d lines, want 4", len(lines))
	}
	if lines[0] != "AWS4-HMAC-SHA256" {
		t.Errorf("line 0 = %q, want AWS4-HMAC-SHA256", lines[0])
	}
	if lines[1] != "20230101T000000Z" {
		t.Errorf("line 1 = %q, want 20230101T000000Z", lines[1])
	}
	if lines[2] != "20230101/us-east-1/s3/aws4_request" {
		t.Errorf("line 2 = %q", lines[2])
	}
	if len(lines[3]) != 64 {
		t.Errorf("line 3 (canonical-request hash) length = %d, want 64", len(lines[3]))
	}
}

// TestUriEncode pins the per-byte encoding rule including the
// encodeSlash flag.
func TestUriEncode(t *testing.T) {
	cases := []struct {
		in          string
		encodeSlash bool
		want        string
	}{
		{"abc", false, "abc"},
		{"abc", true, "abc"},
		{"a/b", false, "a/b"},
		{"a/b", true, "a%2Fb"},
		{" ", false, "%20"},
		{"&=?", false, "%26%3D%3F"},
	}
	for _, tc := range cases {
		got := uriEncode(tc.in, tc.encodeSlash)
		if got != tc.want {
			t.Errorf("uriEncode(%q, %v) = %q, want %q", tc.in, tc.encodeSlash, got, tc.want)
		}
	}
}

func bytesToHex(b []byte) string {
	const hex = "0123456789abcdef"
	out := make([]byte, len(b)*2)
	for i, v := range b {
		out[i*2] = hex[v>>4]
		out[i*2+1] = hex[v&0x0F]
	}
	return string(out)
}
