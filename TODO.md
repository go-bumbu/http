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

- [x] R1-01: Recovered panic after response started makes truncated body look complete
  middleware/middleware.go:139,178-182. Scenario: handler writes '{"items":[', flushes (or not), panics; with PanicRecover:true client gets 200 + clean EOF instead of unexpected EOF. Fix: when Streaming()||Started(), log+observe then panic(http.ErrAbortHandler). Severity high, effort small, confirmed, non-breaking. RESULT: Fixed in 6f6c933 (test TestPanicRecover_StartedResponseAborts). Checks green: tests 232 -> 235 passing, lint 0, race ok, make verify ok, middleware cov 98.6%.
- [x] R1-02: Request-copying middleware between Wrap and ServeMux loses the route
  middleware/route.go:41-42, middleware.go:121. Scenario: Wrap(auth(mux)) where auth does r.WithContext -> route '' -> all series 'unmatched'; also TimeoutHandler/MaxBytesHandler; Wrap(StripPrefix(inner mux)) records outer /api/. Fix: add middleware.CaptureRoute(h) placed directly around the mux (after ServeHTTP, SetRoute(r, r.Pattern) if non-empty); document as the ServeMux setup. Severity high, effort small, confirmed, non-breaking (additive). RESULT: Fixed in 2fc31ce: new CaptureRoute (precedence SetRoute > CaptureRoute > r.Pattern), test TestRoute_CaptureRoute (13 subtests), README/AGENTS updated. Checks green: tests 243 -> 257 passing, lint 0, race ok, make verify ok, middleware cov 98.4%.
- [x] R1-03: spa lists directory when index.html is missing
  spa/spa.go:27-43,117-120. Scenario: SubDir points at parent of build dir or build missing -> deep links return 200 directory listing (incl. names like .env); nonexistent SubDir -> 404 everywhere since fs.Sub doesn't check. Fix: in New, fs.Stat(assets,"index.html") and return error if missing; serve index via ServeContent rather than FileServerFS on '/'. Severity medium, effort small, confirmed, non-breaking (misconfig now errors). RESULT: Fixed in 48dfb89: New stats index.html, serveIndex uses ServeContent (no listing). Tests TestNewRequiresIndex, TestHandlerNeverListsDirectory, +2. Checks green: tests 257 -> 264 passing, lint 0, race ok, make verify ok, spa cov 94.3% -> 90.3% (above 70 gate).
- [x] R1-04: spa mutates caller's request URL
  spa/spa.go:118. Scenario: GET /ui/users/42 is logged by middleware as url=/ because caller's r.URL.Path is set to '/'. Fix: clone request and URL, set Path='/' RawPath='' like http.StripPrefix. Severity medium, effort small, confirmed, non-breaking. RESULT: Deep-link case already fixed by 48dfb89; remaining root-mount asset mutation fixed in 9e496a9 (withURLCopy). Test TestHandlerDoesNotMutateRequest (11 subtests). Checks green: tests 264 -> 276 passing, lint 0, race ok, make verify ok, spa cov 91.0%.
- [x] R1-05: Observer/log contract broken on panics
  middleware/middleware.go:128-149. Scenario: PanicRecover:false + any panic (incl. ErrAbortHandler) -> no access log and 0 Observe calls, contradicting 'exactly once'; with recovery on, abort path observes but never logs. Fix: always defer finalize; recover only when enabled; log+observe then re-panic. Severity medium, effort small, confirmed, non-breaking. RESULT: Fixed in cf3dbbf (test TestCombinedMiddleware_ObservesOncePerPath, 7 paths). Checks green: tests 235 -> 243 passing, lint 0, race ok, make verify ok, middleware cov 98.0%.
- [x] R1-06: problemjson ModeMasked discards the real detail
  problemjson/problemjson.go:146-151,202-204. Scenario: docs say the reference lets a masked response be traced to its real detail in logs, but nothing logs it; middleware err-msg captures the masked body. Fix: add Cfg.OnProblem func(*http.Request, Details) called with unmasked details before masking. Severity medium, effort small, confirmed, non-breaking (additive). RESULT: Fixed in 3a00cf0: Cfg.OnProblem(r, Details) called once per write with unmasked details; README/AGENTS updated. Tests TestOnProblemReceivesUnmaskedDetails, TestOnProblemDevModeCalledOnce. Checks green: tests 276 -> 281 passing, lint 0, make verify ok, problemjson cov 100%.
- [x] R1-07: 101 Switching Protocols and hijacked responses recorded as 200
  middleware/respwriter.go:84-90,136-146. Scenario: WriteHeader(101) takes 1xx passthrough so flushHeader writes 200 (superfluous WriteHeader, metric 200); websocket hijack records 200. Fix: exclude StatusSwitchingProtocols from passthrough; on Hijack with no latched status record 101. Severity low, effort small, confirmed, non-breaking. RESULT: Fixed in ec6e456: 101 latches; Hijack with no status records 101. Tests TestStatWriter_SwitchingProtocolsLatches, _HijackRecordsSwitchingProtocols, _HijackKeepsLatchedStatus. Checks green: tests 281 -> 284 passing, lint 0, race ok, make verify ok.
- [x] R1-08: problemjson writes unvalidated statuses
  problemjson/problemjson.go:244-245,326-329. Scenario: foreign UpstreamError with HTTPStatus()==0 makes WriteHeader panic; Write accepts 2xx. Fix: map status outside 400-599 to 500 (502 in WriteUpstream). Severity low, effort small, plausible, non-breaking. RESULT: Fixed in 5acbd6e: errorStatus helper; Write -> 500, WriteUpstream -> 502 outside 400-599. Test TestStatusNormalised (15 cases). Checks green: tests 284 -> 300 passing, lint 0, make verify ok, problemjson cov 100%.
