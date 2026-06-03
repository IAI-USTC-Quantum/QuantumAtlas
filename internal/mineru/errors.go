package mineru

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// MinerU returns its application-level status as `code` in the envelope.
// We classify each code into one of three buckets:
//
//   - Retryable: a transient backend hiccup. Try the *same* request again
//     after a short pause; typically the call succeeds within a few retries.
//   - DailyLimit: the account has consumed today's free quota. No amount of
//     retrying within the day will help — the daemon should sleep until
//     next 00:01 local time, single-shot runs should bail out.
//   - Fatal: something only a human can fix (bad token, bad PDF, too many
//     pages). Retrying is pointless; abort the affected paper.
//
// Codes not in any map fall through as a generic *Error with Kind=nil so
// callers can still inspect Msg/Code without being mis-routed.
var (
	// ErrRetryable is the sentinel for transient MinerU failures.
	// Use `errors.Is(err, mineru.ErrRetryable)` to detect.
	ErrRetryable = errors.New("mineru: retryable transient error")
	// ErrDailyLimit is the sentinel for "today's quota exhausted" responses.
	ErrDailyLimit = errors.New("mineru: daily submission limit reached")
	// ErrFatal is the sentinel for non-retryable failures (bad token, bad PDF, etc.).
	ErrFatal = errors.New("mineru: fatal non-retryable error")
)

// retryableErrorCodes are MinerU `code` values that indicate a transient
// problem. Source: MinerU error code table (mineru.net/apiManage/docs).
var retryableErrorCodes = map[int]struct{}{
	-10001: {}, // 服务异常 / service unavailable
	-60001: {}, // 生成上传 URL 失败
	-60007: {}, // 模型服务暂时不可用
	-60008: {}, // 文件读取超时 (URL fetch timed out — often network blip)
	-60009: {}, // 任务提交队列已满
	-60010: {}, // 解析失败 (generic transient parse fail)
	-60020: {}, // 文件拆分失败
	-60021: {}, // 读取文件页数失败
	-60022: {}, // 网页读取失败
}

// dailyLimitErrorCodes signal "today's quota is gone".
var dailyLimitErrorCodes = map[int]struct{}{
	-60018: {}, // 每日解析任务数量已达上限
	-60019: {}, // html 文件解析额度不足
}

// fatalErrorCodes are non-retryable failures with human-readable hints
// (extra context to surface in error messages). Sourced from MinerU docs.
var fatalErrorCodes = map[string]string{
	"A0202":  "Token 错误，请检查 Token 是否正确，是否误带 Bearer 前缀，或更换新 Token",
	"A0211":  "Token 过期，请更换新 Token",
	"-500":   "传参错误，请检查参数类型和 Content-Type",
	"-10002": "请求参数错误，请检查请求参数格式",
	"-60002": "文件格式识别失败，请确认文件名和链接后缀正确，且类型受支持",
	"-60003": "文件读取失败，请检查文件是否损坏",
	"-60004": "文件为空，请上传有效文件",
	"-60005": "文件大小超出限制，最大支持 200MB",
	"-60006": "文件页数超过限制（最多 200 页），请拆分文件后重试",
	"-60011": "获取有效文件失败，请确认文件已上传",
	"-60012": "找不到任务，请确认 batch_id/task_id 有效",
	"-60013": "没有权限访问该任务，只能访问自己提交的任务",
	"-60014": "运行中的任务暂不支持删除",
	"-60015": "文件转换失败，可以尝试手动转为 PDF 后再上传",
	"-60016": "文件转换为指定格式失败，可以尝试其他格式导出或重试",
	"-60017": "重试次数达到上限，当前任务不再继续重试",
}

// dailyLimitKeywords matches free-form Chinese / English messages that
// hint at quota exhaustion when the structured `code` field is absent
// (some MinerU responses are looser, especially 4xx with a string body).
// Lower-cased before comparison.
//
// IMPORTANT: keep this list narrow — but `上限` (Chinese "upper limit")
// must stay because it's the canonical word in real daily-quota messages
// ("每日解析任务数量已达上限"). Words like bare "limit" / "exceed" were
// removed because they false-trigger on per-paper fatal phrases such as
// "number of pages exceeds limit (200 pages)" and shut the watch daemon
// down for ~20 hours over a single oversize PDF — fatal-first
// classification (via fatalFreeTextPatterns) catches those before they
// reach this list.
var dailyLimitKeywords = []string{
	"quota", "5000", "restricted",
	"tomorrow", "next day", "daily", "today",
	"额度", "上限", "次日", "明日", "明天",
}

// fatalFreeTextPatterns are substrings that unambiguously indicate a
// per-paper fatal failure (MinerU -60005 / -60006: file size > 200 MB or
// page count > 200) when the batch-result envelope omits the numeric
// code. Match BEFORE the daily-limit keyword scan.
//
// Sources: MinerU error-code table + observed batch-result phrasing
// ("number of pages exceeds limit (200 pages), please split the file
// and try again"). Keep in sync with Python
// qatlas/parser/mineru_client.py::_FATAL_FREE_TEXT_PATTERNS.
var fatalFreeTextPatterns = []string{
	"number of pages exceeds",
	"exceeds limit (200 pages)",
	"exceeds the page limit",
	"exceeds page limit",
	"split the file",
	"页数超过",
	"页数超出",
	"页数已超",
	"file size exceeds",
	"exceeds 200mb",
	"exceeds 200 mb",
	"文件大小超出",
	"文件大小超过",
	"文件过大",
}

