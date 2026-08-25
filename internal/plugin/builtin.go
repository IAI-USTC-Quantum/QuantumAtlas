package plugin

// BuiltinManifests lists the manifests for first-party (kind=builtin)
// plugins compiled into qatlasd. Currently empty — the Lean-content
// builtin moved out of the main repo to live as an external plugin;
// new builtins land by appending to this slice.
func BuiltinManifests() []Manifest {
	return []Manifest{}
}
