package tailnet

import (
	"os"
	"strings"
	"testing"
)

func TestManagerDoesNotDependOnDiagnostics(t *testing.T) {
	source, err := os.ReadFile("manager.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"internal/diagnostics", "diagnostics."} {
		if strings.Contains(string(source), forbidden) {
			t.Fatalf("manager.go retains diagnostics dependency %q", forbidden)
		}
	}
}
