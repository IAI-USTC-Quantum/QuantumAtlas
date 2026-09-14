package tests

import (
	"bytes"
	"reflect"
	"testing"
	"text/template"

	"gopkg.in/yaml.v3"
)

// Evaluate the actual GoReleaser tag templates rather than duplicating their
// conditions in a Python release gate. GoReleaser itself validates the schema
// and parses SemVer; these fixtures cover our GHCR channel selection only.
func TestGoReleaserImageTags(t *testing.T) {
	var config struct {
		Dockers []struct {
			Images []string `yaml:"images"`
			Tags   []string `yaml:"tags"`
		} `yaml:"dockers_v2"`
	}
	if err := yaml.Unmarshal(composeRead(t, composeRoot(t), ".goreleaser.yaml"), &config); err != nil {
		t.Fatal(err)
	}
	if len(config.Dockers) != 1 || !reflect.DeepEqual(config.Dockers[0].Images, []string{"ghcr.io/iai-ustc-quantum/qatlasd"}) {
		t.Fatalf("unexpected image configuration: %+v", config.Dockers)
	}
	for _, tc := range []struct {
		name       string
		Tag        string
		Version    string
		Prerelease string
		IsSnapshot bool
		want       []string
	}{
		{name: "stable", Tag: "v1.2.3", Version: "1.2.3", want: []string{"v1.2.3", "1.2.3", "latest"}},
		{name: "rc", Tag: "v1.2.3-rc.1", Version: "1.2.3-rc.1", Prerelease: "rc.1", want: []string{"v1.2.3-rc.1", "1.2.3-rc.1"}},
		{name: "custom prerelease", Tag: "v1.2.3-preview.2", Version: "1.2.3-preview.2", Prerelease: "preview.2", want: []string{"v1.2.3-preview.2", "1.2.3-preview.2"}},
		{name: "snapshot", Tag: "v1.2.3", Version: "1.2.4-SNAPSHOT-fixture", IsSnapshot: true, want: []string{"v1.2.3", "1.2.4-SNAPSHOT-fixture"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got []string
			for _, raw := range config.Dockers[0].Tags {
				tpl, err := template.New("image tag").Option("missingkey=error").Parse(raw)
				if err != nil {
					t.Fatal(err)
				}
				var out bytes.Buffer
				if err := tpl.Execute(&out, tc); err != nil {
					t.Fatal(err)
				}
				// dockers_v2 discards empty rendered tags.
				if out.Len() > 0 {
					got = append(got, out.String())
				}
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("image tags = %v, want %v", got, tc.want)
			}
		})
	}
}
