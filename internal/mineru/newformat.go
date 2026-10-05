package mineru

import "encoding/json"

// NewFormatResult is the compatibility view of a complete archive. Members
// retains all original names and bytes, including unknown producer extensions.
type NewFormatResult struct {
	MiddleJSON        []byte
	Markdown          []byte
	StructuredContent []byte
	Images            map[string][]byte
	Tier              string
	Members           map[string][]byte
	MiddlePath        string
	MarkdownPath      string
}

const middleJSONBase = "middle_json.json"
const metadataJSONBase = "metadata.json"

// HasMiddleJSON validates the WHOLE archive before recognizing an artifact.
func HasMiddleJSON(raw []byte) bool {
	files, err := extractMembers(raw)
	if err != nil {
		return false
	}
	name, err := selectArtifact(files, middleJSONBase, "layout.json")
	return err == nil && name != ""
}

// ExtractNewFormat keeps the historical optional-markdown surface. Production
// parsing uses ExtractPackage, which also validates the Middle JSON schema.
func ExtractNewFormat(raw []byte) (NewFormatResult, error) {
	files, err := extractMembers(raw)
	if err != nil {
		return NewFormatResult{}, err
	}
	r, err := resultFromMembers(files)
	if err != nil {
		return NewFormatResult{}, err
	}
	if r.MiddlePath == "" {
		return NewFormatResult{}, archiveError("result zip did not contain supported Middle JSON")
	}
	out := NewFormatResult{MiddleJSON: r.MiddleJSON, Markdown: r.Markdown, StructuredContent: r.StructuredContent, Images: r.Images, Members: files, MiddlePath: r.MiddlePath, MarkdownPath: r.MarkdownPath}
	metaPath, err := selectArtifact(files, metadataJSONBase)
	if err != nil {
		return NewFormatResult{}, err
	}
	if metaPath != "" {
		var meta struct {
			Tier string `json:"tier"`
		}
		if json.Unmarshal(files[metaPath], &meta) == nil {
			out.Tier = meta.Tier
		}
	}
	return out, nil
}
