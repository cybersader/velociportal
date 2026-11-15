package main

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"

	"golang.org/x/sys/unix"
)

var (
	errPersonalStorage          = errors.New("private storage unavailable")
	errPersonalStorageUncertain = errors.New("private save outcome uncertain; reopen and reconcile")
)

// Internal whole-file CAS only. Never expose this digest or the full snapshot
// through browser/MCP APIs: doing so would disclose other collections' activity.
type personalStorageVersion [sha256.Size]byte

// UNWIRED low-level backend, not a live application store. Reads do not authorize
// an identity. The future adapter MUST supply every configured presentation path
// (including shared metadata/health paths) as forbidden, guard all private reads
// and renders with fresh ownership, and compose all transport/ownership checks in
// the mandatory Commit finalCheck. No callback may reenter this backend.
// Uncooperative external writers and upstream revocation are not transactional:
// the final check-to-rename gap is intentionally not claimed to be race-proof.
type personalSnapshotStorage struct {
	mu                    sync.Mutex
	path, base, namespace string
	forbidden             []string
	directory, lock       *os.File
	confirmed             personalStorageSnapshot
	contents              []byte
	version               personalStorageVersion
	closed, uncertain     bool
	// Fixture-only; never reachable from runtime configuration.
	failpoint func(string) error
	rename    func(int, string, int, string) error
}

func privateFileInfo(info os.FileInfo) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && info.Mode().IsRegular() && info.Mode().Perm() == 0o600 && info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) == 0 && st.Nlink == 1 && int(st.Uid) == os.Geteuid()
}

func privateDirectoryInfo(info os.FileInfo) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && info.IsDir() && info.Mode().Perm() == 0o700 && info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) == 0 && int(st.Uid) == os.Geteuid()
}

func privateAncestorInfo(info os.FileInfo, path string) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() || int(st.Uid) != os.Geteuid() && st.Uid != 0 || info.Mode()&(os.ModeSetuid|os.ModeSetgid) != 0 {
		return false
	}
	// Only root-owned sticky system temporary ancestors are exempt. The dedicated
	// directory itself is always exact 0700, including in tests.
	return info.Mode().Perm()&0o022 == 0 || st.Uid == 0 && info.Mode()&os.ModeSticky != 0 && (path == "/tmp" || path == "/var/tmp")
}

// Walk from a held root using NOFOLLOW for EVERY component, not just the final
// parent. Rewalking and comparing identities detects path/ancestor replacement;
// all target/temp/lock operations remain relative to the original held parent.
func openPrivateDirectory(path string) (*os.File, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == "/" {
		return nil, errPersonalStorage
	}
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, errPersonalStorage
	}
	current := os.NewFile(uintptr(fd), "/")
	info, err := current.Stat()
	if err != nil || !privateAncestorInfo(info, "/") {
		current.Close()
		return nil, errPersonalStorage
	}
	parts, prefix := strings.Split(strings.TrimPrefix(path, "/"), "/"), ""
	for i, part := range parts {
		prefix += "/" + part
		next, err := unix.Openat(int(current.Fd()), part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		_ = current.Close()
		if err != nil {
			return nil, errPersonalStorage
		}
		current = os.NewFile(uintptr(next), prefix)
		info, err = current.Stat()
		safe := err == nil
		if safe && i == len(parts)-1 {
			safe = privateDirectoryInfo(info)
		} else if safe {
			safe = privateAncestorInfo(info, prefix)
		}
		if !safe {
			current.Close()
			return nil, errPersonalStorage
		}
	}
	return current, nil
}

func privateRelativeInfo(directory *os.File, name string) (os.FileInfo, error) {
	// O_PATH permits inspecting FIFO/device/symlink inodes without opening their
	// data plane. Stat below still rejects every non-regular/unsafe object.
	fd, err := unix.Openat(int(directory.Fd()), name, unix.O_PATH|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, errPersonalStorage
	}
	file := os.NewFile(uintptr(fd), name)
	defer file.Close()
	return file.Stat()
}

func (s *personalSnapshotStorage) validatePaths() error {
	if s.directory == nil {
		return errPersonalStorage
	}
	fresh, err := openPrivateDirectory(filepath.Dir(s.path))
	if err != nil {
		return err
	}
	defer fresh.Close()
	held, err := s.directory.Stat()
	current, otherErr := fresh.Stat()
	if err != nil || otherErr != nil || !privateDirectoryInfo(held) || !os.SameFile(held, current) {
		return errPersonalStorage
	}
	if s.lock != nil {
		held, err = s.lock.Stat()
		current, otherErr = privateRelativeInfo(s.directory, s.base+".lock")
		if err != nil || otherErr != nil || !privateFileInfo(held) || !privateFileInfo(current) || !os.SameFile(held, current) {
			return errPersonalStorage
		}
	}
	return s.checkForbidden()
}

