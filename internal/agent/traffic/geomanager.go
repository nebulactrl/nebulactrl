package traffic

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

const (
	// DefaultDBIPBase hosts the DB-IP Lite monthly files:
	// <base>/dbip-{country|city}-lite-YYYY-MM.mmdb.gz (CC BY 4.0, no key).
	DefaultDBIPBase = "https://download.db-ip.com/free"
	// DefaultMaxMindBase hosts the GeoLite2 download permalinks:
	// <base>/geoip/databases/GeoLite2-{Country|City}/download?suffix=tar.gz
	// (HTTP basic auth with account ID and licence key; redirects to a presigned URL).
	DefaultMaxMindBase = "https://download.maxmind.com"

	geoStateFile  = "state.json"
	geoConfigFile = "config.json"
	geoTmpFile    = "download.tmp"

	// maxGeoDownload bounds the decompressed database (the city edition is ~130 MB).
	maxGeoDownload = 1 << 30
	// geoDownloadTimeout bounds one download.
	geoDownloadTimeout = 20 * time.Minute

	defaultGeoRefreshDays = 7
	maxGeoRefreshDays     = 90
	geoRetryMin           = time.Minute
	geoRetryMax           = time.Hour
)

var (
	errGeoNotPublished = errors.New("database not published")
	errGeoStale        = errors.New("geolocation settings changed during the download")
)

// geoState is what the agent remembers about the installed database.
type geoState struct {
	Source    string    `json:"source"`
	Level     string    `json:"level"`
	Label     string    `json:"label"` // DB-IP: YYYY-MM; MaxMind: build date YYYYMMDD
	File      string    `json:"file"`
	BuildTime time.Time `json:"buildTime"`
	CheckedAt time.Time `json:"checkedAt"`
}

// GeoManager keeps the GeoIP database of the configured source and level in
// <state-dir>/geoip: it downloads, verifies and atomically swaps it in,
// refreshes it every N days, and deletes it when geolocation is turned off or
// the source or level changes. Only the current database is kept.
type GeoManager struct {
	dir string
	log *slog.Logger
	db  *GeoDB

	// HTTP is the download client (no overall timeout; each download has a context deadline).
	HTTP        *http.Client
	DBIPBase    string
	MaxMindBase string
	Now         func() time.Time

	mu       sync.Mutex
	cfg      agentprotocol.GeoConfig
	gen      int // bumped by every change that invalidates an in-flight download
	account  string
	key      string
	state    geoState
	haveDB   bool
	failures int
	lastErr  string

	wake chan struct{}
}

// NewGeoManager returns a manager rooted at dir and reopens a previously
// installed database so origins keep counting across agent restarts.
func NewGeoManager(dir string, logger *slog.Logger) *GeoManager {
	if logger == nil {
		logger = slog.Default()
	}
	m := &GeoManager{
		dir: dir, log: logger.With("component", "geoip"), db: &GeoDB{},
		HTTP: &http.Client{}, DBIPBase: DefaultDBIPBase, MaxMindBase: DefaultMaxMindBase, Now: time.Now,
		wake: make(chan struct{}, 1),
	}
	m.restore()
	return m
}

// DB returns the lookup handle the aggregator uses.
func (m *GeoManager) DB() *GeoDB { return m.db }

// LastError is the reason the last download failed ("" when healthy).
func (m *GeoManager) LastError() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lastErr
}

// Status is the observed database for heartbeats: nil while geolocation is
// off, otherwise whether a database is installed, which release, how big it
// is, when it was last confirmed current and why the last attempt failed.
func (m *GeoManager) Status() *agentprotocol.GeoStatus {
	m.mu.Lock()
	enabled, have, st, lastErr := m.cfg.Enabled, m.haveDB, m.state, m.lastErr
	m.mu.Unlock()
	if !enabled {
		return nil
	}
	out := &agentprotocol.GeoStatus{Error: lastErr}
	if !have {
		return out
	}
	out.Downloaded = true
	out.Source, out.Level, out.Label = st.Source, st.Level, st.Label
	if !st.BuildTime.IsZero() {
		out.BuildTime = &st.BuildTime
	}
	if !st.CheckedAt.IsZero() {
		out.CheckedAt = &st.CheckedAt
	}
	if fi, err := os.Stat(filepath.Join(m.dir, filepath.Base(st.File))); err == nil {
		out.SizeBytes = fi.Size()
	}
	return out
}

