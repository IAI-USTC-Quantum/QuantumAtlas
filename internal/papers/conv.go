package papers

import (
	"fmt"
	"time"
)

// asInt coerces a numeric value to int. Nil / unexpected types yield 0.
func asInt(v any) int {
	switch n := v.(type) {
	case int64:
		return int(n)
	case int:
		return n
	case float64:
		return int(n)
	default:
		return 0
	}
}

// asString coerces a value to string; nil / non-string yields "".
func asString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

// asTime extracts a time.Time value. Returns nil for null / non-time values.
func asTime(v any) *time.Time {
	if t, ok := v.(time.Time); ok {
		tt := t
		return &tt
	}
	return nil
}

func catalogUnavailable(op string, err error) error {
	if err == nil {
		return ErrCatalogUnavailable
	}
	return fmt.Errorf("%s: %w (%v)", op, ErrCatalogUnavailable, err)
}
