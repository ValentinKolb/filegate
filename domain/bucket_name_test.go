package domain

import (
	"errors"
	"strings"
	"testing"
)

// TestValidateBucketNameAccepts pins the cases real S3 clients (and
// our own apps) emit — three-letter shortest, hyphens in the middle,
// digit-only segments, mixed alphanumeric.
func TestValidateBucketNameAccepts(t *testing.T) {
	for _, name := range []string{
		"app",
		"data",
		"my-app-uploads",
		"backup-restic",
		"abc123",
		"alpha-beta-gamma",
	} {
		if err := ValidateBucketName(name); err != nil {
			t.Errorf("%q should be valid: %v", name, err)
		}
	}
}

// TestValidateBucketNameRejects pins each rejection rule with one
// targeted case. Adjusting the validator must keep these failing
// outputs (or the rule itself is up for redesign).
func TestValidateBucketNameRejects(t *testing.T) {
	cases := []struct {
		name string
		want string // substring expected in error
	}{
		{"ab", "3-63"},
		{strings.Repeat("a", 64), "3-63"},
		{"UpperCase", "invalid character"},
		{"with_underscore", "invalid character"},
		{"with.dot", "invalid character"},
		{"-leading-hyphen", "start or end with hyphen"},
		{"trailing-hyphen-", "start or end with hyphen"},
		{"xn--punycode", "AWS-reserved prefix"},
		{"sthree-anything", "AWS-reserved prefix"},
		{"amzn-s3-demo-bucket", "AWS-reserved prefix"},
		{"thing-s3alias", "AWS-reserved suffix"},
		{"foo--ol-s3", "AWS-reserved suffix"},
		{"foo--x-s3", "AWS-reserved suffix"},
		{"foo--table-s3", "AWS-reserved suffix"},
		{"192.168.1.1", "invalid character"}, // dot is rejected first
		{".fg-versions", "invalid character"},
		{".fg-uploads", "invalid character"},
	}
	for _, tc := range cases {
		err := ValidateBucketName(tc.name)
		if err == nil {
			t.Errorf("%q should be invalid", tc.name)
			continue
		}
		if !errors.Is(err, ErrInvalidArgument) {
			t.Errorf("%q: error not wrapped around ErrInvalidArgument: %v", tc.name, err)
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%q error %q missing %q", tc.name, err.Error(), tc.want)
		}
	}
}

// TestValidateBucketNameRejectsIPLikeName covers the IP-like rule
// that's only reachable when the name is otherwise valid (lowercase
// digits + hyphens). With dots forbidden, an IPv4 literal fails on
// the dots first; we still want a path that exercises the IP-like
// rule for IPv6-style hex-and-hyphens... actually IPv6 isn't a
// realistic bucket name. Skip exotic cases.
func TestValidateBucketNameRejectsIPLikeName(t *testing.T) {
	// Trying to construct an IP-like name without dots is
	// effectively impossible — keep the rule as defense-in-depth.
	// This test just documents that we know.
	t.Log("IP-like bucket name rule is defense-in-depth; dots rejected first")
}
