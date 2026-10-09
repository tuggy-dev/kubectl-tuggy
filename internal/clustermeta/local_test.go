package clustermeta

import (
	"bufio"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func newStore(t *testing.T) *Local {
	t.Helper()
	return NewLocal(t.TempDir())
}

func saved(t *testing.T, s *Local, name string, status Status) *Record {
	t.Helper()
	r := NewRecord(name, "gke", "v0.1.0", 8*time.Hour, now)
	if err := r.Transition(StatusCreating, "", now); err != nil {
		t.Fatal(err)
	}
	if err := s.Create(r); err != nil {
		t.Fatal(err)
	}
	for _, next := range map[Status][]Status{
		StatusCreating: nil,
		StatusReady:    {StatusReady},
		StatusFailed:   {StatusFailed},
		StatusDeleting: {StatusReady, StatusDeleting},
	}[status] {
		if err := r.Transition(next, "", now); err != nil {
			t.Fatal(err)
		}
		if err := s.Update(r); err != nil {
			t.Fatal(err)
		}
	}
	return r
}

func TestCreateGetRoundTrip(t *testing.T) {
	s := newStore(t)
	want := saved(t, s, "dev", StatusReady)
	want.Outputs = map[string]string{"endpoint": "https://34.1.2.3"}
	if err := s.Update(want); err != nil {
		t.Fatal(err)
	}

	got, err := s.Get("dev")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "dev" || got.Platform != "gke" || got.Status != StatusReady ||
		got.TuggyVersion != "v0.1.0" || !got.CreatedAt.Equal(now) ||
		got.ExpiresAt == nil || !got.ExpiresAt.Equal(now.Add(8*time.Hour)) ||
		got.Outputs["endpoint"] != "https://34.1.2.3" {
		t.Errorf("round trip mismatch:\n got %+v\nwant %+v", got, want)
	}
}

func TestRecordFileFormat(t *testing.T) {
	s := newStore(t)
	saved(t, s, "dev", StatusReady)

	data, err := os.ReadFile(filepath.Join(s.Dir("dev"), "record.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"name: dev\n", "platform: gke\n", "status: Ready\n",
		"createdAt: \"2026-10-07T15:00:00Z\"\n", "expiresAt: \"2026-10-07T23:00:00Z\"\n",
	} {
		if !strings.Contains(string(data), want) {
			t.Errorf("record.yaml missing %q:\n%s", want, data)
		}
	}
}

func TestPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows uses the user profile's access control lists instead of mode bits")
	}
	s := newStore(t)
	saved(t, s, "dev", StatusReady)

	for path, want := range map[string]os.FileMode{
		s.Dir("dev"): dirPerm,
		filepath.Join(s.Dir("dev"), "record.yaml"): filePerm,
	} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != want {
			t.Errorf("%s mode = %o, want %o", path, got, want)
		}
	}
}

func TestErrors(t *testing.T) {
	s := newStore(t)
	saved(t, s, "dev", StatusReady)

	if _, err := s.Get("missing"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get missing: %v, want ErrNotFound", err)
	}
	if err := s.Update(&Record{Name: "missing"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("Update missing: %v, want ErrNotFound", err)
	}
	if err := s.Create(NewRecord("dev", "gke", "v0.1.0", 0, now)); !errors.Is(err, ErrExists) {
		t.Errorf("Create existing: %v, want ErrExists", err)
	}
	for _, bad := range []string{"", "../etc", "a/b", "Dev", `a\b`, ".", strings.Repeat("a", 64)} {
		if _, err := s.Get(bad); !errors.Is(err, ErrInvalidName) {
			t.Errorf("Get(%q): %v, want ErrInvalidName", bad, err)
		}
		if _, err := s.Lock(bad); !errors.Is(err, ErrInvalidName) {
			t.Errorf("Lock(%q): %v, want ErrInvalidName", bad, err)
		}
	}
}

func TestCreateReusesDirectoryWithoutRecord(t *testing.T) {
	s := newStore(t)
	// A crash between creating the directory and writing the record.
	if err := os.MkdirAll(s.Dir("dev"), dirPerm); err != nil {
		t.Fatal(err)
	}
	if err := s.Create(NewRecord("dev", "gke", "v0.1.0", 0, now)); err != nil {
		t.Errorf("Create over an empty leftover directory: %v", err)
	}
}

func TestList(t *testing.T) {
	s := newStore(t)
	if got, err := s.List(); err != nil || len(got) != 0 {
		t.Fatalf("empty store: got %v, %v", got, err)
	}

	saved(t, s, "zeta", StatusReady)
	saved(t, s, "alpha", StatusFailed)
	if err := os.MkdirAll(s.Dir("leftover"), dirPerm); err != nil { // no record
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.Root(), "clusters", "stray-file"), nil, filePerm); err != nil {
		t.Fatal(err)
	}

	got, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, r := range got {
		names = append(names, r.Name)
	}
	if strings.Join(names, ",") != "alpha,zeta" {
		t.Errorf("List names = %v, want [alpha zeta]", names)
	}
}

