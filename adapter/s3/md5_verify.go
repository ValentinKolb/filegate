package s3

import (
	"crypto/md5"
	"hash"
	"io"
)

// md5VerifyingReader wraps a body io.ReadCloser. As bytes are read,
// they're tee'd through an MD5 hasher. On EOF, if the hasher's sum
// doesn't match the expected digest, Read returns md5MismatchError
// instead of io.EOF — which surfaces from any consumer that reads
// to completion.
//
// Note: a partial read (consumer stops before EOF) doesn't trigger
// verification. That's acceptable: PutObject's body consumer is
// either WriteObjectS3 (reads to EOF) or the chunked-decoder (which
// runs its own per-chunk verification AND reads to EOF). A
// short-circuited consumer would surface as a different domain
// error long before the missing MD5 check could matter.
type md5VerifyingReader struct {
	src      io.ReadCloser
	expected []byte
	hasher   hash.Hash
	verified bool
}

func newMD5Hasher() hash.Hash { return md5.New() }

func (r *md5VerifyingReader) Read(p []byte) (int, error) {
	n, err := r.src.Read(p)
	if n > 0 {
		_, _ = r.hasher.Write(p[:n])
	}
	if err == io.EOF && !r.verified {
		r.verified = true
		actual := r.hasher.Sum(nil)
		if !bytesEqualConstantTime(actual, r.expected) {
			return n, &md5MismatchError{expected: r.expected, actual: actual}
		}
	}
	return n, err
}

func (r *md5VerifyingReader) Close() error { return r.src.Close() }

// bytesEqualConstantTime is bytes.Equal but in constant time.
// MD5 mismatch is not a security boundary (the SigV4 signature is),
// but we still avoid leaking timing info — there is no reason for
// the comparison to be variable-time.
func bytesEqualConstantTime(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	var diff byte
	for i := range a {
		diff |= a[i] ^ b[i]
	}
	return diff == 0
}
