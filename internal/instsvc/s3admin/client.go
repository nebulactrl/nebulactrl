// Package s3admin is the control plane's administrator view of the built-in S3
// (SeaweedFS): it creates a team's bucket, a user whose key can only touch that
// bucket, applies the bucket's native quota, measures usage and purges.
//
// It talks to SeaweedFS as the "nebula-admin" identity that the installer's
// one-shot s3-init container creates, with SigV4 over the embedded IAM API
// (service "iam") and the S3 API (service "s3") on the same port.
package s3admin

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
)

const (
	// Region is the signing region; SeaweedFS accepts any.
	Region = "us-east-1"
	// maxReply bounds what an admin call reads back.
	maxReply = 8 << 20
)

// apiError is an error answer of SeaweedFS (S3 or IAM XML).
type apiError struct {
	Status  int
	Code    string
	Message string
}

func (e *apiError) Error() string {
	if e.Code == "" {
		return fmt.Sprintf("s3 service answered %d", e.Status)
	}
	return fmt.Sprintf("s3 service answered %d %s: %s", e.Status, e.Code, e.Message)
}

// hasCode reports whether err is an apiError with code.
func hasCode(err error, code string) bool {
	e, ok := err.(*apiError)
	return ok && e.Code == code
}

type client struct {
	base   *url.URL
	creds  aws.Credentials
	http   *http.Client
	signer *v4.Signer
}

// call sends one signed request and returns the body of a 2xx answer.
func (c *client) call(ctx context.Context, service, method, path, rawQuery string, body []byte, header map[string]string) ([]byte, error) {
	u := *c.base
	u.Path, u.RawQuery = path, rawQuery
	req, err := http.NewRequestWithContext(ctx, method, u.String(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	for k, v := range header {
		req.Header.Set(k, v)
	}
	sum := sha256.Sum256(body)
	hash := hex.EncodeToString(sum[:])
	// SeaweedFS reads the payload hash from this header; the signer alone does not set it.
	req.Header.Set("X-Amz-Content-Sha256", hash)
	if err := c.signer.SignHTTP(ctx, c.creds, req, hash, service, Region, time.Now()); err != nil {
		return nil, fmt.Errorf("sign request: %w", err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxReply))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		return nil, parseError(resp.StatusCode, data)
	}
	return data, nil
}

// parseError reads the S3 (<Error>) or IAM (<ErrorResponse><Error>) XML error.
func parseError(status int, data []byte) error {
	var s3 struct {
		Code    string `xml:"Code"`
		Message string `xml:"Message"`
	}
	var iam struct {
		Error struct {
			Code    string `xml:"Code"`
			Message string `xml:"Message"`
		} `xml:"Error"`
	}
	e := &apiError{Status: status}
	switch {
	case xml.Unmarshal(data, &iam) == nil && iam.Error.Code != "":
		e.Code, e.Message = iam.Error.Code, iam.Error.Message
	case xml.Unmarshal(data, &s3) == nil && s3.Code != "":
		e.Code, e.Message = s3.Code, s3.Message
	}
	return e
}

// iam calls an IAM action.
func (c *client) iam(ctx context.Context, action string, kv ...string) ([]byte, error) {
	v := url.Values{"Action": {action}, "Version": {"2010-05-08"}}
	for i := 0; i+1 < len(kv); i += 2 {
		v.Set(kv[i], kv[i+1])
	}
	return c.call(ctx, "iam", http.MethodPost, "/", "", []byte(v.Encode()),
		map[string]string{"Content-Type": "application/x-www-form-urlencoded"})
}

// s3 calls an S3 operation on a bucket path.
func (c *client) s3(ctx context.Context, method, path, rawQuery string, body []byte) ([]byte, error) {
	return c.call(ctx, "s3", method, path, rawQuery, body, nil)
}

func escapePath(p string) string { return (&url.URL{Path: p}).EscapedPath() }

func newClient(endpoint, key, secret string, hc *http.Client) (*client, error) {
	u, err := url.Parse(strings.TrimRight(endpoint, "/"))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("s3 endpoint %q is not an http(s) URL", endpoint)
	}
	if hc == nil {
		hc = &http.Client{Timeout: 2 * time.Minute}
	}
	return &client{base: u, creds: aws.Credentials{AccessKeyID: key, SecretAccessKey: secret}, http: hc, signer: v4.NewSigner()}, nil
}
