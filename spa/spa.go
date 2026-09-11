package spa

import (
	_ "embed"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"strings"
)

// Cfg configures a Handler.
type Cfg struct {
	// FS holds the SPA assets; os.DirFS and embed.FS are both tested. Required.
	FS fs.FS
	// SubDir serves the SPA from a sub-directory of FS, so FS may hold more than
	// just the SPA. It must be a relative path (not "./" or "../"); "" serves
	// from the FS root.
	SubDir string
	// PathPrefix is the URL path the SPA is mounted under, e.g. "/ui" for
	// http://host/ui/. It is normalised to a single leading slash and no trailing
	// slash, so "ui", "/ui", and "/ui/" are equivalent; "" mounts at the root.
	PathPrefix string
}

// New returns a Handler serving the single-page application described by cfg.
func New(cfg Cfg) (Handler, error) {
	if cfg.FS == nil {
		return Handler{}, fmt.Errorf("spa: Cfg.FS cannot be nil")
	}
	assets := cfg.FS
	if cfg.SubDir != "" {
		sub, err := fs.Sub(assets, cfg.SubDir)
		if err != nil {
			return Handler{}, err
		}
		assets = sub
	}
	return Handler{
		fs:         assets,
		pathPrefix: normalizePrefix(cfg.PathPrefix),
	}, nil
}

// normalizePrefix canonicalises the mount prefix to a single leading slash and
// no trailing slash ("ui", "/ui", "/ui/" -> "/ui"), so a caller cannot silently
// break asset serving by omitting the leading slash. "" stays "" (root mount).
func normalizePrefix(p string) string {
	if p == "" {
		return ""
	}
	return "/" + strings.Trim(p, "/")
}

// Handler is an http.Handler that serves a single-page application from an
// fs.FS, falling back to the SPA entrypoint (index.html) for unknown paths so
// client-side routing keeps working across reloads and deep links.
type Handler struct {
	fs         fs.FS
	pathPrefix string // normalised mount prefix, e.g. "/ui"; "" for the root.
}

func (h Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	reqPath := strings.TrimPrefix(r.URL.Path, h.pathPrefix)
	if reqPath == "" || reqPath == "/" {
		reqPath = "./"
	}
	reqPath = strings.TrimPrefix(reqPath, "/")

	// A trailing slash is a directory request: serve the SPA entrypoint.
	if strings.HasSuffix(reqPath, "/") {
		h.serveIndex(w, r)
		return
	}

	// Never serve dotfiles (.env, .git/config, ...). http.FileServerFS serves
	// them, so over an os.DirFS build directory a request for /.env or
	// /.git/config would return it verbatim; a 404 keeps even the file's
	// existence hidden. The standard .well-known/ tree (RFC 8615) is the one
	// exception. A plain //go:embed already drops dotfiles from the binary, so
	// this mainly guards os.DirFS and an all:-embedded FS.
	if hasHiddenSegment(reqPath) {
		http.NotFound(w, r)
		return
	}

	// fs.Stat opens and closes internally (via the StatFS fast path that both
	// os.DirFS and embed.FS implement), so it leaks no descriptor; the file
	// itself is opened exactly once, by http.FileServerFS below.
	info, err := fs.Stat(h.fs, reqPath)
	switch {
	case errors.Is(err, fs.ErrNotExist) || (err == nil && info.IsDir()):
		// Unknown path or a directory: hand it to the SPA entrypoint.
		h.serveIndex(w, r)
	case err != nil:
		http.Error(w, "internal server error", http.StatusInternalServerError)
	default:
		http.StripPrefix(h.pathPrefix, http.FileServerFS(h.fs)).ServeHTTP(w, r)
	}
}

// hasHiddenSegment reports whether any element of the slash-separated path
// begins with a dot, other than the standard ".well-known" directory. It keeps
// dotfiles (.env, .git/...) from being served while still allowing .well-known/
// (RFC 8615: security.txt, ACME challenges, ...).
func hasHiddenSegment(p string) bool {
	for _, seg := range strings.Split(p, "/") {
		if strings.HasPrefix(seg, ".") && seg != ".well-known" {
			return true
		}
	}
	return false
}

// serveIndex serves the SPA entrypoint by rewriting the request to the FS root
// and delegating to the file server.
func (h Handler) serveIndex(w http.ResponseWriter, r *http.Request) {
	r.URL.Path = "/"
	http.FileServerFS(h.fs).ServeHTTP(w, r)
}
