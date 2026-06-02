package mineru

import (
	"errors"
	"strings"
	"testing"
)

func TestClassifyAPIErrorByCode(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		code string
		want error // sentinel; nil means unclassified
	}{
		{"daily limit -60018", "-60018", ErrDailyLimit},
		{"daily limit -60019", "-60019", ErrDailyLimit},
		{"retryable -10001", "-10001", ErrRetryable},
		{"retryable -60008 url timeout", "-60008", ErrRetryable},
		{"retryable -60022", "-60022", ErrRetryable},
		{"fatal A0202 token bad", "A0202", ErrFatal},
		{"fatal A0211 token expired", "A0211", ErrFatal},
		{"fatal -60006 too many pages", "-60006", ErrFatal},
		{"fatal -60013 no permission", "-60013", ErrFatal},
		{"unknown code -99999", "-99999", nil},
		{"unknown code random", "ZZZ", nil},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := classifyAPIError(tc.code, "test msg", 0)
			if err.Kind != tc.want {
				t.Fatalf("Kind = %v, want %v", err.Kind, tc.want)
			}
			if tc.want != nil && !errors.Is(err, tc.want) {
				t.Fatalf("errors.Is(err, %v) = false", tc.want)
			}
		})
	}
}

func TestClassifyAPIErrorByHTTPStatus(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		status int
		want   error
	}{
		{"429 throttled", 429, ErrDailyLimit},
		{"401 auth", 401, ErrFatal},
		{"403 forbidden", 403, ErrFatal},
		{"408 timeout", 408, ErrRetryable},
		{"500 internal", 500, ErrRetryable},
		{"502 bad gateway", 502, ErrRetryable},
		{"504 gateway timeout", 504, ErrRetryable},
		{"400 unclassified bad request", 400, nil},
		{"200 OK shouldn't classify", 200, nil},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := classifyAPIError("", "", tc.status)
			if err.Kind != tc.want {
				t.Fatalf("Kind = %v, want %v", err.Kind, tc.want)
			}
		})
	}
}

func TestClassifyAPIErrorCodeBeatsHTTP(t *testing.T) {
	t.Parallel()
	// A 200 with daily-limit code in the envelope must classify as DailyLimit.
	err := classifyAPIError("-60018", "today's quota gone", 200)
	if !errors.Is(err, ErrDailyLimit) {
		t.Fatalf("expected DailyLimit, got Kind=%v", err.Kind)
	}
	// A 401 with a known fatal code should still be Fatal (code path takes
	// precedence over the 401 path because fatal code matches first).
	err = classifyAPIError("-60006", "too many pages", 401)
	if !errors.Is(err, ErrFatal) {
		t.Fatalf("expected Fatal, got Kind=%v", err.Kind)
	}
}

func TestClassifyAPIErrorKeywordScan(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		msg  string
		want error
	}{
		{"english daily", "daily quota exceeded", ErrDailyLimit},
		{"english tomorrow", "please try again tomorrow", ErrDailyLimit},
		{"english 5000 limit", "you have hit the 5000/day limit", ErrDailyLimit},
		{"chinese 额度", "免费额度已用尽", ErrDailyLimit},
		{"chinese 次日", "请于次日重试", ErrDailyLimit},
		{"chinese 明天", "请明天再试", ErrDailyLimit},
		{"unrelated msg", "something went wrong", nil},
		{"empty msg", "", nil},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := classifyAPIError("", tc.msg, 0)
			if err.Kind != tc.want {
				t.Fatalf("Kind = %v, want %v (msg=%q)", err.Kind, tc.want, tc.msg)
			}
		})
	}
}

func TestClassifyAPIErrorFatalAddsHint(t *testing.T) {
	t.Parallel()
	// When server msg is empty, we substitute the canonical hint.
	err := classifyAPIError("A0202", "", 0)
	if !errors.Is(err, ErrFatal) {
		t.Fatalf("expected Fatal, got %v", err.Kind)
	}
	if !strings.Contains(err.Msg, "Token") {
		t.Fatalf("missing token hint in Msg=%q", err.Msg)
	}
	// When server msg is non-empty, hint is appended in parens for context.
	err = classifyAPIError("-60006", "your pdf is too long", 0)
	if !strings.Contains(err.Msg, "your pdf is too long") {
		t.Fatalf("original msg dropped: %q", err.Msg)
	}
	if !strings.Contains(err.Msg, "200 页") {
		t.Fatalf("hint not appended: %q", err.Msg)
	}
}

func TestErrorErrorFormatting(t *testing.T) {
	t.Parallel()
	e := &Error{HTTPStatus: 502, Code: "-10001", Msg: "service unavailable", Kind: ErrRetryable}
	want := "mineru: HTTP 502 code -10001 service unavailable"
	if got := e.Error(); got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}
	// Empty HTTPStatus/Code should be omitted from the prefix.
	e2 := &Error{Msg: "decode envelope: unparseable body"}
	got2 := e2.Error()
	if got2 != "mineru: decode envelope: unparseable body" {
		t.Fatalf("Error() = %q, want exact \"mineru: decode envelope: unparseable body\"", got2)
	}
}

func TestErrorsIsChainWithUnknown(t *testing.T) {
	t.Parallel()
	// Unclassified errors must NOT match any sentinel (no false positives
	// for the daemon's daily-limit handling — that would be catastrophic).
	err := classifyAPIError("-99999", "unknown", 0)
	for _, s := range []error{ErrDailyLimit, ErrFatal, ErrRetryable} {
		if errors.Is(err, s) {
			t.Fatalf("unclassified err falsely matched sentinel %v", s)
		}
	}
}
