package handlers

import (
	_ "embed"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"strings"
)

// SpaHandler is a http handler capable of serving SPAs from a fs.FS ( tested are os.DirFS and embed.FS)
// configuration:
// FsSubDir allows to keep more files that only the SPA in an FS and serve the data from a sub dir
// Notice that the dir path needs to be relative and cannot be ./ or ../; empty string will be replaced by "."

func NewSpaHAndler(inputFs fs.FS, fsSubDir, pathPrefix string) (SpaHandler, error) {
	if inputFs == nil {
		return SpaHandler{}, fmt.Errorf("fs cannot be nil")
	}
	if fsSubDir != "" {
		newFs, err := fs.Sub(inputFs, fsSubDir)
		if err != nil {
			return SpaHandler{}, err
		}
		inputFs = newFs
	}

	s := SpaHandler{
		fs:         inputFs,
		pathPrefix: pathPrefix,
	}
	return s, nil
}

type SpaHandler struct {
	fs         fs.FS
	pathPrefix string // if the SPA is served with a path prefix, e.g. "ui" in  http://my-app.com/ui/
}

func (h SpaHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {

	reqPath := strings.TrimPrefix(r.URL.Path, h.pathPrefix)
	if reqPath == "" || reqPath == "/" {
		reqPath = "./"
	}

	reqPath = strings.TrimPrefix(reqPath, "/")

	f, err := h.fs.Open(reqPath)
	if os.IsNotExist(err) || strings.HasSuffix(reqPath, "/") {
		// file does not exist or path is a directory, serve index.html
		r.URL.Path = "/"
		http.FileServerFS(h.fs).ServeHTTP(w, r)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	fstat, err := f.Stat()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
	if fstat.IsDir() {
		// path is an existing dir, in this case we also serve the index file
		r.URL.Path = "/"
		http.FileServerFS(h.fs).ServeHTTP(w, r)
		return
	}
	http.StripPrefix(h.pathPrefix, http.FileServerFS(h.fs)).ServeHTTP(w, r)
}
