package s3admin

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/nebulactrl/nebulactrl/internal/instsvc"
)

// Config is how the control plane reaches SeaweedFS.
type Config struct {
	// Endpoint is the S3/IAM address on the internal network (http://seaweedfs:8333).
	Endpoint string
	// AdminKey and AdminSecret are the nebula-admin identity the installer created.
	AdminKey, AdminSecret string
	// Slices opens a slice's secret (the key the team's backups will use).
	Slices instsvc.Slices
	// HTTP is the client used for every call (nil: a client with a 2-minute timeout).
	HTTP *http.Client
}

// Backend implements instsvc.Backend for the s3 kind.
type Backend struct {
	c      *client
	slices instsvc.Slices
}

var _ instsvc.Backend = (*Backend)(nil)

// New returns a Backend.
func New(cfg Config) (*Backend, error) {
	c, err := newClient(cfg.Endpoint, cfg.AdminKey, cfg.AdminSecret, cfg.HTTP)
	if err != nil {
		return nil, err
	}
	return &Backend{c: c, slices: cfg.Slices}, nil
}

// sliceUser is the IAM user of a bucket: one user per slice, named like its bucket.
func sliceUser(s instsvc.Slice) string { return s.Ref }

// Health checks that SeaweedFS answers and accepts the admin identity.
func (b *Backend) Health(ctx context.Context) error {
	if _, err := b.c.iam(ctx, "ListUsers"); err != nil {
		return fmt.Errorf("built-in S3: %w", err)
	}
	return nil
}

// policyFor is the policy that confines a user to one bucket.
func policyFor(bucket string) (string, error) {
	raw, err := json.Marshal(map[string]any{
		"Version": "2012-10-17",
		"Statement": []map[string]any{{
			"Effect": "Allow",
			"Action": []string{"s3:*"},
			"Resource": []string{
				"arn:aws:s3:::" + bucket,
				"arn:aws:s3:::" + bucket + "/*",
			},
		}},
	})
	return string(raw), err
}

// EnsureSlice makes the bucket, a user whose only key is the slice's key and
// whose policy reaches only that bucket, and the bucket's native quota
// (quotaBytes 0: none). It is idempotent, and it repairs a half-made slice.
func (b *Backend) EnsureSlice(ctx context.Context, s instsvc.Slice, quotaBytes int64) error {
	if s.Ref == "" || s.AccessKey == "" {
		return errors.New("a bucket slice needs a bucket and an access key")
	}
	secret, err := b.slices.Secret(ctx, s)
	if err != nil {
		return fmt.Errorf("open slice key: %w", err)
	}
	// 1. The bucket (admin identity).
	if _, err := b.c.s3(ctx, http.MethodPut, "/"+s.Ref, ""); err != nil && !hasCode(err, "BucketAlreadyOwnedByYou") {
		return fmt.Errorf("create bucket: %w", err)
	}
	// 2. The user.
	user := sliceUser(s)
	if _, err := b.c.iam(ctx, "CreateUser", "UserName", user); err != nil && !hasCode(err, "EntityAlreadyExists") {
		return fmt.Errorf("create user: %w", err)
	}
	// 3. Exactly one key: the slice's. Any other key of this user is revoked.
	keys, err := b.accessKeys(ctx, user)
	if err != nil {
		return fmt.Errorf("list keys: %w", err)
	}
	have := false
	for _, k := range keys {
		if k == s.AccessKey {
			have = true
			continue
		}
		if _, err := b.c.iam(ctx, "DeleteAccessKey", "UserName", user, "AccessKeyId", k); err != nil && !hasCode(err, "NoSuchEntity") {
			return fmt.Errorf("revoke old key: %w", err)
		}
	}
	if !have {
		if _, err := b.c.iam(ctx, "CreateAccessKey", "UserName", user, "AccessKeyId", s.AccessKey, "SecretAccessKey", secret); err != nil {
			return fmt.Errorf("create key: %w", err)
		}
	}
	// 4. The policy: the user's key reaches this bucket and nothing else.
	pol, err := policyFor(s.Ref)
	if err != nil {
		return err
	}
	if _, err := b.c.iam(ctx, "PutUserPolicy", "UserName", user, "PolicyName", "slice", "PolicyDocument", pol); err != nil {
		return fmt.Errorf("put policy: %w", err)
	}
	// 5. The native quota, a second fence behind the control plane's own gate.
	if quotaBytes > 0 {
		if err := b.SetQuota(ctx, s.Ref, quotaBytes); err != nil {
			return err
		}
	}
	return nil
}

