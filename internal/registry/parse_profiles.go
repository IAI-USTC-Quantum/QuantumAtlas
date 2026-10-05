package registry

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/paperbundle"
)

const (
	DocVortexMiddleSchema        = "docvortex.middle"
	DocVortexMiddleSchemaVersion = "2.0"
	NativeMiddleSchema           = "mineru.native.middle"
	NativeMiddleSchemaVersion    = "pdf_info-v1"
)

var ErrUnsupportedParseProfile = errors.New("registry: unsupported parse profile")

// SupportedParseProfile is the catalog/read/publication profile predicate.
// Producer release (_version_name) is provenance, NOT a schema version.
func SupportedParseProfile(schema, version string) bool {
	return schema == DocVortexMiddleSchema && version == DocVortexMiddleSchemaVersion ||
		schema == NativeMiddleSchema && version == NativeMiddleSchemaVersion
}

func requireParseProfile(schema, version string) error {
	if !SupportedParseProfile(schema, version) {
		return fmt.Errorf("%w: %s/%s: %w", ErrUnsupportedParseProfile, schema, version, paperbundle.ErrIntegrity)
	}
	return nil
}

// Check only the original envelope/profile agreement here. The producer/parser
// owns full block validation and native in-memory adaptation; registry never
// serializes, renames, adds schema fields to, or normalizes producer artifacts.
func verifyMiddleProfile(data []byte, schema, version string) error {
	if err := requireParseProfile(schema, version); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil || fields == nil {
		return fmt.Errorf("%w: Middle JSON is not an object", paperbundle.ErrIntegrity)
	}
	if schema == NativeMiddleSchema {
		for _, name := range []string{"schema", "schema_version", "pages", "blocks", "metadata", "producer", "page_index_map"} {
			if _, present := fields[name]; present {
				return fmt.Errorf("%w: mixed native Middle envelope field %s", paperbundle.ErrIntegrity, name)
			}
		}
		if raw := bytes.TrimSpace(fields["pdf_info"]); len(raw) == 0 || raw[0] != '[' {
			return fmt.Errorf("%w: native Middle must retain pdf_info array", paperbundle.ErrIntegrity)
		}
		return nil
	}
	var actualSchema, actualVersion string
	if err := json.Unmarshal(fields["schema"], &actualSchema); err != nil {
		return paperbundle.ErrIntegrity
	}
	if err := json.Unmarshal(fields["schema_version"], &actualVersion); err != nil {
		return paperbundle.ErrIntegrity
	}
	if actualSchema != schema || actualVersion != version {
		return fmt.Errorf("%w: Middle profile disagrees with publication metadata", paperbundle.ErrIntegrity)
	}
	if raw := bytes.TrimSpace(fields["pdf_info"]); len(raw) > 0 && raw[0] == '[' {
		return fmt.Errorf("%w: native pdf_info array mislabeled as DocVortex", paperbundle.ErrIntegrity)
	}
	return nil
}
