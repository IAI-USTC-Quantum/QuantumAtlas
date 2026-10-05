package routes

import (
	"archive/zip"
	"bytes"
	"sort"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/paperbundle"
)

// Derived archives retain each verified original relative name exactly. No
// basename flattening, legacy images/ stripping, or '..' substring filtering.
func buildOriginalMembersZIP(members map[string][]byte) ([]byte, error) {
	names := make([]string, 0, len(members))
	for name := range members {
		if err := paperbundle.ValidateMemberPath(name); err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	sort.Strings(names)
	var body bytes.Buffer
	writer := zip.NewWriter(&body)
	for _, name := range names {
		entry, err := writer.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate})
		if err != nil {
			return nil, err
		}
		if _, err = entry.Write(members[name]); err != nil {
			return nil, err
		}
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	return body.Bytes(), nil
}
