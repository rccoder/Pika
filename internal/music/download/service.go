package download

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/rccoder/Pika/internal/music/platform"
	"github.com/rccoder/Pika/internal/music/util"
)

type ProgressFunc = util.ProgressFunc

type DownloadService struct {
	client              *http.Client
	timeout             time.Duration
	proxy               string
	checkMD5            bool
	maxRetries          int
	multipartEnabled    bool
	multipartOpts       MultipartDownloadOptions
	multipartDownloader *MultipartDownloader
	inflightMu          sync.Mutex
	inflight            map[string]*inflightDownload
}

type inflightDownload struct {
	done      chan struct{}
	temp      string
	written   int64
	size      int64
	format    string
	sourceURL string
	err       error
	refs      int
	closed    bool
}

type downloadPolicy struct {
	headers     map[string]string
	validateURL func(string) error
}

type downloadPolicyContextKey struct{}
type prevalidatedURLContextKey struct{}
type downloadOwnedHeadersContextKey struct{}

type DownloadServiceOptions struct {
	Timeout              time.Duration
	Proxy                string
	CheckMD5             bool
	MaxRetries           int
	EnableMultipart      bool
	MultipartConcurrency int
	MultipartMinSize     int64
}

const (
	defaultDownloadMaxRetries = 3
	maxDownloadRetryDelay     = 30 * time.Second
)

var (
	retryJitterMu        sync.Mutex
	retryJitterRng       = rand.New(rand.NewSource(time.Now().UnixNano()))
	neteaseHostReplacer  = strings.NewReplacer("m8.", "m7.", "m801.", "m701.", "m804.", "m701.", "m704.", "m701.")
	errDownloadIntegrity = errors.New("download integrity check failed")
)

func NewDownloadService(opts DownloadServiceOptions) *DownloadService {
	proxyAddr := strings.TrimSpace(opts.Proxy)
	transport := &http.Transport{
		DialContext: (&net.Dialer{
			Timeout:   minDuration(opts.Timeout, 10*time.Second),
			KeepAlive: 30 * time.Second,
		}).DialContext,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   10,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   minDuration(opts.Timeout, 10*time.Second),
		ResponseHeaderTimeout: minDuration(opts.Timeout, 10*time.Second),
		ExpectContinueTimeout: 1 * time.Second,
	}
	if proxyAddr != "" {
		if proxyURL, err := normalizeProxyURL(proxyAddr); err == nil {
			transport.Proxy = http.ProxyURL(proxyURL)
		}
	}

	client := &http.Client{
		Transport: transport,
	}
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if policy, ok := req.Context().Value(downloadPolicyContextKey{}).(downloadPolicy); ok {
			if policy.validateURL != nil {
				if err := policy.validateURL(req.URL.String()); err != nil {
					return err
				}
				setDownloadPolicyHeaders(req, policy.headers)
			} else if redirectChainSameOrigin(req, via) {
				setDownloadPolicyHeaders(req, policy.headers)
			} else {
				deleteDownloadPolicyHeaders(req, policy.headers)
			}
		}
		if len(via) >= 10 {
			return errors.New("stopped after 10 redirects")
		}
		return nil
	}

	s := &DownloadService{
		client:           client,
		timeout:          opts.Timeout,
		proxy:            proxyAddr,
		checkMD5:         opts.CheckMD5,
		maxRetries:       opts.MaxRetries,
		multipartEnabled: opts.EnableMultipart,
		inflight:         make(map[string]*inflightDownload),
	}
	if s.maxRetries <= 0 {
		s.maxRetries = defaultDownloadMaxRetries
	}

	s.multipartOpts = MultipartDownloadOptions{
		Concurrency: opts.MultipartConcurrency,
		MinSize:     opts.MultipartMinSize,
	}
	// Always build the chunked/multipart downloader, even when multipart is
	// disabled: some sources (e.g. googlevideo, advertised via
	// DownloadInfo.MaxChunkSize) MUST be fetched in bounded Range chunks or they
	// 403 on plain GET. The plain-GET single-thread path can't serve those.
	s.multipartDownloader = NewMultipartDownloader(client, opts.Timeout, s.multipartOpts)

	return s
}

