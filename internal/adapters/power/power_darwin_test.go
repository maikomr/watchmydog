//go:build darwin

package power

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// assertionsOfThisProcess returns the lines of `pmset -g assertions` that
// belong to this test process.
func assertionsOfThisProcess(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("pmset", "-g", "assertions").Output()
	if err != nil {
		t.Skipf("pmset unavailable: %v", err)
	}
	marker := fmt.Sprintf("pid %d(", os.Getpid())
	var mine []string
	for _, line := range strings.Split(string(out), "\n") {
		if strings.Contains(line, marker) {
			mine = append(mine, line)
		}
	}
	return strings.Join(mine, "\n")
}

func TestPreventIdleSleepIsVisibleAndReleased(t *testing.T) {
	if !Supported() {
		t.Fatal("darwin must be supported")
	}
	name := fmt.Sprintf("watchmydog test assertion %d", os.Getpid())
	a, err := PreventIdleSleep(name)
	if err != nil {
		t.Fatal(err)
	}
	got := assertionsOfThisProcess(t)
	if !strings.Contains(got, "PreventUserIdleSystemSleep") || !strings.Contains(got, name) {
		t.Errorf("assertion not listed by pmset:\n%s", got)
	}

	if err := a.Release(); err != nil {
		t.Fatal(err)
	}
	if err := a.Release(); err != nil {
		t.Errorf("second release must be a no-op, got %v", err)
	}
	if got := assertionsOfThisProcess(t); strings.Contains(got, name) {
		t.Errorf("assertion still listed after release:\n%s", got)
	}
}
