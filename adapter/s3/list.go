package s3

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/valentinkolb/filegate/domain"
)

// AWS S3 ListObjectsV2 limits.
const (
	maxKeysCap     = 1000
	defaultMaxKeys = 1000
)

// handleListObjectsV2 implements GET /{bucket}?list-type=2 — the
// modern S3 listing op. Returns a flat list of object metadata for
// the bucket, optionally filtered by prefix + start-after, paginated
// via continuation-token.
//
// Backed by the Pebble flat-key index from M0 (22aahwtm); for each
// matching flat-key we fetch the entity metadata (mtime, size, etag)
// to populate the Contents entries. The flat-key iterator returns
// entries in lexical order, which is exactly what S3 ListObjectsV2
// expects.
//
// Delimiter / CommonPrefixes are NOT implemented here — they land in
// M2 per the dex plan. A request that includes ?delimiter=… is
// rejected with InvalidArgument so clients fail loudly rather than
// silently see no virtual hierarchy.
func (rt *router) handleListObjectsV2(w http.ResponseWriter, r *http.Request, _ *sigV4Result, bucket string) {
	q := r.URL.Query()
	if got := q.Get("list-type"); got != "2" {
		writeError(w, r, errInvalidArgument, "only ListObjectsV2 (list-type=2) is supported", withBucket(bucket))
		return
	}
	if q.Get("delimiter") != "" {
		// Push 1's plan deferred delimiter (CommonPrefixes
		// virtual-hierarchy) to M2. Returning NotImplemented
		// lets clients fail loudly instead of silently seeing a
		// flat result when they expect grouping.
		writeError(w, r, errNotImplemented, "delimiter is not supported in M1; use a recursive listing", withBucket(bucket))
		return
	}
	if v := q.Get("encoding-type"); v != "" && v != "url" {
		writeError(w, r, errInvalidArgument, "encoding-type must be \"url\" if specified", withBucket(bucket))
		return
	}
	encodeURL := q.Get("encoding-type") == "url"
	if v := q.Get("fetch-owner"); v != "" && v != "false" {
		// AWS returns per-object <Owner> when fetch-owner=true.
		// We don't track per-key owners (single-tenant filegate
		// has one principal); reject the request rather than lie
		// about ownership.
		writeError(w, r, errNotImplemented, "fetch-owner is not supported (filegate has no per-object owner model in M1)", withBucket(bucket))
		return
	}

	prefix := q.Get("prefix")
	startAfter := q.Get("start-after")
	contToken := q.Get("continuation-token")

	maxKeys := defaultMaxKeys
	if raw := q.Get("max-keys"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 {
			writeError(w, r, errInvalidArgument, "max-keys must be a non-negative integer", withBucket(bucket))
			return
		}
		if n > maxKeysCap {
			n = maxKeysCap
		}
		maxKeys = n
	}
	// max-keys=0 is technically valid but cannot make progress —
	// no contents means no NextContinuationToken to carry. Return
	// an empty, non-truncated page so clients don't loop.
	if maxKeys == 0 {
		writeListBucketResult(w, listBucketResultV2{
			Name:     bucket,
			Prefix:   maybeURLEncode(prefix, encodeURL),
			MaxKeys:  0,
			KeyCount: 0,
		})
		return
	}

	// Resolve the cursor: continuation-token (when set) wins over
	// start-after — that's the AWS behavior. Both express a
	// "strict-greater than" bound on the relPath ordering.
	cursor := startAfter
	if contToken != "" {
		decoded, err := decodeContinuationToken(contToken)
		if err != nil {
			writeError(w, r, errInvalidArgument, "continuation-token is malformed", withBucket(bucket))
			return
		}
		cursor = decoded
	}

	// We pass limit=0 (unlimited) and stop the iteration ourselves
	// once we've accepted maxKeys+1 RETURNABLE objects. Counting
	// callbacks instead of returnables (e.g. via iterLimit=maxKeys+1)
	// would mis-detect truncation when an entry is skipped (stale
	// race, non-file, reserved namespace) — IsTruncated could go
	// wrong in either direction. The "+1" lets us peek one past
	// the cap to set IsTruncated correctly without losing data.
	contents := make([]listObjectXML, 0, maxKeys)
	truncated := false

	err := rt.svc.IterateFlatKeysForS3(bucket, prefix, cursor, 0, func(relPath string, id domain.FileID) (bool, error) {
		if len(contents) >= maxKeys {
			// We already have maxKeys returnables — this
			// callback is the truncation peek. The strict-
			// greater cursor for the next page is the LAST
			// returnable we accepted. (Note: this only fires
			// for a callback that would have been returnable;
			// a stale or filtered entry below the maxKeys-th
			// returnable doesn't cause an early IsTruncated.)
			if !isReturnable(rt.svc, relPath, id) {
				// Skip filtered peek — keep walking until we
				// see a real next entry.
				return true, nil
			}
			truncated = true
			return false, nil
		}
		// Fetch entity for size + mtime + etag. A missing entity
		// for a flat-key entry is a transient race (the rescan
		// sweep will clean it up); skip it rather than abort the
		// listing.
		meta, err := rt.svc.GetFile(id)
		if err != nil {
			if errors.Is(err, domain.ErrNotFound) {
				return true, nil
			}
			return false, err
		}
		if meta.Type != "file" {
			// Defensive: flat-key entries are file-only, but a
			// race during dir-rename could briefly point at a
			// directory. Skip.
			return true, nil
		}
		// Apply the same key-validation we use on the PUT path —
		// reserved-namespace and out-of-subset shapes shouldn't
		// surface even if something rogue ended up indexed.
		if validateObjectKey(relPath) != nil {
			return true, nil
		}
		view, _ := rt.svc.GetS3Metadata(id)
		contents = append(contents, listObjectXML{
			Key:          maybeURLEncode(relPath, encodeURL),
			LastModified: time.UnixMilli(meta.Mtime).UTC().Format("2006-01-02T15:04:05.000Z"),
			ETag:         quoteETag(effectiveETag(meta, view)),
			Size:         meta.Size,
			StorageClass: "STANDARD",
		})
		return true, nil
	})
	if err != nil {
		writeError(w, r, errInternalError, fmt.Sprintf("listing failed: %s", err), withBucket(bucket))
		return
	}

	res := listBucketResultV2{
		Name:        bucket,
		Prefix:      maybeURLEncode(prefix, encodeURL),
		StartAfter:  maybeURLEncode(startAfter, encodeURL),
		KeyCount:    len(contents),
		MaxKeys:     maxKeys,
		IsTruncated: truncated,
		Contents:    contents,
	}
	if encodeURL {
		res.EncodingType = "url"
	}
	if contToken != "" {
		res.ContinuationToken = contToken
	}
	if truncated && len(contents) > 0 {
		// Token encodes the LAST RAW relPath we accepted (NOT
		// url-encoded — the iterator wants the original bytes).
		// We backtrack from the response Contents which may be
		// url-encoded, so keep a parallel raw cursor.
		res.NextContinuationToken = encodeContinuationToken(lastRawKey(contents, encodeURL))
	}
	writeListBucketResult(w, res)
}

