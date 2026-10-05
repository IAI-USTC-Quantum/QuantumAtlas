package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestContentStorageDefaultsAndDistinctBucket(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte("paper_access:\n  enabled: true\n"), 0600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.S3BucketContent != "qatlas-content" || c.MinerUTier != "standard" {
		t.Fatalf("content=%q tier=%q", c.S3BucketContent, c.MinerUTier)
	}
	c.S3Endpoint = "storage:9000"
	c.S3BucketPDF = "pdf"
	c.S3BucketMD = "md"
	c.S3BucketImages = "images"
	c.S3BucketOpenAlex = "corpus"
	c.S3AccessKeyID = "test"
	c.S3SecretAccessKey = "test"
	if err := c.ValidateForServe(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{c.S3BucketPDF, c.S3BucketMD, c.S3BucketImages, c.S3BucketOpenAlex} {
		c.S3BucketContent = name
		if err := c.ValidateForServe(); err == nil {
			t.Fatalf("accepted shared content bucket %q", name)
		}
	}
	c.S3BucketContent = ""
	if err := c.ValidateForServe(); err != nil {
		t.Fatal("programmatic empty content bucket should use dedicated default", err)
	}
}

func TestMinerUTierIsIndependentOfLegacyModel(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte("paper_access:\n  enabled: true\n  mineru:\n    model_version: vlm\n    tier: LITE\n"), 0600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.MinerUTier != "lite" || c.MinerUModelVersion != "vlm" {
		t.Fatalf("tier=%q model=%q", c.MinerUTier, c.MinerUModelVersion)
	}
}
