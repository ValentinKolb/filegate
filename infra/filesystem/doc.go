// Package filesystem implements the domain.Store interface using OS filesystem
// operations. It provides file I/O, directory management, and extended attribute
// support for persisting stable FileID identities.
//
// Key Components:
//
//   - Store: thin adapter over os and syscall packages.
//   - New: constructor returning a ready-to-use Store instance.
//
// Related Packages:
//
//   - domain: defines the Store port interface this package fulfills.
package filesystem
