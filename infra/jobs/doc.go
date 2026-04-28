// Package jobs provides a bounded worker pool with keyed job deduplication.
// It is used for background tasks such as thumbnail generation and EXIF
// extraction where work items should not be duplicated.
//
// Key Components:
//
//   - Scheduler: bounded queue with configurable worker count.
//   - Do: submit a job and wait for its result.
//   - Trigger: fire-and-forget job submission.
//
// Related Packages:
//
//   - adapter/http: uses Scheduler for thumbnail and EXIF background jobs.
package jobs
