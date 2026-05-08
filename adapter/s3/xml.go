package s3

import (
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"time"
)

// errorPayload is the wire format S3 returns for all error responses.
// Field order matches AWS — older clients (and a few rclone code
// paths) parse positionally for some fields, so we don't reorder.
type errorPayload struct {
	XMLName   xml.Name `xml:"Error"`
	Code      string   `xml:"Code"`
	Message   string   `xml:"Message"`
	Resource  string   `xml:"Resource,omitempty"`
	RequestID string   `xml:"RequestId,omitempty"`
	HostID    string   `xml:"HostId,omitempty"`
	// BucketName / Key are added in some specific error variants by
	// real S3; we surface them when the operation has them in scope.
	BucketName string `xml:"BucketName,omitempty"`
	Key        string `xml:"Key,omitempty"`
}

// errorCode is the symbolic identifier returned in <Code>. Each value
// has a fixed HTTP status (mostly per AWS docs); the mapping lives in
// statusFor.
type errorCode string

const (
	errAccessDenied             errorCode = "AccessDenied"
	errBadDigest                errorCode = "BadDigest"
	errEntityTooLarge           errorCode = "EntityTooLarge"
	errIncompleteBody           errorCode = "IncompleteBody"
	errInternalError            errorCode = "InternalError"
	errInvalidArgument          errorCode = "InvalidArgument"
	errInvalidDigest            errorCode = "InvalidDigest"
	errInvalidRequest           errorCode = "InvalidRequest"
	errMethodNotAllowed         errorCode = "MethodNotAllowed"
	errMissingContentLength     errorCode = "MissingContentLength"
	errNoSuchBucket             errorCode = "NoSuchBucket"
	errNoSuchKey                errorCode = "NoSuchKey"
	errNotImplemented           errorCode = "NotImplemented"
	errPreconditionFailed       errorCode = "PreconditionFailed"
	errRequestTimeTooSkewed     errorCode = "RequestTimeTooSkewed"
	errSignatureDoesNotMatch    errorCode = "SignatureDoesNotMatch"
	errAuthorizationHeaderError errorCode = "AuthorizationHeaderMalformed"
	errInvalidAccessKeyID       errorCode = "InvalidAccessKeyId"
)

func statusFor(code errorCode) int {
	switch code {
	case errAccessDenied, errSignatureDoesNotMatch, errInvalidAccessKeyID:
		return http.StatusForbidden
	case errNoSuchBucket, errNoSuchKey:
		return http.StatusNotFound
	case errPreconditionFailed:
		return http.StatusPreconditionFailed
	case errRequestTimeTooSkewed:
		return http.StatusForbidden
	case errBadDigest, errInvalidDigest, errInvalidArgument,
		errInvalidRequest, errAuthorizationHeaderError, errIncompleteBody:
		return http.StatusBadRequest
	case errMissingContentLength:
		return http.StatusLengthRequired
	case errEntityTooLarge:
		return http.StatusRequestEntityTooLarge
	case errNotImplemented:
		return http.StatusNotImplemented
	case errMethodNotAllowed:
		return http.StatusMethodNotAllowed
	default:
		return http.StatusInternalServerError
	}
}

// writeError renders an S3-shaped error response. The XML body is
// always emitted, even for HEAD requests where the body is dropped
// by net/http — clients still rely on the status code + headers.
func writeError(w http.ResponseWriter, r *http.Request, code errorCode, message string, opts ...errorOption) {
	payload := errorPayload{
		Code:      string(code),
		Message:   message,
		Resource:  r.URL.Path,
		RequestID: r.Header.Get("X-Amz-Request-Id"),
	}
	for _, opt := range opts {
		opt(&payload)
	}
	w.Header().Set("Content-Type", "application/xml")
	w.Header().Set("Server", "filegate")
	w.WriteHeader(statusFor(code))
	if r.Method != http.MethodHead {
		_, _ = io.WriteString(w, xml.Header)
		_ = xml.NewEncoder(w).Encode(payload)
	}
}

type errorOption func(*errorPayload)

func withBucket(name string) errorOption { return func(p *errorPayload) { p.BucketName = name } }
func withKey(key string) errorOption     { return func(p *errorPayload) { p.Key = key } }

// listAllMyBucketsResult is the wire format for GET / (ListBuckets).
// Owner.ID/DisplayName are AWS-shaped strings; we set them to a fixed
// "filegate" identity since we don't model AWS-style accounts.
type listAllMyBucketsResult struct {
	XMLName xml.Name `xml:"ListAllMyBucketsResult"`
	Owner   ownerXML `xml:"Owner"`
	Buckets bucketsListXML
}

type bucketsListXML struct {
	XMLName xml.Name    `xml:"Buckets"`
	Bucket  []bucketXML `xml:"Bucket"`
}

type bucketXML struct {
	Name         string `xml:"Name"`
	CreationDate string `xml:"CreationDate"`
}

type ownerXML struct {
	ID          string `xml:"ID"`
	DisplayName string `xml:"DisplayName"`
}

const filegateOwner = "filegate"

func writeListAllMyBuckets(w http.ResponseWriter, names []string) {
	res := listAllMyBucketsResult{
		Owner: ownerXML{ID: filegateOwner, DisplayName: filegateOwner},
	}
	now := time.Now().UTC().Format(time.RFC3339)
	for _, n := range names {
		res.Buckets.Bucket = append(res.Buckets.Bucket, bucketXML{
			Name: n,
			// We don't track per-mount creation timestamps; report
			// the current time so clients have a stable shape.
			// Operators who need real values will get them in M3.
			CreationDate: now,
		})
	}
	w.Header().Set("Content-Type", "application/xml")
	w.Header().Set("Server", "filegate")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, xml.Header)
	_ = xml.NewEncoder(w).Encode(res)
}

// formatHTTPDate returns a time in the RFC1123 GMT format S3 uses
// for Last-Modified headers. Returns "" for the zero time so callers
// can omit the header cleanly.
func formatHTTPDate(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(http.TimeFormat)
}

// quoteETag wraps a hex digest in double-quotes as S3 does on the
// wire. Pass-through if already quoted (defensive — should not
// normally happen).
func quoteETag(s string) string {
	if s == "" {
		return ""
	}
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		return s
	}
	return fmt.Sprintf("%q", s)
}
