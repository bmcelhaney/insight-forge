package storage

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"path"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// TigrisConfig holds S3-compatible Tigris connection settings (never log secrets).
type TigrisConfig struct {
	Enabled   bool
	Bucket    string
	Region    string
	Endpoint  string
	AccessKey string
	SecretKey string
	// KeyPrefix is prepended to all object keys (default insight-forge/).
	KeyPrefix string
}

// Client is a thin Tigris/S3 wrapper for pricing-evidence artifacts.
type Client struct {
	s3     *s3.Client
	presign *s3.PresignClient
	bucket string
	prefix string
}

// NewTigrisClient builds an S3 client pointed at Tigris. Returns (nil, nil) when disabled.
func NewTigrisClient(cfg TigrisConfig) (*Client, error) {
	if !cfg.Enabled {
		return nil, nil
	}
	bucket := strings.TrimSpace(cfg.Bucket)
	endpoint := strings.TrimSpace(cfg.Endpoint)
	ak := strings.TrimSpace(cfg.AccessKey)
	sk := strings.TrimSpace(cfg.SecretKey)
	if bucket == "" || endpoint == "" || ak == "" || sk == "" {
		return nil, fmt.Errorf("tigris: enabled but bucket/endpoint/credentials incomplete")
	}
	region := strings.TrimSpace(cfg.Region)
	if region == "" {
		region = "auto"
	}
	prefix := strings.TrimSpace(cfg.KeyPrefix)
	if prefix == "" {
		prefix = "insight-forge/"
	}
	if !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}

	awsCfg := aws.Config{
		Region: region,
		Credentials: credentials.NewStaticCredentialsProvider(ak, sk, ""),
	}
	s3c := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(endpoint)
		o.UsePathStyle = true // Tigris-friendly
	})
	return &Client{
		s3:      s3c,
		presign: s3.NewPresignClient(s3c),
		bucket:  bucket,
		prefix:  prefix,
	}, nil
}

// Bucket returns the configured bucket name.
func (c *Client) Bucket() string {
	if c == nil {
		return ""
	}
	return c.bucket
}

// EvidenceObjectKey builds a stable key for a hit screenshot.
// Layout: insight-forge/{yyyy}/{mm}/{dd}/{nsn}/{analysisID}/hits/{hitID}.png
func (c *Client) EvidenceObjectKey(nsn, analysisID, hitID string, capturedAt time.Time) string {
	if capturedAt.IsZero() {
		capturedAt = time.Now().UTC()
	}
	nsn = digitsOnly(nsn)
	analysisID = sanitizeKeyPart(analysisID)
	hitID = sanitizeKeyPart(hitID)
	return path.Join(
		strings.TrimSuffix(c.prefix, "/"),
		capturedAt.Format("2006"),
		capturedAt.Format("01"),
		capturedAt.Format("02"),
		nsn,
		analysisID,
		"hits",
		hitID+".png",
	)
}

// PutObject uploads bytes to the bucket.
func (c *Client) PutObject(ctx context.Context, key string, body []byte, contentType string, meta map[string]string) error {
	if c == nil {
		return fmt.Errorf("tigris: client not configured")
	}
	key = strings.TrimPrefix(key, "/")
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	// S3 metadata values must be ASCII-ish; trim aggressively.
	md := map[string]string{}
	for k, v := range meta {
		k = strings.ToLower(strings.TrimSpace(k))
		v = strings.TrimSpace(v)
		if k == "" || v == "" {
			continue
		}
		if len(v) > 2000 {
			v = v[:2000]
		}
		md[k] = v
	}
	_, err := c.s3.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(c.bucket),
		Key:         aws.String(key),
		Body:        bytes.NewReader(body),
		ContentType: aws.String(contentType),
		Metadata:    md,
	})
	if err != nil {
		return fmt.Errorf("tigris put %s: %w", key, err)
	}
	return nil
}

// PresignGet returns a time-limited GET URL for an object (UI/proxy use only;
// not embedded in data-capture JSON for long-term storage).
func (c *Client) PresignGet(ctx context.Context, key string, ttl time.Duration) (string, error) {
	if c == nil {
		return "", fmt.Errorf("tigris: client not configured")
	}
	if ttl <= 0 {
		ttl = time.Hour
	}
	if ttl > 24*time.Hour {
		ttl = 24 * time.Hour
	}
	out, err := c.presign.PresignGetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(c.bucket),
		Key:    aws.String(key),
	}, s3.WithPresignExpires(ttl))
	if err != nil {
		return "", fmt.Errorf("tigris presign: %w", err)
	}
	return out.URL, nil
}

// GetObject downloads object bytes from the bucket (for UI image proxy).
func (c *Client) GetObject(ctx context.Context, key string) (body []byte, contentType string, err error) {
	if c == nil {
		return nil, "", fmt.Errorf("tigris: client not configured")
	}
	key = strings.TrimPrefix(key, "/")
	out, err := c.s3.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(c.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return nil, "", fmt.Errorf("tigris get %s: %w", key, err)
	}
	defer out.Body.Close()
	data, err := io.ReadAll(io.LimitReader(out.Body, 15<<20)) // 15 MiB cap
	if err != nil {
		return nil, "", fmt.Errorf("tigris get read %s: %w", key, err)
	}
	ct := ""
	if out.ContentType != nil {
		ct = *out.ContentType
	}
	if ct == "" {
		ct = "application/octet-stream"
	}
	return data, ct, nil
}

// Ping does a cheap HeadBucket to verify credentials/bucket (no secrets logged).
func (c *Client) Ping(ctx context.Context) error {
	if c == nil {
		return fmt.Errorf("tigris: client not configured")
	}
	_, err := c.s3.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(c.bucket)})
	if err != nil {
		return fmt.Errorf("tigris head bucket %q: %w", c.bucket, err)
	}
	return nil
}

func sanitizeKeyPart(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "unknown"
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	out := b.String()
	if out == "" {
		return "unknown"
	}
	if len(out) > 120 {
		out = out[:120]
	}
	return out
}

func digitsOnly(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "unknown"
	}
	return b.String()
}
