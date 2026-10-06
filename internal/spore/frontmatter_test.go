package spore

import (
	"bytes"
	"strings"
	"testing"
)

func TestFrontmatterStringPreservesSignedYAML(t *testing.T) {
	id, priv, resolve := signingIdentity(t)
	signed, err := Sign([]byte(v2TestSource+"\n# Authored body\n"), priv, id.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, scalar := range []string{"'unreviewed'", `"unreviewed" # pending`, "unreviewed # pending", "|-\n  unreviewed", ">-\n  unreviewed"} {
		t.Run(scalar, func(t *testing.T) {
			source := bytes.Replace(signed, []byte("status: unreviewed"), []byte("'status': "+scalar), 1)
			value, ok := FrontmatterString(source, "status")
			if !ok || value != "unreviewed" {
				t.Fatalf("%q %t", value, ok)
			}
			updated, err := SetFrontmatterString(source, "status", "accepted")
			if err != nil {
				t.Fatal(err)
			}
			if value, _ := FrontmatterString(updated, "status"); value != "accepted" {
				t.Fatal(value)
			}
			if strings.Contains(scalar, "# pending") && !bytes.Contains(updated, []byte("# pending")) {
				t.Fatal("lost inline comment")
			}
			if err := Verify(updated, resolve); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestFrontmatterFlowAndCRLF(t *testing.T) {
	for _, source := range []string{
		"---\nid: spore.example\ninitial: &state unreviewed\nstatus: *state\nnested: {status: keep}\n---\n# Body\n",
		"---\n{id: 'spore.example', status: 'unreviewed', nested: {status: keep}}\n---\n# Body\n",
		"---\r\nid: \"spore.example\" # id\r\nstatus: unreviewed # pending\r\nnested: {status: keep}\r\n---\r\n# Body\r\n",
	} {
		id, ok := FrontmatterString([]byte(source), "id")
		if !ok || id != "spore.example" {
			t.Fatalf("%q %t", id, ok)
		}
		updated, err := SetFrontmatterString([]byte(source), "status", "accepted")
		if err != nil {
			t.Fatal(err)
		}
		if status, _ := FrontmatterString(updated, "status"); status != "accepted" {
			t.Fatalf("%q", updated)
		}
		if !bytes.Contains(updated, []byte("nested: {status: keep}")) {
			t.Fatal("edited nested status")
		}
	}
}
