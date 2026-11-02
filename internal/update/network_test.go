package update

import (
	"os/exec"
	"strings"
	"testing"
)

// githints opens no network connection except when a person runs `githints
// update`. This keeps every other package from importing net/http directly;
// mcp-go's own imports are its business and do not dial anything here.
func TestOnlyUpdateImportsNetHTTP(t *testing.T) {
	out, err := exec.Command("go", "list", "-f", `{{.ImportPath}} {{join .Imports " "}}`, "github.com/cjrdz/githints/...").CombinedOutput()
	if err != nil {
		t.Fatalf("go list: %v\n%s", err, out)
	}
	const self = "github.com/cjrdz/githints/internal/update"
	checked := 0
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		checked++
		for _, imp := range fields[1:] {
			if (imp == "net/http" || strings.HasPrefix(imp, "net/http/")) && fields[0] != self {
				t.Errorf("%s imports %s; only %s may use the network", fields[0], imp, self)
			}
		}
	}
	if checked < 10 {
		t.Fatalf("go list returned %d packages; the check is not looking at the module", checked)
	}
}
