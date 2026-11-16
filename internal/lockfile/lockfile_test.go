package lockfile

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

// With LOCKFILE_HOLD set, the test binary holds that lock until killed.
func TestMain(m *testing.M) {
	if path := os.Getenv("LOCKFILE_HOLD"); path != "" {
		l, err := Acquire(path)
		if err != nil {
			fmt.Println(err)
			os.Exit(1)
		}
		fmt.Println("locked")
		_, _ = io.Copy(io.Discard, os.Stdin)
		_ = l.Release()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestOneHolderAtATime(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lock")
	a, err := Acquire(path)
	must(t, err)
	if _, err := Acquire(path); !errors.Is(err, ErrLocked) {
		t.Fatalf("a second lock in the same process: %v", err)
	}
	if pid := Holder(path); runtime.GOOS != "windows" && pid != os.Getpid() {
		t.Errorf("holder %d, want %d", pid, os.Getpid())
	}
	must(t, a.Release())
	must(t, a.Release())
	b, err := Acquire(path)
	must(t, err)
	must(t, b.Release())
}

func TestLockIsReleasedWhenTheHolderDies(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lock")
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(), "LOCKFILE_HOLD="+path)
	stdin, err := cmd.StdinPipe()
	must(t, err)
	defer stdin.Close()
	out, err := cmd.StdoutPipe()
	must(t, err)
	must(t, cmd.Start())
	if line, err := bufio.NewReader(out).ReadString('\n'); line != "locked\n" {
		_ = cmd.Process.Kill()
		t.Fatalf("helper: %q, %v", line, err)
	}

	if _, err := Acquire(path); !errors.Is(err, ErrLocked) {
		t.Fatalf("another process's lock: %v", err)
	}
	if pid := Holder(path); runtime.GOOS != "windows" && pid != cmd.Process.Pid {
		t.Errorf("holder %d, want %d", pid, cmd.Process.Pid)
	}
	must(t, cmd.Process.Kill())
	_ = cmd.Wait()
	l, err := Acquire(path)
	must(t, err)
	must(t, l.Release())
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
