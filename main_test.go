package main

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSavePath(t *testing.T) {
	base := filepath.Join("/tmp", "sites")

	tests := []struct {
		name    string
		host    string
		path    string
		want    string
		wantErr bool
	}{
		{name: "root", host: "example.com", path: "/", want: "example.com/index.html"},
		{name: "empty path", host: "example.com", path: "", want: "example.com/index.html"},
		{name: "file", host: "example.com", path: "/a/b.html", want: "example.com/a/b.html"},
		{name: "directory", host: "example.com", path: "/a/b/", want: "example.com/a/b/index.html"},
		{name: "upper case host", host: "EXAMPLE.com", path: "/x", want: "example.com/x"},
		{name: "host with port", host: "example.com:8443", path: "/x", want: "example.com_8443/x"},
		{name: "traversal in path", host: "example.com", path: "/../../etc/passwd", want: "example.com/etc/passwd"},
		{name: "traversal segment", host: "example.com", path: "/a/../../b", want: "example.com/b"},
		{name: "percent stays literal", host: "example.com", path: "/a/%2e%2e/b", want: "example.com/a/%2e%2e/b"},
		{name: "traversal in host", host: "../../etc", want: ".._.._etc/index.html"},
		{name: "separator in host", host: "a/../../b", want: "a_.._.._b/index.html"},
		{name: "empty host", host: "", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := savePath(base, tt.host, tt.path)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("savePath(%q, %q) = %q, want error", tt.host, tt.path, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("savePath(%q, %q): %v", tt.host, tt.path, err)
			}
			if want := filepath.Join(base, tt.want); got != want {
				t.Errorf("savePath(%q, %q) = %q, want %q", tt.host, tt.path, got, want)
			}
		})
	}
}

// A request line carries an encoded path; net/http decodes it before
// ModifyResponse sees it, so savePath must handle the decoded form.
func TestSavePathDecodedTraversal(t *testing.T) {
	base := t.TempDir()
	u, err := url.Parse("http://example.com/a/%2e%2e/%2e%2e/etc/passwd")
	if err != nil {
		t.Fatal(err)
	}
	got, err := savePath(base, u.Host, u.Path)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(base, "example.com", "etc", "passwd"); got != want {
		t.Errorf("savePath = %q, want %q", got, want)
	}
}

func TestSavePathStaysInsideBaseDir(t *testing.T) {
	base := t.TempDir()
	for _, host := range []string{"..", "../..", "%2e%2e", ".", "\x00"} {
		for _, p := range []string{"/", "/../x", "/a/../../../x"} {
			got, err := savePath(base, host, p)
			if err != nil {
				continue
			}
			if !strings.HasPrefix(got, base+string(filepath.Separator)) {
				t.Errorf("savePath(%q, %q) = %q, escapes %q", host, p, got, base)
			}
		}
	}
}

func TestRequestScheme(t *testing.T) {
	tests := []struct {
		header string
		want   string
	}{
		{header: "", want: "http"},
		{header: "https", want: "https"},
		{header: "HTTPS", want: "https"},
		{header: "https, http", want: "https"},
		{header: "http, https", want: "http"},
		{header: "http", want: "http"},
	}
	for _, tt := range tests {
		r := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
		if tt.header != "" {
			r.Header.Set("X-Forwarded-Proto", tt.header)
		}
		if got := requestScheme(r); got != tt.want {
			t.Errorf("requestScheme(%q) = %q, want %q", tt.header, got, tt.want)
		}
	}
}

func TestDecodeBody(t *testing.T) {
	var gz bytes.Buffer
	w := gzip.NewWriter(&gz)
	if _, err := w.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	got, err := decodeBody("gzip", gz.Bytes())
	if err != nil {
		t.Fatalf("decodeBody(gzip): %v", err)
	}
	if string(got) != "hello" {
		t.Errorf("decodeBody(gzip) = %q, want %q", got, "hello")
	}

	if got, err := decodeBody("", []byte("plain")); err != nil || string(got) != "plain" {
		t.Errorf("decodeBody(identity) = %q, %v", got, err)
	}
	if _, err := decodeBody("br", []byte("x")); err == nil {
		t.Error("decodeBody(br) = nil error, want error")
	}
}