// restore loads the persisted config and reopens the installed database.
func (m *GeoManager) restore() {
	var cfg agentprotocol.GeoConfig
	if !readJSON(filepath.Join(m.dir, geoConfigFile), &cfg) || !cfg.Enabled {
		return
	}
	m.cfg = normalizeGeo(cfg)
	var st geoState
	if !readJSON(filepath.Join(m.dir, geoStateFile), &st) || st.Source != m.cfg.Source || st.Level != m.cfg.Level || st.File == "" {
		return
	}
	path := filepath.Join(m.dir, filepath.Base(st.File))
	r, err := openGeoDatabaseFast(path, m.cfg.Level)
	if err != nil {
		m.log.Warn("stored geoip database unusable; it will be downloaded again", "err", err)
		m.removeDatabaseFiles()
		return
	}
	m.db.install(r, m.cfg.Level)
	m.state, m.haveDB = st, true
}

// Apply installs the control plane's geolocation setting (the MaxMind
// credentials arrive in secrets and are never persisted). Unchanged settings
// are a no-op, so it is safe to call on every reconcile.
func (m *GeoManager) Apply(cfg agentprotocol.GeoConfig, secrets map[string]string) {
	cfg = normalizeGeo(cfg)
	account, key := secrets[agentprotocol.SecretGeoMaxMindAccount], secrets[agentprotocol.SecretGeoMaxMindKey]

	m.mu.Lock()
	prev := m.cfg
	changed := prev != cfg
	credsChanged := m.account != account || m.key != key
	m.account, m.key = account, key
	m.cfg = cfg

	switch {
	case !cfg.Enabled:
		if prev.Enabled || m.haveDB {
			m.gen++
			m.purgeLocked()
		}
	case prev.Source != cfg.Source || prev.Level != cfg.Level:
		m.gen++
		m.dropDatabaseLocked()
	}
	if changed {
		m.persistConfigLocked()
	}
	wake := cfg.Enabled && (changed || (credsChanged && !m.haveDB))
	m.mu.Unlock()

	if wake {
		m.kick()
	}
}

func (m *GeoManager) kick() {
	select {
	case m.wake <- struct{}{}:
	default:
	}
}

// normalizeGeo fills defaults and clamps the agent-side view of a setting.
func normalizeGeo(c agentprotocol.GeoConfig) agentprotocol.GeoConfig {
	if c.Source != agentprotocol.GeoSourceMaxMind {
		c.Source = agentprotocol.GeoSourceDBIP
	}
	if c.Level != agentprotocol.GeoLevelCity {
		c.Level = agentprotocol.GeoLevelCountry
	}
	if c.RefreshDays < 1 {
		c.RefreshDays = defaultGeoRefreshDays
	}
	c.RefreshDays = min(c.RefreshDays, maxGeoRefreshDays)
	return c
}

// Run downloads and refreshes the database until ctx ends.
func (m *GeoManager) Run(ctx context.Context) {
	for {
		wait := m.syncOnce(ctx)
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-m.wake:
			t.Stop()
		case <-t.C:
		}
	}
}

// syncOnce brings the database up to date when it is due and returns how long
// to wait before looking again.
func (m *GeoManager) syncOnce(ctx context.Context) time.Duration {
	m.mu.Lock()
	cfg, gen, st, have := m.cfg, m.gen, m.state, m.haveDB
	account, key := m.account, m.key
	m.mu.Unlock()
	if !cfg.Enabled {
		return geoRetryMax
	}
	now := m.Now()
	interval := time.Duration(cfg.RefreshDays) * 24 * time.Hour
	if have {
		if due := st.CheckedAt.Add(interval); now.Before(due) {
			return min(due.Sub(now), 24*time.Hour)
		}
	}
	if cfg.Source == agentprotocol.GeoSourceMaxMind && (account == "" || key == "") {
		m.setError("MaxMind GeoLite2 needs an account ID and a licence key")
		return geoRetryMax
	}

	upToDate, err := m.refresh(ctx, cfg, gen, st, have, account, key)
	switch {
	case errors.Is(err, errGeoStale), ctx.Err() != nil:
		return time.Second
	case err != nil:
		m.mu.Lock()
		m.failures++
		m.lastErr = err.Error()
		failures := m.failures
		m.mu.Unlock()
		m.log.Warn("geoip database update failed", "source", cfg.Source, "level", cfg.Level, "err", err)
		if have {
			return geoRetryMax
		}
		return min(geoRetryMin<<min(failures-1, 6), geoRetryMax)
	}
	m.mu.Lock()
	m.failures, m.lastErr = 0, ""
	if upToDate && gen == m.gen {
		m.state.CheckedAt = now
		m.saveStateLocked()
	}
	m.mu.Unlock()
	return interval
}

func (m *GeoManager) setError(msg string) {
	m.mu.Lock()
	m.lastErr = msg
	m.mu.Unlock()
}

