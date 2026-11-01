package backup

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// Object describes one stored object found by Lister.List.
type Object struct {
	Key     string
	Size    int64
	ModTime time.Time
}

// Lister is implemented by stores that can enumerate their objects. The agent
// never lists (it is told exact keys); the control plane does, to find the
// control-plane backups a restore can use. It is separate from Store because
// not every Store (or fake) can enumerate its objects.
type Lister interface {
	// List returns the objects whose key starts with prefix (a "/"-separated
	// key prefix, "" for everything), oldest-agnostic and in no particular
	// order. Hidden partial uploads (".partial-*") are not objects and are skipped.
	List(ctx context.Context, prefix string) ([]Object, error)
}

// List implements Lister by walking the store directory.
func (s *LocalStore) List(ctx context.Context, prefix string) ([]Object, error) {
	var out []Object
	err := filepath.WalkDir(s.root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) && p == s.root {
				return fs.SkipAll // nothing was ever stored
			}
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if d.IsDir() || strings.HasPrefix(d.Name(), ".partial-") {
			return nil
		}
		rel, err := filepath.Rel(s.root, p)
		if err != nil {
			return err
		}
		key := filepath.ToSlash(rel)
		if !strings.HasPrefix(key, prefix) {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		out = append(out, Object{Key: key, Size: info.Size(), ModTime: info.ModTime()})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("list %s: %w", s.root, err)
	}
	return out, nil
}

// List implements Lister with ListObjectsV2.
func (s *S3Store) List(ctx context.Context, prefix string) ([]Object, error) {
	var out []Object
	p := s3.NewListObjectsV2Paginator(s.client, &s3.ListObjectsV2Input{Bucket: aws.String(s.bucket), Prefix: aws.String(prefix)})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("list %s: %w", path.Join(s.bucket, prefix), err)
		}
		for _, o := range page.Contents {
			out = append(out, Object{Key: aws.ToString(o.Key), Size: aws.ToInt64(o.Size), ModTime: aws.ToTime(o.LastModified)})
		}
	}
	return out, nil
}