// writeFile must cope with a URL path that is a file for one request and a
// directory prefix for the next.
func TestWriteFileDirectoryCollision(t *testing.T) {
	base := t.TempDir()

	if err := writeFile(filepath.Join(base, "a"), []byte("file")); err != nil {
		t.Fatal(err)
	}
	// "/a" is now a file; saving "/a/b" cannot create the directory.
	if err := writeFile(filepath.Join(base, "a", "b"), []byte("nested")); err == nil {
		t.Error("writeFile into a file path succeeded, want error")
	}

	if err := writeFile(filepath.Join(base, "c", "d"), []byte("nested")); err != nil {
		t.Fatal(err)
	}
	// "/c" is now a directory; it must be saved as c/index.html.
	if err := writeFile(filepath.Join(base, "c"), []byte("dir")); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(base, "c", "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "dir" {
		t.Errorf("c/index.html = %q, want %q", got, "dir")
	}
}

func TestProxyEndToEnd(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Set("Content-Type", "text/html")
		gz := gzip.NewWriter(w)
		defer func() { _ = gz.Close() }()
		_, _ = gz.Write([]byte("<html>" + r.URL.Path + "</html>"))
	}))
	defer upstream.Close()

	upstreamURL, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}

	base := t.TempDir()
	proxy, err := newProxy(base, "", nil) // system resolver: upstream is on 127.0.0.1
	if err != nil {
		t.Fatal(err)
	}
	front := httptest.NewServer(proxy)
	defer front.Close()

	req, err := http.NewRequest(http.MethodGet, front.URL+"/dir/", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = upstreamURL.Host // decides the upstream, as in production
	req.Header.Set("Accept-Encoding", "gzip")

	resp, err := front.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	// The client asked for gzip, so it must still receive gzip.
	gzr, err := gzip.NewReader(bytes.NewReader(body))
	if err != nil {
		t.Fatalf("client response is not gzip: %v", err)
	}
	clientBody, err := io.ReadAll(gzr)
	if err != nil {
		t.Fatal(err)
	}
	if want := "<html>/dir/</html>"; string(clientBody) != want {
		t.Errorf("client body = %q, want %q", clientBody, want)
	}

	// The saved file must be the decompressed content.
	saved := filepath.Join(base, sanitizeSegment(upstreamURL.Host), "dir", "index.html")
	got, err := os.ReadFile(saved)
	if err != nil {
		t.Fatal(err)
	}
	if want := "<html>/dir/</html>"; string(got) != want {
		t.Errorf("saved body = %q, want %q", got, want)
	}
}

