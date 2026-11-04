package web

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"
)

func TestStandalone(t *testing.T) {
	api := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("api:" + r.URL.Path)) })
	h := Standalone(api, "")

	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/state", nil))
	if w.Body.String() != "api:/state" {
		t.Errorf("api route = %q", w.Body)
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "standalone.js") || w.Header().Get("Content-Security-Policy") == "" {
		t.Errorf("index = %d %q", w.Code, w.Body)
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/lib", nil))
	if w.Code != http.StatusNotFound {
		t.Errorf("directory listing status %d", w.Code)
	}
}

func TestToken(t *testing.T) {
	h := Standalone(http.NotFoundHandler(), "s3cret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
	if w.Code != http.StatusUnauthorized {
		t.Errorf("no token status %d", w.Code)
	}
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.SetBasicAuth("", "s3cret")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Errorf("basic auth status %d", w.Code)
	}
}

func TestCrossOriginWritesAreRefused(t *testing.T) {
	called := false
	h := Standalone(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }), "")
	r := httptest.NewRequest(http.MethodPost, "/api/dtc/clear", strings.NewReader("{}"))
	r.Header.Set("Sec-Fetch-Site", "cross-site")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if called || w.Code != http.StatusForbidden {
		t.Errorf("called=%v status=%d", called, w.Code)
	}
}

// Every module the UI imports must exist in the embedded tree, since nothing
// else would catch a missing file until a browser hits it.
func TestRelativeImportsResolve(t *testing.T) {
	importRe := regexp.MustCompile(`(?:from\s+|import\s*\(\s*|import\s+)['"](\.{1,2}/[^'"]+)['"]`)
	err := fs.WalkDir(Assets, ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(name, ".js") || strings.Contains(name, "node_modules/") {
			return err
		}
		data, err := fs.ReadFile(Assets, name)
		if err != nil {
			return err
		}
		for _, m := range importRe.FindAllStringSubmatch(string(data), -1) {
			target := resolve(name, m[1])
			if _, err := fs.Stat(Assets, target); err != nil {
				t.Errorf("%s imports %s, which is missing; run make deps", name, m[1])
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func resolve(from, rel string) string {
	parts := strings.Split(from, "/")
	parts = parts[:len(parts)-1]
	for _, p := range strings.Split(rel, "/") {
		switch p {
		case ".":
		case "..":
			parts = parts[:len(parts)-1]
		default:
			parts = append(parts, p)
		}
	}
	return strings.Join(parts, "/")
}

func TestStaticIsMethodLimited(t *testing.T) {
	h := Static(fstest.MapFS{"index.html": {Data: []byte("x")}})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/", nil))
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status %d", w.Code)
	}
}