func setDownloadPolicyHeaders(req *http.Request, headers map[string]string) {
	if req == nil {
		return
	}
	if req.Header == nil {
		req.Header = make(http.Header)
	}
	for key, value := range headers {
		if downloadOwnsHeader(req.Context(), key) {
			continue
		}
		req.Header.Set(key, value)
	}
}

func deleteDownloadPolicyHeaders(req *http.Request, headers map[string]string) {
	if req == nil {
		return
	}
	for key := range headers {
		if downloadOwnsHeader(req.Context(), key) {
			continue
		}
		req.Header.Del(key)
	}
}

func withDownloadOwnedHeader(ctx context.Context, header string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	header = strings.ToLower(strings.TrimSpace(header))
	if header == "" {
		return ctx
	}

	owned := make(map[string]struct{})
	if existing, ok := ctx.Value(downloadOwnedHeadersContextKey{}).(map[string]struct{}); ok {
		for key := range existing {
			owned[key] = struct{}{}
		}
	}
	owned[header] = struct{}{}
	return context.WithValue(ctx, downloadOwnedHeadersContextKey{}, owned)
}

func downloadOwnsHeader(ctx context.Context, header string) bool {
	if ctx == nil {
		return false
	}
	owned, ok := ctx.Value(downloadOwnedHeadersContextKey{}).(map[string]struct{})
	if !ok {
		return false
	}
	_, ok = owned[strings.ToLower(strings.TrimSpace(header))]
	return ok
}

// violatesDeclaredSize reports whether a transferred byte count contradicts the
// platform's declared size. The declared size is treated as an exact contract
// unless the platform marked it advisory, in which case only a short transfer
// counts — see platform.DownloadInfo.SizeIsAdvisory.
func violatesDeclaredSize(info *platform.DownloadInfo, actual int64) bool {
	if info == nil || info.Size <= 0 || actual < 0 {
		return false
	}
	if info.SizeIsAdvisory {
		return actual < info.Size
	}
	return actual != info.Size
}

// declaredSizeError describes a size violation using wording that matches how
// the declared size is enforced for this source.
func declaredSizeError(info *platform.DownloadInfo, actual int64, subject string) error {
	if info != nil && info.SizeIsAdvisory {
		return fmt.Errorf("%w: %s too small: got %d bytes, expected at least %d", errDownloadIntegrity, subject, actual, info.Size)
	}
	return fmt.Errorf("%w: %s mismatch: got %d bytes, expected %d", errDownloadIntegrity, subject, actual, info.Size)
}

// wrapDownloadIntegrityReadError marks a mid-transfer read failure as an
// integrity problem only when the transfer fell short of what was expected.
// When the expectation is advisory, reading at or past it means the platform
// understated the file rather than that the transfer was truncated.
func wrapDownloadIntegrityReadError(err error, written, expected int64, subject string, advisory bool) error {
	satisfied := written == expected
	if advisory {
		satisfied = written >= expected
	}
	if err == nil || expected <= 0 || satisfied ||
		errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return fmt.Errorf(
		"%w: %s read failed after %d of %d bytes: %w",
		errDownloadIntegrity,
		subject,
		written,
		expected,
		err,
	)
}

func redirectChainSameOrigin(req *http.Request, via []*http.Request) bool {
	if req == nil || req.URL == nil || len(via) == 0 || via[0] == nil || via[0].URL == nil {
		return false
	}
	origin := via[0].URL
	for _, previous := range via[1:] {
		if previous == nil || !sameDownloadOrigin(origin, previous.URL) {
			return false
		}
	}
	return sameDownloadOrigin(origin, req.URL)
}

