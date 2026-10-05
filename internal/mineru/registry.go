package mineru

import "strings"

// bucketRelKey is retained for compatibility with historical object-key
// conventions. The production converter publishes complete content bundles;
// it no longer writes legacy markdown/json/images registry pointers.
func bucketRelKey(assetKey string) string {
	if i := strings.IndexByte(assetKey, '/'); i >= 0 {
		return assetKey[i+1:]
	}
	return assetKey
}