func (b *Backend) accessKeys(ctx context.Context, user string) ([]string, error) {
	data, err := b.c.iam(ctx, "ListAccessKeys", "UserName", user)
	if err != nil {
		return nil, err
	}
	var out struct {
		Members []struct {
			ID string `xml:"AccessKeyId"`
		} `xml:"ListAccessKeysResult>AccessKeyMetadata>member"`
	}
	if err := xml.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("decode key list: %w", err)
	}
	ids := make([]string, 0, len(out.Members))
	for _, m := range out.Members {
		ids = append(ids, m.ID)
	}
	return ids, nil
}

// SetQuota sets and enables the bucket's native quota (0 removes it). SeaweedFS
// flips a bucket read-only when a periodic job (s3.bucket.quota.enforce, every
// ten minutes) finds it over, and back when it finds it under. A changed quota
// is applied as disable-then-enable: disabling clears the read-only flag at once,
// so raising a quota does not leave the team locked out until the next run (if the
// bucket is still over the new quota the next run flips it again). An unchanged
// quota is left alone, so the periodic EnsureSlice does not undo the enforcer.
func (b *Backend) SetQuota(ctx context.Context, bucket string, quotaBytes int64) error {
	if cur, err := b.Quota(ctx, bucket); err == nil && cur == quotaBytes {
		return nil
	}
	put := func(size int64, enabled bool) error {
		body, _ := json.Marshal(map[string]any{"quota_size": size, "quota_unit": "B", "quota_enabled": enabled})
		if _, err := b.c.call(ctx, "s3", http.MethodPut, "/"+bucket, "seaweedfs-quota", body,
			map[string]string{"Content-Type": "application/json"}); err != nil {
			return fmt.Errorf("set bucket quota: %w", err)
		}
		return nil
	}
	if err := put(0, false); err != nil {
		return err
	}
	if quotaBytes <= 0 {
		return nil
	}
	return put(quotaBytes, true)
}

// Quota reads the bucket's native quota in bytes (0: none or disabled).
func (b *Backend) Quota(ctx context.Context, bucket string) (int64, error) {
	data, err := b.c.s3(ctx, http.MethodGet, "/"+bucket, "seaweedfs-quota")
	if err != nil {
		return 0, fmt.Errorf("get bucket quota: %w", err)
	}
	var q struct {
		Size    int64  `json:"quota_size"`
		Unit    string `json:"quota_unit"`
		Enabled bool   `json:"quota_enabled"`
	}
	if err := json.Unmarshal(data, &q); err != nil {
		return 0, fmt.Errorf("decode bucket quota: %w", err)
	}
	if !q.Enabled {
		return 0, nil
	}
	return q.Size, nil
}

type listResult struct {
	IsTruncated bool   `xml:"IsTruncated"`
	NextToken   string `xml:"NextContinuationToken"`
	Contents    []struct {
		Key  string `xml:"Key"`
		Size int64  `xml:"Size"`
	} `xml:"Contents"`
}

func (b *Backend) list(ctx context.Context, bucket, token string) (listResult, error) {
	q := url.Values{"list-type": {"2"}, "max-keys": {"1000"}}
	if token != "" {
		q.Set("continuation-token", token)
	}
	data, err := b.c.s3(ctx, http.MethodGet, "/"+bucket, q.Encode())
	if err != nil {
		return listResult{}, err
	}
	var r listResult
	if err := xml.Unmarshal(data, &r); err != nil {
		return r, fmt.Errorf("decode listing: %w", err)
	}
	return r, nil
}

