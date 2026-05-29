//go:build linux

package proclife

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestMain lets this test binary re-exec itself as a "launcher" or "victim"
// so we can exercise PR_SET_PDEATHSIG end to end:
//
//	test ── spawns ──▶ launcher ── spawns ──▶ victim (DieWithParent + sleep)
//	                       │ prints victim pid, then exits
//	                       ▼
//	            victim's parent dies ⇒ kernel sends SIGTERM ⇒ victim must die
func TestMain(m *testing.M) {
	switch os.Getenv("PROCLIFE_TEST_MODE") {
	case "victim":
		// Register parent-death, THEN announce our pid (so the launcher only
		// exits after prctl is set — no fork/exit race), then block.
		_ = DieWithParent()
		fmt.Println(os.Getpid())
		time.Sleep(60 * time.Second) // pdeathsig should kill us long before this
		os.Exit(0)
	case "launcher":
		cmd := exec.Command(os.Args[0])
		cmd.Env = append(os.Environ(), "PROCLIFE_TEST_MODE=victim")
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			os.Exit(1)
		}
		if err := cmd.Start(); err != nil {
			os.Exit(1)
		}
		line, _ := bufio.NewReader(stdout).ReadString('\n')
		fmt.Print(line) // relay victim pid up to the test
		os.Exit(0)      // launcher dies → victim is orphaned → SIGTERM fires
	default:
		os.Exit(m.Run())
	}
}

func TestDieWithParent_KillsOrphanOnParentDeath(t *testing.T) {
	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(), "PROCLIFE_TEST_MODE=launcher")
	out, err := cmd.Output() // launcher prints victim pid, then exits
	if err != nil {
		t.Fatalf("launcher run: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil || pid <= 0 {
		t.Fatalf("bad victim pid from launcher: %q (%v)", out, err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if syscall.Kill(pid, 0) != nil {
			return // ESRCH — victim died from pdeathsig SIGTERM. Success.
		}
		time.Sleep(20 * time.Millisecond)
	}
	_ = syscall.Kill(pid, syscall.SIGKILL) // cleanup the leaked victim
	t.Fatalf("victim %d still alive 3s after parent death — PR_SET_PDEATHSIG did not fire", pid)
}
