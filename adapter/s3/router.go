package s3

import (
	"crypto/subtle"
	"errors"
	"log"
	"net/http"
	"strings"

	"github.com/valentinkolb/filegate/domain"
)

// Options configures the S3 listener.
type Options struct {
	// Region is the AWS region the listener identifies as in SigV4.
	// Single-tenant deployments can keep the AWS default "us-east-1";
	// the value only needs to match what the client signs with.
	Region string
	// AccessKey + SecretKey are the single-tenant credentials. M3
	// will replace this with a multi-key store; the verifier already
	// abstracts via authConfig.SecretForKeyID.
	AccessKey string
	SecretKey string
	// AccessLogEnabled mirrors the REST adapter's setting.
	AccessLogEnabled bool
}

// NewHandler returns the http.Handler for the S3 listener. Returns
// an error if Options is misconfigured (missing access/secret key);
// callers gate this on cfg.S3.Enabled but a misconfigured config
// shouldn't crash the daemon.
func NewHandler(svc *domain.Service, opts Options) (http.Handler, error) {
	if opts.Region == "" {
		opts.Region = "us-east-1"
	}
	if opts.AccessKey == "" || opts.SecretKey == "" {
		return nil, errors.New("s3: AccessKey and SecretKey must be set")
	}
	auth := authConfig{
		Region: opts.Region,
		SecretForKeyID: func(keyID string) (string, bool) {
			// We use ConstantTimeCompare for symmetry with the
			// signature comparison; note that for a length
			// mismatch it returns 0 immediately without comparing
			// bytes, so an attacker can probe the configured key
			// length but not its bytes. AWS access keys are a
			// fixed 20-char shape in practice, so the length-leak
			// is not load-bearing for security.
			if subtle.ConstantTimeCompare([]byte(keyID), []byte(opts.AccessKey)) == 1 {
				return opts.SecretKey, true
			}
			return "", false
		},
	}
	r := &router{svc: svc, auth: auth, accessLog: opts.AccessLogEnabled}
	return http.HandlerFunc(r.serve), nil
}

type router struct {
	svc       *domain.Service
	auth      authConfig
	accessLog bool
}

func (r *router) serve(w http.ResponseWriter, req *http.Request) {
	// Authenticate first. The verifier returns a sigV4Result whose
	// BodyReader is what handlers must read (might be a chunked
	// decoder). Handlers MUST NOT read req.Body directly.
	verified, sigErr := verifyRequest(req, r.auth)
	if sigErr != nil {
		writeError(w, req, sigErr.Code, sigErr.Message)
		return
	}

	bucket, key := parsePathStyle(req.URL.Path)

	switch {
	case bucket == "":
		// "/" — root operations. Only ListBuckets in M1.
		switch req.Method {
		case http.MethodGet:
			r.handleListBuckets(w, req, verified)
		default:
			writeError(w, req, errMethodNotAllowed, "only GET / is supported at the root")
		}
		return
	case key == "":
		// Bucket-level operations. M1 routes them but only a small
		// subset is implemented; the rest land in M2/M3.
		r.handleBucketOp(w, req, verified, bucket)
		return
	default:
		// Object-level operations.
		r.handleObjectOp(w, req, verified, bucket, key)
	}
}

// parsePathStyle splits "/{bucket}/{key}" form. Empty path-segments
// are tolerated (the spec allows "/" for root, "/bucket" for bucket
// op, "/bucket/key/with/slashes" for object).
func parsePathStyle(path string) (bucket, key string) {
	// Trim the leading "/" — without it everything is a no-op
	// special case.
	rest := strings.TrimPrefix(path, "/")
	if rest == "" {
		return "", ""
	}
	slash := strings.IndexByte(rest, '/')
	if slash < 0 {
		return rest, ""
	}
	return rest[:slash], rest[slash+1:]
}

// handleListBuckets is the M1 placeholder for GET /. Returns the
// configured mounts. Multi-tenant filtering arrives in M3.
func (r *router) handleListBuckets(w http.ResponseWriter, _ *http.Request, _ *sigV4Result) {
	roots := r.svc.ListRoot()
	names := make([]string, 0, len(roots))
	for _, root := range roots {
		names = append(names, root.Name)
	}
	writeListAllMyBuckets(w, names)
	if r.accessLog {
		log.Printf("[filegate-s3] ListBuckets returned %d bucket(s)", len(names))
	}
}

// handleBucketOp dispatches bucket-level methods. The dispatcher
// passes the verified SigV4 context through so handlers can log
// access-key info if access-logging is enabled.
func (r *router) handleBucketOp(w http.ResponseWriter, req *http.Request, verified *sigV4Result, bucket string) {
	if !r.bucketExists(bucket) {
		writeError(w, req, errNoSuchBucket, "bucket does not exist", withBucket(bucket))
		return
	}
	switch req.Method {
	case http.MethodGet:
		// Bucket-level GET = ListObjectsV2 (when list-type=2 in
		// the query, which the modern S3 SDKs always set).
		r.handleListObjectsV2(w, req, verified, bucket)
	case http.MethodPut, http.MethodDelete:
		writeError(w, req, errMethodNotAllowed, "buckets come from filegate config; CreateBucket/DeleteBucket are rejected")
	case http.MethodHead:
		// HEAD on a bucket = bucket existence check (HEAD bucket).
		w.WriteHeader(http.StatusOK)
	default:
		writeError(w, req, errMethodNotAllowed, "method not supported")
	}
}

// handleObjectOp dispatches single-object methods to the per-method
// handlers in object.go. The bucket-existence check happens first so
// every handler can assume the bucket is real.
func (r *router) handleObjectOp(w http.ResponseWriter, req *http.Request, verified *sigV4Result, bucket, key string) {
	if !r.bucketExists(bucket) {
		writeError(w, req, errNoSuchBucket, "bucket does not exist", withBucket(bucket), withKey(key))
		return
	}
	switch req.Method {
	case http.MethodGet:
		r.handleGetObject(w, req, verified, bucket, key)
	case http.MethodHead:
		r.handleHeadObject(w, req, verified, bucket, key)
	case http.MethodPut:
		r.handlePutObject(w, req, verified, bucket, key)
	case http.MethodDelete:
		r.handleDeleteObject(w, req, verified, bucket, key)
	default:
		writeError(w, req, errMethodNotAllowed, "method not supported")
	}
}

// bucketExists checks whether a mount with the given name exists.
// The check happens BEFORE bucket-name S3 validation — operators
// who failed startup-validation never reach this code path because
// the listener wouldn't have started.
func (r *router) bucketExists(name string) bool {
	for _, root := range r.svc.ListRoot() {
		if root.Name == name {
			return true
		}
	}
	return false
}
