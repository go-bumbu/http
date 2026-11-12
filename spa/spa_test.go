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

// PathPrefix matches whole path segments only: /uix.js and /uifoo/bar are not
// under /ui, so they must 404 instead of serving x.js or the SPA entrypoint.
func TestHandlerPathPrefixMatchesWholeSegments(t *testing.T) {
	fsys := mapFS()
	fsys["x.js"] = &fstest.MapFile{Data: []byte("x js")}
	fsys["app.js"] = &fstest.MapFile{Data: []byte("root app js")}

	cases := []struct {
		prefix, target string
		want           int
		body           string
	}{
		{prefix: "/ui", target: "/uix.js", want: http.StatusNotFound},
		{prefix: "/ui", target: "/uifoo/bar", want: http.StatusNotFound},
		{prefix: "/ui", target: "/uifoo/", want: http.StatusNotFound},
		{prefix: "/ui", target: "/other/app.js", want: http.StatusNotFound},
		{prefix: "/ui", target: "/%75i/app.js", want: http.StatusNotFound}, // StripPrefix cannot strip the escaped form
		{prefix: "/ui", target: "/%75i/deep", want: http.StatusNotFound},
		{prefix: "/ui", target: "/ui", want: http.StatusOK, body: "test index"},
		{prefix: "/ui", target: "/ui/", want: http.StatusOK, body: "test index"},
		{prefix: "/ui", target: "/ui/app.js", want: http.StatusOK, body: "root app js"},
		{prefix: "/ui", target: "/ui/x.js", want: http.StatusOK, body: "x js"},
		{prefix: "/ui", target: "/ui/users/42", want: http.StatusOK, body: "test index"},
		{prefix: "/ui", target: "/ui/users/a%2Fb", want: http.StatusOK, body: "test index"},
		{prefix: "/static/ui", target: "/static/uix.js", want: http.StatusNotFound},
		{prefix: "/static/ui", target: "/static/ui/x.js", want: http.StatusOK, body: "x js"},
		{prefix: "", target: "/uix.js", want: http.StatusOK, body: "test index"},
		{prefix: "", target: "/x.js", want: http.StatusOK, body: "x js"},
		{prefix: "", target: "/uifoo/bar", want: http.StatusOK, body: "test index"},
		{prefix: "/", target: "/x.js", want: http.StatusOK, body: "x js"},
		{prefix: "/", target: "/uifoo/bar", want: http.StatusOK, body: "test index"},
	}
	for _, c := range cases {
		h, err := New(Cfg{FS: fsys, PathPrefix: c.prefix})
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodGet, c.target, nil)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != c.want {
			t.Errorf("prefix %q %s: code = %d, want %d (body %q)", c.prefix, c.target, w.Code, c.want, w.Body.String())
			continue
		}
		if c.body != "" && w.Body.String() != c.body {
			t.Errorf("prefix %q %s: body = %q, want %q", c.prefix, c.target, w.Body.String(), c.body)
		}
	}
}

// A SubDir that does not hold index.html — the parent of the build dir, a
// missing build output, or a path that does not exist — must fail at New
// instead of serving a directory listing (or a 404) for every deep link.
func TestNewRequiresIndex(t *testing.T) {
	fsys := fstest.MapFS{
		"web/dist/index.html":   {Data: []byte("test index")},
		"web/.env":              {Data: []byte("SECRET=1")},
		"dirindex/index.html/x": {Data: []byte("not a file")},
	}
	for _, sub := range []string{"web", "missing", "dirindex"} {
		if _, err := New(Cfg{FS: fsys, SubDir: sub}); err == nil {
			t.Errorf("SubDir %q: New must reject a tree without an index.html file", sub)
		}
	}
	if _, err := New(Cfg{FS: fsys, SubDir: "web/dist"}); err != nil {
		t.Errorf("SubDir web/dist: unexpected error %v", err)
	}
}

// Deep links serve index.html itself, never a directory listing, with the
// same content headers the file server set.
func TestHandlerServesIndexForDeepLinks(t *testing.T) {
	h, err := New(Cfg{FS: mapFS(), PathPrefix: "/ui"})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"/ui/", "/ui/users/42", "/ui/assets", "/ui/assets/"} {
		req := httptest.NewRequest(http.MethodGet, p, nil)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != http.StatusOK || w.Body.String() != "test index" {
			t.Errorf("%s: code=%d body=%q, want 200 index", p, w.Code, w.Body.String())
		}
		if ct := w.Header().Get("Content-Type"); ct != "text/html; charset=utf-8" {
			t.Errorf("%s: Content-Type = %q, want text/html", p, ct)
		}
	}
}

// The handler must never mutate the caller's request: an outer wrapper (e.g.
// logging middleware) reads r.URL after ServeHTTP returns, and a deep link
// once came back as url=/ because the fallback rewrote r.URL.Path.
func TestHandlerDoesNotMutateRequest(t *testing.T) {
	cases := []struct {
		name, prefix, target string
		path                 string // overrides the parsed Path, e.g. as left by an outer http.StripPrefix
	}{
		{name: "root deep link", target: "/users/42"},
		{name: "root asset", target: "/assets/app.js"},
		{name: "root dotfile", target: "/.env"},
		{name: "root index", target: "/"},
		{name: "prefixed deep link", prefix: "/ui", target: "/ui/users/42"},
		{name: "prefixed escaped deep link", prefix: "/ui", target: "/ui/users/a%2Fb"},
		{name: "prefixed asset", prefix: "/ui", target: "/ui/assets/app.js"},
		{name: "prefixed escaped asset", prefix: "/ui", target: "/ui/assets/app%2Ejs"},
		{name: "prefixed directory", prefix: "/ui", target: "/ui/assets/"},
		{name: "root asset without leading slash", target: "/assets/app.js", path: "assets/app.js"},
		{name: "root deep link without leading slash", target: "/users/42", path: "users/42"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h, err := New(Cfg{FS: mapFS(), PathPrefix: c.prefix})
			if err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest(http.MethodGet, c.target, nil)
			if c.path != "" {
				req.URL.Path = c.path
			}
			wantURL := *req.URL
			h.ServeHTTP(httptest.NewRecorder(), req)
			if req.URL.Path != wantURL.Path || req.URL.RawPath != wantURL.RawPath {
				t.Errorf("caller's URL mutated: Path=%q RawPath=%q, want Path=%q RawPath=%q",
					req.URL.Path, req.URL.RawPath, wantURL.Path, wantURL.RawPath)
			}
		})
	}
}

