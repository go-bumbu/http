package spa

import (
	"embed"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/google/go-cmp/cmp"
)

//go:embed testdata/ui/*
var embedFs embed.FS

func TestHandler(t *testing.T) {
	tcs := []struct {
		name    string
		reqPath string
		subDir  string
		expect  int
		data    string
	}{
		{
			name:    "root loads index.html",
			reqPath: "/",
			subDir:  "testdata/ui",
			expect:  http.StatusOK,
			data:    "test index",
		},
		{
			name:    "any path returns index",
			reqPath: "/fruit/banana",
			subDir:  "testdata/ui",
			expect:  http.StatusOK,
			data:    "test index",
		},
		{
			name:    "folder returns index file",
			reqPath: "/assets/style.css/",
			subDir:  "testdata/ui",
			expect:  http.StatusOK,
			data:    "test index",
		},
		{
			name:    "existing folder returns index fil",
			reqPath: "/assets",
			subDir:  "testdata/ui",
			expect:  http.StatusOK,
			data:    "test index",
		},
		{
			name:    "serve css file",
			reqPath: "/assets/style.css",
			subDir:  "testdata/ui",
			expect:  http.StatusOK,
			data:    "css style file",
		},
	}

	localFs := os.DirFS("./")

	fileSystems := map[string]fs.FS{
		"localFs": localFs,
		"embedFs": embedFs,
	}

	subPaths := map[string]string{
		"empty":           "",
		"root":            "/",
		"static-ui-slash": "/static/ui/",
	}

	// test on different path permutations
	for pathName, pathPrefix := range subPaths {
		t.Run("path prefix "+pathName, func(t *testing.T) {

			// test on all supported fs.FS
			for name, fileSystem := range fileSystems {
				t.Run(name, func(t *testing.T) {

					// test all cases
					for _, tc := range tcs {
						t.Run(tc.name, func(t *testing.T) {

							joinPath, _ := url.JoinPath(pathPrefix, tc.reqPath)
							if !strings.HasPrefix(joinPath, "/") {
								joinPath = "/" + joinPath
							}

							req := httptest.NewRequest(http.MethodGet, joinPath, nil)
							w := httptest.NewRecorder()

							handler, err := New(Cfg{
								FS:         fileSystem,
								SubDir:     tc.subDir,
								PathPrefix: pathPrefix,
							})
							if err != nil {
								t.Fatal(err)
							}
							handler.ServeHTTP(w, req)

							got := w.Code
							if diff := cmp.Diff(got, tc.expect); diff != "" {

								t.Errorf("unexpected response code (-got +want)\n%s", diff)
								t.Logf("got body response: %s", w.Body)
							}

							// field tc.data used to verify the file content
							if w.Code == http.StatusOK && tc.data != "" {
								resp := w.Result()
								defer func() { _ = resp.Body.Close() }()

								data, err := io.ReadAll(resp.Body)
								if err != nil {
									t.Errorf("expected error to be nil got %v", err)
								}
								if !strings.Contains(string(data), tc.data) {
									t.Logf("got: %s", string(data))
									t.Errorf("the response body does NOT contain the expected string: %s ", tc.data)
								}
							}
							// field data used to verify redirect target
							if w.Code == http.StatusMovedPermanently && tc.data != "" {
								target := w.Header().Get("location")

								if diff := cmp.Diff(target, tc.data); diff != "" {
									t.Errorf("unexpected value (-got +want)\n%s", diff)
								}
							}
						})
					}
				})
			}
		})
	}

}

// mapFS is a small in-memory SPA tree for exercising handler policy (dotfile
// blocking, prefix normalisation) without depending on testdata or embed rules.
func mapFS() fstest.MapFS {
	return fstest.MapFS{
		"index.html":               {Data: []byte("test index")},
		"assets/app.js":            {Data: []byte("app js")},
		".env":                     {Data: []byte("SECRET=1")},
		".git/config":              {Data: []byte("[core]")},
		".well-known/security.txt": {Data: []byte("Contact: mailto:x@y")},
		".well-known/.secret":      {Data: []byte("nope")},
	}
}

func TestNewRejectsNilFS(t *testing.T) {
	if _, err := New(Cfg{}); err == nil {
		t.Fatal("New must reject a nil FS")
	}
}

// Dotfiles must never be served — http.FileServerFS would serve them, exposing
// /.env or /.git/config over an os.DirFS build dir — except the standard
// .well-known/ tree.
func TestHandlerBlocksDotfiles(t *testing.T) {
	h, err := New(Cfg{FS: mapFS()})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		path string
		want int
	}{
		{"/", http.StatusOK},
		{"/assets/app.js", http.StatusOK},
		{"/.env", http.StatusNotFound},
		{"/.git/config", http.StatusNotFound},
		{"/.well-known/security.txt", http.StatusOK},  // the one allowed dotpath
		{"/.well-known/.secret", http.StatusNotFound}, // nested dotfile still blocked
	}
	for _, c := range cases {
		req := httptest.NewRequest(http.MethodGet, c.path, nil)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != c.want {
			t.Errorf("%s: code = %d, want %d", c.path, w.Code, c.want)
		}
	}
}

// PathPrefix is normalised, so every spelling of the mount point serves assets
// from the same place — a missing leading slash no longer silently breaks it.
func TestHandlerNormalizesPathPrefix(t *testing.T) {
	for _, prefix := range []string{"ui", "/ui", "/ui/", "ui/"} {
		h, err := New(Cfg{FS: mapFS(), PathPrefix: prefix})
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodGet, "/ui/assets/app.js", nil)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Errorf("prefix %q: /ui/assets/app.js code = %d, want 200", prefix, w.Code)
		}
		if body := w.Body.String(); !strings.Contains(body, "app js") {
			t.Errorf("prefix %q: body = %q, want app.js content", prefix, body)
		}
	}
}
