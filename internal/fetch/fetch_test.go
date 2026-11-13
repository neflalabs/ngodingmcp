package fetch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ngodingmcp/internal/workspace"
)

func newWS(t *testing.T) (*workspace.Workspace, string) {
	t.Helper()
	root := t.TempDir()
	ws, err := workspace.New(root, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	return ws, root
}

// testClient trusts srv's certificate and allows loopback (test-only).
func testClient(srv *httptest.Server, max int64) *Client {
	pool := srv.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs
	return New(Options{MaxBytes: max, Testing: TestingOptions{AllowLoopback: true, RootCAs: pool}})
}

func fileServer(body []byte) *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/file", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/webp")
		_, _ = w.Write(body)
	})
	// Lies about its size: Content-Length is absent (chunked), body is big.
	mux.HandleFunc("/chunked", func(w http.ResponseWriter, r *http.Request) {
		for i := 0; i < 4; i++ {
			_, _ = w.Write(body)
			w.(http.Flusher).Flush()
		}
	})
	mux.HandleFunc("/to-private", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://10.0.0.1/secret", http.StatusFound)
	})
	mux.HandleFunc("/to-metadata", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://169.254.169.254/latest/meta-data/", http.StatusFound)
	})
	mux.HandleFunc("/to-http", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://example.com/file", http.StatusFound)
	})
	mux.HandleFunc("/loop", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/loop", http.StatusFound)
	})
	return httptest.NewTLSServer(mux)
}

func assertNoFiles(t *testing.T, root string) {
	t.Helper()
	_ = filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			t.Errorf("unexpected file left in workspace: %s", strings.TrimPrefix(p, root))
		}
		return nil
	})
}

func TestDownloadSuccess(t *testing.T) {
	body := []byte("RIFF\x00\x00WEBPVP8 binary\x00\xff")
	srv := fileServer(body)
	defer srv.Close()
	ws, root := newWS(t)
	sum := sha256.Sum256(body)
	want := hex.EncodeToString(sum[:])

	res, err := testClient(srv, 1024).Download(context.Background(), ws, srv.URL+"/file?token=SIGNED", "static/images/hero.webp", false, want)
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	if res.Path != "static/images/hero.webp" || res.Bytes != int64(len(body)) || res.SHA256 != want || res.ContentType != "image/webp" {
		t.Fatalf("result = %+v", res)
	}
	got, _ := os.ReadFile(filepath.Join(root, "static", "images", "hero.webp"))
	if string(got) != string(body) {
		t.Fatal("content differs")
	}
}

func TestDownloadSizeLimitStreamed(t *testing.T) {
	srv := fileServer([]byte(strings.Repeat("x", 100)))
	defer srv.Close()
	ws, root := newWS(t)
	c := testClient(srv, 250)
	// /file declares Content-Length 100 (fits); /chunked sends 400 with no
	// Content-Length, so only the streamed limit can catch it.
	_, err := c.Download(context.Background(), ws, srv.URL+"/chunked", "big.bin", false, "")
	if !errors.Is(err, workspace.ErrStreamTooLarge) {
		t.Fatalf("err = %v, want ErrStreamTooLarge", err)
	}
	assertNoFiles(t, root)
}

func TestDownloadRedirectToPrivateBlocked(t *testing.T) {
	srv := fileServer([]byte("x"))
	defer srv.Close()
	ws, root := newWS(t)
	for _, p := range []string{"/to-private", "/to-metadata"} {
		_, err := testClient(srv, 1024).Download(context.Background(), ws, srv.URL+p, "x.bin", false, "")
		if !errors.Is(err, ErrBlockedAddress) {
			t.Errorf("%s: err = %v, want ErrBlockedAddress", p, err)
		}
	}
	assertNoFiles(t, root)
}

func TestDownloadLoopbackBlockedWithoutTestOption(t *testing.T) {
	srv := fileServer([]byte("x"))
	defer srv.Close()
	ws, _ := newWS(t)
	pool := srv.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs
	c := New(Options{Testing: TestingOptions{RootCAs: pool}}) // loopback NOT allowed
	_, err := c.Download(context.Background(), ws, srv.URL+"/file", "x.bin", false, "")
	if !errors.Is(err, ErrBlockedAddress) {
		t.Fatalf("err = %v, want ErrBlockedAddress", err)
	}
}

func TestDownloadHTTPRejected(t *testing.T) {
	srv := fileServer([]byte("x"))
	defer srv.Close()
	ws, root := newWS(t)
	c := testClient(srv, 1024)
	if _, err := c.Download(context.Background(), ws, "http://example.com/file", "x.bin", false, ""); !errors.Is(err, ErrNotHTTPS) {
		t.Fatalf("plain http: err = %v", err)
	}
	if _, err := c.Download(context.Background(), ws, srv.URL+"/to-http", "x.bin", false, ""); !errors.Is(err, ErrNotHTTPS) {
		t.Fatalf("redirect to http: err = %v", err)
	}
	if _, err := c.Download(context.Background(), ws, "file:///etc/passwd", "x.bin", false, ""); !errors.Is(err, ErrNotHTTPS) {
		t.Fatalf("file scheme: err = %v", err)
	}
	assertNoFiles(t, root)
}

func TestDownloadRedirectCap(t *testing.T) {
	srv := fileServer([]byte("x"))
	defer srv.Close()
	ws, root := newWS(t)
	_, err := testClient(srv, 1024).Download(context.Background(), ws, srv.URL+"/loop", "x.bin", false, "")
	if err == nil || !strings.Contains(err.Error(), "redirects") {
		t.Fatalf("err = %v, want redirect cap", err)
	}
	assertNoFiles(t, root)
}