// refresh checks the source and installs a newer database. upToDate is true
// when the installed database is already the newest one.
func (m *GeoManager) refresh(ctx context.Context, cfg agentprotocol.GeoConfig, gen int, st geoState, have bool, account, key string) (upToDate bool, err error) {
	if err := os.MkdirAll(m.dir, 0o700); err != nil {
		return false, fmt.Errorf("create geoip directory: %w", err)
	}
	tmp := filepath.Join(m.dir, geoTmpFile)
	defer os.Remove(tmp)
	current := func() bool { return have && st.Source == cfg.Source && st.Level == cfg.Level }

	var label string
	var built time.Time
	switch cfg.Source {
	case agentprotocol.GeoSourceMaxMind:
		edition := "GeoLite2-Country"
		if cfg.Level == agentprotocol.GeoLevelCity {
			edition = "GeoLite2-City"
		}
		url := strings.TrimRight(m.MaxMindBase, "/") + "/geoip/databases/" + edition + "/download?suffix=tar.gz"
		head, err := m.maxmindHead(ctx, url, account, key)
		if err != nil {
			return false, err
		}
		if !head.IsZero() && current() && !head.After(st.BuildTime) {
			return true, nil
		}
		if err := m.download(ctx, url, account, key, tmp, extractTarGzMMDB); err != nil {
			return false, err
		}
		built, label = head, head.UTC().Format("20060102")
		if head.IsZero() {
			built, label = m.Now().UTC(), m.Now().UTC().Format("20060102")
		}
	default:
		edition := "country"
		if cfg.Level == agentprotocol.GeoLevelCity {
			edition = "city"
		}
		now := m.Now().UTC()
		first := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
		found := false
		for _, month := range []time.Time{first, first.AddDate(0, -1, 0)} {
			label = month.Format("2006-01")
			// A database of this month or newer is already installed.
			if current() && st.Label >= label {
				return true, nil
			}
			url := fmt.Sprintf("%s/dbip-%s-lite-%s.mmdb.gz", strings.TrimRight(m.DBIPBase, "/"), edition, label)
			err := m.download(ctx, url, "", "", tmp, extractGzip)
			if errors.Is(err, errGeoNotPublished) {
				continue // the new month's file appears a few days into the month
			}
			if err != nil {
				return false, err
			}
			found = true
			built = month
			break
		}
		if !found {
			return false, fmt.Errorf("DB-IP Lite %s: %w for this or the previous month", edition, errGeoNotPublished)
		}
	}
	return false, m.install(tmp, cfg, gen, label, built)
}

// maxmindHead returns the build time of the newest GeoLite2 release without
// spending a download (HEAD requests do not count against the daily limit).
func (m *GeoManager) maxmindHead(ctx context.Context, url, account, key string) (time.Time, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, url, nil)
	if err != nil {
		return time.Time{}, err
	}
	req.SetBasicAuth(account, key)
	req.Header.Set("User-Agent", "nebulactrl-agent")
	resp, err := m.HTTP.Do(req)
	if err != nil {
		return time.Time{}, fmt.Errorf("check MaxMind release: %w", err)
	}
	_ = resp.Body.Close()
	if err := maxmindStatus(resp.StatusCode); err != nil {
		return time.Time{}, err
	}
	lm, err := http.ParseTime(resp.Header.Get("Last-Modified"))
	if err != nil {
		return time.Time{}, nil
	}
	return lm.UTC(), nil
}

func maxmindStatus(code int) error {
	switch {
	case code == http.StatusOK:
		return nil
	case code == http.StatusUnauthorized || code == http.StatusForbidden:
		return fmt.Errorf("MaxMind rejected the account ID or licence key (HTTP %d)", code)
	case code == http.StatusTooManyRequests:
		return errors.New("MaxMind rate limit reached (HTTP 429); the daily download limit of a GeoLite account is 30")
	case code == http.StatusNotFound:
		return fmt.Errorf("MaxMind edition not found for this account (HTTP 404): %w", errGeoNotPublished)
	}
	return fmt.Errorf("MaxMind returned HTTP %d", code)
}

// download fetches url into dst through extract (which unpacks the archive).
func (m *GeoManager) download(ctx context.Context, url, account, key, dst string, extract func(io.Reader, io.Writer) error) error {
	ctx, cancel := context.WithTimeout(ctx, geoDownloadTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	if account != "" {
		req.SetBasicAuth(account, key)
	}
	req.Header.Set("User-Agent", "nebulactrl-agent")
	resp, err := m.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("download geoip database: %w", err)
	}
	defer resp.Body.Close()
	if account != "" {
		if err := maxmindStatus(resp.StatusCode); err != nil {
			return err
		}
	} else if resp.StatusCode == http.StatusNotFound {
		return errGeoNotPublished
	} else if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download geoip database: HTTP %d", resp.StatusCode)
	}
	f, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("create download file: %w", err)
	}
	err = extract(resp.Body, f)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("unpack geoip database: %w", err)
	}
	return nil
}