func sameDownloadOrigin(left, right *url.URL) bool {
	if left == nil || right == nil {
		return false
	}
	leftScheme := strings.ToLower(left.Scheme)
	rightScheme := strings.ToLower(right.Scheme)
	if (leftScheme != "http" && leftScheme != "https") ||
		(rightScheme != "http" && rightScheme != "https") ||
		leftScheme != rightScheme {
		return false
	}
	if left.Hostname() == "" || right.Hostname() == "" ||
		!strings.EqualFold(left.Hostname(), right.Hostname()) {
		return false
	}
	return effectiveURLPort(left) == effectiveURLPort(right)
}

func effectiveURLPort(parsed *url.URL) string {
	if parsed == nil {
		return ""
	}
	if port := parsed.Port(); port != "" {
		return port
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http":
		return "80"
	case "https":
		return "443"
	default:
		return ""
	}
}

func (s *DownloadService) FillMetadata(info *platform.DownloadInfo, resp *http.Response) {
	if info == nil || resp == nil {
		return
	}

	contentType := resp.Header.Get("Content-Type")
	contentDisposition := resp.Header.Get("Content-Disposition")

	isAudioContent := strings.HasPrefix(contentType, "audio/")
	hasFilename := strings.Contains(contentDisposition, "filename")

	if info.Size <= 0 && (isAudioContent || hasFilename) && resp.ContentLength > 0 {
		info.Size = resp.ContentLength
	}

	if info.Format == "" && contentType != "" {
		if strings.Contains(contentType, "mpeg") || strings.Contains(contentType, "mp3") {
			info.Format = "mp3"
		} else if strings.Contains(contentType, "flac") {
			info.Format = "flac"
		} else if strings.Contains(contentType, "aac") || strings.Contains(contentType, "mp4") {
			info.Format = "m4a"
		}
	}
}