func TestDownloadOverwriteFalse(t *testing.T) {
	srv := fileServer([]byte("new"))
	defer srv.Close()
	ws, root := newWS(t)
	dest := filepath.Join(root, "a.txt")
	_ = os.WriteFile(dest, []byte("old"), 0o644)
	c := testClient(srv, 1024)
	if _, err := c.Download(context.Background(), ws, srv.URL+"/file", "a.txt", false, ""); !errors.Is(err, workspace.ErrExists) {
		t.Fatalf("err = %v, want ErrExists", err)
	}
	if got, _ := os.ReadFile(dest); string(got) != "old" {
		t.Fatal("file replaced without overwrite")
	}
	if _, err := c.Download(context.Background(), ws, srv.URL+"/file", "a.txt", true, ""); err != nil {
		t.Fatalf("overwrite: %v", err)
	}
	if got, _ := os.ReadFile(dest); string(got) != "new" {
		t.Fatal("overwrite did not replace")
	}
}

func TestDownloadSHA256Mismatch(t *testing.T) {
	srv := fileServer([]byte("data"))
	defer srv.Close()
	ws, root := newWS(t)
	wrong := strings.Repeat("0", 64)
	_, err := testClient(srv, 1024).Download(context.Background(), ws, srv.URL+"/file", "x.bin", false, wrong)
	if !errors.Is(err, ErrChecksum) {
		t.Fatalf("err = %v, want ErrChecksum", err)
	}
	if _, err := testClient(srv, 1024).Download(context.Background(), ws, srv.URL+"/file", "x.bin", false, "nothex"); err == nil {
		t.Fatal("malformed sha256 accepted")
	}
	assertNoFiles(t, root)
}

func TestDownloadPathTraversal(t *testing.T) {
	srv := fileServer([]byte("x"))
	defer srv.Close()
	ws, root := newWS(t)
	// A leading "/" is read as workspace-relative, like every other tool,
	// so only ".." escapes and the root itself are tested here.
	for _, dest := range []string{"../escape.txt", "a/../../escape.txt", `..\escape.txt`, ".", ""} {
		_, err := testClient(srv, 1024).Download(context.Background(), ws, srv.URL+"/file", dest, false, "")
		if err == nil {
			t.Errorf("%q accepted", dest)
		}
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(root), "escape.txt")); !os.IsNotExist(err) {
		t.Fatal("file escaped the workspace")
	}
}

func TestDownloadSensitiveDestination(t *testing.T) {
	srv := fileServer([]byte("SECRET=1"))
	defer srv.Close()
	ws, root := newWS(t)
	c := testClient(srv, 1024)
	for _, dest := range []string{".env", "config/.env.production", ".git/config", "keys/id_rsa", "tls/server.key"} {
		if _, err := c.Download(context.Background(), ws, srv.URL+"/file", dest, false, ""); !errors.Is(err, workspace.ErrSensitive) {
			t.Errorf("%s: err = %v, want ErrSensitive", dest, err)
		}
	}
	assertNoFiles(t, root)
	ws.SetAllowSensitive(true)
	if _, err := c.Download(context.Background(), ws, srv.URL+"/file", ".env", false, ""); err != nil {
		t.Fatalf("allow-sensitive: %v", err)
	}
}

func TestDownloadErrorsHideToken(t *testing.T) {
	srv := fileServer([]byte("x"))
	defer srv.Close()
	ws, _ := newWS(t)
	_, err := testClient(srv, 1024).Download(context.Background(), ws, srv.URL+"/missing?token=SIGNED-SECRET", "x.bin", false, "")
	if err == nil || strings.Contains(err.Error(), "SIGNED-SECRET") || !strings.Contains(err.Error(), "404") {
		t.Fatalf("err = %v", err)
	}
	// Transport errors (*url.Error) carry the full URL; it must be stripped.
	srv2 := fileServer([]byte("x"))
	srv2.Close()
	_, err = testClient(srv2, 1024).Download(context.Background(), ws, srv2.URL+"/file?token=SIGNED-SECRET", "x.bin", false, "")
	if err == nil || strings.Contains(err.Error(), "SIGNED-SECRET") {
		t.Fatalf("err = %v", err)
	}
}

func TestHostAllowlist(t *testing.T) {
	srv := fileServer([]byte("x"))
	defer srv.Close()
	ws, _ := newWS(t)
	pool := srv.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs
	c := New(Options{AllowHosts: []string{"*.example.com"}, Testing: TestingOptions{AllowLoopback: true, RootCAs: pool}})
	if _, err := c.Download(context.Background(), ws, srv.URL+"/file", "x.bin", false, ""); !errors.Is(err, ErrHostNotAllowed) {
		t.Fatalf("err = %v, want ErrHostNotAllowed", err)
	}
	if !hostAllowed("cdn.example.com", []string{"*.example.com"}) || hostAllowed("example.com.evil.io", []string{"*.example.com"}) {
		t.Fatal("wildcard matching wrong")
	}
}

func TestIsPublicIP(t *testing.T) {
	blocked := []string{"127.0.0.1", "10.1.2.3", "172.16.0.1", "192.168.1.1", "169.254.169.254", "100.100.1.1",
		"0.0.0.0", "::1", "fe80::1", "fd00::1", "::ffff:127.0.0.1", "::ffff:10.0.0.1", "224.0.0.1", "255.255.255.255"}
	for _, s := range blocked {
		if IsPublicIP(netip.MustParseAddr(s)) {
			t.Errorf("%s should be blocked", s)
		}
	}
	for _, s := range []string{"1.1.1.1", "8.8.8.8", "2606:4700:4700::1111"} {
		if !IsPublicIP(netip.MustParseAddr(s)) {
			t.Errorf("%s should be public", s)
		}
	}
}
