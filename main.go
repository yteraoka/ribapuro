// ribapuro is a small reverse proxy that saves upstream response bodies to
// local files.
//
// The Host header of the incoming request decides the upstream, so the proxy
// needs a resolver that does not see the local override (/etc/hosts, dnsmasq,
// ...) which points the domain at this proxy. That is what -resolver is for.
package main

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"os"
	"os/signal"
	"path"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const (
	indexFileName = "index.html"
	dirPerm       = 0o750
	filePerm      = 0o640
)

type config struct {
	addr            string
	dir             string
	resolver        string
	shutdownTimeout time.Duration
}

func main() {
	log.SetFlags(log.LstdFlags | log.LUTC)

	var cfg config
	flag.StringVar(&cfg.addr, "addr", envOr("RIBAPURO_ADDR", ":8080"), "listen address")
	flag.StringVar(&cfg.dir, "dir", envOr("RIBAPURO_DIR", "sites"), "directory to save response bodies into")
	flag.StringVar(&cfg.resolver, "resolver", envOr("RIBAPURO_RESOLVER", "1.1.1.1:53"),
		"DNS server (host:port) used to resolve upstream hosts; empty to use the system resolver")
	flag.DurationVar(&cfg.shutdownTimeout, "shutdown-timeout", 10*time.Second, "graceful shutdown timeout")
	flag.Parse()

	if err := run(cfg); err != nil {
		log.Fatal(err)
	}
}

func run(cfg config) error {
	baseDir, err := filepath.Abs(cfg.dir)
	if err != nil {
		return fmt.Errorf("resolve -dir: %w", err)
	}
	if err := os.MkdirAll(baseDir, dirPerm); err != nil {
		return fmt.Errorf("create -dir: %w", err)
	}

	proxy, err := newProxy(baseDir, cfg.resolver)
	if err != nil {
		return err
	}

	server := &http.Server{
		Addr:              cfg.addr,
		Handler:           proxy,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		ErrorLog:          log.Default(),
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		log.Printf("listening on %s, saving into %s", cfg.addr, baseDir)
		errCh <- server.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		stop()
		log.Println("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.shutdownTimeout)
		defer cancel()
		return server.Shutdown(shutdownCtx)
	}
}

func newProxy(baseDir, resolver string) (*httputil.ReverseProxy, error) {
	transport, err := newTransport(resolver)
	if err != nil {
		return nil, err
	}

	return &httputil.ReverseProxy{
		Transport: transport,
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL.Scheme = requestScheme(pr.In)
			pr.Out.URL.Host = pr.In.Host
			pr.Out.Host = pr.In.Host
			pr.SetXForwarded()
			// This proxy runs behind a TLS terminator (see README), so keep
			// the values it already set instead of the ones describing the
			// plaintext hop between it and us.
			preserveHeader(pr, "X-Forwarded-Proto")
			preserveHeader(pr, "X-Forwarded-Host")
			log.Printf("%s %s://%s%s", pr.In.Method, pr.Out.URL.Scheme, pr.Out.URL.Host, pr.Out.URL.RequestURI())
		},
		ModifyResponse: func(resp *http.Response) error {
			return saveResponse(baseDir, resp)
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			log.Printf("proxy error: %s %s: %v", r.Method, r.Host, err)
			w.WriteHeader(http.StatusBadGateway)
		},
	}, nil
}

func newTransport(resolver string) (*http.Transport, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if resolver == "" {
		return transport, nil
	}
	if _, _, err := net.SplitHostPort(resolver); err != nil {
		return nil, fmt.Errorf("invalid -resolver %q (want host:port): %w", resolver, err)
	}

	// https://qiita.com/izumin5210/items/7cdefe52cc54794c85fc
	dialer := &net.Dialer{
		Timeout:   30 * time.Second,
		KeepAlive: 30 * time.Second,
		Resolver: &net.Resolver{
			PreferGo: true,
			Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
				d := net.Dialer{Timeout: 5 * time.Second}
				if strings.HasPrefix(network, "tcp") {
					return d.DialContext(ctx, "tcp", resolver)
				}
				return d.DialContext(ctx, "udp", resolver)
			},
		},
	}
	transport.DialContext = dialer.DialContext
	return transport, nil
}