// Usage is the bytes the bucket holds: the sum of ListObjectsV2 sizes plus the
// parts of multipart uploads that were started and not completed (an unfinished
// upload occupies disk but is absent from the object listing). Uploads older
// than StaleUploadAge are aborted and no longer count.
func (b *Backend) Usage(ctx context.Context, s instsvc.Slice) (int64, error) {
	var total int64
	token := ""
	for {
		r, err := b.list(ctx, s.Ref, token)
		if err != nil {
			return 0, fmt.Errorf("list bucket: %w", err)
		}
		for _, o := range r.Contents {
			total += o.Size
		}
		if !r.IsTruncated || r.NextToken == "" {
			break
		}
		token = r.NextToken
	}
	pending, err := b.pendingUploadBytes(ctx, s.Ref)
	if err != nil {
		return 0, err
	}
	return total + pending, nil
}

// StaleUploadAge is how long a multipart upload may stay unfinished before the
// usage job aborts it.
const StaleUploadAge = 24 * time.Hour

// maxUploadsScanned bounds one usage run: a bucket with more unfinished uploads
// than this has the surplus aborted, oldest listing order first, so the count
// can never be dodged by opening thousands of uploads.
const maxUploadsScanned = 2000

type uploadsResult struct {
	IsTruncated        bool   `xml:"IsTruncated"`
	NextKeyMarker      string `xml:"NextKeyMarker"`
	NextUploadIDMarker string `xml:"NextUploadIdMarker"`
	Uploads            []struct {
		Key       string `xml:"Key"`
		UploadID  string `xml:"UploadId"`
		Initiated string `xml:"Initiated"`
	} `xml:"Upload"`
}

type partsResult struct {
	IsTruncated          bool `xml:"IsTruncated"`
	NextPartNumberMarker int  `xml:"NextPartNumberMarker"`
	Parts                []struct {
		Size int64 `xml:"Size"`
	} `xml:"Part"`
}

// pendingUploadBytes sums the uploaded parts of the bucket's unfinished
// multipart uploads and aborts the stale ones.
func (b *Backend) pendingUploadBytes(ctx context.Context, bucket string) (int64, error) {
	var total int64
	scanned := 0
	keyMarker, idMarker := "", ""
	for {
		q := url.Values{"uploads": {""}, "max-uploads": {"1000"}}
		if keyMarker != "" {
			q.Set("key-marker", keyMarker)
		}
		if idMarker != "" {
			q.Set("upload-id-marker", idMarker)
		}
		data, err := b.c.s3(ctx, http.MethodGet, "/"+bucket, q.Encode())
		if err != nil {
			return 0, fmt.Errorf("list multipart uploads: %w", err)
		}
		var r uploadsResult
		if len(bytes.TrimSpace(data)) > 0 {
			if err := xml.Unmarshal(data, &r); err != nil {
				return 0, fmt.Errorf("decode multipart uploads: %w", err)
			}
		}
		for _, u := range r.Uploads {
			scanned++
			started, perr := time.Parse(time.RFC3339, u.Initiated)
			stale := perr == nil && time.Since(started) > StaleUploadAge
			if stale || scanned > maxUploadsScanned {
				if err := b.abortUpload(ctx, bucket, u.Key, u.UploadID); err != nil {
					return 0, err
				}
				continue
			}
			n, err := b.uploadPartBytes(ctx, bucket, u.Key, u.UploadID)
			if err != nil {
				return 0, err
			}
			total += n
		}
		if !r.IsTruncated || (r.NextKeyMarker == "" && r.NextUploadIDMarker == "") {
			return total, nil
		}
		keyMarker, idMarker = r.NextKeyMarker, r.NextUploadIDMarker
	}
}

