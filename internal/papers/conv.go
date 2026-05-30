package papers

import "time"

// asInt coerces a Neo4j numeric (int64 from the driver, sometimes
// float64 for sums) to int. Nil / unexpected types yield 0.
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

// asString coerces a Neo4j value to string; nil / non-string yields "".
func asString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

// asTime extracts a time.Time from a Neo4j datetime value (the v6 driver
// returns time.Time). Returns nil for null / non-time values.
func asTime(v any) *time.Time {
	if t, ok := v.(time.Time); ok {
		tt := t
		return &tt
	}
	return nil
}