func preserveHeader(pr *httputil.ProxyRequest, name string) {
	if v := pr.In.Header.Get(name); v != "" {
		pr.Out.Header.Set(name, v)
	}
}

// requestScheme reports the scheme to use for the upstream request, trusting
// the X-Forwarded-Proto set by the TLS terminator in front of this proxy.
func requestScheme(r *http.Request) string {
	proto := r.Header.Get("X-Forwarded-Proto")
	if i := strings.IndexByte(proto, ','); i >= 0 {
		proto = proto[:i] // may be a list; the left-most entry is the client's
	}
	if strings.EqualFold(strings.TrimSpace(proto), "https") {
		return "https"
	}
	if r.TLS != nil {
		return "https"
	}
	return "http"
}

// saveResponse writes the response body to a file and restores the body so
// that it can still be sent to the client. Bodies are buffered in memory.
func saveResponse(baseDir string, resp *http.Response) error {
	buf, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		return err
	}
	resp.Body = io.NopCloser(bytes.NewReader(buf))

	// Saving is best effort: a failure here must not break the proxied
	// response.
	localFile, err := savePath(baseDir, resp.Request.URL.Host, resp.Request.URL.Path)
	if err != nil {
		log.Printf("skip saving %s: %v", resp.Request.URL, err)
		return nil
	}
	body, err := decodeBody(resp.Header.Get("Content-Encoding"), buf)
	if err != nil {
		log.Printf("save %s undecoded: %v", localFile, err)
		body = buf
	}
	if err := writeFile(localFile, body); err != nil {
		log.Printf("save %s: %v", localFile, err)
		return nil
	}
	log.Printf("saved %s (%d bytes)", localFile, len(body))
	return nil
}

// savePath maps a request to a file below baseDir. The host and the path both
// come from the client, so the result is verified to stay inside baseDir.
func savePath(baseDir, host, urlPath string) (string, error) {
	host = sanitizeSegment(strings.ToLower(host))
	if host == "" {
		return "", errors.New("empty host")
	}

	cleaned := path.Clean("/" + urlPath)
	if cleaned == "/" || strings.HasSuffix(urlPath, "/") {
		cleaned = path.Join(cleaned, indexFileName)
	}

	segments := []string{baseDir, host}
	for _, segment := range strings.Split(strings.TrimPrefix(cleaned, "/"), "/") {
		segments = append(segments, sanitizeSegment(segment))
	}
	local := filepath.Join(segments...)

	if local != baseDir && !strings.HasPrefix(local, baseDir+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes %q", local, baseDir)
	}
	return local, nil
}

// sanitizeSegment makes a single path element safe to use as a file name.
func sanitizeSegment(segment string) string {
	if segment == "." || segment == ".." {
		return "_"
	}
	return strings.Map(func(r rune) rune {
		switch {
		case r < 0x20 || r == 0x7f:
			return '_'
		case strings.ContainsRune(`/\:*?"<>|`, r):
			return '_'
		}
		return r
	}, segment)
}

// decodeBody undoes the Content-Encoding so that the saved file holds the
// content itself rather than its compressed form.
func decodeBody(encoding string, buf []byte) ([]byte, error) {
	var reader io.ReadCloser
	switch strings.ToLower(strings.TrimSpace(encoding)) {
	case "", "identity":
		return buf, nil
	case "gzip", "x-gzip":
		r, err := gzip.NewReader(bytes.NewReader(buf))
		if err != nil {
			return nil, err
		}
		reader = r
	case "deflate":
		reader = flate.NewReader(bytes.NewReader(buf))
	default:
		return nil, fmt.Errorf("unsupported Content-Encoding %q", encoding)
	}
	defer reader.Close()

	decoded, err := io.ReadAll(reader)
	if err != nil {
		return nil, err
	}
	return decoded, nil
}

// writeFile writes body to localFile, creating parent directories as needed.
// A directory may already exist where a file is wanted (or the other way
// around) because URL paths do not map onto a file system one to one.
func writeFile(localFile string, body []byte) error {
	if err := os.MkdirAll(filepath.Dir(localFile), dirPerm); err != nil {
		return err
	}
	if info, err := os.Stat(localFile); err == nil && info.IsDir() {
		localFile = filepath.Join(localFile, indexFileName)
	}
	return os.WriteFile(localFile, body, filePerm)
}

func envOr(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return fallback
}
