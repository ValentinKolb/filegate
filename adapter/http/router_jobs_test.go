package httpadapter

import "testing"

func TestJobTuningResolversUseGlobalFallbacks(t *testing.T) {
	opts := RouterOptions{
		JobWorkers:   20,
		JobQueueSize: 5000,
	}

	if got := resolveThumbnailJobWorkers(opts); got != 20 {
		t.Fatalf("thumbnail workers=%d, want 20", got)
	}
	if got := resolveThumbnailQueueSize(opts); got != 10000 {
		t.Fatalf("thumbnail queue=%d, want 10000", got)
	}
}

func TestJobTuningResolversApplyMinimumClamps(t *testing.T) {
	opts := RouterOptions{
		JobWorkers:   4,
		JobQueueSize: 100,
	}

	if got := resolveThumbnailJobWorkers(opts); got != 16 {
		t.Fatalf("thumbnail workers=%d, want 16", got)
	}
	if got := resolveThumbnailQueueSize(opts); got != 8192 {
		t.Fatalf("thumbnail queue=%d, want 8192", got)
	}
}

func TestJobTuningResolversRespectEndpointOverrides(t *testing.T) {
	opts := RouterOptions{
		JobWorkers:            64,
		JobQueueSize:          8192,
		ThumbnailJobWorkers:   96,
		ThumbnailJobQueueSize: 24000,
	}

	if got := resolveThumbnailJobWorkers(opts); got != 96 {
		t.Fatalf("thumbnail workers=%d, want 96", got)
	}
	if got := resolveThumbnailQueueSize(opts); got != 24000 {
		t.Fatalf("thumbnail queue=%d, want 24000", got)
	}
}
