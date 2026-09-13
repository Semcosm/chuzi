package launcher

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	pathpkg "path"
	"path/filepath"
	"strings"
	"time"
)

const (
	defaultIndexMaxBytes    int64 = 2 << 20
	defaultArtifactMaxBytes int64 = 512 << 20
	defaultHTTPAttempts           = 3
	defaultRetryDelay             = 250 * time.Millisecond
)

// HTTPReleaseIndexSource fetches and validates a release catalog. It has no
// client-side timeout: callers own cancellation through context, which keeps
// the same operation usable by a UI, CLI, or service supervisor.
type HTTPReleaseIndexSource struct {
	URL                  string
	Client               *http.Client
	MaxBytes             int64
	MaxAttempts          int
	RetryDelay           time.Duration
	AllowHTTPForLoopback bool
}

func (s HTTPReleaseIndexSource) FetchIndex(ctx context.Context) (ReleaseIndex, error) {
	if err := contextErr(ctx); err != nil {
		return ReleaseIndex{}, err
	}
	indexURL, err := validateDownloadURL(s.URL, s.AllowHTTPForLoopback)
	if err != nil {
		return ReleaseIndex{}, err
	}
	data, err := fetchBytes(ctx, s.clientForOrigin(indexURL), indexURL.String(), normalizeLimit(s.MaxBytes, defaultIndexMaxBytes), s.MaxAttempts, s.RetryDelay)
	if err != nil {
		return ReleaseIndex{}, err
	}
	var index ReleaseIndex
	if err := decodeStrictJSON(data, &index); err != nil {
		return ReleaseIndex{}, fmt.Errorf("decode release index: %w", err)
	}
	if err := index.Validate(); err != nil {
		return ReleaseIndex{}, err
	}
	return index, nil
}

func (s HTTPReleaseIndexSource) Fetch(ctx context.Context, _ UpdateRequest) (ReleaseManifest, error) {
	index, err := s.FetchIndex(ctx)
	if err != nil {
		return ReleaseManifest{}, err
	}
	return index.Manifest, nil
}

// ArtifactDownloader verifies the remote archive before making it visible to
// the component manager. Retries are limited and only transport/server
// failures are retried; digest, size, URL, and archive errors fail closed.
type ArtifactDownloader struct {
	Client               *http.Client
	MaxBytes             int64
	MaxAttempts          int
	RetryDelay           time.Duration
	AllowHTTPForLoopback bool
}

func (d ArtifactDownloader) Download(ctx context.Context, indexURL string, artifact ReleaseArtifact, destinationDir string) (string, error) {
	if err := contextErr(ctx); err != nil {
		return "", err
	}
	if artifact.Size <= 0 || !validSHA256(artifact.SHA256) {
		return "", fmt.Errorf("%w: invalid artifact metadata", ErrInvalidManifest)
	}
	if err := validateRelativePath(artifact.Path); err != nil {
		return "", err
	}
	base, err := validateDownloadURL(indexURL, d.AllowHTTPForLoopback)
	if err != nil {
		return "", err
	}
	artifactURL, err := resolveArtifactURL(base, artifact, d.AllowHTTPForLoopback)
	if err != nil {
		return "", err
	}
	destinationDir, err = filepath.Abs(destinationDir)
	if err != nil {
		return "", fmt.Errorf("resolve download directory: %w", err)
	}
	if err := os.MkdirAll(destinationDir, 0o700); err != nil {
		return "", fmt.Errorf("create download directory: %w", err)
	}
	name := artifactCacheName(artifact)
	target := filepath.Join(destinationDir, name)
	if info, statErr := os.Stat(target); statErr == nil && info.Mode().IsRegular() {
		if info.Size() == artifact.Size {
			actual, hashErr := hashFile(ctx, target)
			if hashErr == nil && strings.EqualFold(actual, artifact.SHA256) {
				return target, nil
			}
			if hashErr != nil && (errors.Is(hashErr, context.Canceled) || errors.Is(hashErr, context.DeadlineExceeded)) {
				return "", hashErr
			}
		}
		_ = os.Remove(target)
	}
	return d.downloadWithRetry(ctx, artifactURL.String(), artifact, target, d.clientForOrigin(base))
}