// If index.html vanishes after New (an os.DirFS build dir being rebuilt), a
// deep link must 404 rather than fall back to a directory listing.
func TestHandlerNeverListsDirectory(t *testing.T) {
	fsys := mapFS()
	h, err := New(Cfg{FS: fsys})
	if err != nil {
		t.Fatal(err)
	}
	delete(fsys, "index.html")
	req := httptest.NewRequest(http.MethodGet, "/users/42", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound || strings.Contains(w.Body.String(), "assets") {
		t.Errorf("code=%d body=%q, want 404 without a listing", w.Code, w.Body.String())
	}
}

// noSeekFS hides Seek on its files, and can fail Open with a non-NotExist error
// after construction, to exercise serveIndex's fallback and error paths.
type noSeekFS struct {
	fstest.MapFS
	openErr error
}

type noSeekFile struct{ fs.File }

func (f noSeekFS) Open(name string) (fs.File, error) {
	if f.openErr != nil && name == "index.html" {
		return nil, f.openErr
	}
	file, err := f.MapFS.Open(name)
	if err != nil {
		return nil, err
	}
	return noSeekFile{file}, nil
}

func TestHandlerServeIndexEdgeCases(t *testing.T) {
	get := func(h Handler) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/users/42", nil))
		return w
	}

	t.Run("non-seekable file is buffered", func(t *testing.T) {
		h, err := New(Cfg{FS: noSeekFS{MapFS: mapFS()}})
		if err != nil {
			t.Fatal(err)
		}
		if w := get(h); w.Code != http.StatusOK || w.Body.String() != "test index" {
			t.Errorf("code=%d body=%q, want 200 index", w.Code, w.Body.String())
		}
	})

	t.Run("index replaced by a directory", func(t *testing.T) {
		fsys := mapFS()
		h, err := New(Cfg{FS: fsys})
		if err != nil {
			t.Fatal(err)
		}
		delete(fsys, "index.html")
		fsys["index.html/x"] = &fstest.MapFile{Data: []byte("x")}
		if w := get(h); w.Code != http.StatusNotFound {
			t.Errorf("code=%d, want 404", w.Code)
		}
	})

	t.Run("open error is a 500", func(t *testing.T) {
		fsys := &noSeekFS{MapFS: mapFS()}
		h, err := New(Cfg{FS: fsys})
		if err != nil {
			t.Fatal(err)
		}
		fsys.openErr = fs.ErrPermission
		if w := get(h); w.Code != http.StatusInternalServerError {
			t.Errorf("code=%d, want 500", w.Code)
		}
	})
}

// An unclean request path (an empty segment from "//", or a "." segment) is not
// a valid fs path: os.DirFS answers fs.Stat with fs.ErrInvalid, which once
// surfaced as a client-triggerable 500. It must be a 404 on every FS.
func TestHandlerUncleanPathIsNotFound(t *testing.T) {
	dir := t.TempDir()
	for name, data := range map[string]string{"index.html": "test index", "app.js": "app js"} {
		if err := os.WriteFile(dir+"/"+name, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	fileSystems := map[string]fs.FS{
		"dirFS": os.DirFS(dir),
		"mapFS": fstest.MapFS{"index.html": {Data: []byte("test index")}, "app.js": {Data: []byte("app js")}},
	}
	for fsName, fsys := range fileSystems {
		h, err := New(Cfg{FS: fsys, PathPrefix: "/ui"})
		if err != nil {
			t.Fatal(err)
		}
		cases := []struct {
			path string
			want int
			body string
		}{
			{path: "/ui//app.js", want: http.StatusNotFound},
			{path: "/ui/a//b", want: http.StatusNotFound},
			{path: "/ui/./app.js", want: http.StatusNotFound},
			{path: "/ui/a/./b", want: http.StatusNotFound},
			{path: "/ui/app.js", want: http.StatusOK, body: "app js"},
			{path: "/ui/a/b", want: http.StatusOK, body: "test index"},
			{path: "/ui/a//", want: http.StatusOK, body: "test index"}, // directory request
			{path: "/ui", want: http.StatusOK, body: "test index"},
			{path: "/ui/", want: http.StatusOK, body: "test index"},
		}
		for _, c := range cases {
			req := httptest.NewRequest(http.MethodGet, c.path, nil)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)
			if w.Code != c.want {
				t.Errorf("%s %s: code = %d, want %d (body %q)", fsName, c.path, w.Code, c.want, w.Body.String())
				continue
			}
			if c.body != "" && w.Body.String() != c.body {
				t.Errorf("%s %s: body = %q, want %q", fsName, c.path, w.Body.String(), c.body)
			}
		}
	}
}
