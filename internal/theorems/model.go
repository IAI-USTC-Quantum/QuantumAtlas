package theorems

import (
	"encoding/json"

	"github.com/pocketbase/pocketbase/core"
)

type Theorem struct {
	ID          string `json:"id"`
	PageID      string `json:"page_id,omitempty"`
	PaperID     string `json:"paper_id,omitempty"`
	Section     string `json:"section,omitempty"`
	MDLines     []int  `json:"md_lines,omitempty"`
	StatementNL string `json:"statement_nl,omitempty"`
	Created     string `json:"created_at,omitempty"`
	Updated     string `json:"updated_at,omitempty"`
}

func FromRecord(rec *core.Record) Theorem {
	var lines []int
	_ = json.Unmarshal([]byte(rec.GetString("md_lines")), &lines)
	return Theorem{
		ID:          rec.GetString("theorem_id"),
		PageID:      rec.GetString("page_id"),
		PaperID:     rec.GetString("paper_id"),
		Section:     rec.GetString("section"),
		MDLines:     lines,
		StatementNL: rec.GetString("statement_nl"),
		Created:     DateString(rec.GetDateTime("created")),
		Updated:     DateString(rec.GetDateTime("updated")),
	}
}

func EncodeLines(lines []int) string {
	if len(lines) == 0 {
		return ""
	}
	data, _ := json.Marshal(lines)
	return string(data)
}
