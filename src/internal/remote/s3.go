package remote

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// newS3Remote parses s3://bucket[/prefix] and returns a segment-protocol remote over
// that bucket. Credentials/region resolve the standard AWS way (env vars, shared
// config/profile, IMDS); any S3-compatible service works via AWS_ENDPOINT_URL.
func newS3Remote(url string) (Remote, error) {
	rest := strings.TrimPrefix(url, "s3://")
	bucket, prefix, _ := strings.Cut(rest, "/")
	if bucket == "" {
		return nil, fmt.Errorf("invalid S3 remote %q (want s3://bucket[/prefix])", url)
	}
	client, err := newS3Client()
	if err != nil {
		return nil, err
	}
	return &objectRemote{
		os:     &s3Store{client: client, bucket: bucket},
		prefix: strings.Trim(prefix, "/"),
		name:   url,
	}, nil
}

func newS3Client() (*s3.Client, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cfg, err := awsconfig.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("loading AWS config: %w", err)
	}
	if cfg.Region == "" {
		cfg.Region = "us-east-1" // harmless default; buckets redirect as needed
	}
	return s3.NewFromConfig(cfg, func(o *s3.Options) {
		// Custom endpoints (MinIO, R2, LocalStack) usually need path-style addressing.
		if os.Getenv("AWS_ENDPOINT_URL") != "" || os.Getenv("AWS_ENDPOINT_URL_S3") != "" {
			o.UsePathStyle = true
		}
	}), nil
}

// s3Store adapts the AWS S3 client to the ObjectStore interface.
type s3Store struct {
	client *s3.Client
	bucket string
}

func (s *s3Store) ctx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 60*time.Second)
}

func (s *s3Store) List(prefix string) ([]string, error) {
	ctx, cancel := s.ctx()
	defer cancel()
	var keys []string
	p := s3.NewListObjectsV2Paginator(s.client, &s3.ListObjectsV2Input{
		Bucket: aws.String(s.bucket),
		Prefix: aws.String(strings.Trim(prefix, "/")),
	})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, o := range page.Contents {
			if o.Key != nil {
				keys = append(keys, *o.Key)
			}
		}
	}
	return keys, nil
}

func (s *s3Store) Get(key string) ([]byte, error) {
	ctx, cancel := s.ctx()
	defer cancel()
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return nil, err
	}
	defer out.Body.Close()
	return io.ReadAll(out.Body)
}

func (s *s3Store) Put(key string, data []byte) error {
	ctx, cancel := s.ctx()
	defer cancel()
	// Segments are write-once: If-None-Match:* makes S3 reject an overwrite, which
	// would only happen on a reused installID racing itself.
	_, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(s.bucket),
		Key:         aws.String(key),
		Body:        strings.NewReader(string(data)),
		IfNoneMatch: aws.String("*"),
	})
	if err != nil && isPreconditionFailed(err) {
		return fmt.Errorf("segment %s already exists on the remote (concurrent push from a duplicated install?)", key)
	}
	return err
}

func isPreconditionFailed(err error) bool {
	var ae interface{ ErrorCode() string }
	if errors.As(err, &ae) {
		code := ae.ErrorCode()
		return code == "PreconditionFailed" || code == "ConditionalRequestConflict"
	}
	return false
}
