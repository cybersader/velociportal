package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

func storageTestPath(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "private.json")
	if err := os.WriteFile(path, storageTestBytes(t), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func storageFixture(t *testing.T) (*personalSnapshotStorage, string) {
	t.Helper()
	path := storageTestPath(t)
	s, err := newPersonalSnapshotStorage(path, "fixture-1", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, path
}

func storageNext(t *testing.T, s *personalSnapshotStorage) (personalStorageSnapshot, personalStorageVersion) {
	t.Helper()
	current, version, err := s.Read()
	if err != nil {
		t.Fatal(err)
	}
	current.Collections[0].Revision = "2"
	current.Collections[0].Document.Entries[0].Name = "Next"
	return current, version
}

func requireNoStorageResult(t *testing.T, snapshot personalStorageSnapshot, version personalStorageVersion, err error) {
	t.Helper()
	if err == nil || !reflect.DeepEqual(snapshot, personalStorageSnapshot{}) || version != (personalStorageVersion{}) {
		t.Fatal("error exposed snapshot/version", err)
	}
}

func TestPersonalStorageCommitReopenAndCopies(t *testing.T) {
	s, path := storageFixture(t)
	before, _ := os.ReadFile(path)
	lockBefore, _ := os.Stat(path + ".lock")
	old, oldVersion, err := s.Read()
	if err != nil {
		t.Fatal(err)
	}
	old.Collections[0].Document.Entries[0].Name = "Changed copy"
	candidate, version := storageNext(t, s)
	if version != oldVersion {
		t.Fatal("read changed CAS")
	}
	called := 0
	result, newVersion, err := s.Commit(version, candidate, func() error {
		called++
		disk, _ := os.ReadFile(path)
		if !bytes.Equal(disk, before) {
			t.Error("published before final check")
		}
		return nil
	})
	if err != nil || called != 1 || newVersion == version || result.Collections[0].Document.Entries[0].Name != "Next" {
		t.Fatal("commit", err)
	}
	candidate.Collections[0].Document.Entries[0].Name = "Caller change"
	result.Collections[0].Document.Entries[0].Name = "Returned change"
	current, _, err := s.Read()
	if err != nil || current.Collections[0].Document.Entries[0].Name != "Next" {
		t.Fatal("alias", err)
	}
	if len(current.Collections[1].Document.Entries) != 0 || current.Collections[1].Document.Entries == nil {
		t.Fatal("empty state lost")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal("second close", err)
	}
	out, v, e := s.Read()
	requireNoStorageResult(t, out, v, e)
	out, v, e = s.Commit(newVersion, current, func() error { return nil })
	requireNoStorageResult(t, out, v, e)
	reopened, err := newPersonalSnapshotStorage(path, "fixture-1", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	loaded, loadedVersion, err := reopened.Read()
	if err != nil || !reflect.DeepEqual(loaded, current) || loadedVersion != newVersion {
		t.Fatal("restart", err)
	}
	lockAfter, _ := os.Stat(path + ".lock")
	if !os.SameFile(lockBefore, lockAfter) || !privateFileInfo(lockAfter) {
		t.Fatal("lock replaced/unsafe")
	}
	if loaded.StoreID != strings.Repeat("a", 32) {
		t.Fatal("store ID regenerated")
	}
}

func TestPersonalStorageMandatoryFinalCheckAndBinding(t *testing.T) {
	s, path := storageFixture(t)
	before, _ := os.ReadFile(path)
	candidate, version := storageNext(t, s)
	for _, mode := range []string{"nil", "denied", "namespace", "store_id", "version", "invalid"} {
		next := copyPersonalSnapshot(candidate)
		called := false
		check := func() error { called = true; return nil }
		switch mode {
		case "nil":
			check = nil
		case "denied":
			check = func() error { called = true; return errPersonalUnavailable }
		case "namespace":
			next.Namespace = "other"
		case "store_id":
			next.StoreID = strings.Repeat("b", 32)
		case "version":
			next.Version = 2
		case "invalid":
			next.Collections[0].Revision = "0"
		}
		out, v, err := s.Commit(version, next, check)
		requireNoStorageResult(t, out, v, err)
		if called != (mode == "denied") {
			t.Fatal("unexpected final callback", mode)
		}
		disk, _ := os.ReadFile(path)
		if !bytes.Equal(before, disk) {
			t.Fatal("failed commit wrote", mode)
		}
	}
	out, _, err := s.Read()
	if err != nil || out.Collections[0].Document.Entries[0].Name != "Fixture" {
		t.Fatal("old state unavailable", err)
	}
	var nilStore *personalSnapshotStorage
	out, v, err := nilStore.Read()
	requireNoStorageResult(t, out, v, err)
	out, v, err = nilStore.Commit(version, candidate, func() error { return nil })
	requireNoStorageResult(t, out, v, err)
	if nilStore.Close() != nil {
		t.Fatal("nil close")
	}
}

func TestPersonalStorageCommitFailpoints(t *testing.T) {
	for _, stage := range []string{"create", "write", "filesync", "close", "recheck", "finalcheck", "rename", "afterrename", "dirsync", "publish"} {
		t.Run(stage, func(t *testing.T) {
			s, path := storageFixture(t)
			before, _ := os.ReadFile(path)
			candidate, version := storageNext(t, s)
			s.failpoint = func(hit string) error {
				if stage == hit {
					return errors.New("fixture failure")
				}
				return nil
			}
			out, v, err := s.Commit(version, candidate, func() error { return nil })
			requireNoStorageResult(t, out, v, err)
			post := stage == "afterrename" || stage == "dirsync" || stage == "publish"
			disk, _ := os.ReadFile(path)
			if bytes.Equal(before, disk) == post {
				t.Fatal("wrong disk result")
			}
			out, v, readErr := s.Read()
			if post {
				if !errors.Is(err, errPersonalStorageUncertain) || !errors.Is(readErr, errPersonalStorageUncertain) {
					t.Fatal("not uncertain", err, readErr)
				}
				requireNoStorageResult(t, out, v, readErr)
				s.failpoint = nil
				out, v, err = s.Commit(version, candidate, func() error { t.Error("called when uncertain"); return nil })
				requireNoStorageResult(t, out, v, err)
			} else {
				if readErr != nil || v != version || out.Collections[0].Document.Entries[0].Name != "Fixture" {
					t.Fatal("old confirmed state lost", readErr)
				}
				s.failpoint = nil
				if _, _, err := s.Commit(version, candidate, func() error { return nil }); err != nil {
					t.Fatal("pre-rename backend unusable", err)
				}
			}
			files, err := os.ReadDir(filepath.Dir(path))
			if err != nil {
				t.Fatal(err)
			}
			if len(files) != 2 {
				t.Fatal("owned temp leaked", files)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := newPersonalSnapshotStorage(path, "fixture-1", nil)
			if err != nil {
				t.Fatal("reopen", err)
			}
			defer reopened.Close()
			loaded, _, err := reopened.Read()
			if err != nil || loaded.Collections[0].Document.Entries[0].Name != "Next" {
				t.Fatal("reopen did not reconcile disk", err)
			}
		})
	}
}

// Model both possible outcomes of a rename that reports failure. This is
// syscall fault injection, not a claim that a deployed filesystem failed.
func TestPersonalStorageAmbiguousRenameError(t *testing.T) {
	for _, applied := range []bool{false, true} {
		name := "not-applied"
		if applied {
			name = "applied"
		}
		t.Run(name, func(t *testing.T) {
			s, path := storageFixture(t)
			candidate, version := storageNext(t, s)
			called := 0
			s.rename = func(oldFD int, oldName string, newFD int, newName string) error {
				called++
				if applied {
					if err := unix.Renameat(oldFD, oldName, newFD, newName); err != nil {
						t.Fatal(err)
					}
				}
				return unix.EIO
			}
			out, v, err := s.Commit(version, candidate, func() error { return nil })
			requireNoStorageResult(t, out, v, err)
			if called != 1 || !errors.Is(err, errPersonalStorageUncertain) {
				t.Fatal("rename error did not enter uncertainty", err)
			}
			out, v, err = s.Read()
			requireNoStorageResult(t, out, v, err)
			if !errors.Is(err, errPersonalStorageUncertain) {
				t.Fatal("read did not preserve uncertainty", err)
			}
			out, v, err = s.Commit(version, candidate, func() error {
				t.Error("authorization invoked after uncertainty")
				return nil
			})
			requireNoStorageResult(t, out, v, err)
			if !errors.Is(err, errPersonalStorageUncertain) || called != 1 {
				t.Fatal("uncertain write was replayed", err)
			}
			files, err := os.ReadDir(filepath.Dir(path))
			if err != nil || len(files) != 2 {
				t.Fatal("owned temporary file was not cleaned", err)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := newPersonalSnapshotStorage(path, "fixture-1", nil)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			loaded, _, err := reopened.Read()
			want := "Fixture"
			if applied {
				want = "Next"
			}
			if err != nil || loaded.Collections[0].Document.Entries[0].Name != want {
				t.Fatal("reopen did not reconcile actual disk outcome", err)
			}
		})
	}
}

func TestPersonalStorageExternalEditAndConcurrentCAS(t *testing.T) {
	for _, timing := range []string{"before", "staged"} {
		t.Run(timing, func(t *testing.T) {
			s, path := storageFixture(t)
			candidate, version := storageNext(t, s)
			edit := func() {
				if err := os.WriteFile(path, append(storageTestBytes(t), ' '), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if timing == "before" {
				edit()
			} else {
				s.failpoint = func(stage string) error {
					if stage == "recheck" {
						edit()
					}
					return nil
				}
			}
			out, v, err := s.Commit(version, candidate, func() error { t.Error("authorization after conflict"); return nil })
			requireNoStorageResult(t, out, v, err)
			if !errors.Is(err, errPersonalConflict) {
				t.Fatal("not conflict", err)
			}
			if _, _, err := s.Read(); err != nil {
				t.Fatal("last confirmed unreadable", err)
			}
		})
	}
	s, _ := storageFixture(t)
	candidate, version := storageNext(t, s)
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, err := s.Commit(version, candidate, func() error { return nil })
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	success, conflicts := 0, 0
	for err := range results {
		if err == nil {
			success++
		} else if errors.Is(err, errPersonalConflict) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || conflicts != 1 {
		t.Fatal("CAS", success, conflicts)
	}
}

func TestPersonalStorageLockProcess(t *testing.T) {
	path := os.Getenv("VELOCIPORTAL_STORAGE_LOCK_FIXTURE")
	if path == "" {
		return
	}
	s, err := newPersonalSnapshotStorage(path, "fixture-1", nil)
	if os.Getenv("VELOCIPORTAL_STORAGE_LOCK_EXPECT") == "available" {
		if err != nil {
			t.Fatal("lock unavailable after close", err)
		}
		defer s.Close()
	} else if err == nil {
		s.Close()
		t.Fatal("second process acquired lifetime lock")
	}
}

func TestPersonalStorageSecondProcessLock(t *testing.T) {
	s, path := storageFixture(t)
	for _, expect := range []string{"locked", "available"} {
		if expect == "available" {
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
		}
		cmd := exec.Command(os.Args[0], "-test.run=^TestPersonalStorageLockProcess$", "-test.count=1")
		cmd.Env = append(os.Environ(), "VELOCIPORTAL_STORAGE_LOCK_FIXTURE="+path, "VELOCIPORTAL_STORAGE_LOCK_EXPECT="+expect)
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("child %s: %v %s", expect, err, output)
		}
	}
}

func TestPersonalStorageUnsafeInitialFiles(t *testing.T) {
	for _, mode := range []string{"missing", "corrupt", "oversized", "file0640", "file0400", "file0666", "dir0750", "dir0710", "dir0770", "symlink", "hardlink", "fifo", "directory", "parent-symlink", "ancestor-symlink", "unsafe-ancestor", "setgid-directory", "lock-symlink", "lock-hardlink", "lock-fifo", "lock0640", "lock0400", "lock-directory"} {
		t.Run(mode, func(t *testing.T) {
			path := storageTestPath(t)
			switch mode {
			case "missing":
				os.Remove(path)
			case "corrupt":
				os.WriteFile(path, []byte(`{}`), 0o600)
			case "oversized":
				f, e := os.OpenFile(path, os.O_WRONLY, 0)
				if e != nil {
					t.Fatal(e)
				}
				e = f.Truncate(maxPersonalSnapshotBytes + 1)
				f.Close()
				if e != nil {
					t.Fatal(e)
				}
			case "file0640":
				os.Chmod(path, 0o640)
			case "file0400":
				os.Chmod(path, 0o400)
			case "file0666":
				os.Chmod(path, 0o666)
			case "dir0750":
				os.Chmod(filepath.Dir(path), 0o750)
			case "dir0710":
				os.Chmod(filepath.Dir(path), 0o710)
			case "dir0770":
				os.Chmod(filepath.Dir(path), 0o770)
			case "symlink":
				os.Rename(path, path+".real")
				os.Symlink(path+".real", path)
			case "hardlink":
				os.Link(path, path+".alias")
			case "fifo":
				os.Remove(path)
				if err := unix.Mkfifo(path, 0o600); err != nil {
					t.Fatal(err)
				}
			case "directory":
				os.Remove(path)
				os.Mkdir(path, 0o700)
			case "parent-symlink":
				alias := filepath.Join(t.TempDir(), "alias")
				os.Symlink(filepath.Dir(path), alias)
				path = filepath.Join(alias, "private.json")
			case "ancestor-symlink":
				dir := filepath.Dir(path)
				outer := filepath.Dir(dir)
				alias := filepath.Join(t.TempDir(), "alias")
				os.Symlink(outer, alias)
				path = filepath.Join(alias, filepath.Base(dir), "private.json")
			case "unsafe-ancestor":
				outer := t.TempDir()
				os.Chmod(outer, 0o777)
				dir := filepath.Join(outer, "private")
				os.Mkdir(dir, 0o700)
				path = filepath.Join(dir, "private.json")
				os.WriteFile(path, storageTestBytes(t), 0o600)
			case "setgid-directory":
				os.Chmod(filepath.Dir(path), 0o700|os.ModeSetgid)
			case "lock-symlink":
				os.Symlink(path, path+".lock")
			case "lock-hardlink":
				other := filepath.Join(filepath.Dir(path), "other")
				os.WriteFile(other, []byte{}, 0o600)
				os.Link(other, path+".lock")
			case "lock-fifo":
				unix.Mkfifo(path+".lock", 0o600)
			case "lock0640":
				os.WriteFile(path+".lock", nil, 0o640)
			case "lock0400":
				os.WriteFile(path+".lock", nil, 0o400)
			case "lock-directory":
				os.Mkdir(path+".lock", 0o700)
			}
			s, err := newPersonalSnapshotStorage(path, "fixture-1", nil)
			if err == nil {
				s.Close()
				t.Fatal("unsafe fixture accepted", mode)
			}
			if mode == "missing" || mode == "corrupt" || mode == "oversized" {
				if _, e := os.Lstat(path + ".lock"); !errors.Is(e, os.ErrNotExist) {
					t.Fatal("created lock for invalid target")
				}
			}
		})
	}
	path := storageTestPath(t)
	for _, p := range []string{"relative.json", path + "/../other.json", "/"} {
		if s, e := newPersonalSnapshotStorage(p, "fixture-1", nil); e == nil {
			s.Close()
			t.Fatal("noncanonical path")
		}
	}
	if s, e := newPersonalSnapshotStorage(path, "other", nil); e == nil {
		s.Close()
		t.Fatal("namespace mismatch")
	}
}

func TestPersonalStorageForbiddenPaths(t *testing.T) {
	for _, mode := range []string{"target", "own-lock", "forbidden-lock-is-target", "symlink-target", "symlink-lock", "parent-alias", "hardlink", "noncanonical", "safe-missing"} {
		t.Run(mode, func(t *testing.T) {
			path := storageTestPath(t)
			forbidden := path
			switch mode {
			case "own-lock":
				forbidden = path + ".lock"
			case "forbidden-lock-is-target":
				newPath := path + ".lock"
				os.Rename(path, newPath)
				forbidden = path
				path = newPath
			case "symlink-target":
				forbidden = filepath.Join(t.TempDir(), "shared.json")
				os.Symlink(path, forbidden)
			case "symlink-lock":
				os.WriteFile(path+".lock", nil, 0o600)
				forbidden = filepath.Join(t.TempDir(), "shared.json")
				os.Symlink(path+".lock", forbidden)
			case "parent-alias":
				alias := filepath.Join(t.TempDir(), "alias")
				os.Symlink(filepath.Dir(path), alias)
				forbidden = filepath.Join(alias, "private.json")
			case "hardlink":
				forbidden = filepath.Join(filepath.Dir(path), "shared.json")
				os.Link(path, forbidden)
			case "noncanonical":
				forbidden = "relative"
			case "safe-missing":
				forbidden = filepath.Join(t.TempDir(), "shared.json")
			}
			s, err := newPersonalSnapshotStorage(path, "fixture-1", []string{forbidden})
			if mode == "safe-missing" {
				if err != nil {
					t.Fatal(err)
				}
				s.Close()
			} else if err == nil {
				s.Close()
				t.Fatal("collision admitted")
			}
		})
	}
}

func TestPersonalStoragePathAndLockReplacement(t *testing.T) {
	for _, mode := range []string{"lock", "directory", "target-symlink", "target-mode", "temp", "forbidden-alias", "postrename-directory"} {
		t.Run(mode, func(t *testing.T) {
			s, path := storageFixture(t)
			candidate, version := storageNext(t, s)
			forbidden := filepath.Join(t.TempDir(), "shared.json")
			s.forbidden = []string{forbidden}
			s.failpoint = func(stage string) error {
				if mode == "postrename-directory" {
					if stage == "afterrename" {
						if err := os.Rename(filepath.Dir(path), filepath.Dir(path)+"-moved"); err != nil {
							t.Fatal(err)
						}
						os.Mkdir(filepath.Dir(path), 0o700)
						t.Cleanup(func() { os.RemoveAll(filepath.Dir(path) + "-moved") })
					}
					return nil
				}
				if stage != "recheck" {
					return nil
				}
				switch mode {
				case "lock":
					os.Rename(path+".lock", path+".old-lock")
					os.WriteFile(path+".lock", nil, 0o600)
				case "directory":
					os.Rename(filepath.Dir(path), filepath.Dir(path)+"-moved")
					os.Mkdir(filepath.Dir(path), 0o700)
					t.Cleanup(func() { os.RemoveAll(filepath.Dir(path) + "-moved") })
				case "target-symlink":
					os.Rename(path, path+".real")
					os.Symlink(path+".real", path)
				case "target-mode":
					os.Chmod(path, 0o640)
				case "temp":
					files, _ := os.ReadDir(filepath.Dir(path))
					for _, f := range files {
						if strings.Contains(f.Name(), ".tmp-") {
							os.Rename(filepath.Join(filepath.Dir(path), f.Name()), filepath.Join(filepath.Dir(path), f.Name()+".held"))
							os.WriteFile(filepath.Join(filepath.Dir(path), f.Name()), []byte("replacement"), 0o600)
						}
					}
				case "forbidden-alias":
					os.Symlink(path, forbidden)
				}
				return nil
			}
			out, v, err := s.Commit(version, candidate, func() error { return nil })
			requireNoStorageResult(t, out, v, err)
			out, v, err = s.Read()
			if mode == "postrename-directory" {
				requireNoStorageResult(t, out, v, err)
				if !errors.Is(err, errPersonalStorageUncertain) {
					t.Fatal(err)
				}
			} else if err != nil || out.Collections[0].Document.Entries[0].Name != "Fixture" {
				t.Fatal("old RAM not usable", err)
			}
		})
	}
}

// Synthetic stat wrappers test foreign ownership and special inodes without
// privileged chown, arbitrary live paths, or device creation in tests.
type storageStatFixture struct {
	os.FileInfo
	mode os.FileMode
	stat syscall.Stat_t
}

func (f storageStatFixture) Mode() os.FileMode { return f.mode }
func (f storageStatFixture) IsDir() bool       { return f.mode.IsDir() }
func (f storageStatFixture) Sys() any          { return &f.stat }

func TestPersonalStorageOwnershipAndSpecialModes(t *testing.T) {
	path := storageTestPath(t)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	st := *info.Sys().(*syscall.Stat_t)
	for _, mode := range []os.FileMode{0o600 | os.ModeSetuid, 0o600 | os.ModeSetgid, 0o600 | os.ModeSticky, 0o600 | os.ModeDevice, 0o600 | os.ModeNamedPipe, 0o600 | os.ModeSocket} {
		if privateFileInfo(storageStatFixture{info, mode, st}) {
			t.Fatal("special mode accepted")
		}
	}
	foreign := st
	foreign.Uid = uint32(os.Geteuid() + 1)
	if privateFileInfo(storageStatFixture{info, 0o600, foreign}) || privateDirectoryInfo(storageStatFixture{info, 0o700 | os.ModeDir, foreign}) || privateAncestorInfo(storageStatFixture{info, 0o755 | os.ModeDir, foreign}, "/fixture") {
		t.Fatal("foreign ownership")
	}
	root := st
	root.Uid = 0
	if !privateAncestorInfo(storageStatFixture{info, 0o777 | os.ModeDir | os.ModeSticky, root}, "/tmp") || privateAncestorInfo(storageStatFixture{info, 0o777 | os.ModeDir | os.ModeSticky, root}, "/not-system-temp") {
		t.Fatal("sticky exception too broad/narrow")
	}
}

func TestPersonalStorageCapacityCommit(t *testing.T) {
	s, _ := storageFixture(t)
	_, version, err := s.Read()
	if err != nil {
		t.Fatal(err)
	}
	candidate := storageCapacitySnapshot(t)
	result, _, err := s.Commit(version, candidate, func() error { return nil })
	if err != nil || len(result.Collections) != 128 {
		t.Fatal("capacity commit", err)
	}
}
