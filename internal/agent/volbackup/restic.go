package volbackup

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"path"
	"strconv"
	"strings"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// Exit codes of restic (doc/075_scripting.rst).
const (
	exitIncomplete = 3  // backup: some source files could not be read
	exitNoRepo     = 10 // the repository does not exist
	exitWrongKey   = 12 // wrong password
)

// Environment variable names of restic and the S3 SDK it uses.
const (
	envRepository = "RESTIC_REPOSITORY"
	envPassword   = "RESTIC_PASSWORD"
	envCache      = "RESTIC_CACHE_DIR"
	envAccessKey  = "AWS_ACCESS_KEY_ID"
	envSecretKey  = "AWS_SECRET_ACCESS_KEY"
	envRegion     = "AWS_DEFAULT_REGION"
)

// repoSpec is how restic reaches a repository from inside a helper.
type repoSpec struct {
	// URL is RESTIC_REPOSITORY.
	URL string
	// Opts are `-o key=value` extended options.
	Opts []string
	// Region is exported as AWS_DEFAULT_REGION (S3).
	Region string
}

// s3Network is the helper network an S3 destination needs: the default bridge,
// except for an endpoint on the server's own loopback (the built-in backup
// storage, offered by the agent's localproxy on 127.0.0.1), which a bridged
// helper cannot reach: it runs on the host network.
func s3Network(endpoint string) string {
	u, err := url.Parse(endpoint)
	if err != nil {
		return ""
	}
	h := u.Hostname()
	if ip := net.ParseIP(h); h == "localhost" || ip != nil && ip.IsLoopback() {
		return "host"
	}
	return ""
}

// s3Repo builds the restic repository URL of an S3 destination:
// s3:<endpoint>/<bucket>/<path>. An empty endpoint means AWS.
func s3Repo(dest agentprotocol.BackupDestination, repoPath string) repoSpec {
	var host string
	switch {
	case dest.Endpoint != "":
		host = strings.TrimSuffix(dest.Endpoint, "/") // keeps the scheme: restic honours http/https
	case dest.Region != "":
		host = "s3." + dest.Region + ".amazonaws.com"
	default:
		host = "s3.amazonaws.com"
	}
	spec := repoSpec{URL: "s3:" + host + "/" + dest.Bucket + "/" + strings.Trim(repoPath, "/"), Region: dest.Region}
	if dest.Region != "" {
		spec.Opts = append(spec.Opts, "s3.region="+dest.Region)
	}
	if dest.Endpoint != "" {
		if dest.PathStyle {
			spec.Opts = append(spec.Opts, "s3.bucket-lookup=path")
		} else {
			spec.Opts = append(spec.Opts, "s3.bucket-lookup=dns")
		}
	}
	return spec
}

// localRepo is the repository path of a local destination: <root>[/<localDir>]/<repoPath>.
func localRepo(root string, dest agentprotocol.BackupDestination, repoPath string) repoSpec {
	p := root
	if dest.LocalDir != "" {
		p = path.Join(p, dest.LocalDir)
	}
	return repoSpec{URL: path.Join(p, repoPath)}
}

// resticEnv is the exec environment of a restic run: the repository, the
// password and the S3 credentials. It is passed per exec (never in the helper's
// create config, which `docker inspect` would show).
func resticEnv(spec repoSpec, secrets map[string]string) []string {
	env := []string{
		envRepository + "=" + spec.URL,
		envPassword + "=" + secrets[agentprotocol.SecretResticPassword],
		envCache + "=" + agentprotocol.HelperCacheDir,
	}
	if v := secrets[agentprotocol.SecretBackupAccessKey]; v != "" {
		env = append(env, envAccessKey+"="+v)
	}
	if v := secrets[agentprotocol.SecretBackupSecretKey]; v != "" {
		env = append(env, envSecretKey+"="+v)
	}
	if spec.Region != "" {
		env = append(env, envRegion+"="+spec.Region)
	}
	return env
}

// resticArgv prefixes args with the extended options of the repository.
func resticArgv(spec repoSpec, args ...string) []string {
	out := []string{"restic"}
	for _, o := range spec.Opts {
		out = append(out, "-o", o)
	}
	return append(out, args...)
}

// backupArgs builds the `restic backup` arguments. The path (/vol), the host
// and --ignore-ctime/--ignore-inode keep the parent snapshot findable: a staged
// copy has new inodes and ctimes on every run, a random host name would hide
// the parent entirely.
func backupArgs(p agentprotocol.VolumeBackupPayload) []string {
	tags := []string{"nc-svc=" + p.ServiceID, "nc-backup=" + p.BackupID}
	if p.Trigger != "" {
		tags = append(tags, "nc-trigger="+p.Trigger)
	}
	args := []string{
		"backup", agentprotocol.HelperVolRoot,
		"--host", agentprotocol.VolumeHostName(p.ServiceID),
		"--tag", strings.Join(tags, ","),
		"--ignore-ctime", "--ignore-inode", "--exclude-caches", "--no-scan", "--json",
		"--exclude", agentprotocol.HelperVolRoot + "/*/lost+found",
	}
	for _, ex := range p.Excludes {
		if ex = strings.TrimSpace(ex); ex != "" {
			args = append(args, "--exclude", ex)
		}
	}
	return args
}

