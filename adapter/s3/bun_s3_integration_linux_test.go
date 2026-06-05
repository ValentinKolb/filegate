//go:build linux

package s3

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestBunS3ClientObjectLifecycle(t *testing.T) {
	bun := requireBun(t)
	_, handler, mount, cleanup := newTestServer(t)
	defer cleanup()

	counter := newS3RequestCounter(handler)
	srv := httptest.NewServer(counter)
	defer srv.Close()

	script := fmt.Sprintf(`
import { S3Client } from "bun";

const client = new S3Client({
  accessKeyId: %q,
  secretAccessKey: %q,
  region: %q,
  endpoint: %q,
  bucket: %q,
});

await client.write("small/hello.txt", "hello from bun");

const text = await client.file("small/hello.txt").text();
if (text !== "hello from bun") {
  throw new Error("readback mismatch: " + text);
}

const stat = await client.stat("small/hello.txt");
if (stat.size !== "hello from bun".length) {
  throw new Error("stat size mismatch: " + JSON.stringify(stat));
}

if (!(await client.exists("small/hello.txt"))) {
  throw new Error("exists returned false for uploaded object");
}

const list = await client.list({ prefix: "small/" });
const keys = (list.contents ?? []).map((item) => item.key).sort();
if (!keys.includes("small/hello.txt")) {
  throw new Error("list did not include uploaded object: " + JSON.stringify(list));
}

const partial = await client.file("small/hello.txt").slice(0, 5).text();
if (partial !== "hello") {
  throw new Error("range read mismatch: " + partial);
}

await client.delete("small/hello.txt");

if (await client.exists("small/hello.txt")) {
  throw new Error("exists returned true after delete");
}
`, testAccessKey, testSecretKey, testRegion, srv.URL, mount)

	runBunScript(t, bun, script)

	headers, ok := counter.putObjectHeaders("small/hello.txt")
	if !ok {
		t.Fatalf("did not capture Bun PutObject headers")
	}
	// Bun 1.3.14 does not currently exercise the flexible-checksum trailer path.
	// Keep this pinned so the test fails loudly if Bun changes its wire format.
	if got := headers.Get("x-amz-content-sha256"); got != sigUnsignedBody {
		t.Fatalf("Bun PutObject x-amz-content-sha256=%q, want %q", got, sigUnsignedBody)
	}
	if got := headers.Get("Content-Encoding"); got == "aws-chunked" {
		t.Fatalf("Bun PutObject unexpectedly used aws-chunked without trailer coverage update")
	}
}

func TestBunS3ClientLargeWriteRoundTrip(t *testing.T) {
	bun := requireBun(t)
	_, handler, mount, cleanup := newTestServer(t)
	defer cleanup()

	counter := newS3RequestCounter(handler)
	srv := httptest.NewServer(counter)
	defer srv.Close()

	script := fmt.Sprintf(`
import { S3Client } from "bun";

const client = new S3Client({
  accessKeyId: %q,
  secretAccessKey: %q,
  region: %q,
  endpoint: %q,
  bucket: %q,
});

const size = 50_000_000;
const body = new Uint8Array(size);
for (let i = 0; i < body.length; i++) {
  body[i] = i %% 251;
}

await client.write("large/bun-multipart.bin", body);

const stat = await client.stat("large/bun-multipart.bin");
if (stat.size !== size) {
  throw new Error("large stat size mismatch: " + JSON.stringify(stat));
}

const head = await client.file("large/bun-multipart.bin").slice(0, 8).bytes();
for (let i = 0; i < head.length; i++) {
  if (head[i] !== i %% 251) {
    throw new Error("large prefix mismatch at " + i + ": " + head[i]);
  }
}
`, testAccessKey, testSecretKey, testRegion, srv.URL, mount)

	runBunScript(t, bun, script)

	counts := counter.snapshot()
	switch {
	case counts.putObject > 0 && counts.createMultipart == 0 && counts.uploadPart == 0 && counts.completeMultipart == 0:
		return
	case counts.putObject == 0 && counts.createMultipart > 0 && counts.uploadPart > 0 && counts.completeMultipart > 0:
		return
	default:
		t.Fatalf("Bun large write used unexpected S3 operation mix; counts=%+v", counts)
	}
}

func requireBun(t *testing.T) string {
	t.Helper()
	bun, err := exec.LookPath("bun")
	if err != nil {
		t.Skip("bun not found on PATH; skipping Bun S3 integration test")
	}
	out, err := exec.Command(bun, "--version").CombinedOutput()
	if err != nil {
		t.Fatalf("bun --version failed: %v\n%s", err, out)
	}
	return bun
}

func runBunScript(t *testing.T, bun, script string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "bun-s3-integration.ts")
	if err := os.WriteFile(path, []byte(script), 0o644); err != nil {
		t.Fatalf("write bun script: %v", err)
	}

	cmd := exec.Command(bun, path)
	cmd.Env = append(os.Environ(),
		"AWS_EC2_METADATA_DISABLED=true",
		"AWS_REGION="+testRegion,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bun script failed: %v\n%s", err, out)
	}
}

type s3RequestCounter struct {
	next http.Handler

	mu                sync.Mutex
	putObject         int
	createMultipart   int
	uploadPart        int
	completeMultipart int
	putHeadersByKey   map[string]http.Header
}

type s3RequestCounts struct {
	putObject         int
	createMultipart   int
	uploadPart        int
	completeMultipart int
}

func newS3RequestCounter(next http.Handler) *s3RequestCounter {
	return &s3RequestCounter{
		next:            next,
		putHeadersByKey: make(map[string]http.Header),
	}
}

func (c *s3RequestCounter) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	c.mu.Lock()
	switch {
	case r.Method == http.MethodPut && q.Get("partNumber") == "" && q.Get("uploadId") == "" && r.Header.Get("x-amz-copy-source") == "":
		c.putObject++
		c.putHeadersByKey[requestObjectKey(r)] = r.Header.Clone()
	case r.Method == http.MethodPost && hasRawQueryKey(r.URL.RawQuery, "uploads"):
		c.createMultipart++
	case r.Method == http.MethodPut && q.Get("partNumber") != "" && q.Get("uploadId") != "":
		c.uploadPart++
	case r.Method == http.MethodPost && q.Get("uploadId") != "":
		c.completeMultipart++
	}
	c.mu.Unlock()

	c.next.ServeHTTP(w, r)
}

func (c *s3RequestCounter) snapshot() s3RequestCounts {
	c.mu.Lock()
	defer c.mu.Unlock()
	return s3RequestCounts{
		putObject:         c.putObject,
		createMultipart:   c.createMultipart,
		uploadPart:        c.uploadPart,
		completeMultipart: c.completeMultipart,
	}
}

func (c *s3RequestCounter) putObjectHeaders(key string) (http.Header, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	headers, ok := c.putHeadersByKey[key]
	if !ok {
		return nil, false
	}
	return headers.Clone(), true
}

func requestObjectKey(r *http.Request) string {
	path := strings.TrimPrefix(r.URL.Path, "/")
	_, key, ok := strings.Cut(path, "/")
	if !ok {
		return ""
	}
	return key
}

func hasRawQueryKey(raw, key string) bool {
	for _, part := range strings.Split(raw, "&") {
		if part == key || strings.HasPrefix(part, key+"=") {
			return true
		}
	}
	return false
}