// Error is the envelope-decoded MinerU error type.
//
// It embeds a `Kind` sentinel so callers can do
// `errors.Is(err, mineru.ErrDailyLimit)` without sniffing the Msg string.
// `Kind == nil` means "unknown / unclassified" — fall through to generic
// retry-or-abort policy at the caller.
type Error struct {
	// Msg is the human-readable message (server's `msg` or our own wrap).
	Msg string
	// Code is the MinerU application-level code if the response had one
	// (e.g. "-60018"), or empty when the failure was at the HTTP layer
	// (5xx without a JSON envelope, etc.).
	Code string
	// HTTPStatus is the HTTP status code if non-zero (used for 4xx / 5xx
	// classification when MinerU didn't include a code).
	HTTPStatus int
	// Kind is one of ErrRetryable / ErrDailyLimit / ErrFatal, or nil for
	// unclassified errors that callers should treat conservatively.
	Kind error
}

func (e *Error) Error() string {
	parts := []string{"mineru:"}
	if e.HTTPStatus != 0 {
		parts = append(parts, fmt.Sprintf("HTTP %d", e.HTTPStatus))
	}
	if e.Code != "" {
		parts = append(parts, "code "+e.Code)
	}
	if e.Msg != "" {
		parts = append(parts, e.Msg)
	}
	return strings.Join(parts, " ")
}

// Unwrap returns the Kind sentinel so errors.Is matches via the standard
// chain-walking algorithm.
func (e *Error) Unwrap() error { return e.Kind }

// Is reports whether the target matches this error's Kind sentinel.
// This makes `errors.Is(err, mineru.ErrDailyLimit)` work directly even
// when callers wrap us further.
func (e *Error) Is(target error) bool {
	if e.Kind == nil {
		return false
	}
	return target == e.Kind
}

// classifyAPIError builds an *Error from a parsed MinerU response. The
// envelope arguments are the response code (as decoded from JSON
// `code`, possibly numeric or string), the server message, and the HTTP
// status. Pass httpStatus=0 if not applicable (or 2xx).
//
// Used by both single-task and batch result paths so the same code →
// Kind mapping applies everywhere.
func classifyAPIError(code, msg string, httpStatus int) *Error {
	e := &Error{Code: code, Msg: msg, HTTPStatus: httpStatus}

	// HTTP-layer signals first: 429 is unambiguous "throttled".
	if httpStatus == 429 {
		e.Kind = ErrDailyLimit
		if e.Msg == "" {
			e.Msg = "HTTP 429: throttled"
		}
		return e
	}

	// Numeric application code wins next.
	if code != "" {
		if n, err := strconv.Atoi(code); err == nil {
			if _, ok := dailyLimitErrorCodes[n]; ok {
				e.Kind = ErrDailyLimit
				return e
			}
			if _, ok := retryableErrorCodes[n]; ok {
				e.Kind = ErrRetryable
				return e
			}
		}
		if hint, ok := fatalErrorCodes[code]; ok {
			e.Kind = ErrFatal
			if e.Msg == "" {
				e.Msg = hint
			} else {
				e.Msg = e.Msg + " (" + hint + ")"
			}
			return e
		}
	}

	// Token-level HTTP failures are fatal regardless of body content.
	if httpStatus == 401 || httpStatus == 403 {
		e.Kind = ErrFatal
		if e.Msg == "" {
			e.Msg = fmt.Sprintf("HTTP %d: authentication failed", httpStatus)
		}
		return e
	}

	// Free-text classifier: per-paper fatal first, then daily-limit hints.
	if msg != "" {
		low := strings.ToLower(msg)
		if hasFatalFreeText(low) {
			e.Kind = ErrFatal
			return e
		}
		if hasDailyLimitKeywordLower(low) {
			e.Kind = ErrDailyLimit
			return e
		}
	}

	// HTTP 5xx / 408 are retryable transport issues.
	if httpStatus >= 500 || httpStatus == 408 {
		e.Kind = ErrRetryable
		if e.Msg == "" {
			e.Msg = fmt.Sprintf("HTTP %d: transient transport error", httpStatus)
		}
		return e
	}

	// Unclassified: leave Kind=nil. Caller can decide to retry conservatively
	// (e.g. count toward a max-consecutive-failures budget) or surface to the user.
	return e
}

// hasDailyLimitKeyword scans a free-form message for any of the documented
// daily-limit hint words (Chinese + English).
func hasDailyLimitKeyword(msg string) bool {
	if msg == "" {
		return false
	}
	return hasDailyLimitKeywordLower(strings.ToLower(msg))
}

func hasDailyLimitKeywordLower(low string) bool {
	for _, kw := range dailyLimitKeywords {
		if strings.Contains(low, kw) {
			return true
		}
	}
	return false
}

// hasFatalFreeText scans a lower-cased free-form message for unambiguous
// per-paper fatal phrases (oversized / overlong PDFs). Used to prevent a
// single bad PDF from being misread as daily-quota exhaustion.
func hasFatalFreeText(low string) bool {
	for _, p := range fatalFreeTextPatterns {
		if strings.Contains(low, p) {
			return true
		}
	}
	return false
}
