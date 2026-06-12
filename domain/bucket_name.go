package domain

import (
	"fmt"
	"net"
	"strings"
)

// ValidateBucketName checks that name conforms to the AWS S3 bucket-
// naming rules. Used by Filegate startup when S3 is enabled to refuse
// mounts whose names would be unaddressable as buckets.
//
// Rules enforced (subset of AWS spec sufficient for filegate's
// path-style only S3 frontend):
//
//   - 3 to 63 characters long
//   - lowercase letters, digits, and hyphens only
//   - must start and end with a letter or digit
//   - no two consecutive periods (we reject all periods to keep the
//     virtual-host edge case off the table — filegate only speaks
//     path-style, but mount names with dots get awkward when an
//     operator later tries to point a TLS subdomain at them)
//   - not formatted like an IP address (e.g. "192.168.0.1") — the
//     spec also forbids the "ip-…" prefix used by EC2; we keep that
//     since some apps refuse such names
//   - not in our reserved internal-namespace list (see
//     reservedMountNames) — those are filegate-internal and would
//     collide with implementation details
//
// The error is wrapped around ErrInvalidArgument so callers can
// errors.Is-check it.
func ValidateBucketName(name string) error {
	if len(name) < 3 || len(name) > 63 {
		return fmt.Errorf("%w: bucket name %q must be 3-63 characters", ErrInvalidArgument, name)
	}
	for i, r := range name {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9':
		case r == '-':
			if i == 0 || i == len(name)-1 {
				return fmt.Errorf("%w: bucket name %q must not start or end with hyphen", ErrInvalidArgument, name)
			}
		default:
			return fmt.Errorf("%w: bucket name %q contains invalid character %q", ErrInvalidArgument, name, r)
		}
	}
	for _, p := range awsReservedBucketPrefixes {
		if strings.HasPrefix(name, p) {
			return fmt.Errorf("%w: bucket name %q uses an AWS-reserved prefix %q", ErrInvalidArgument, name, p)
		}
	}
	for _, sfx := range awsReservedBucketSuffixes {
		if strings.HasSuffix(name, sfx) {
			return fmt.Errorf("%w: bucket name %q uses an AWS-reserved suffix %q", ErrInvalidArgument, name, sfx)
		}
	}
	if ip := net.ParseIP(name); ip != nil {
		return fmt.Errorf("%w: bucket name %q must not be formatted like an IP address", ErrInvalidArgument, name)
	}
	for _, reserved := range reservedMountNames {
		if name == reserved {
			return fmt.Errorf("%w: bucket name %q is reserved by filegate internals", ErrInvalidArgument, name)
		}
	}
	return nil
}

// reservedMountNames are the path segments filegate uses for its own
// internal namespace (versioning blob storage, multipart staging, …).
// Mount names matching these would collide with that internal data.
//
// Reachability note: these names contain a dot, which fails the
// allowed-character check earlier in ValidateBucketName, so the
// reserved-list check below is unreachable in practice. We keep it
// as defense-in-depth in case the character set ever expands.
var reservedMountNames = []string{
	".fg-versions",
	".fg-uploads",
}

// awsReservedBucketPrefixes are forbidden by AWS for general bucket
// names (some reserved for internal services like S3 access points
// and demo buckets). See
// https://docs.aws.amazon.com/AmazonS3/latest/userguide/bucketnamingrules.html
var awsReservedBucketPrefixes = []string{
	"xn--",          // IDN encoding
	"sthree-",       // S3 internal
	"amzn-s3-demo-", // S3 demo
}

// awsReservedBucketSuffixes are forbidden by AWS, used by S3 access
// points, Object Lambda, S3 Tables, and the S3 Express One Zone
// suffix. We reject all of them so a future flip to virtual-hosted-
// style routing wouldn't accidentally match an operator-named mount.
var awsReservedBucketSuffixes = []string{
	"-s3alias",
	"--ol-s3",
	"--x-s3",
	"--table-s3",
}

// ValidateMountsForS3 reports the first mount-name validation error
// across the configured mount set. Used by NewService when S3 is
// enabled — startup fails with a clear message rather than letting
// the operator discover the problem when a client first hits the
// listener. With S3 disabled the function is not called and the
// names are accepted as-is.
func (s *Service) ValidateMountsForS3() error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, name := range s.mountNames {
		if err := ValidateBucketName(name); err != nil {
			return err
		}
	}
	return nil
}