func (s *DownloadService) Download(ctx context.Context, info *platform.DownloadInfo, destPath string, progress ProgressFunc) (int64, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if info == nil || info.URL == "" {
		return 0, errors.New("download info missing")
	}
	if destPath == "" {
		return 0, errors.New("dest path missing")
	}
	if err := os.MkdirAll(filepath.Dir(destPath), os.ModePerm); err != nil {
		return 0, err
	}
	if info.Downloader != nil {
		return info.Downloader(ctx, info, destPath, progress)
	}

	policy := immutableDownloadPolicy(info)
	if len(policy.headers) > 0 || policy.validateURL != nil {
		baseURL := strings.TrimSpace(rewriteNeteaseHost(info.URL))
		if baseURL == "" {
			baseURL = strings.TrimSpace(info.URL)
		}
		if policy.validateURL != nil {
			if err := policy.validateURL(baseURL); err != nil {
				return 0, err
			}
		}
		ctx = context.WithValue(ctx, downloadPolicyContextKey{}, policy)
		ctx = context.WithValue(ctx, prevalidatedURLContextKey{}, baseURL)
		infoCopy := *info
		infoCopy.Headers = policy.headers
		written, err := s.downloadToPath(ctx, &infoCopy, destPath, progress)
		if err == nil {
			reconcileAdvisorySize(&infoCopy, written)
			copyDownloadMetadata(info, &infoCopy)
		}
		return written, err
	}

	key := strings.TrimSpace(rewriteNeteaseHost(info.URL))
	if key == "" {
		key = strings.TrimSpace(info.URL)
	}
	call, leader := s.acquireInflight(key)
	defer s.releaseInflight(key, call)

	if leader {
		// Stage the download next to its destination rather than in os.TempDir.
		// The two are routinely on different filesystems (the shipped
		// docker-compose bind-mounts the work dir while /tmp stays on the
		// container overlay), which makes the os.Link fast path in copyToPath
		// fail with EXDEV and forces a full copy of every downloaded track.
		// The dot prefix keeps the staged file out of cache directory scans.
		tmpFile, err := os.CreateTemp(filepath.Dir(destPath), ".musicbot-download-*")
		if err != nil {
			call.err = err
			s.inflightMu.Lock()
			call.closed = true
			s.inflightMu.Unlock()
			close(call.done)
			return 0, err
		}
		_ = tmpFile.Close()
		call.temp = tmpFile.Name()

		infoCopy := *info
		written, err := s.downloadToPath(ctx, &infoCopy, call.temp, progress)
		if err == nil {
			reconcileAdvisorySize(&infoCopy, written)
		}
		call.written = written
		call.err = err
		call.size = infoCopy.Size
		call.format = infoCopy.Format
		call.sourceURL = strings.TrimSpace(infoCopy.URL)
		s.inflightMu.Lock()
		call.closed = true
		s.inflightMu.Unlock()
		close(call.done)
	} else {
		select {
		case <-call.done:
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	}

	if call.err != nil {
		return 0, call.err
	}
	if info != nil {
		if call.size > 0 {
			info.Size = call.size
		}
		if strings.TrimSpace(info.Format) == "" && strings.TrimSpace(call.format) != "" {
			info.Format = call.format
		}
		if strings.TrimSpace(call.sourceURL) != "" {
			info.URL = call.sourceURL
		}
	}

	copyProgress := progress
	if leader {
		copyProgress = nil
	}
	if call.temp == "" {
		return 0, errors.New("download temp file missing")
	}
	total := call.size
	if total <= 0 {
		total = call.written
	}

	return copyToPath(call.temp, destPath, total, copyProgress)
}

func (s *DownloadService) downloadToPath(ctx context.Context, info *platform.DownloadInfo, destPath string, progress ProgressFunc) (int64, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if info == nil || info.URL == "" {
		return 0, errors.New("download info missing")
	}
	if destPath == "" {
		return 0, errors.New("dest path missing")
	}

	urls := candidateDownloadURLs(info)
	var lastErr error
	for idx, raw := range urls {
		info.URL = raw
		baseURL := rewriteNeteaseHost(raw)
		if info.ValidateURL != nil {
			prevalidated, _ := ctx.Value(prevalidatedURLContextKey{}).(string)
			if prevalidated != baseURL {
				if err := info.ValidateURL(baseURL); err != nil {
					lastErr = err
					if idx < len(urls)-1 {
						continue
					}
					return 0, err
				}
			}
		}

		// Sources advertising MaxChunkSize (e.g. googlevideo) reject plain GET,
		// HEAD, and oversized ranges with 403. They MUST go through the bounded
		// Range chunk path and must NOT fall back to the single-thread plain GET
		// below — that GET is exactly what 403s. Treat chunked failure as fatal
		// for this URL and move to the next candidate (if any).
		chunkedRequired := info.MaxChunkSize > 0 && s.multipartDownloader != nil
		if chunkedRequired {
			written, err := s.tryMultipartDownload(ctx, baseURL, info, destPath, progress)
			if err == nil {
				if s.checkMD5 && info.MD5 != "" {
					if ok, err := util.VerifyMD5(destPath, info.MD5); err != nil || !ok {
						_ = os.Remove(destPath)
						if err != nil {
							return 0, err
						}
						return 0, errors.New("md5 verification failed")
					}
				}
				return written, nil
			}
			lastErr = err
			_ = os.Remove(destPath)
			if errors.Is(err, errDownloadIntegrity) {
				return 0, err
			}
			continue
		}

		if s.multipartEnabled && s.multipartDownloader != nil {
			written, err := s.tryMultipartDownload(ctx, baseURL, info, destPath, progress)
			if err == nil {
				if s.checkMD5 && info.MD5 != "" {
					if ok, err := util.VerifyMD5(destPath, info.MD5); err != nil || !ok {
						_ = os.Remove(destPath)
						if err != nil {
							return 0, err
						}
						return 0, errors.New("md5 verification failed")
					}
				}
				return written, nil
			}
			lastErr = err
			_ = os.Remove(destPath)
			if errors.Is(err, errDownloadIntegrity) {
				return 0, err
			}
		}

		for attempt := 0; attempt < s.maxRetries; attempt++ {
			written, err := s.downloadOnce(ctx, baseURL, info, destPath, progress)
			if err == nil {
				if violatesDeclaredSize(info, written) {
					_ = os.Remove(destPath)
					return 0, declaredSizeError(info, written, "download size")
				}
				if s.checkMD5 && info.MD5 != "" {
					if ok, err := util.VerifyMD5(destPath, info.MD5); err != nil || !ok {
						_ = os.Remove(destPath)
						if err != nil {
							return 0, err
						}
						return 0, errors.New("md5 verification failed")
					}
				}
				return written, nil
			}
			lastErr = err
			_ = os.Remove(destPath)
			if errors.Is(err, errDownloadIntegrity) {
				return 0, err
			}
			if attempt < s.maxRetries-1 {
				wait := retryDelayWithJitter(attempt)
				select {
				case <-ctx.Done():
					return 0, ctx.Err()
				case <-time.After(wait):
				}
			}
		}

		if idx < len(urls)-1 {
			select {
			case <-ctx.Done():
				return 0, ctx.Err()
			default:
			}
		}
	}
	return 0, lastErr
}

func immutableDownloadPolicy(info *platform.DownloadInfo) downloadPolicy {
	if info == nil {
		return downloadPolicy{}
	}
	policy := downloadPolicy{validateURL: info.ValidateURL}
	if len(info.Headers) > 0 {
		policy.headers = make(map[string]string, len(info.Headers))
		for key, value := range info.Headers {
			policy.headers[key] = value
		}
	}
	return policy
}

// reconcileAdvisorySize aligns an advisory declared size with what was actually
// transferred. Callers use Size for the upload and the cache record, so once a
// platform's understated size has been accepted it must be corrected to the
// real byte count rather than left describing a shorter file than exists.
func reconcileAdvisorySize(info *platform.DownloadInfo, written int64) {
	if info != nil && info.SizeIsAdvisory && written > info.Size {
		info.Size = written
	}
}

func copyDownloadMetadata(destination, source *platform.DownloadInfo) {
	if destination == nil || source == nil {
		return
	}
	destination.URL = source.URL
	destination.Size = source.Size
	if strings.TrimSpace(destination.Format) == "" {
		destination.Format = source.Format
	}
}

func candidateDownloadURLs(info *platform.DownloadInfo) []string {
	if info == nil {
		return nil
	}
	seen := make(map[string]struct{}, 1+len(info.CandidateURLs))
	urls := make([]string, 0, 1+len(info.CandidateURLs))
	add := func(raw string) {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			return
		}
		if _, ok := seen[raw]; ok {
			return
		}
		seen[raw] = struct{}{}
		urls = append(urls, raw)
	}
	add(info.URL)
	for _, item := range info.CandidateURLs {
		add(item)
	}
	return urls
}

