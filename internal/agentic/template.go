package agentic

import (
	"bytes"
	"text/template"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/search"
)

// defaultPromptTemplate is the built-in prompt for the claude CLI. It
// points claude at the raw fan-out hits (resultsFile, relative to the
// run/ cwd) and pins the refinement rules; search.agentic.local.
// prompt_template overrides it. Placeholders: .Query, .MaxResults,
// .ResultsFile.
const defaultPromptTemplate = `You are refining academic paper search results for the QuantumAtlas paper registry.

Query: {{.Query}}
Maximum results to return: {{.MaxResults}}

The raw multi-source search hits are in the file {{.ResultsFile}} (a JSON array of objects with title, authors, year, doi, arxiv_id, source, score). Read that file, then:

1. Drop hits that are clearly irrelevant to the query.
2. Deduplicate entries referring to the same paper, keeping the most complete record.
3. Rank the remaining hits by relevance to the query.

Produce the structured output only: a concise academic conclusion (2-4 sentences) summarizing what the refined literature says about the query — empty string when nothing is relevant — and the refined hit list with at most {{.MaxResults}} entries. Preserve doi / arxiv_id / url / venue / citations exactly as given; NEVER invent identifiers, links or citation counts.
`

// promptData feeds the prompt template.
type promptData struct {
	Query       string
	MaxResults  int
	ResultsFile string
}

// renderPrompt renders the configured (or embedded) template for entry.
// resultsFile is the hits file's path relative to the claude cwd.
func renderPrompt(tmpl *template.Template, entry search.SearchEntry, resultsFile string) (string, error) {
	query := entry.Text
	if query == "" {
		query = entry.Title
	}
	maxResults := entry.MaxResults
	if maxResults <= 0 {
		maxResults = search.DefaultMaxResults
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, promptData{
		Query:       query,
		MaxResults:  maxResults,
		ResultsFile: resultsFile,
	}); err != nil {
		return "", err
	}
	return buf.String(), nil
}