func (d ArtifactDownloader) downloadWithRetry(ctx context.Context, artifactURL string, artifact ReleaseArtifact, target string, client *http.Client) (string, error) {
	attempts := normalizeAttempts(d.MaxAttempts)
	delay := d.RetryDelay
	if delay < 0 {
		delay = defaultRetryDelay
	}
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		if err := contextErr(ctx); err != nil {
			return "", err
		}
		path, retry, err := d.downloadOnce(ctx, artifactURL, artifact, target, client)
		if err == nil {
			return path, nil
		}
		lastErr = err
		if !retry || attempt == attempts {
			break
		}
		if err := waitRetry(ctx, delay, attempt); err != nil {
			return "", err
		}
	}
	return "", lastErr
}

func (d ArtifactDownloader) downloadOnce(ctx context.Context, artifactURL string, artifact ReleaseArtifact, target string, client *http.Client) (string, bool, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, artifactURL, nil)
	if err != nil {
		return "", false, fmt.Errorf("create artifact request: %w", err)
	}
	response, err := client.Do(request)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return "", false, err
		}
		return "", true, fmt.Errorf("download artifact: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		retry := response.StatusCode == http.StatusRequestTimeout || response.StatusCode == http.StatusTooManyRequests || response.StatusCode >= 500
		return "", retry, fmt.Errorf("download artifact: http status %d", response.StatusCode)
	}
	limit := normalizeLimit(d.MaxBytes, defaultArtifactMaxBytes)
	if response.ContentLength > limit || response.ContentLength >= 0 && response.ContentLength != artifact.Size {
		return "", false, fmt.Errorf("%w: artifact content length %d", ErrInvalidManifest, response.ContentLength)
	}
	temporary, err := os.CreateTemp(filepath.Dir(target), ".chuzi-artifact-")
	if err != nil {
		return "", false, fmt.Errorf("create artifact temporary file: %w", err)
	}
	temporaryName := temporary.Name()
	committed := false
	defer func() {
		_ = temporary.Close()
		if !committed {
			_ = os.Remove(temporaryName)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return "", false, err
	}
	hasher := sha256.New()
	writer := io.MultiWriter(temporary, hasher)
	count, err := copyWithContextLimit(ctx, writer, response.Body, limit)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return "", false, err
		}
		return "", true, fmt.Errorf("read artifact: %w", err)
	}
	if count > limit {
		return "", false, fmt.Errorf("%w: artifact exceeds maximum size", ErrInvalidManifest)
	}
	if count != artifact.Size {
		return "", false, fmt.Errorf("%w: artifact size %d, expected %d", ErrInvalidManifest, count, artifact.Size)
	}
	actual := hex.EncodeToString(hasher.Sum(nil))
	if !strings.EqualFold(actual, artifact.SHA256) {
		return "", false, fmt.Errorf("%w: artifact sha256 mismatch", ErrInvalidManifest)
	}
	if err := temporary.Sync(); err != nil {
		return "", false, fmt.Errorf("sync artifact: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return "", false, fmt.Errorf("close artifact: %w", err)
	}
	if err := os.Rename(temporaryName, target); err != nil {
		return "", false, fmt.Errorf("commit artifact: %w", err)
	}
	committed = true
	return target, false, nil
}

func (d ArtifactDownloader) client() *http.Client {
	if d.Client != nil {
		return d.Client
	}
	return http.DefaultClient
}

func (d ArtifactDownloader) clientForOrigin(origin *url.URL) *http.Client {
	return httpClientForOrigin(d.client(), origin, d.AllowHTTPForLoopback)
}

