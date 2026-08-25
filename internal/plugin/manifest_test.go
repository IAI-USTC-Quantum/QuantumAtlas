package plugin

import "testing"

func TestManifestValidateKindTransport(t *testing.T) {
	base := func() Manifest {
		return Manifest{ID: "p", ABIVersion: HostABIVersion}
	}
	cases := []struct {
		name    string
		mutate  func(*Manifest)
		wantErr bool
	}{
		{"builtin ok", func(m *Manifest) { m.Kind = KindBuiltin }, false},
		{"builtin with transport rejected", func(m *Manifest) { m.Kind = KindBuiltin; m.Transport = TransportSocket }, true},
		{"builtin with spawn rejected", func(m *Manifest) { m.Kind = KindBuiltin; m.Spawn = &SpawnConfig{Command: "x"} }, true},
		{"external socket ok", func(m *Manifest) { m.Kind = KindExternal; m.Transport = TransportSocket }, false},
		{"external socket with spawn rejected", func(m *Manifest) {
			m.Kind = KindExternal
			m.Transport = TransportSocket
			m.Spawn = &SpawnConfig{Command: "x"}
		}, true},
		{"external stdio without command rejected", func(m *Manifest) { m.Kind = KindExternal; m.Transport = TransportStdio }, true},
		{"external stdio with command ok", func(m *Manifest) {
			m.Kind = KindExternal
			m.Transport = TransportStdio
			m.Spawn = &SpawnConfig{Command: "lean-plugin"}
		}, false},
		{"external unknown transport rejected", func(m *Manifest) { m.Kind = KindExternal; m.Transport = "carrier-pigeon" }, true},
		{"unknown kind rejected", func(m *Manifest) { m.Kind = "in-process-go" }, true},
		{"empty kind rejected", func(m *Manifest) {}, true},
		{"missing abi rejected", func(m *Manifest) { m.Kind = KindBuiltin; m.ABIVersion = "" }, true},
		{"bad id rejected", func(m *Manifest) { m.Kind = KindBuiltin; m.ID = "Bad Id!" }, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := base()
			tc.mutate(&m)
			err := m.Validate()
			if tc.wantErr && err == nil {
				t.Fatalf("Validate() = nil, want error")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("Validate() = %v, want nil", err)
			}
		})
	}
}

func TestBuiltinManifestsValid(t *testing.T) {
	// Currently no builtins ship in-repo; keep the validation loop so any
	// future builtin manifest is still checked for validity + kind.
	for _, m := range BuiltinManifests() {
		if err := m.Validate(); err != nil {
			t.Fatalf("builtin %q invalid: %v", m.ID, err)
		}
		if m.Kind != KindBuiltin {
			t.Fatalf("builtin %q kind = %q, want builtin", m.ID, m.Kind)
		}
	}
}
