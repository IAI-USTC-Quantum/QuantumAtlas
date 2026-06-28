package plugin

func BuiltinManifests() []Manifest {
	return []Manifest{
		{
			ID:         "graph",
			Name:       "QuantumAtlas Graph",
			Version:    "1.0.0",
			ABIVersion: HostABIVersion,
			Transport:  TransportInProcessGo,
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
			Transport:  TransportInProcessGo,
			Contributes: Contributes{
				Capabilities: []string{"rag.search"},
			},
			Needs: []string{"papers:read"},
		},
	}
}
