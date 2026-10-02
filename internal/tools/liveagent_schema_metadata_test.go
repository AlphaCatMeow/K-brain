package tools

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"testing"
)

// Digests cover every original property description, type, bound and required key.
func TestLiveAgentOriginalSchemaMetadataDigests(t *testing.T) {
	expected := map[string]string{
		"Read":   "61a54de4b1856fd180d6ef9e49c843a0119657cb395f461c01a30f271247e863",
		"Image":  "285f1cc7db9911687630b5970e9f6198d174d44c54442a6c9daf58b0486dcf0b",
		"Write":  "0e27dddbcec1a2c038fc72d675ff69b8ccd7556209e88f2f52d7f54393a2b11a",
		"Edit":   "972b59273e1330de0363321cae9575278beea2dc3faeeff93d2daf564cc77d00",
		"Delete": "601b2ec30af18d8bd852e75c5ff31907ab2107c3ca1cbc54b9d667f950b8cd56",
		"List":   "79a2a8f0b297dd667b2d7d8df62c9e528b0f072c22564ec0c01d3654bc4a4422",
		"Glob":   "d116caa2dbe8e053df96f9ce184a33c182680d3bad606ca201ac362857809f24",
		"Grep":   "78e6185a6fe0b75c75f078974bf974145278f99a559a7f07d7f988424fac8841",
	}
	for _, tool := range LiveAgentCatalog() {
		want, ok := expected[tool.Def.Function.Name]
		if !ok {
			continue
		}
		var schema any
		if err := json.Unmarshal(tool.Def.Function.Parameters, &schema); err != nil {
			t.Fatal(err)
		}
		canonical, err := json.Marshal(schema)
		if err != nil {
			t.Fatal(err)
		}
		got := fmt.Sprintf("%x", sha256.Sum256(canonical))
		if got != want {
			t.Errorf("%s original schema metadata changed: got %s want %s; canonical=%s", tool.Def.Function.Name, got, want, canonical)
		}
	}
}