- [x] R1-09: outbound has no overall time budget; cancelled backoff drops upstream error
  outbound/outbound.go:252,297-306. Scenario: 3x20s timeouts + backoff ~61.5s for a user-facing call; ctx expiring during backoff returns bare ctx error and loses the upstream 503 *Error so WriteUpstream falls back to generic message. Fix: per-call Cfg.Timeout budget; return last error joined with ctx error (errors.Join). Severity low, effort small, confirmed by reading, non-breaking. RESULT: Fixed in caabf31 (keep upstream *Error joined with ctx error) and 20a37eb (Cfg.Timeout overall budget; KindTimeout 504 when budget expires first). Tests TestGetKeepsUpstreamErrorWhenContextEndsDuringBackoff, TestGetTimeoutCapsWholeCall, +2. Checks green: tests 300 -> 306 passing, lint 0, race ok, make verify ok, outbound cov 84.5% -> 91.4%.
- [x] R1-10: outbound drops keep-alive connections and can send two User-Agents
  outbound/outbound.go:330,353-356. Scenario: error bodies closed without draining -> connections lost in 5xx/429 storms; caller-supplied User-Agent header added on top of configured one. Fix: drain up to 64KB to io.Discard before close; use Set for caller headers. Severity low, effort small, confirmed by reading, non-breaking. RESULT: Fixed in 1c0a6fd: discardBody drains up to 64KiB; caller header names replace client defaults. Note: real bug was caller UA silently dropped (Transport sends first value only). Tests TestGetRetryReusesConnection, TestGetCallerUserAgentOverridesConfigured. Checks green: tests 306 -> 308 passing, lint 0, make verify ok, outbound cov 91.6%.
- [x] R1-11: spa matches PathPrefix against partial segments
  spa/spa.go:64. Scenario: prefix /ui, GET /uix.js serves x.js. Fix: accept only path==prefix or HasPrefix(path, prefix+"/"), else 404. Severity low, effort small, confirmed, non-breaking. RESULT: Fixed in 1b571f5: underPrefix whole-segment check at top of ServeHTTP (also 404s paths entirely outside prefix); '/' normalises to root. Test TestHandlerPathPrefixMatchesWholeSegments. Checks green: tests 308 -> 309 passing, lint 0, make verify ok, spa cov 92.2%.
- [x] R1-12: Unused embed import in spa
  spa/spa.go:4: blank _ "embed" import is unused. Fix: remove. Severity low, effort small, confirmed, non-breaking. RESULT: Fixed in 9940c3c (pure cleanup). Checks green: tests 309 -> 309 passing, lint 0, make verify ok.
- [x] R1-13: problemjson Details construction duplicated three times
  problemjson/problemjson.go:194,214,259. Fix: extract a wr.details(r,status,slug,detail) helper; pure refactor pinned by tests. Severity low, effort small, confirmed, non-breaking. RESULT: Fixed in c3cc2e2: wr.details helper used by Write/WriteValidation/WriteUpstream; pinned first by TestBodyPinnedPerPath (8 subtests). Checks green: tests 309 -> 318 passing, lint 0, make verify ok, problemjson cov 100%.
- [x] R1-14: Coverage gate skips outbound and problemjson
  Makefile:27 coverage target only checks ./middleware/... and ./spa/.... Fix: include all packages (outbound 84.5%, problemjson 100% today, threshold 70). Severity low, effort small, confirmed, non-breaking. RESULT: Fixed in b89bf66: coverage target uses go list ./... (all 5 packages gated, threshold 70). Evidence: COVERAGE_THRESHOLD=92 passed before (outbound unchecked), fails after. Checks green: tests 318 -> 318 passing, lint 0, make verify ok.

# auto-improve round 2

- [ ] R2-01: Aborted request with no response written is logged and observed as 200
  middleware/middleware.go + respwriter.go (noticed by R1-05 fixer). Scenario: handler panics (PanicRecover:false, or ErrAbortHandler) before writing anything; StatWriter default status 200 is what the access log and metric record. Fix: record 500 (or a dedicated value) for aborted requests with no committed status. Severity low, effort small, plausible, behaviour change.
- [ ] R2-02: SetRoute from outer router overrides more specific CaptureRoute of a nested ServeMux
  middleware/route.go (noticed by R1-02 fixer). Scenario: gorilla router calls SetRoute and mounts a CaptureRoute-wrapped ServeMux under it; SetRoute wins so the outer gorilla template is recorded instead of the inner mux pattern. Fix: consider innermost-wins across both sources, or document. Severity low, effort small, plausible.
- [ ] R2-03: OnProblem hook omits validation field errors and upstream reason
  problemjson/problemjson.go (noticed by R1-06 fixer). Scenario: OnProblem receives Details only, so masked WriteValidation field errors and WriteUpstream dev reason never reach logs. Fix: consider a richer hook argument (e.g. Problem with Fields/Reason), ideally alongside R1-13 Details dedupe. Severity low, effort small, plausible, possibly breaking for hook signature.
- [ ] R2-04: Limiter wait failing on budget deadline is reported as throttle, not timeout
  outbound/outbound.go (noticed by R1-09 fixer). Scenario: with Cfg.Timeout set, limiter.Wait fails early with 'would exceed context deadline' before the deadline passes; error stays a plain throttle error instead of KindTimeout. Fix: classify limiter-wait deadline failures as KindTimeout when the budget ctx has a deadline. Severity low, effort small, plausible.
- [ ] R2-05: Coverage gate only warns on packages with no test files
  Makefile coverage target (noticed by R1-14 fixer). Scenario: a new package added without tests prints a warning but the gate passes. Fix: treat [no test files] as 0% and fail (or explicitly allow-list doc-only packages). Severity low, effort small, confirmed by reading.
