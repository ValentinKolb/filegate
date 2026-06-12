package s3

import (
	"bufio"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// AWS S3 streaming-chunked-payload signing (algorithm
// STREAMING-AWS4-HMAC-SHA256-PAYLOAD).
//
// Reference: https://docs.aws.amazon.com/AmazonS3/latest/API/sigv4-streaming.html
//
// Wire format on the body, repeated until the final 0-length chunk:
//
//   <chunk-size-hex>;chunk-signature=<hex sig>\r\n
//   <chunk-bytes>\r\n
//
// The final chunk has size 0 and an explicit signature too:
//
//   0;chunk-signature=<hex sig>\r\n
//   \r\n   (no chunk bytes)
//
// Each chunk's signature is computed over a per-chunk string-to-sign:
//
//   "AWS4-HMAC-SHA256-PAYLOAD\n" +
//   <timestamp>\n +
//   <scope>\n +
//   <previous chunk's signature>\n +
//   sha256("")\n +    // sha256 of empty string for chunk-of-headers (none here)
//   sha256(<chunk bytes>)
//
// The "previous chunk's signature" for the FIRST chunk is the
// request-level Authorization signature ("seed signature"). For each
// subsequent chunk, it's the previous chunk's chunk-signature.
//
// chunkedDecoder hides this from handlers: handlers Read() it like a
// normal body and get the decoded bytes; signature verification
// happens transparently. A signature mismatch returns an io.EOF-like
// error (errChunkSignatureMismatch) that handlers surface as a 403.

var (
	errChunkSignatureMismatch = errors.New("s3: streaming chunk signature mismatch")
	errMalformedChunkHeader   = errors.New("s3: malformed streaming chunk header")
	errChunkTrailerMismatch   = errors.New("s3: chunk did not end with CRLF")
	errChunkTooLarge          = errors.New("s3: streaming chunk exceeds maxChunkBytes")
)

// maxChunkBytes caps the per-chunk byte count we accept in the
// streaming-chunked decoder. AWS allows up to 5 GiB per chunk in
// theory, but real clients (rclone, aws-cli, Bun.s3) chunk in the
// MB range. Capping at 64 MiB prevents an authenticated client from
// allocating a giant per-chunk buffer through us.
const maxChunkBytes = 64 << 20

const chunkPayloadAlgo = "AWS4-HMAC-SHA256-PAYLOAD"

// emptySHA256Hex is the SHA-256 hex of the empty byte string,
// embedded in every chunk's string-to-sign in the "hashed-headers"
// position (always empty for streaming-payload chunks).
const emptySHA256Hex = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

type chunkedDecoder struct {
	src        *bufio.Reader
	closer     io.Closer
	signingKey []byte
	prevSig    string
	timestamp  string
	scope      string

	// State for incremental Read(): the current chunk's decoded
	// bytes are cached here and returned across multiple Read calls
	// until exhausted.
	curBuf []byte
	curPos int
	done   bool
	err    error
}

func newChunkedDecoder(body io.ReadCloser, signingKey []byte, seedSig, timestamp, scope string) *chunkedDecoder {
	return &chunkedDecoder{
		src:        bufio.NewReader(body),
		closer:     body,
		signingKey: signingKey,
		prevSig:    seedSig,
		timestamp:  timestamp,
		scope:      scope,
	}
}

// Read returns decoded chunk bytes. Returns (0, err) on signature
// mismatch or malformed input — the underlying reader is NOT
// consumed past the failure point.
func (d *chunkedDecoder) Read(p []byte) (int, error) {
	if d.err != nil {
		return 0, d.err
	}
	for d.curPos >= len(d.curBuf) {
		if d.done {
			return 0, io.EOF
		}
		if err := d.loadNextChunk(); err != nil {
			d.err = err
			return 0, err
		}
		// loadNextChunk sets d.done when it consumed the final 0-length
		// chunk. Loop once more to return io.EOF.
	}
	n := copy(p, d.curBuf[d.curPos:])
	d.curPos += n
	return n, nil
}

func (d *chunkedDecoder) Close() error { return d.closer.Close() }

// loadNextChunk reads one chunk header + body, verifies its
// signature, and stores the decoded bytes in d.curBuf. Sets d.done
// when the final 0-length chunk is consumed.
func (d *chunkedDecoder) loadNextChunk() error {
	header, err := d.src.ReadString('\n')
	if err != nil {
		if errors.Is(err, io.EOF) && header == "" {
			return io.ErrUnexpectedEOF
		}
		return err
	}
	// AWS streaming chunk framing requires CRLF after the header
	// line; reject LF-only as malformed (cheap defense against
	// crafted inputs that some HTTP intermediaries normalize).
	if !strings.HasSuffix(header, "\r\n") {
		return fmt.Errorf("%w: chunk header missing CRLF terminator", errMalformedChunkHeader)
	}
	header = strings.TrimSuffix(header, "\r\n")
	sizeHex, sigVal, err := parseChunkHeader(header)
	if err != nil {
		return err
	}
	size, err := strconv.ParseUint(sizeHex, 16, 64)
	if err != nil {
		return fmt.Errorf("%w: invalid chunk size %q", errMalformedChunkHeader, sizeHex)
	}
	if size > maxChunkBytes {
		return fmt.Errorf("%w: chunk size %d exceeds cap %d", errChunkTooLarge, size, maxChunkBytes)
	}
	body := make([]byte, size)
	if size > 0 {
		if _, err := io.ReadFull(d.src, body); err != nil {
			return err
		}
		trailer := make([]byte, 2)
		if _, err := io.ReadFull(d.src, trailer); err != nil {
			return err
		}
		if string(trailer) != "\r\n" {
			return errChunkTrailerMismatch
		}
	}

	chunkHash := sha256.Sum256(body)
	stringToSign := strings.Join([]string{
		chunkPayloadAlgo,
		d.timestamp,
		d.scope,
		d.prevSig,
		emptySHA256Hex,
		hex.EncodeToString(chunkHash[:]),
	}, "\n")
	expected := signature(d.signingKey, stringToSign)
	if subtle.ConstantTimeCompare([]byte(expected), []byte(sigVal)) != 1 {
		return errChunkSignatureMismatch
	}
	d.prevSig = sigVal

	if size == 0 {
		if err := discardChunkTrailers(d.src); err != nil {
			return err
		}
		// Final chunk consumed — any further Read returns io.EOF.
		d.done = true
		d.curBuf = nil
		d.curPos = 0
		return nil
	}
	d.curBuf = body
	d.curPos = 0
	return nil
}

// parseChunkHeader splits "<sizeHex>;chunk-signature=<sigHex>" into
// its two fields. Returns errMalformedChunkHeader for any deviation
// from the expected shape — chunk headers are tightly specified so
// there's no need for tolerant parsing.
func parseChunkHeader(line string) (sizeHex, sigVal string, err error) {
	semi := strings.IndexByte(line, ';')
	if semi < 0 {
		return "", "", fmt.Errorf("%w: missing chunk-signature in %q", errMalformedChunkHeader, line)
	}
	sizeHex = line[:semi]
	rest := line[semi+1:]
	const sigPrefix = "chunk-signature="
	if !strings.HasPrefix(rest, sigPrefix) {
		return "", "", fmt.Errorf("%w: expected chunk-signature= in %q", errMalformedChunkHeader, line)
	}
	sigVal = strings.TrimPrefix(rest, sigPrefix)
	if sigVal == "" {
		return "", "", fmt.Errorf("%w: empty chunk-signature in %q", errMalformedChunkHeader, line)
	}
	return sizeHex, sigVal, nil
}

type unsignedChunkedDecoder struct {
	src    *bufio.Reader
	closer io.Closer

	curBuf []byte
	curPos int
	done   bool
	err    error
}

func newUnsignedChunkedDecoder(body io.ReadCloser) *unsignedChunkedDecoder {
	return &unsignedChunkedDecoder{
		src:    bufio.NewReader(body),
		closer: body,
	}
}

func (d *unsignedChunkedDecoder) Read(p []byte) (int, error) {
	if d.err != nil {
		return 0, d.err
	}
	for d.curPos >= len(d.curBuf) {
		if d.done {
			return 0, io.EOF
		}
		if err := d.loadNextChunk(); err != nil {
			d.err = err
			return 0, err
		}
	}
	n := copy(p, d.curBuf[d.curPos:])
	d.curPos += n
	return n, nil
}

func (d *unsignedChunkedDecoder) Close() error { return d.closer.Close() }

func (d *unsignedChunkedDecoder) loadNextChunk() error {
	header, err := d.src.ReadString('\n')
	if err != nil {
		if errors.Is(err, io.EOF) && header == "" {
			return io.ErrUnexpectedEOF
		}
		return err
	}
	if !strings.HasSuffix(header, "\r\n") {
		return fmt.Errorf("%w: chunk header missing CRLF terminator", errMalformedChunkHeader)
	}
	header = strings.TrimSuffix(header, "\r\n")
	sizeHex := header
	if semi := strings.IndexByte(sizeHex, ';'); semi >= 0 {
		sizeHex = sizeHex[:semi]
	}
	sizeHex = strings.TrimSpace(sizeHex)
	if sizeHex == "" {
		return fmt.Errorf("%w: empty chunk size", errMalformedChunkHeader)
	}
	size, err := strconv.ParseUint(sizeHex, 16, 64)
	if err != nil {
		return fmt.Errorf("%w: invalid chunk size %q", errMalformedChunkHeader, sizeHex)
	}
	if size > maxChunkBytes {
		return fmt.Errorf("%w: chunk size %d exceeds cap %d", errChunkTooLarge, size, maxChunkBytes)
	}
	body := make([]byte, size)
	if size > 0 {
		if _, err := io.ReadFull(d.src, body); err != nil {
			return err
		}
		trailer := make([]byte, 2)
		if _, err := io.ReadFull(d.src, trailer); err != nil {
			return err
		}
		if string(trailer) != "\r\n" {
			return errChunkTrailerMismatch
		}
	}
	if size == 0 {
		if err := discardChunkTrailers(d.src); err != nil {
			return err
		}
		d.done = true
		d.curBuf = nil
		d.curPos = 0
		return nil
	}
	d.curBuf = body
	d.curPos = 0
	return nil
}

func discardChunkTrailers(src *bufio.Reader) error {
	for {
		line, err := src.ReadString('\n')
		if err != nil {
			if errors.Is(err, io.EOF) && line == "" {
				return io.ErrUnexpectedEOF
			}
			return err
		}
		if !strings.HasSuffix(line, "\r\n") {
			return fmt.Errorf("%w: trailer header missing CRLF terminator", errMalformedChunkHeader)
		}
		if line == "\r\n" {
			return nil
		}
	}
}