func (s *DownloadService) acquireInflight(key string) (*inflightDownload, bool) {
	s.inflightMu.Lock()
	defer s.inflightMu.Unlock()
	if call, ok := s.inflight[key]; ok {
		call.refs++
		return call, false
	}
	call := &inflightDownload{done: make(chan struct{}), refs: 1}
	s.inflight[key] = call
	return call, true
}

func (s *DownloadService) releaseInflight(key string, call *inflightDownload) {
	if call == nil {
		return
	}
	s.inflightMu.Lock()
	defer s.inflightMu.Unlock()
	if call.refs > 0 {
		call.refs--
	}
	if call.refs == 0 && call.closed {
		delete(s.inflight, key)
		if strings.TrimSpace(call.temp) != "" {
			_ = os.Remove(call.temp)
		}
	}
}

func copyToPath(srcPath, destPath string, total int64, progress ProgressFunc) (int64, error) {
	if srcPath == "" {
		return 0, errors.New("source path missing")
	}
	if progress == nil {
		_ = os.Remove(destPath)
		if err := os.Link(srcPath, destPath); err == nil {
			if total > 0 {
				return total, nil
			}
			if stat, statErr := os.Stat(srcPath); statErr == nil {
				return stat.Size(), nil
			}
			return 0, nil
		}
	}
	in, err := os.Open(srcPath)
	if err != nil {
		return 0, err
	}
	defer in.Close()

	out, err := os.Create(destPath)
	if err != nil {
		return 0, err
	}
	written, copyErr := util.CopyWithProgress(out, in, total, progress)
	closeErr := out.Close()
	if copyErr != nil {
		return written, copyErr
	}
	if closeErr != nil {
		return written, closeErr
	}
	return written, nil
}

