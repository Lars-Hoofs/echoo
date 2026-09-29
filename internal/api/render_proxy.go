package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"github.com/jackc/pgx/v5"

	"echoo/internal/db/dbq"
	"echoo/internal/netguard"
	"echoo/internal/sniff"
)

const (
	proxyMaxBytes     = 5 << 20
	proxyTimeout      = 10 * time.Second
	proxyMaxRedirects = 3
	proxyConcurrency  = 8
)

var errImageUnavailable = &apiError{Status: http.StatusBadGateway, Code: "image_unavailable", Message: "the image could not be fetched"}

// imageProxy fetches remote mail images on behalf of the browser, so the sender only ever
// sees Echoo's address. Everything it will fetch was signed by the server first.
type imageProxy struct {
	client       *http.Client
	slots        chan struct{}
	allowedPorts map[string]bool
}

func newImageProxy() *imageProxy {
	dialer := netguard.Dialer(false, proxyTimeout)
	p := &imageProxy{
		slots:        make(chan struct{}, proxyConcurrency),
		allowedPorts: map[string]bool{"443": true, "8443": true},
	}
	p.client = &http.Client{
		Timeout: proxyTimeout,
		Transport: &http.Transport{
			// The environment proxy would bypass the dialer's address check.
			Proxy:                  nil,
			DialContext:            dialer.DialContext,
			ForceAttemptHTTP2:      true,
			TLSHandshakeTimeout:    proxyTimeout,
			ResponseHeaderTimeout:  proxyTimeout,
			MaxResponseHeaderBytes: 64 << 10,
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > proxyMaxRedirects {
				return errors.New("too many redirects")
			}
			return p.check(req.URL)
		},
	}
	return p
}

// check enforces the SECURITY.md policy: https only, ports 443 and 8443, no credentials. The
// address check itself happens in the dialer, on the connected IP.
func (p *imageProxy) check(u *url.URL) error {
	if u.Scheme != "https" || u.Hostname() == "" || u.User != nil {
		return errors.New("only plain https URLs are proxied")
	}
	if port := u.Port(); port != "" && !p.allowedPorts[port] {
		return errors.New("port not allowed")
	}
	return nil
}

func (p *imageProxy) fetch(ctx context.Context, u *url.URL) ([]byte, string, error) {
	select {
	case p.slots <- struct{}{}:
		defer func() { <-p.slots }()
	case <-ctx.Done():
		return nil, "", ctx.Err()
	}
	// The URL was signed by this server after the message passed its scope check, and the
	// dialer refuses internal addresses.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil) //nolint:gosec // see above
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Accept", "image/*")
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; EchooImageProxy)")
	resp, err := p.client.Do(req) //nolint:gosec // see above
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("upstream status %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, proxyMaxBytes+1))
	if err != nil {
		return nil, "", err
	}
	if len(data) > proxyMaxBytes {
		return nil, "", errors.New("image exceeds the size limit")
	}
	typ := sniff.Type(data)
	if !servableInlineImage(typ) {
		return nil, "", errors.New("response is not an image")
	}
	return data, typ, nil
}

// renderProxy serves a remote image that a rendered message was allowed to show.
func (s *Server) renderProxy(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	q := r.URL.Query()
	remote := q.Get("url")
	if !s.verifyRenderSignature("proxy", remote, q) {
		writeError(w, r, errNotFound)
		return
	}
	u, err := url.Parse(remote)
	if err != nil || s.proxy.check(u) != nil {
		writeError(w, r, errNotFound)
		return
	}
	sum := sha256.Sum256([]byte(remote))

	cached, err := s.q.GetImageProxyCache(ctx, sum[:])
	switch {
	case err == nil:
		rc, err := s.openBlob(ctx, cached.BlobKey)
		if err == nil {
			defer func() { _ = rc.Close() }()
			serveBlob(w, cached.ContentType, cached.SizeBytes, rc)
			return
		}
		if !errors.Is(err, errNotFound) {
			writeError(w, r, err)
			return
		}
		// The blob was removed behind the cache row; fetch it again below.
	case !errors.Is(err, pgx.ErrNoRows):
		writeError(w, r, fmt.Errorf("image cache lookup: %w", err))
		return
	}

	if s.store == nil {
		writeError(w, r, errors.New("blob storage is not configured"))
		return
	}
	data, typ, err := s.proxy.fetch(ctx, u)
	if err != nil {
		// The URL is sender-controlled and identifies a reader; only the cause is logged.
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		slog.WarnContext(ctx, "image proxy fetch failed", "err", err, "request_id", requestIDFrom(ctx))
		writeError(w, r, errImageUnavailable)
		return
	}
	key, _, err := s.store.Put(ctx, data)
	if err != nil {
		writeError(w, r, fmt.Errorf("store proxied image: %w", err))
		return
	}
	if err := s.q.PutImageProxyCache(ctx, dbq.PutImageProxyCacheParams{
		UrlHash: sum[:], BlobKey: key, ContentType: typ, SizeBytes: int64(len(data)),
	}); err != nil {
		writeError(w, r, fmt.Errorf("cache proxied image: %w", err))
		return
	}
	serveBlob(w, typ, int64(len(data)), bytes.NewReader(data))
}
