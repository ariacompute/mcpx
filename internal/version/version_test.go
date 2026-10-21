package version

import (
	"strings"
	"testing"
)

func TestShortAndFull(t *testing.T) {
	originalVersion, originalCommit := Version, Commit
	defer func() { Version, Commit = originalVersion, originalCommit }()

	Version, Commit = "dev", "unknown"
	if got := Short(); got != "dev" {
		t.Errorf("Short = %q", got)
	}

	Version, Commit = "v1.2.3", "abc1234"
	if got := Short(); got != "v1.2.3@abc1234" {
		t.Errorf("Short = %q", got)
	}
	full := Full()
	if !strings.Contains(full, "v1.2.3") || !strings.Contains(full, "abc1234") || !strings.Contains(full, "mcpx") {
		t.Errorf("Full = %q", full)
	}
}