func (b *Backend) uploadPartBytes(ctx context.Context, bucket, key, uploadID string) (int64, error) {
	var total int64
	marker := 0
	for {
		q := url.Values{"uploadId": {uploadID}, "max-parts": {"1000"}}
		if marker > 0 {
			q.Set("part-number-marker", strconv.Itoa(marker))
		}
		data, err := b.c.s3(ctx, http.MethodGet, "/"+bucket+"/"+key, q.Encode())
		switch {
		case hasCode(err, "NoSuchUpload"):
			return total, nil // completed or aborted since the listing
		case err != nil:
			return 0, fmt.Errorf("list parts: %w", err)
		}
		var r partsResult
		if len(bytes.TrimSpace(data)) > 0 {
			if err := xml.Unmarshal(data, &r); err != nil {
				return 0, fmt.Errorf("decode parts: %w", err)
			}
		}
		for _, p := range r.Parts {
			total += p.Size
		}
		if !r.IsTruncated || r.NextPartNumberMarker <= marker {
			return total, nil
		}
		marker = r.NextPartNumberMarker
	}
}

func (b *Backend) abortUpload(ctx context.Context, bucket, key, uploadID string) error {
	_, err := b.c.s3(ctx, http.MethodDelete, "/"+bucket+"/"+key, url.Values{"uploadId": {uploadID}}.Encode())
	if err != nil && !hasCode(err, "NoSuchUpload") {
		return fmt.Errorf("abort multipart upload: %w", err)
	}
	return nil
}

// Purge deletes the slice's bucket with everything in it, then its user and
// key. It is idempotent: what is already gone is not an error.
func (b *Backend) Purge(ctx context.Context, s instsvc.Slice) error {
	if s.Ref == "" {
		return nil
	}
	if s.Ref == instsvc.SystemBucket {
		return errors.New("the system bucket is not purged")
	}
	err := b.deleteBucket(ctx, s.Ref)
	if err != nil {
		return err
	}
	user := sliceUser(s)
	keys, err := b.accessKeys(ctx, user)
	switch {
	case hasCode(err, "NoSuchEntity"):
		return nil
	case err != nil:
		return fmt.Errorf("list keys: %w", err)
	}
	for _, k := range keys {
		if _, err := b.c.iam(ctx, "DeleteAccessKey", "UserName", user, "AccessKeyId", k); err != nil && !hasCode(err, "NoSuchEntity") {
			return fmt.Errorf("delete key: %w", err)
		}
	}
	if _, err := b.c.iam(ctx, "DeleteUserPolicy", "UserName", user, "PolicyName", "slice"); err != nil && !hasCode(err, "NoSuchEntity") {
		return fmt.Errorf("delete policy: %w", err)
	}
	if _, err := b.c.iam(ctx, "DeleteUser", "UserName", user); err != nil && !hasCode(err, "NoSuchEntity") {
		return fmt.Errorf("delete user: %w", err)
	}
	return nil
}

// deleteBucket empties and deletes a bucket. A bucket that does not exist is done.
func (b *Backend) deleteBucket(ctx context.Context, bucket string) error {
	for {
		_, err := b.c.s3(ctx, http.MethodDelete, "/"+bucket, "")
		switch {
		case err == nil, hasCode(err, "NoSuchBucket"):
			return nil
		case !hasCode(err, "BucketNotEmpty"):
			return fmt.Errorf("delete bucket: %w", err)
		}
		if err := b.empty(ctx, bucket); err != nil {
			return err
		}
	}
}

// empty deletes every object of the bucket, a page at a time.
func (b *Backend) empty(ctx context.Context, bucket string) error {
	for {
		r, err := b.list(ctx, bucket, "")
		if err != nil {
			return fmt.Errorf("list bucket: %w", err)
		}
		if len(r.Contents) == 0 {
			return nil
		}
		type object struct {
			Key string `xml:"Key"`
		}
		var req struct {
			XMLName xml.Name `xml:"Delete"`
			Quiet   bool     `xml:"Quiet"`
			Objects []object `xml:"Object"`
		}
		req.Quiet = true
		for _, o := range r.Contents {
			req.Objects = append(req.Objects, object{Key: o.Key})
		}
		body, err := xml.Marshal(req)
		if err != nil {
			return err
		}
		if _, err := b.c.call(ctx, "s3", http.MethodPost, "/"+bucket, "delete", body,
			map[string]string{"Content-Type": "application/xml"}); err != nil {
			return fmt.Errorf("delete objects: %w", err)
		}
	}
}
