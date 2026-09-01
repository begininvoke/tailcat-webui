package docs

import (
	"os"
	"strings"
	"testing"
)

func TestCIVerifiesCommittedWebdistBeforeBuilding(t *testing.T) {
	workflowBytes, err := os.ReadFile("../.github/workflows/ci.yml")
	if err != nil {
		t.Fatal(err)
	}
	workflow := string(workflowBytes)
	previous := -1
	for _, fragment := range []string{
		"pnpm build",
		"diff -qr web/dist webdist/dist",
		"go build -trimpath -o /tmp/tailcat-webui ./cmd/tailcat-webui",
	} {
		index := strings.Index(workflow, fragment)
		if index <= previous {
			t.Fatalf("CI fragment %q is missing or out of order", fragment)
		}
		previous = index
	}
	for _, forbidden := range []string{"rm -rf webdist/dist", "cp -R web/dist webdist/dist"} {
		if strings.Contains(workflow, forbidden) {
			t.Fatalf("CI must not refresh committed assets with %q", forbidden)
		}
	}
	if !strings.Contains(workflow, "push:\n    branches: [main]") {
		t.Fatal("CI push scope must remain main-only")
	}
	if !strings.Contains(workflow, "target-compile:\n    name: Compile ${{ matrix.platform }}\n    needs: verify") {
		t.Fatal("target compile matrix must depend on the parity-verifying job")
	}
	if !strings.Contains(workflow, "go test -count=1 ./internal/privatefs ./internal/transfer") {
		t.Fatal("Windows runtime job must exercise private filesystem and transfer storage behavior")
	}
}
