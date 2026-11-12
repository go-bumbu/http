<!-- todo:guide — managed by todo; this block is rewritten on save. Docs: https://github.com/candy-tools/todo
This file is a todo list managed by "todo", a terminal TODO app:
https://github.com/candy-tools/todo

todo watches this file and reloads it automatically when it changes on disk, so
you — human or agent — can edit it directly in any editor. Keep to this format
so todo can parse what you write:

  # Heading           Headings ("#" to "######") are categories; they nest by
                      heading level.
  - [ ] Open task     A "- [ ]" line is an open task; "- [x]" marks it done.
  - [/] In progress   "- [/]" flags a task in progress, "- [>]" defers it.
  - [x] Done task     Tasks must live under a category heading.
    - [ ] Subtask     Indent by two spaces to nest a subtask under a task.
    Description text  An indented, non-checkbox line is the task's description.

Notes for editors:
- Text above the first heading (this block included) is preserved on save.
- todo rewrites the file into the canonical form above on every change, so any
  other free-form markdown placed between items is not kept.

Prefer the CLI over editing by hand: it writes this file in the exact format
above. `todo add`, `todo done|progress|defer|reopen`, `todo edit`, `todo rm`
and `todo list --json` cover the common actions — run `todo --help`.
-->

# Middleware

# auto-improve round 1

- [ ] R1-01: Recovered panic after response started makes truncated body look complete
  middleware/middleware.go:139,178-182. Scenario: handler writes '{"items":[', flushes (or not), panics; with PanicRecover:true client gets 200 + clean EOF instead of unexpected EOF. Fix: when Streaming()||Started(), log+observe then panic(http.ErrAbortHandler). Severity high, effort small, confirmed, non-breaking.
- [ ] R1-02: Request-copying middleware between Wrap and ServeMux loses the route
  middleware/route.go:41-42, middleware.go:121. Scenario: Wrap(auth(mux)) where auth does r.WithContext -> route '' -> all series 'unmatched'; also TimeoutHandler/MaxBytesHandler; Wrap(StripPrefix(inner mux)) records outer /api/. Fix: add middleware.CaptureRoute(h) placed directly around the mux (after ServeHTTP, SetRoute(r, r.Pattern) if non-empty); document as the ServeMux setup. Severity high, effort small, confirmed, non-breaking (additive).
- [ ] R1-03: spa lists directory when index.html is missing
  spa/spa.go:27-43,117-120. Scenario: SubDir points at parent of build dir or build missing -> deep links return 200 directory listing (incl. names like .env); nonexistent SubDir -> 404 everywhere since fs.Sub doesn't check. Fix: in New, fs.Stat(assets,"index.html") and return error if missing; serve index via ServeContent rather than FileServerFS on '/'. Severity medium, effort small, confirmed, non-breaking (misconfig now errors).
- [ ] R1-04: spa mutates caller's request URL
  spa/spa.go:118. Scenario: GET /ui/users/42 is logged by middleware as url=/ because caller's r.URL.Path is set to '/'. Fix: clone request and URL, set Path='/' RawPath='' like http.StripPrefix. Severity medium, effort small, confirmed, non-breaking.
- [ ] R1-05: Observer/log contract broken on panics
  middleware/middleware.go:128-149. Scenario: PanicRecover:false + any panic (incl. ErrAbortHandler) -> no access log and 0 Observe calls, contradicting 'exactly once'; with recovery on, abort path observes but never logs. Fix: always defer finalize; recover only when enabled; log+observe then re-panic. Severity medium, effort small, confirmed, non-breaking.
- [ ] R1-06: problemjson ModeMasked discards the real detail
  problemjson/problemjson.go:146-151,202-204. Scenario: docs say the reference lets a masked response be traced to its real detail in logs, but nothing logs it; middleware err-msg captures the masked body. Fix: add Cfg.OnProblem func(*http.Request, Details) called with unmasked details before masking. Severity medium, effort small, confirmed, non-breaking (additive).
- [ ] R1-07: 101 Switching Protocols and hijacked responses recorded as 200
  middleware/respwriter.go:84-90,136-146. Scenario: WriteHeader(101) takes 1xx passthrough so flushHeader writes 200 (superfluous WriteHeader, metric 200); websocket hijack records 200. Fix: exclude StatusSwitchingProtocols from passthrough; on Hijack with no latched status record 101. Severity low, effort small, confirmed, non-breaking.
- [ ] R1-08: problemjson writes unvalidated statuses
  problemjson/problemjson.go:244-245,326-329. Scenario: foreign UpstreamError with HTTPStatus()==0 makes WriteHeader panic; Write accepts 2xx. Fix: map status outside 400-599 to 500 (502 in WriteUpstream). Severity low, effort small, plausible, non-breaking.
- [ ] R1-09: outbound has no overall time budget; cancelled backoff drops upstream error
  outbound/outbound.go:252,297-306. Scenario: 3x20s timeouts + backoff ~61.5s for a user-facing call; ctx expiring during backoff returns bare ctx error and loses the upstream 503 *Error so WriteUpstream falls back to generic message. Fix: per-call Cfg.Timeout budget; return last error joined with ctx error (errors.Join). Severity low, effort small, confirmed by reading, non-breaking.
- [ ] R1-10: outbound drops keep-alive connections and can send two User-Agents
  outbound/outbound.go:330,353-356. Scenario: error bodies closed without draining -> connections lost in 5xx/429 storms; caller-supplied User-Agent header added on top of configured one. Fix: drain up to 64KB to io.Discard before close; use Set for caller headers. Severity low, effort small, confirmed by reading, non-breaking.
- [ ] R1-11: spa matches PathPrefix against partial segments
  spa/spa.go:64. Scenario: prefix /ui, GET /uix.js serves x.js. Fix: accept only path==prefix or HasPrefix(path, prefix+"/"), else 404. Severity low, effort small, confirmed, non-breaking.
- [ ] R1-12: Unused embed import in spa
  spa/spa.go:4: blank _ "embed" import is unused. Fix: remove. Severity low, effort small, confirmed, non-breaking.
- [ ] R1-13: problemjson Details construction duplicated three times
  problemjson/problemjson.go:194,214,259. Fix: extract a wr.details(r,status,slug,detail) helper; pure refactor pinned by tests. Severity low, effort small, confirmed, non-breaking.
- [ ] R1-14: Coverage gate skips outbound and problemjson
  Makefile:27 coverage target only checks ./middleware/... and ./spa/.... Fix: include all packages (outbound 84.5%, problemjson 100% today, threshold 70). Severity low, effort small, confirmed, non-breaking.
