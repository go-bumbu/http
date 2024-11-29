package handlers

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

	"github.com/google/go-cmp/cmp"
)

//go:embed testdata/ui/*
var embedFs embed.FS

func TestSpaHandler(t *testing.T) {
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

							handler, err := NewSpaHAndler(
								fileSystem,
								tc.subDir,
								pathPrefix,
							)
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
								defer resp.Body.Close()

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