func TestContentTypeFilter(t *testing.T) {
	tests := []struct {
		name        string
		patterns    []string
		contentType string
		want        bool
	}{
		{name: "no filter saves everything", contentType: "image/png", want: true},
		{name: "no filter saves missing type", want: true},
		{name: "exact", patterns: []string{"text/html"}, contentType: "text/html", want: true},
		{name: "exact miss", patterns: []string{"text/html"}, contentType: "text/plain"},
		{name: "with parameters", patterns: []string{"text/html"}, contentType: "text/html; charset=UTF-8", want: true},
		{name: "malformed parameters", patterns: []string{"text/html"}, contentType: "text/html; charset", want: true},
		{name: "upper case header", patterns: []string{"text/html"}, contentType: "TEXT/HTML", want: true},
		{name: "upper case pattern", patterns: []string{"TEXT/HTML"}, contentType: "text/html", want: true},
		{name: "subtype glob", patterns: []string{"text/*"}, contentType: "text/css", want: true},
		{name: "subtype glob miss", patterns: []string{"text/*"}, contentType: "image/png"},
		{name: "type glob", patterns: []string{"*/json"}, contentType: "application/json", want: true},
		{name: "subtype glob does not cross slash", patterns: []string{"*/*"}, contentType: "text/html/x"},
		{name: "bare type means whole type", patterns: []string{"text"}, contentType: "text/html", want: true},
		{name: "bare star means everything", patterns: []string{"*"}, contentType: "image/png", want: true},
		{name: "suffix glob", patterns: []string{"*/*+json"}, contentType: "application/problem+json", want: true},
		{name: "multiple patterns", patterns: []string{"text/*", "application/json"}, contentType: "application/json", want: true},
		{name: "multiple patterns miss", patterns: []string{"text/*", "application/json"}, contentType: "image/png"},
		{name: "missing content type", patterns: []string{"text/*"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			filter, err := newContentTypeFilter(tt.patterns)
			if err != nil {
				t.Fatalf("newContentTypeFilter(%q): %v", tt.patterns, err)
			}
			if got := filter.match(tt.contentType); got != tt.want {
				t.Errorf("match(%q) with %q = %v, want %v", tt.contentType, tt.patterns, got, tt.want)
			}
		})
	}
}

func TestNewContentTypeFilter(t *testing.T) {
	filter, err := newContentTypeFilter([]string{"  Text/HTML  ", "", "image"})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := filter.String(), "text/html, image/*"; got != want {
		t.Errorf("filter = %q, want %q", got, want)
	}
	if _, err := newContentTypeFilter([]string{"text/[html"}); err == nil {
		t.Error("newContentTypeFilter with a bad glob = nil error, want error")
	}
}

func TestStringListSet(t *testing.T) {
	var list stringList
	if err := list.Set("text/html, text/css"); err != nil {
		t.Fatal(err)
	}
	if err := list.Set("application/json"); err != nil {
		t.Fatal(err)
	}
	if err := list.Set(""); err != nil {
		t.Fatal(err)
	}
	if got, want := list.String(), "text/html,text/css,application/json"; got != want {
		t.Errorf("list = %q, want %q", got, want)
	}
	if len(list) != 3 {
		t.Errorf("len(list) = %d, want 3", len(list))
	}
}

// A response whose Content-Type is not selected must still reach the client
// untouched, it is only not written to disk.
func TestProxyContentTypeFilter(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".png") {
			w.Header().Set("Content-Type", "image/png")
		} else {
			w.Header().Set("Content-Type", "text/html; charset=UTF-8")
		}
		_, _ = io.WriteString(w, "body:"+r.URL.Path)
	}))
	defer upstream.Close()

	upstreamURL, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}

	base := t.TempDir()
	filter, err := newContentTypeFilter([]string{"text/*"})
	if err != nil {
		t.Fatal(err)
	}
	proxy, err := newProxy(base, "", filter)
	if err != nil {
		t.Fatal(err)
	}
	front := httptest.NewServer(proxy)
	defer front.Close()

	for _, p := range []string{"/a.html", "/b.png"} {
		req, err := http.NewRequest(http.MethodGet, front.URL+p, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Host = upstreamURL.Host
		resp, err := front.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		if want := "body:" + p; string(body) != want {
			t.Errorf("GET %s body = %q, want %q", p, body, want)
		}
	}

	host := sanitizeSegment(upstreamURL.Host)
	if got, err := os.ReadFile(filepath.Join(base, host, "a.html")); err != nil {
		t.Errorf("a.html was not saved: %v", err)
	} else if want := "body:/a.html"; string(got) != want {
		t.Errorf("saved a.html = %q, want %q", got, want)
	}
	if _, err := os.Stat(filepath.Join(base, host, "b.png")); !os.IsNotExist(err) {
		t.Errorf("b.png stat error = %v, want not exist", err)
	}
}