func (s HTTPReleaseIndexSource) client() *http.Client {
	if s.Client != nil {
		return s.Client
	}
	return http.DefaultClient
}

func (s HTTPReleaseIndexSource) clientForOrigin(origin *url.URL) *http.Client {
	return httpClientForOrigin(s.client(), origin, s.AllowHTTPForLoopback)
}

func httpClientForOrigin(client *http.Client, origin *url.URL, allowHTTPForLoopback bool) *http.Client {
	clone := *client
	previous := client.CheckRedirect
	clone.CheckRedirect = func(request *http.Request, via []*http.Request) error {
		redirect, err := validateDownloadURL(request.URL.String(), allowHTTPForLoopback)
		if err != nil {
			return err
		}
		if !sameOrigin(origin, redirect) {
			return fmt.Errorf("%w: redirect is not same-origin", ErrInvalidPath)
		}
		if previous != nil {
			return previous(request, via)
		}
		return nil
	}
	return &clone
}

func fetchBytes(ctx context.Context, client *http.Client, rawURL string, maxBytes int64, maxAttempts int, retryDelay time.Duration) ([]byte, error) {
	attempts := normalizeAttempts(maxAttempts)
	if retryDelay < 0 {
		retryDelay = defaultRetryDelay
	}
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		if err := contextErr(ctx); err != nil {
			return nil, err
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
		if err != nil {
			return nil, fmt.Errorf("create release index request: %w", err)
		}
		response, err := client.Do(request)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return nil, err
			}
			lastErr = fmt.Errorf("fetch release index: %w", err)
		} else {
			data, readErr := readLimited(ctx, response.Body, maxBytes)
			_ = response.Body.Close()
			if response.StatusCode >= http.StatusOK && response.StatusCode < http.StatusMultipleChoices {
				if readErr != nil {
					return nil, fmt.Errorf("read release index: %w", readErr)
				}
				return data, nil
			}
			retry := response.StatusCode == http.StatusRequestTimeout || response.StatusCode == http.StatusTooManyRequests || response.StatusCode >= 500
			lastErr = fmt.Errorf("fetch release index: http status %d", response.StatusCode)
			if readErr != nil && !errors.Is(readErr, errBodyTooLarge) {
				lastErr = fmt.Errorf("read release index: %w", readErr)
			}
			if !retry {
				return nil, lastErr
			}
		}
		if attempt == attempts {
			break
		}
		if err := waitRetry(ctx, retryDelay, attempt); err != nil {
			return nil, err
		}
	}
	return nil, lastErr
}

var errBodyTooLarge = errors.New("response exceeds configured size limit")

func readLimited(ctx context.Context, reader io.Reader, maxBytes int64) ([]byte, error) {
	if maxBytes <= 0 {
		return nil, fmt.Errorf("%w: response size limit must be positive", ErrInvalidManifest)
	}
	var output strings.Builder
	output.Grow(int(minInt64(maxBytes, 64<<10)))
	buffer := make([]byte, 32<<10)
	var count int64
	for {
		if err := contextErr(ctx); err != nil {
			return nil, err
		}
		read, err := reader.Read(buffer)
		if read > 0 {
			count += int64(read)
			if count > maxBytes {
				return nil, errBodyTooLarge
			}
			_, _ = output.Write(buffer[:read])
		}
		if errors.Is(err, io.EOF) {
			return []byte(output.String()), nil
		}
		if err != nil {
			return nil, err
		}
	}
}

func decodeStrictJSON(data []byte, target any) error {
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return fmt.Errorf("%w: trailing JSON", ErrInvalidManifest)
		}
		return fmt.Errorf("%w: trailing content: %v", ErrInvalidManifest, err)
	}
	return nil
}