// forgetPolicyArgs builds the dry-run `restic forget` of a retention policy.
func forgetPolicyArgs(serviceID string, k agentprotocol.VolumeKeep) []string {
	args := []string{"forget", "--dry-run", "--json"}
	if k.Daily > 0 {
		args = append(args, "--keep-daily", strconv.Itoa(k.Daily))
	}
	if k.Weekly > 0 {
		args = append(args, "--keep-weekly", strconv.Itoa(k.Weekly))
	}
	if k.Monthly > 0 {
		args = append(args, "--keep-monthly", strconv.Itoa(k.Monthly))
	}
	return append(args, "--host", agentprotocol.VolumeHostName(serviceID), "--tag", "nc-svc="+serviceID)
}

// forgetIDsArgs builds `restic forget --prune <ids...>`.
func forgetIDsArgs(ids []string) []string {
	return append([]string{"forget", "--prune"}, ids...)
}

// summary is the final `restic backup --json` message.
type summary struct {
	FilesNew        int64  `json:"files_new"`
	FilesChanged    int64  `json:"files_changed"`
	FilesUnmodified int64  `json:"files_unmodified"`
	DataAdded       int64  `json:"data_added"`
	TotalBytes      int64  `json:"total_bytes_processed"`
	SnapshotID      string `json:"snapshot_id"`
}

// summaryCollector is an io.Writer that reads the JSON lines of
// `restic backup --json` as they arrive and keeps the summary message and the
// error messages: the progress lines (one per second for hours) are dropped.
type summaryCollector struct {
	partial []byte
	s       summary
	found   bool
	errs    []string
	// OnProgress, when set, receives the percent done of status lines.
	OnProgress func(percent float64)
}

func (c *summaryCollector) Write(p []byte) (int, error) {
	c.partial = append(c.partial, p...)
	for {
		i := bytes.IndexByte(c.partial, '\n')
		if i < 0 {
			break
		}
		c.line(c.partial[:i])
		c.partial = c.partial[i+1:]
	}
	if len(c.partial) > 4<<20 { // a runaway line without a newline
		c.partial = nil
	}
	return len(p), nil
}

func (c *summaryCollector) line(line []byte) {
	line = bytes.TrimSpace(line)
	if len(line) == 0 || line[0] != '{' {
		return
	}
	var head struct {
		Type    string  `json:"message_type"`
		Percent float64 `json:"percent_done"`
		Error   struct {
			Message string `json:"message"`
		} `json:"error"`
		Item string `json:"item"`
	}
	if json.Unmarshal(line, &head) != nil {
		return
	}
	switch head.Type {
	case "summary":
		if json.Unmarshal(line, &c.s) == nil {
			c.found = true
		}
	case "status":
		if c.OnProgress != nil {
			c.OnProgress(head.Percent)
		}
	case "error":
		msg := head.Error.Message
		if head.Item != "" {
			msg = head.Item + ": " + msg
		}
		if len(c.errs) < 20 {
			c.errs = append(c.errs, msg)
		}
	}
}

// flush handles a final line without a newline.
func (c *summaryCollector) flush() {
	if len(c.partial) > 0 {
		c.line(c.partial)
		c.partial = nil
	}
}

// snapshotIDs extracts the ids of `restic snapshots --json` output.
func snapshotIDs(out []byte) ([]string, error) {
	var snaps []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(out), &snaps); err != nil {
		return nil, fmt.Errorf("parse restic snapshots: %w", err)
	}
	ids := make([]string, 0, len(snaps))
	for _, s := range snaps {
		ids = append(ids, s.ID)
	}
	return ids, nil
}

// forgetVictims extracts the snapshot ids the dry-run `restic forget --json`
// would remove, minus those matching protect (full ids or id prefixes).
func forgetVictims(out []byte, protect []string) ([]string, error) {
	var groups []struct {
		Remove []struct {
			ID string `json:"id"`
		} `json:"remove"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(out), &groups); err != nil {
		return nil, fmt.Errorf("parse restic forget: %w", err)
	}
	var victims []string
	for _, g := range groups {
		for _, r := range g.Remove {
			if !matchesAny(r.ID, protect) {
				victims = append(victims, r.ID)
			}
		}
	}
	return victims, nil
}

func matchesAny(id string, prefixes []string) bool {
	for _, p := range prefixes {
		if p != "" && (strings.HasPrefix(id, p) || strings.HasPrefix(p, id)) {
			return true
		}
	}
	return false
}

// lsNode is one node of `restic ls --json`.
type lsNode struct {
	Name  string `json:"name"`
	Type  string `json:"type"`
	Path  string `json:"path"`
	Size  int64  `json:"size"`
	Mode  uint32 `json:"mode"`
	MTime string `json:"mtime"`
	Link  string `json:"linktarget"`
	Kind  string `json:"message_type"`
}

// parseLs reads `restic ls --json` output: the snapshot header line is
// skipped, and so is the listed directory itself (restic prints it first).
func parseLs(out []byte, self string) []lsNode {
	var nodes []lsNode
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 0, 64<<10), 4<<20)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		var n lsNode
		if json.Unmarshal(line, &n) != nil || n.Kind != "node" {
			continue
		}
		if n.Path == self && n.Type == "dir" {
			continue
		}
		nodes = append(nodes, n)
	}
	return nodes
}

// snapshotPath is the path of a volume's file inside the snapshot.
func snapshotPath(volume, p string) string {
	if p == "/" || p == "" {
		return agentprotocol.HelperVolRoot + "/" + volume
	}
	return agentprotocol.HelperVolRoot + "/" + volume + p
}