func (s *personalSnapshotStorage) checkForbidden() error {
	for _, forbidden := range s.forbidden {
		if !filepath.IsAbs(forbidden) || filepath.Clean(forbidden) != forbidden {
			return errPersonalStorage
		}
		for _, candidate := range []string{forbidden, forbidden + ".lock"} {
			for _, name := range []string{s.base, s.base + ".lock"} {
				if candidate == filepath.Join(filepath.Dir(s.path), name) {
					return errPersonalStorage
				}
				// Resolve existing configured aliases without reading any data. Only
				// ENOENT is an acceptable absent configured target/lock.
				other, err := os.Stat(candidate)
				if errors.Is(err, os.ErrNotExist) {
					continue
				}
				if err != nil {
					return errPersonalStorage
				}
				own, err := privateRelativeInfo(s.directory, name)
				if err == nil && os.SameFile(own, other) {
					return errPersonalStorage
				}
			}
		}
	}
	return nil
}

func (s *personalSnapshotStorage) readFile() ([]byte, *os.File, error) {
	if err := s.validatePaths(); err != nil {
		return nil, nil, err
	}
	// Inspect BEFORE opening for data: no transient read from unsafe targets.
	before, err := privateRelativeInfo(s.directory, s.base)
	if err != nil || !privateFileInfo(before) || before.Size() > maxPersonalSnapshotBytes {
		return nil, nil, errPersonalStorage
	}
	fd, err := unix.Openat(int(s.directory.Fd()), s.base, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, nil, errPersonalStorage
	}
	file := os.NewFile(uintptr(fd), s.base)
	fail := func() ([]byte, *os.File, error) { file.Close(); return nil, nil, errPersonalStorage }
	opened, err := file.Stat()
	if err != nil || !privateFileInfo(opened) || !os.SameFile(before, opened) || opened.Size() > maxPersonalSnapshotBytes {
		return fail()
	}
	data, err := io.ReadAll(io.LimitReader(file, maxPersonalSnapshotBytes+1))
	if err != nil || len(data) > maxPersonalSnapshotBytes {
		return fail()
	}
	after, err := privateRelativeInfo(s.directory, s.base)
	held, heldErr := file.Stat()
	if err != nil || heldErr != nil || !privateFileInfo(after) || !privateFileInfo(held) || !os.SameFile(opened, after) || held.Size() != int64(len(data)) || s.validatePaths() != nil {
		return fail()
	}
	return data, file, nil
}

func newPersonalSnapshotStorage(path, namespace string, forbidden []string) (*personalSnapshotStorage, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == "/" || !validPersonalNamespace(namespace) {
		return nil, errPersonalStorage
	}
	directory, err := openPrivateDirectory(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	s := &personalSnapshotStorage{path: path, base: filepath.Base(path), namespace: namespace, forbidden: append([]string{}, forbidden...), directory: directory}
	success := false
	defer func() {
		if !success {
			_ = s.Close()
		}
	}()
	// Missing/corrupt initial files never cause provisioning, even of the lock.
	data, file, err := s.readFile()
	if err != nil {
		return nil, errPersonalStorage
	}
	_, parseErr := parsePersonalSnapshot(data, namespace)
	closeErr := file.Close()
	if parseErr != nil || closeErr != nil {
		return nil, errPersonalStorage
	}
	fd, err := unix.Openat(int(directory.Fd()), s.base+".lock", unix.O_RDWR|unix.O_CREAT|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return nil, errPersonalStorage
	}
	s.lock = os.NewFile(uintptr(fd), s.base+".lock")
	if s.validatePaths() != nil || unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB) != nil {
		return nil, errPersonalStorage
	}
	data, file, err = s.readFile()
	if err != nil {
		return nil, errPersonalStorage
	}
	snapshot, parseErr := parsePersonalSnapshot(data, namespace)
	syncErr := file.Sync()
	closeErr = file.Close()
	if parseErr != nil || syncErr != nil || closeErr != nil || s.lock.Sync() != nil || directory.Sync() != nil || s.validatePaths() != nil {
		return nil, errPersonalStorage
	}
	s.confirmed, s.contents, s.version = snapshot, data, personalStorageVersion(sha256.Sum256(data))
	success = true
	return s, nil
}

