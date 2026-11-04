package upstream

import "testing"

func TestExpandRejectsDecodedTraversal(t *testing.T) {
	template, err := CompileTemplate("/{name}")
	if err != nil {
		t.Fatalf("CompileTemplate: %v", err)
	}
	// The escaped form stays a single segment, but the decoded form starts with
	// "//", which would turn the path into an authority.
	if _, err := template.Expand(map[string]string{"name": "//"}); err == nil {
		t.Fatal("a value that decodes to a double slash must not expand")
	}
}

func TestExpandKeepsEscapedAndDecodedFormsInSync(t *testing.T) {
	template, err := CompileTemplate("/api/{owner}/{repo}")
	if err != nil {
		t.Fatalf("CompileTemplate: %v", err)
	}
	path, err := template.Expand(map[string]string{"owner": " acme ", "repo": "a/b"})
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if path != "/api/acme/a%2Fb" {
		t.Fatalf("path = %q, want the trimmed owner and an escaped slash", path)
	}
}