func copyLimited(dst io.Writer, src io.Reader) error {
	n, err := io.Copy(dst, io.LimitReader(src, maxGeoDownload+1))
	if err != nil {
		return err
	}
	if n > maxGeoDownload {
		return errors.New("database is larger than 1 GiB")
	}
	if n == 0 {
		return errors.New("empty database")
	}
	return nil
}

// extractGzip writes the gunzipped stream.
func extractGzip(src io.Reader, dst io.Writer) error {
	zr, err := gzip.NewReader(src)
	if err != nil {
		return err
	}
	defer zr.Close()
	return copyLimited(dst, zr)
}

// extractTarGzMMDB writes the first .mmdb file of a gzipped tar archive.
func extractTarGzMMDB(src io.Reader, dst io.Writer) error {
	zr, err := gzip.NewReader(src)
	if err != nil {
		return err
	}
	defer zr.Close()
	tr := tar.NewReader(zr)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return errors.New("archive holds no .mmdb file")
		}
		if err != nil {
			return err
		}
		if h.Typeflag == tar.TypeReg && strings.HasSuffix(strings.ToLower(h.Name), ".mmdb") {
			return copyLimited(dst, tr)
		}
	}
}

// install verifies the downloaded file, then swaps it in atomically and
// deletes the previous database.
func (m *GeoManager) install(tmp string, cfg agentprotocol.GeoConfig, gen int, label string, built time.Time) error {
	r, err := openGeoDatabase(tmp, cfg.Level)
	if err != nil {
		return err
	}
	final := fmt.Sprintf("%s-%s-%s.mmdb", cfg.Source, cfg.Level, label)

	m.mu.Lock()
	if gen != m.gen {
		m.mu.Unlock()
		_ = r.Close()
		return errGeoStale
	}
	if err := os.Rename(tmp, filepath.Join(m.dir, final)); err != nil {
		m.mu.Unlock()
		_ = r.Close()
		return fmt.Errorf("install geoip database: %w", err)
	}
	old := m.db.install(r, cfg.Level)
	m.state = geoState{Source: cfg.Source, Level: cfg.Level, Label: label, File: final, BuildTime: built, CheckedAt: m.Now()}
	m.haveDB = true
	m.saveStateLocked()
	m.mu.Unlock()

	m.db.closeReader(old)
	m.removeOtherDatabases(final)
	m.log.Info("geoip database installed", "source", cfg.Source, "level", cfg.Level, "release", label)
	return nil
}

// purgeLocked deletes everything: the setting is off.
func (m *GeoManager) purgeLocked() {
	m.db.Close()
	m.haveDB, m.state, m.lastErr, m.failures = false, geoState{}, "", 0
	if err := os.RemoveAll(m.dir); err != nil {
		m.log.Warn("remove geoip directory", "err", err)
	}
}

// dropDatabaseLocked deletes the database but keeps the stored setting.
func (m *GeoManager) dropDatabaseLocked() {
	m.db.Close()
	m.haveDB, m.state, m.failures = false, geoState{}, 0
	m.removeDatabaseFiles()
}

func (m *GeoManager) removeDatabaseFiles() {
	entries, err := os.ReadDir(m.dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if n := e.Name(); strings.HasSuffix(n, ".mmdb") || n == geoStateFile || n == geoTmpFile {
			_ = os.Remove(filepath.Join(m.dir, n))
		}
	}
}

// removeOtherDatabases deletes every database file except keep.
func (m *GeoManager) removeOtherDatabases(keep string) {
	entries, err := os.ReadDir(m.dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if n := e.Name(); strings.HasSuffix(n, ".mmdb") && n != keep {
			_ = os.Remove(filepath.Join(m.dir, n))
		}
	}
}

func (m *GeoManager) persistConfigLocked() {
	if !m.cfg.Enabled {
		return // the directory was purged: nothing is stored while off
	}
	if err := os.MkdirAll(m.dir, 0o700); err != nil {
		m.log.Warn("create geoip directory", "err", err)
		return
	}
	writeJSON(filepath.Join(m.dir, geoConfigFile), m.cfg, m.log)
}

func (m *GeoManager) saveStateLocked() {
	if m.haveDB {
		writeJSON(filepath.Join(m.dir, geoStateFile), m.state, m.log)
	}
}

func readJSON(path string, v any) bool {
	data, err := os.ReadFile(path)
	return err == nil && json.Unmarshal(data, v) == nil
}

func writeJSON(path string, v any, log *slog.Logger) {
	data, err := json.Marshal(v)
	if err == nil {
		tmp := path + ".tmp"
		if err = os.WriteFile(tmp, data, 0o600); err == nil {
			err = os.Rename(tmp, path)
		}
	}
	if err != nil {
		log.Warn("persist geoip state", "path", filepath.Base(path), "err", err)
	}
}