func TestListReportsCorruptRecord(t *testing.T) {
	s := newStore(t)
	saved(t, s, "dev", StatusReady)
	if err := os.WriteFile(filepath.Join(s.Dir("dev"), "record.yaml"), []byte("name: [broken"), filePerm); err != nil {
		t.Fatal(err)
	}
	if _, err := s.List(); err == nil || !strings.Contains(err.Error(), `cluster "dev"`) {
		t.Errorf("List with corrupt record: %v, want error naming the cluster", err)
	}
}

func TestGetRejectsRecordForOtherCluster(t *testing.T) {
	s := newStore(t)
	saved(t, s, "dev", StatusReady)
	data, _ := os.ReadFile(filepath.Join(s.Dir("dev"), "record.yaml"))
	if err := os.MkdirAll(s.Dir("prod"), dirPerm); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.Dir("prod"), "record.yaml"), data, filePerm); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get("prod"); err == nil {
		t.Error("Get should refuse a record whose name doesn't match its directory")
	}
}

func TestDelete(t *testing.T) {
	s := newStore(t)

	t.Run("refuses unless deleting", func(t *testing.T) {
		saved(t, s, "ready", StatusReady)
		if err := s.Delete("ready"); err == nil {
			t.Error("Delete of a Ready cluster should fail")
		}
		if _, err := s.Get("ready"); err != nil {
			t.Errorf("record should still exist: %v", err)
		}
	})

	t.Run("removes directory and keeps logs", func(t *testing.T) {
		saved(t, s, "dev", StatusDeleting)
		logs := filepath.Join(s.Dir("dev"), "logs")
		if err := os.MkdirAll(logs, dirPerm); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(logs, "create.log"), []byte("created"), filePerm); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(s.Dir("dev"), "terraform.tfstate"), []byte("{}"), filePerm); err != nil {
			t.Fatal(err)
		}

		if err := s.Delete("dev"); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(s.Dir("dev")); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("cluster directory still exists: %v", err)
		}
		kept, err := os.ReadFile(filepath.Join(s.Root(), "logs", "dev", "create.log"))
		if err != nil || string(kept) != "created" {
			t.Errorf("log not kept: %q, %v", kept, err)
		}
		if _, err := s.Get("dev"); !errors.Is(err, ErrNotFound) {
			t.Errorf("Get after Delete: %v, want ErrNotFound", err)
		}
	})
}

func TestLockExclusiveWithinProcess(t *testing.T) {
	s := newStore(t)
	unlock, err := s.Lock("dev")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Lock("dev"); !errors.Is(err, ErrLocked) {
		t.Errorf("second Lock: %v, want ErrLocked", err)
	}
	unlockOther, err := s.Lock("other")
	if err != nil {
		t.Errorf("Lock of a different cluster should succeed: %v", err)
	} else {
		// Release it: Windows can't remove the test's temporary folder while
		// the lock file is open.
		t.Cleanup(func() { _ = unlockOther() })
	}
	if err := unlock(); err != nil {
		t.Fatal(err)
	}
	again, err := s.Lock("dev")
	if err != nil {
		t.Fatalf("Lock after unlock: %v", err)
	}
	_ = again()
}

// TestLockAcrossProcesses starts a second process that holds the lock, checks
// this process can't take it, then kills the holder and checks the lock is
// released without anyone calling unlock.
func TestLockAcrossProcesses(t *testing.T) {
	if os.Getenv("TUGGY_TEST_LOCK_HOLDER") != "" {
		holdLockAndWait()
		return
	}

	s := newStore(t)
	cmd := exec.Command(os.Args[0], "-test.run=^TestLockAcrossProcesses$")
	cmd.Env = append(os.Environ(), "TUGGY_TEST_LOCK_HOLDER=1", HomeEnv+"="+s.Root())
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })

	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil || strings.TrimSpace(line) != "locked" {
		t.Fatalf("holder did not report the lock: %q, %v", line, err)
	}

	if _, err := s.Lock("dev"); !errors.Is(err, ErrLocked) {
		t.Fatalf("Lock while another process holds it: %v, want ErrLocked", err)
	}

	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()

	unlock, err := s.Lock("dev")
	if err != nil {
		t.Fatalf("lock not released after the holder was killed: %v", err)
	}
	_ = unlock()
}

func holdLockAndWait() {
	root, _ := DefaultRoot()
	if _, err := NewLocal(root).Lock("dev"); err != nil {
		os.Exit(2)
	}
	os.Stdout.WriteString("locked\n")
	time.Sleep(time.Minute)
	os.Exit(0)
}

func TestDefaultRoot(t *testing.T) {
	t.Setenv(HomeEnv, "")
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	if got, _ := DefaultRoot(); got != filepath.Join(home, ".tuggy") {
		t.Errorf("DefaultRoot = %q, want ~/.tuggy", got)
	}

	dir := t.TempDir()
	t.Setenv(HomeEnv, dir)
	if got, _ := DefaultRoot(); got != dir {
		t.Errorf("DefaultRoot with %s = %q, want %q", HomeEnv, got, dir)
	}
}