func validateDownloadURL(raw string, allowHTTPForLoopback bool) (*url.URL, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, fmt.Errorf("%w: release URL is required", ErrInvalidPath)
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" {
		return nil, fmt.Errorf("%w: invalid release URL", ErrInvalidPath)
	}
	if parsed.Scheme != "https" && !(parsed.Scheme == "http" && allowHTTPForLoopback && isLoopbackHost(parsed.Hostname())) {
		return nil, fmt.Errorf("%w: release URL must use HTTPS", ErrInvalidPath)
	}
	return parsed, nil
}

func resolveArtifactURL(indexURL *url.URL, artifact ReleaseArtifact, allowHTTPForLoopback bool) (*url.URL, error) {
	var resolved *url.URL
	if strings.TrimSpace(artifact.URL) == "" {
		base := *indexURL
		base.Path = pathpkg.Join(pathpkg.Dir(base.Path), artifact.Path)
		base.RawPath = ""
		resolved = &base
	} else {
		parsed, err := url.Parse(artifact.URL)
		if err != nil {
			return nil, fmt.Errorf("%w: invalid artifact URL", ErrInvalidPath)
		}
		resolved = indexURL.ResolveReference(parsed)
	}
	validated, err := validateDownloadURL(resolved.String(), allowHTTPForLoopback)
	if err != nil {
		return nil, err
	}
	if !sameOrigin(indexURL, validated) {
		return nil, fmt.Errorf("%w: artifact URL is not same-origin", ErrInvalidPath)
	}
	return validated, nil
}

func sameOrigin(left, right *url.URL) bool {
	return strings.EqualFold(left.Scheme, right.Scheme) && strings.EqualFold(left.Hostname(), right.Hostname()) && normalizedPort(left) == normalizedPort(right)
}

func normalizedPort(value *url.URL) string {
	if port := value.Port(); port != "" {
		return port
	}
	if strings.EqualFold(value.Scheme, "https") {
		return "443"
	}
	return "80"
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func artifactCacheName(artifact ReleaseArtifact) string {
	base := pathpkg.Base(artifact.Path)
	if strings.HasSuffix(strings.ToLower(base), ".tar.gz") {
		return base[:len(base)-len(".tar.gz")] + "-" + strings.ToLower(artifact.SHA256[:16]) + base[len(base)-len(".tar.gz"):]
	}
	if strings.HasSuffix(strings.ToLower(base), ".zip") {
		return base[:len(base)-len(filepath.Ext(base))] + "-" + strings.ToLower(artifact.SHA256[:16]) + filepath.Ext(base)
	}
	return base + "-" + strings.ToLower(artifact.SHA256[:16])
}

func copyWithContextLimit(ctx context.Context, destination io.Writer, source io.Reader, limit int64) (int64, error) {
	if limit < 0 {
		return 0, fmt.Errorf("%w: copy limit must not be negative", ErrInvalidManifest)
	}
	reader := &limitedContextReader{ctx: ctx, reader: io.LimitReader(source, limit+1)}
	return io.Copy(destination, reader)
}

type limitedContextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *limitedContextReader) Read(buffer []byte) (int, error) {
	if err := contextErr(r.ctx); err != nil {
		return 0, err
	}
	return r.reader.Read(buffer)
}

func waitRetry(ctx context.Context, delay time.Duration, attempt int) error {
	if delay <= 0 {
		return contextErr(ctx)
	}
	backoff := delay
	for index := 1; index < attempt; index++ {
		if backoff >= 2*time.Second {
			backoff = 2 * time.Second
			break
		}
		backoff *= 2
	}
	timer := time.NewTimer(backoff)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func normalizeAttempts(value int) int {
	if value <= 0 {
		return defaultHTTPAttempts
	}
	if value > 8 {
		return 8
	}
	return value
}

func normalizeLimit(value, fallback int64) int64 {
	if value <= 0 {
		return fallback
	}
	return value
}

func minInt64(left, right int64) int64 {
	if left < right {
		return left
	}
	return right
}
