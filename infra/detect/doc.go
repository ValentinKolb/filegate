// Package detect provides filesystem change detection backends for Filegate.
// It abstracts the mechanism used to discover created, changed, and deleted
// files so the domain layer can re-index only what changed.
//
// The package focuses on:
//   - A pluggable Runner interface for change detection loops.
//   - Btrfs-native delta detection via generation-based subvolume queries.
//   - Polling-based detection via periodic readdir/lstat scans.
//   - Automatic backend selection based on filesystem capabilities.
//
// Key Components:
//
//   - Runner: interface for detection loop implementations.
//   - BTRFSDetector: btrfs-optimized change detector.
//   - Poller: cross-filesystem polling detector.
//   - New: auto-detecting constructor that picks the best backend.
//
// Related Packages:
//
//   - domain: consumes detection events to trigger index syncs.
//   - cli: wires the detector into the serve command lifecycle.
package detect