// isReturnable mirrors the post-fetch filter in the main loop. Used
// during truncation-peek to decide whether the (maxKeys+1)-th
// callback represents a "real next item" worth signalling
// IsTruncated for.
func isReturnable(svc *domain.Service, relPath string, id domain.FileID) bool {
	if validateObjectKey(relPath) != nil {
		return false
	}
	meta, err := svc.GetFile(id)
	if err != nil || meta == nil {
		return false
	}
	return meta.Type == "file"
}

// maybeURLEncode percent-encodes s when the listing was requested
// with encoding-type=url. Per AWS spec we encode the contents that
// might contain XML-unsafe bytes; clients opt in.
func maybeURLEncode(s string, enabled bool) string {
	if !enabled {
		return s
	}
	return uriEncode(s, true)
}

// lastRawKey extracts the raw relPath of the last contents entry,
// undoing the url-encoding we may have applied for response shape.
// Used to emit a NextContinuationToken that the iterator can use as
// a strict-greater bound.
func lastRawKey(contents []listObjectXML, encoded bool) string {
	if len(contents) == 0 {
		return ""
	}
	last := contents[len(contents)-1].Key
	if !encoded {
		return last
	}
	dec, ok := percentDecode(last)
	if !ok {
		return last
	}
	return dec
}

// encodeContinuationToken / decodeContinuationToken render the
// internal cursor (the relPath where the next page should begin) as
// an opaque string the client passes back unmodified. Base64-URL
// without padding keeps the token URL-safe.
func encodeContinuationToken(relPath string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(relPath))
}

func decodeContinuationToken(token string) (string, error) {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// IterateFlatKeysForS3 forwards to the Service-layer iterator. We
// add this as a thin domain method instead of poking idx directly
// from the adapter: the flat-key keyspace is filegate-internal, the
// adapter only sees what the service exposes.
//
// Lives in service_s3_write.go alongside GetS3Metadata for cohesion.

