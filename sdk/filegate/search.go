package filegate

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// SearchClient contains search operations.
type SearchClient struct {
	core *clientCore
}

// GlobOptions configures glob search behavior.
type GlobOptions struct {
	Pattern     string
	Paths       []string
	Limit       int
	ShowHidden  bool
	Files       *bool
	Directories *bool
}

// Glob executes GET /v1/search/glob.
func (c SearchClient) Glob(ctx context.Context, opts GlobOptions) (*GlobSearchResponse, error) {
	pattern := strings.TrimSpace(opts.Pattern)
	if pattern == "" {
		return nil, fmt.Errorf("pattern is required")
	}
	query := url.Values{}
	query.Set("pattern", pattern)
	if opts.Limit > 0 {
		query.Set("limit", fmt.Sprintf("%d", opts.Limit))
	}
	if len(opts.Paths) > 0 {
		query.Set("paths", strings.Join(opts.Paths, ","))
	}
	boolQuery(query, "showHidden", opts.ShowHidden)
	if opts.Files != nil {
		query.Set("files", fmt.Sprintf("%t", *opts.Files))
	}
	if opts.Directories != nil {
		query.Set("directories", fmt.Sprintf("%t", *opts.Directories))
	}

	var out GlobSearchResponse
	if err := c.core.doJSON(ctx, http.MethodGet, "/v1/search/glob", query, nil, "", &out); err != nil {
		return nil, err
	}
	return &out, nil
}
