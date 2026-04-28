// Package relay provides HTTP passthrough helpers for backends that proxy
// Filegate responses to their own clients without re-buffering bodies.
//
// Like the chunks helpers, these functions do not depend on the SDK
// client.
package relay

import (
	"errors"
	"io"
	"net/http"
)

// CopyResponse mirrors src to dst: it copies every header, writes the
// upstream status code, and streams the body. The caller's ResponseWriter
// must not have called WriteHeader yet. The upstream body is closed when
// the function returns.
func CopyResponse(dst http.ResponseWriter, src *http.Response) (int64, error) {
	if dst == nil || src == nil {
		return 0, errors.New("dst and src are required")
	}
	defer src.Body.Close()

	for key, values := range src.Header {
		for _, v := range values {
			dst.Header().Add(key, v)
		}
	}
	dst.WriteHeader(src.StatusCode)
	return io.Copy(dst, src.Body)
}