func retryDelayWithJitter(attempt int) time.Duration {
	if attempt < 0 {
		attempt = 0
	}

	base := time.Duration(1<<attempt) * time.Second
	if base > maxDownloadRetryDelay {
		base = maxDownloadRetryDelay
	}

	retryJitterMu.Lock()
	jitter := 0.75 + retryJitterRng.Float64()*0.5
	retryJitterMu.Unlock()

	wait := time.Duration(float64(base) * jitter)
	if wait <= 0 {
		return time.Second
	}
	return wait
}

func (s *DownloadService) tryMultipartDownload(ctx context.Context, baseURL string, info *platform.DownloadInfo, destPath string, progress ProgressFunc) (int64, error) {
	written, err := s.multipartDownloader.Download(ctx, baseURL, info, destPath, progress)
	if err != nil {
		_ = os.Remove(destPath)
		if errors.Is(err, errDownloadIntegrity) {
			return 0, fmt.Errorf("multipart download failed: %w", err)
		}
		return 0, fmt.Errorf("multipart download failed (will retry with single-thread): %w", err)
	}
	if violatesDeclaredSize(info, written) {
		_ = os.Remove(destPath)
		return 0, declaredSizeError(info, written, "multipart download size")
	}
	return written, nil
}

func (s *DownloadService) downloadOnce(ctx context.Context, rawURL string, info *platform.DownloadInfo, destPath string, progress ProgressFunc) (int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return 0, err
	}
	for k, v := range info.Headers {
		req.Header.Set(k, v)
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("download failed with status %d", resp.StatusCode)
	}
	expectedSize := info.Size
	if resp.ContentLength >= 0 && violatesDeclaredSize(info, resp.ContentLength) {
		return 0, declaredSizeError(info, resp.ContentLength, "download content length")
	}

	s.FillMetadata(info, resp)

	file, err := os.Create(destPath)
	if err != nil {
		return 0, err
	}

	throttledProgress := progress
	if progress != nil {
		lastUpdate := time.Time{}
		interval := 500 * time.Millisecond
		throttledProgress = func(written, total int64) {
			now := time.Now()
			if !lastUpdate.IsZero() && now.Sub(lastUpdate) < interval {
				if total <= 0 || written < total {
					return
				}
			}
			lastUpdate = now
			progress(written, total)
		}
	}

	totalSize := info.Size
	if info.SizeIsAdvisory && resp.ContentLength > totalSize {
		totalSize = resp.ContentLength
	}
	if totalSize <= 0 && resp.ContentLength > 0 {
		totalSize = resp.ContentLength
	}
	written, err := util.CopyWithProgress(file, resp.Body, totalSize, throttledProgress)
	closeErr := file.Close()
	if err != nil {
		return written, wrapDownloadIntegrityReadError(err, written, expectedSize, "download body", info.SizeIsAdvisory)
	}
	if closeErr != nil {
		return written, closeErr
	}
	if written == 0 {
		_ = os.Remove(destPath)
		return 0, errors.New("download returned empty file")
	}
	if expectedSize > 0 && violatesDeclaredSize(info, written) {
		_ = os.Remove(destPath)
		return 0, declaredSizeError(info, written, "download size")
	}
	return written, nil
}

func rewriteNeteaseHost(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	parsed.Host = neteaseHostReplacer.Replace(parsed.Host)
	return parsed.String()
}

func normalizeProxyURL(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, errors.New("empty proxy")
	}
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	proxyURL, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	if proxyURL.Scheme == "" || proxyURL.Host == "" {
		return nil, errors.New("invalid proxy")
	}
	return proxyURL, nil
}

func minDuration(a, b time.Duration) time.Duration {
	if a == 0 || a > b {
		return b
	}
	return a
}
