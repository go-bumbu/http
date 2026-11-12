package spa

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
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
	// slash, so "ui", "/ui", and "/ui/" are equivalent; "" or "/" mounts at the
	// root. It matches whole path segments: under "/ui", /ui and /ui/... are
	// served, while /uix.js or /uifoo/bar get a 404.
	PathPrefix string
}

// indexFile is the SPA entrypoint served for the root and every deep link.
const indexFile = "index.html"

// New returns a Handler serving the single-page application described by cfg.
// It returns an error if the served tree (FS, after SubDir) has no index.html
// file.
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
	// Validate the entrypoint up front: fs.Sub does not check that SubDir
	// exists, so a typo, the parent of the build dir, or a missing build would
	// otherwise only surface as 404s or directory listings on every deep link.
	info, err := fs.Stat(assets, indexFile)
	if err != nil {
		return Handler{}, fmt.Errorf("spa: %s not found in FS (SubDir %q): %w", indexFile, cfg.SubDir, err)
	}
	if info.IsDir() {
		return Handler{}, fmt.Errorf("spa: %s in FS (SubDir %q) is a directory", indexFile, cfg.SubDir)
	}
	return Handler{
		fs:         assets,
		pathPrefix: normalizePrefix(cfg.PathPrefix),
	}, nil
}

// normalizePrefix canonicalises the mount prefix to a single leading slash and
// no trailing slash ("ui", "/ui", "/ui/" -> "/ui"), so a caller cannot silently
// break asset serving by omitting the leading slash. "" and "/" both become ""
// (root mount).
func normalizePrefix(p string) string {
	p = strings.Trim(p, "/")
	if p == "" {
		return ""
	}
	return "/" + p
}

// Handler is an http.Handler that serves a single-page application from an
// fs.FS, falling back to the SPA entrypoint (index.html) for unknown paths so
// client-side routing keeps working across reloads and deep links.
type Handler struct {
	fs         fs.FS
	pathPrefix string // normalised mount prefix, e.g. "/ui"; "" for the root.
}

func (h Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Requests outside the mount prefix are not the SPA's: neither an asset nor
	// a deep link, so 404 rather than serve /uix.js as x.js under "/ui".
	if !h.underPrefix(r.URL) {
		http.NotFound(w, r)
		return
	}
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

	// An unclean path (an empty segment from "//", e.g. /ui//app.js) is not a
	// valid fs path. os.DirFS rejects it with fs.ErrInvalid, which would
	// otherwise surface as a client-triggerable 500, and other FSes report it as
	// not-exist and fall back to the index; answer 404 on every FS instead.
	// ("." and ".." segments are already caught as hidden segments above.)
	if !fs.ValidPath(reqPath) {
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
		http.StripPrefix(h.pathPrefix, http.FileServerFS(h.fs)).ServeHTTP(w, withURLCopy(r))
	}
}

// underPrefix reports whether u is within the mount prefix on a whole-segment
// boundary: "/ui" and "/ui/..." are, "/uix.js" and "/uifoo/bar" are not. The
// root mount ("") holds every path. When the URL carries an escaped form,
// RawPath must also start with the prefix, mirroring http.StripPrefix, so that
// asset serving and the index fallback accept exactly the same requests.
func (h Handler) underPrefix(u *url.URL) bool {
	p := h.pathPrefix
	if p == "" {
		return true
	}
	if u.Path != p && !strings.HasPrefix(u.Path, p+"/") {
		return false
	}
	return u.RawPath == "" || strings.HasPrefix(u.RawPath, p)
}

// withURLCopy returns a shallow copy of r with its own URL, so the file server
// cannot mutate the caller's request: http.StripPrefix("") passes r through
// as-is, and the file server then rewrites r.URL.Path to add a leading slash
// (e.g. "app.js" left by an outer http.StripPrefix("/static/", ...)).
func withURLCopy(r *http.Request) *http.Request {
	r2 := new(http.Request)
	*r2 = *r
	u := *r.URL
	r2.URL = &u
	return r2
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

// serveIndex serves the SPA entrypoint with http.ServeContent rather than a
// file server on "/", so it can never fall back to a directory listing: if
// index.html has gone missing (e.g. an os.DirFS build dir mid-rebuild) the
// response is a 404. ServeContent sets the same Content-Type, Last-Modified,
// and conditional/range handling the file server did.
func (h Handler) serveIndex(w http.ResponseWriter, r *http.Request) {
	f, err := h.fs.Open(indexFile)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	defer func() { _ = f.Close() }()

	info, err := f.Stat()
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	if info.IsDir() {
		http.NotFound(w, r)
		return
	}

	content, ok := f.(io.ReadSeeker)
	if !ok {
		// os.DirFS, embed.FS, and fstest.MapFS files all seek; buffer any FS
		// whose files do not, so ServeContent can still size and range them.
		data, err := io.ReadAll(f)
		if err != nil {
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
		content = bytes.NewReader(data)
	}
	http.ServeContent(w, r, indexFile, info.ModTime(), content)
}
