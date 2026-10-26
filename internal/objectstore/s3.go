package objectstore

import (
	"context"
	"io"
	"net/url"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/eugene-ruby/filehola/internal/config"
)

type S3 struct {
	client *s3.Client
	bucket string
}

func New(c config.Config) *S3 {
	client := s3.NewFromConfig(aws.Config{Region: c.S3Region, Credentials: credentials.NewStaticCredentialsProvider(c.S3AccessKey, c.S3SecretKey, "")}, func(o *s3.Options) { o.BaseEndpoint = aws.String(c.S3Endpoint); o.UsePathStyle = true })
	return &S3{client: client, bucket: c.S3Bucket}
}
func (s *S3) HeadBucket(ctx context.Context) error {
	_, err := s.client.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: &s.bucket})
	return err
}
func (s *S3) Put(ctx context.Context, key string, body io.ReadSeeker, size int64, mime string) error {
	_, err := s.client.PutObject(ctx, &s3.PutObjectInput{Bucket: &s.bucket, Key: &key, Body: body, ContentLength: &size, ContentType: &mime, IfNoneMatch: aws.String("*")})
	return err
}
func (s *S3) Copy(ctx context.Context, source, dest string) error {
	_, err := s.client.CopyObject(ctx, &s3.CopyObjectInput{Bucket: &s.bucket, Key: &dest, CopySource: aws.String(url.PathEscape(s.bucket + "/" + source))})
	return err
}
func (s *S3) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: &s.bucket, Key: &key})
	if err != nil {
		return nil, err
	}
	return out.Body, nil
}
func (s *S3) Delete(ctx context.Context, key string) error {
	_, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: &s.bucket, Key: &key})
	return err
}