func (s *personalSnapshotStorage) Read() (personalStorageSnapshot, personalStorageVersion, error) {
	if s == nil {
		return personalStorageSnapshot{}, personalStorageVersion{}, errPersonalStorage
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.uncertain {
		return personalStorageSnapshot{}, personalStorageVersion{}, errPersonalStorageUncertain
	}
	if s.closed {
		return personalStorageSnapshot{}, personalStorageVersion{}, errPersonalStorage
	}
	return copyPersonalSnapshot(s.confirmed), s.version, nil
}

func (s *personalSnapshotStorage) checkDisk() error {
	data, file, err := s.readFile()
	if err != nil {
		return err
	}
	if file.Close() != nil {
		return errPersonalStorage
	}
	if !bytes.Equal(data, s.contents) {
		return errPersonalConflict
	}
	return nil
}

func (s *personalSnapshotStorage) hit(stage string) error {
	if s.failpoint != nil {
		return s.failpoint(stage)
	}
	return nil
}

// Commit is synchronous and serialized. No successful result precedes file and
// directory sync. Every error returns NO snapshot/version. Pre-rename failures
// leave the old confirmed RAM readable; attempted-rename errors and post-rename
// failures disable ALL reads and commits until validated locked reopen.
func (s *personalSnapshotStorage) Commit(expected personalStorageVersion, candidate personalStorageSnapshot, finalCheck func() error) (personalStorageSnapshot, personalStorageVersion, error) {
	if s == nil {
		return personalStorageSnapshot{}, personalStorageVersion{}, errPersonalStorage
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	fail := func(err error) (personalStorageSnapshot, personalStorageVersion, error) {
		return personalStorageSnapshot{}, personalStorageVersion{}, err
	}
	if s.uncertain {
		return fail(errPersonalStorageUncertain)
	}
	if s.closed {
		return fail(errPersonalStorage)
	}
	if finalCheck == nil {
		return fail(errPersonalInvalid)
	}
	if expected != s.version {
		return fail(errPersonalConflict)
	}
	if candidate.Namespace != s.namespace || candidate.StoreID != s.confirmed.StoreID {
		return fail(errPersonalInvalid)
	}
	contents, err := serializePersonalSnapshot(candidate)
	if err != nil {
		return fail(err)
	}
	// Parse our exact encoded candidate before any filesystem mutation; the
	// published value has canonical ordering and no caller-owned slices.
	snapshot, err := parsePersonalSnapshot(contents, s.namespace)
	if err != nil {
		return fail(err)
	}
	if err := s.checkDisk(); err != nil {
		return fail(err)
	}
	if s.hit("create") != nil {
		return fail(errPersonalStorage)
	}
	id, err := personalRandomID("")
	if err != nil {
		return fail(errPersonalStorage)
	}
	name := "." + s.base + ".tmp-" + id
	fd, err := unix.Openat(int(s.directory.Fd()), name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return fail(errPersonalStorage)
	}
	temporary := os.NewFile(uintptr(fd), name)
	info, statErr := temporary.Stat()
	defer temporary.Close()
	defer func() {
		// Only remove our own inode, relative to our held directory. Never scan
		// the directory or remove another writer's replacement temporary file.
		current, err := privateRelativeInfo(s.directory, name)
		if statErr == nil && err == nil && os.SameFile(info, current) {
			_ = unix.Unlinkat(int(s.directory.Fd()), name, 0)
		}
	}()
	if statErr != nil || !privateFileInfo(info) {
		return fail(errPersonalStorage)
	}
	if s.hit("write") != nil {
		return fail(errPersonalStorage)
	}
	if n, err := temporary.Write(contents); err != nil || n != len(contents) {
		return fail(errPersonalStorage)
	}
	if s.hit("filesync") != nil || temporary.Sync() != nil {
		return fail(errPersonalStorage)
	}
	if s.hit("close") != nil {
		return fail(errPersonalStorage)
	}
	if temporary.Close() != nil {
		return fail(errPersonalStorage)
	}
	if s.hit("recheck") != nil {
		return fail(errPersonalStorage)
	}
	if err := s.checkDisk(); err != nil {
		return fail(err)
	}
	current, err := privateRelativeInfo(s.directory, name)
	if err != nil || !privateFileInfo(current) || !os.SameFile(info, current) {
		return fail(errPersonalStorage)
	}
	if s.hit("finalcheck") != nil || s.hit("rename") != nil {
		return fail(errPersonalStorage)
	}
	if err := finalCheck(); err != nil {
		return fail(err)
	}
	uncertain := func() (personalStorageSnapshot, personalStorageVersion, error) {
		s.uncertain = true
		// Do not retain an authoritative-looking old private view after an
		// uncertain save; reads and writes remain closed even after Close.
		s.confirmed, s.contents, s.version = personalStorageSnapshot{}, nil, personalStorageVersion{}
		return fail(errPersonalStorageUncertain)
	}
	rename := unix.Renameat
	if s.rename != nil {
		rename = s.rename
	}
	if err := rename(int(s.directory.Fd()), name, int(s.directory.Fd()), s.base); err != nil {
		// A syscall error is not proof that the destination was unchanged (for
		// example, a lost filesystem acknowledgement). Never serve old RAM as
		// confirmation of rollback; locked reopen must reconcile the outcome.
		return uncertain()
	}
	if s.hit("afterrename") != nil || s.validatePaths() != nil {
		return uncertain()
	}
	current, err = privateRelativeInfo(s.directory, s.base)
	if err != nil || !privateFileInfo(current) || !os.SameFile(info, current) {
		return uncertain()
	}
	if s.hit("dirsync") != nil || s.directory.Sync() != nil || s.hit("publish") != nil {
		return uncertain()
	}
	s.confirmed, s.contents, s.version = snapshot, contents, personalStorageVersion(sha256.Sum256(contents))
	return copyPersonalSnapshot(snapshot), s.version, nil
}

func (s *personalSnapshotStorage) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	s.confirmed, s.contents = personalStorageSnapshot{}, nil
	var err error
	if s.lock != nil {
		err = s.lock.Close()
		s.lock = nil
	}
	if s.directory != nil {
		other := s.directory.Close()
		s.directory = nil
		if err == nil {
			err = other
		}
	}
	return err
}
