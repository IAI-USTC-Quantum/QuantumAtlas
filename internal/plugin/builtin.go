package plugin

func BuiltinManifests() []Manifest {
	return []Manifest{
		{
			ID:         "graph",
			Name:       "QuantumAtlas Graph",
			Version:    "1.0.0",
			ABIVersion: HostABIVersion,
			Kind:       KindBuiltin,
			Contributes: Contributes{
				Capabilities: []string{"graph.query", "graph.schema", "graph.stats"},
			},
			Needs: []string{"wiki:read", "papers:read"},
		},
		{
			ID:         "rag",
			Name:       "QuantumAtlas RAG",
			Version:    "1.0.0",
			ABIVersion: HostABIVersion,
			Kind:       KindBuiltin,
			Contributes: Contributes{
				Capabilities: []string{"rag.search"},
			},
			Needs: []string{"papers:read"},
		},
		{
			ID:         "wiki",
			Name:       "QuantumAtlas Wiki",
			Version:    "1.0.0",
			ABIVersion: HostABIVersion,
			Kind:       KindBuiltin,
			Contributes: Contributes{
				Capabilities: []string{"wiki.pages", "wiki.search", "wiki.stats", "wiki.sync"},
			},
		},
		{
			ID:         "theorems",
			Name:       "QuantumAtlas Theorems",
			Version:    "1.0.0",
			ABIVersion: HostABIVersion,
			Kind:       KindBuiltin,
			Contributes: Contributes{
				Capabilities: []string{"theorems.list", "theorems.get", "theorems.source", "theorems.sync"},
			},
		},
	}
}
