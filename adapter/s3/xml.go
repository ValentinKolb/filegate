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
	// Multipart-specific codes — clients (rclone, awscli, MinIO SDK)
	// branch on these; emitting NoSuchKey for a missing upload is
	// technically allowed by status-code but breaks resume/retry
	// flows that look at <Code>.
	errNoSuchUpload     errorCode = "NoSuchUpload"
	errInvalidPart      errorCode = "InvalidPart"
	errInvalidPartOrder errorCode = "InvalidPartOrder"
	errEntityTooSmall   errorCode = "EntityTooSmall"
	errMalformedXML     errorCode = "MalformedXML"
	// errSlowDown is the AWS-spec back-off response when the
	// requesting access key exceeds its configured rate. SDKs
	// honour it with exponential backoff (boto3, awscli, rclone
	// all implement this). Status 503 + a Retry-After header.
	errSlowDown errorCode = "SlowDown"
)

func statusFor(code errorCode) int {
	switch code {
	case errAccessDenied, errSignatureDoesNotMatch, errInvalidAccessKeyID:
		return http.StatusForbidden
	case errNoSuchBucket, errNoSuchKey, errNoSuchUpload:
		return http.StatusNotFound
	case errInvalidPart, errInvalidPartOrder, errEntityTooSmall, errMalformedXML:
		return http.StatusBadRequest
	case errSlowDown:
		return http.StatusServiceUnavailable
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

// listBucketResultV2 is the wire format for GET /{bucket}?list-type=2.
// Element order matches what AWS emits — clients (rclone, awscli)
// don't strictly require positional order, but matching keeps wire
// diffs tight.
type listBucketResultV2 struct {
	XMLName               xml.Name             `xml:"ListBucketResult"`
	Name                  string               `xml:"Name"`
	Prefix                string               `xml:"Prefix"`
	Delimiter             string               `xml:"Delimiter,omitempty"`
	StartAfter            string               `xml:"StartAfter,omitempty"`
	EncodingType          string               `xml:"EncodingType,omitempty"`
	KeyCount              int                  `xml:"KeyCount"`
	MaxKeys               int                  `xml:"MaxKeys"`
	IsTruncated           bool                 `xml:"IsTruncated"`
	ContinuationToken     string               `xml:"ContinuationToken,omitempty"`
	NextContinuationToken string               `xml:"NextContinuationToken,omitempty"`
	Contents              []listObjectXML      `xml:"Contents"`
	CommonPrefixes        []commonPrefixXML    `xml:"CommonPrefixes,omitempty"`
}

// commonPrefixXML represents a virtual "directory" entry in
// ListObjectsV2 output when delimiter is set. Each grouping appears
// as <CommonPrefixes><Prefix>...</Prefix></CommonPrefixes>.
type commonPrefixXML struct {
	Prefix string `xml:"Prefix"`
}

type listObjectXML struct {
	Key          string `xml:"Key"`
	LastModified string `xml:"LastModified"`
	ETag         string `xml:"ETag"`
	Size         int64  `xml:"Size"`
	StorageClass string `xml:"StorageClass"`
}

// writeListBucketResult emits a ListObjectsV2 XML response.
func writeListBucketResult(w http.ResponseWriter, res listBucketResultV2) {
	w.Header().Set("Content-Type", "application/xml")
	w.Header().Set("Server", "filegate")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, xml.Header)
	_ = xml.NewEncoder(w).Encode(res)
}

// initiateMultipartUploadResult is the XML payload returned by
// CreateMultipartUpload. Clients echo the UploadId back on every
// subsequent UploadPart / Complete / Abort call.
type initiateMultipartUploadResult struct {
	XMLName  xml.Name `xml:"InitiateMultipartUploadResult"`
	Bucket   string   `xml:"Bucket"`
	Key      string   `xml:"Key"`
	UploadID string   `xml:"UploadId"`
}

// listPartsResult is the XML payload for GET ?uploadId=X (the
// ListParts S3 op). MaxParts/IsTruncated are set when paginating;
// our M2 push 2 returns all parts in a single response since
// realistic uploads have ≤10000 parts and the response is small.
type listPartsResult struct {
	XMLName     xml.Name  `xml:"ListPartsResult"`
	Bucket      string    `xml:"Bucket"`
	Key         string    `xml:"Key"`
	UploadID    string    `xml:"UploadId"`
	IsTruncated bool      `xml:"IsTruncated"`
	Parts       []partXML `xml:"Part"`
}

type partXML struct {
	PartNumber   int    `xml:"PartNumber"`
	LastModified string `xml:"LastModified"`
	ETag         string `xml:"ETag"`
	Size         int64  `xml:"Size"`
}

// listMultipartUploadsResult is the XML payload for GET
// /{bucket}?uploads (ListMultipartUploads). Used by clients to
// resume / clean up unfinished uploads.
type listMultipartUploadsResult struct {
	XMLName     xml.Name    `xml:"ListMultipartUploadsResult"`
	Bucket      string      `xml:"Bucket"`
	IsTruncated bool        `xml:"IsTruncated"`
	Uploads     []uploadXML `xml:"Upload"`
}

type uploadXML struct {
	Key       string `xml:"Key"`
	UploadID  string `xml:"UploadId"`
	Initiated string `xml:"Initiated"`
}

// completeMultipartUploadRequest is the XML body the client sends on
// POST /{bucket}/{key}?uploadId=X. The Parts list MUST be in
// ascending PartNumber order with the per-part ETag from
// UploadPart.
type completeMultipartUploadRequest struct {
	XMLName xml.Name             `xml:"CompleteMultipartUpload"`
	Parts   []completeRequestPart `xml:"Part"`
}

type completeRequestPart struct {
	PartNumber int    `xml:"PartNumber"`
	ETag       string `xml:"ETag"`
}

// completeMultipartUploadResult is the XML response. Location is a
// best-effort URL; clients mostly care about Bucket/Key/ETag.
type completeMultipartUploadResult struct {
	XMLName  xml.Name `xml:"CompleteMultipartUploadResult"`
	Location string   `xml:"Location"`
	Bucket   string   `xml:"Bucket"`
	Key      string   `xml:"Key"`
	ETag     string   `xml:"ETag"`
}

// copyObjectResult is the XML response for a successful S3
// CopyObject. AWS returns it with HTTP 200 — never 201 — even when
// the destination is freshly created.
type copyObjectResult struct {
	XMLName      xml.Name `xml:"CopyObjectResult"`
	ETag         string   `xml:"ETag"`
	LastModified string   `xml:"LastModified"`
}

// deleteObjectsRequest is the XML body POSTed to /{bucket}?delete.
// AWS allows a Quiet flag (suppress per-key Deleted entries in the
// response, errors still surface) and up to 1000 Object entries per
// call. We enforce the 1000 cap to bound memory + lock churn.
type deleteObjectsRequest struct {
	XMLName xml.Name              `xml:"Delete"`
	Quiet   bool                  `xml:"Quiet"`
	Objects []deleteRequestObject `xml:"Object"`
}

type deleteRequestObject struct {
	Key string `xml:"Key"`
	// VersionId is part of the AWS schema but filegate has no
	// per-object version concept on the S3 surface — anything
	// non-empty surfaces as InvalidArgument so clients catch
	// misconfigurations early.
	VersionID string `xml:"VersionId,omitempty"`
}

// deleteObjectsResult is the XML response for DeleteObjects. Each
// successful key produces a <Deleted>; each failure a <Error>.
type deleteObjectsResult struct {
	XMLName xml.Name              `xml:"DeleteResult"`
	Deleted []deletedResultEntry  `xml:"Deleted,omitempty"`
	Errors  []deleteResultError   `xml:"Error,omitempty"`
}

type deletedResultEntry struct {
	Key string `xml:"Key"`
}

type deleteResultError struct {
	Key     string `xml:"Key"`
	Code    string `xml:"Code"`
	Message string `xml:"Message"`
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

