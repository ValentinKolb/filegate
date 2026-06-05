//go:build linux

package s3

import (
	"bytes"
	"context"
	"io"
	"net/http/httptest"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

func TestGoS3ClientPutObjectStreamingUnsignedPayloadTrailer(t *testing.T) {
	_, handler, mount, cleanup := newTestServer(t)
	defer cleanup()

	counter := newS3RequestCounter(handler)
	srv := httptest.NewTLSServer(counter)
	defer srv.Close()

	ctx := context.Background()
	cfg, err := config.LoadDefaultConfig(ctx,
		config.WithRegion(testRegion),
		config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(testAccessKey, testSecretKey, "")),
		config.WithRequestChecksumCalculation(aws.RequestChecksumCalculationWhenSupported),
	)
	if err != nil {
		t.Fatalf("load AWS SDK config: %v", err)
	}
	client := awss3.NewFromConfig(cfg, func(o *awss3.Options) {
		o.BaseEndpoint = aws.String(srv.URL)
		o.HTTPClient = srv.Client()
		o.UsePathStyle = true
	})

	key := "go-sdk/unsigned-trailer.txt"
	body := []byte("hello from aws sdk v2 trailer body")
	_, err = client.PutObject(ctx, &awss3.PutObjectInput{
		Bucket:            aws.String(mount),
		Key:               aws.String(key),
		Body:              bytes.NewBuffer(body),
		ChecksumAlgorithm: types.ChecksumAlgorithmCrc32,
	})
	if err != nil {
		t.Fatalf("Go SDK PutObject: %v", err)
	}

	headers, ok := counter.putObjectHeaders(key)
	if !ok {
		t.Fatalf("did not capture Go SDK PutObject headers")
	}
	if got := headers.Get("x-amz-content-sha256"); got != sigUnsignedTrailer {
		t.Fatalf("Go SDK PutObject x-amz-content-sha256=%q, want %q", got, sigUnsignedTrailer)
	}
	if got := headers.Get("Content-Encoding"); got != "aws-chunked" {
		t.Fatalf("Go SDK PutObject Content-Encoding=%q, want aws-chunked", got)
	}
	if got := headers.Get("x-amz-trailer"); got == "" {
		t.Fatalf("Go SDK PutObject x-amz-trailer missing")
	}

	gotObj, err := client.GetObject(ctx, &awss3.GetObjectInput{
		Bucket: aws.String(mount),
		Key:    aws.String(key),
	})
	if err != nil {
		t.Fatalf("Go SDK GetObject: %v", err)
	}
	defer gotObj.Body.Close()
	got, err := io.ReadAll(gotObj.Body)
	if err != nil {
		t.Fatalf("read Go SDK GetObject body: %v", err)
	}
	if !bytes.Equal(got, body) {
		t.Fatalf("Go SDK readback body=%q, want %q", got, body)
	}
}
