package backup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/feature/s3/transfermanager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// s3PartSize is the multipart part size; at most this much of a dump is
// buffered in agent memory per in-flight part. s3Concurrency bounds in-flight
// parts (memory = concurrency * part size). Variables so tests can shrink them.
var (
	s3PartSize    int64 = 16 << 20
	s3Concurrency       = 2
)

// S3Store stores objects in an S3-compatible bucket with aws-sdk-go-v2.
type S3Store struct {
	client *s3.Client
	up     *transfermanager.Client
	bucket string
}

// NewS3Store builds a store for dest. With dest.CompatMode the SDK's default
// CRC32 request checksums and response validation are limited to operations that
// require them, which non-AWS S3 implementations (R2, B2, Wasabi, SeaweedFS,
// Garage, ...) otherwise reject.
func NewS3Store(dest agentprotocol.BackupDestination, accessKey, secretKey string) (*S3Store, error) {
	if dest.Bucket == "" {
		return nil, errors.New("s3 destination has no bucket")
	}
	if accessKey == "" || secretKey == "" {
		return nil, errors.New("s3 credentials missing")
	}
	region := dest.Region
	if region == "" {
		region = "us-east-1"
	}
	cfg := aws.Config{
		Region:      region,
		Credentials: credentials.NewStaticCredentialsProvider(accessKey, secretKey, ""),
	}
	client := s3.NewFromConfig(cfg, func(o *s3.Options) {
		o.UsePathStyle = dest.PathStyle
		if dest.Endpoint != "" {
			o.BaseEndpoint = aws.String(dest.Endpoint)
		}
		if dest.CompatMode {
			o.RequestChecksumCalculation = aws.RequestChecksumCalculationWhenRequired
			o.ResponseChecksumValidation = aws.ResponseChecksumValidationWhenRequired
		}
	})
	up := transfermanager.New(client, func(o *transfermanager.Options) {
		o.PartSizeBytes = s3PartSize
		o.MultipartUploadThreshold = s3PartSize
		o.Concurrency = s3Concurrency
		if dest.CompatMode {
			o.RequestChecksumCalculation = aws.RequestChecksumCalculationWhenRequired
		}
	})
	return &S3Store{client: client, up: up, bucket: dest.Bucket}, nil
}

// Put implements Store. The transfer manager reads r in parts; when r fails
// before EOF it aborts the multipart upload (or never issues the single PUT), so
// no object appears.
func (s *S3Store) Put(ctx context.Context, key string, r io.Reader) error {
	if err := ValidKey(key); err != nil {
		return err
	}
	_, err := s.up.UploadObject(ctx, &transfermanager.UploadObjectInput{
		Bucket: aws.String(s.bucket), Key: aws.String(key), Body: r,
	})
	if err != nil {
		return fmt.Errorf("upload %s: %w", key, err)
	}
	return nil
}

// Get implements Store.
func (s *S3Store) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	if err := ValidKey(key); err != nil {
		return nil, err
	}
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
	if err != nil {
		if isNotFound(err) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get %s: %w", key, err)
	}
	return out.Body, nil
}

// Size implements Store.
func (s *S3Store) Size(ctx context.Context, key string) (int64, error) {
	if err := ValidKey(key); err != nil {
		return 0, err
	}
	out, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
	if err != nil {
		if isNotFound(err) {
			return 0, ErrNotFound
		}
		return 0, fmt.Errorf("head %s: %w", key, err)
	}
	return aws.ToInt64(out.ContentLength), nil
}

// Delete implements Store.
func (s *S3Store) Delete(ctx context.Context, key string) error {
	if err := ValidKey(key); err != nil {
		return err
	}
	_, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
	if err != nil && !isNotFound(err) {
		return fmt.Errorf("delete %s: %w", key, err)
	}
	return nil
}

func isNotFound(err error) bool {
	var nsk *s3types.NoSuchKey
	var nf *s3types.NotFound
	if errors.As(err, &nsk) || errors.As(err, &nf) {
		return true
	}
	var ae smithy.APIError
	if errors.As(err, &ae) {
		switch ae.ErrorCode() {
		case "NoSuchKey", "NotFound", "404":
			return true
		}
	}
	return strings.Contains(err.Error(), "StatusCode: 404")
}
