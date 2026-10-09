package main

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"syscall"

	"golang.org/x/sys/unix"
)

var (
	errMetadataConflict  = errors.New("metadata changed; reload and review")
	errMetadataStorage   = errors.New("metadata storage unavailable")
	errMetadataUncertain = errors.New("save outcome uncertain; editing disabled until restart and reconciliation")
)

type serviceMetadataEditorView struct {
	metadata *ServiceMetadata
	bytes    []byte
	revision string
}

// ServiceMetadataEditor owns one existing file and one stable adjacent lock for
// its lifetime. Published views are immutable; polling cannot replace them.
// Live uncooperative external writers are unsupported, not race-proof CAS.
type ServiceMetadataEditor struct {
	config    *ServiceMetadataEditorConfig
	path      string
	base      string
	directory *os.File
	lock      *os.File
	mu        sync.Mutex
	view      atomic.Pointer[serviceMetadataEditorView]
	key       [32]byte
	disabled  bool
	// failpoint is test-only and runs only against temporary fixtures.
	failpoint func(string) error
}

func newServiceMetadataEditor(path string, config *ServiceMetadataEditorConfig) (*ServiceMetadataEditor, error) {
	if config == nil {
		return nil, nil
	} // Disabled: no open/create/lock/write.
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, errMetadataStorage
	}
	directoryPath := filepath.Dir(path)
	if err := validateEditorAncestors(directoryPath); err != nil {
		return nil, errMetadataStorage
	}
	fd, err := unix.Open(directoryPath, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, errMetadataStorage
	}
	e := &ServiceMetadataEditor{config: config, path: path, base: filepath.Base(path), directory: os.NewFile(uintptr(fd), directoryPath)}
	success := false
	defer func() {
		if !success {
			_ = e.Close()
		}
	}()
	info, err := e.directory.Stat()
	if err != nil || !safeEditorDirectory(info) {
		return nil, errMetadataStorage
	}
	// Existing target is validated before creating even the synchronization file.
	data, file, err := e.readFile()
	if err != nil {
		return nil, err
	}
	metadata, err := parseServiceMetadata(data)
	if err != nil {
		_ = file.Close()
		return nil, errors.New("metadata editor requires valid existing service metadata")
	}
	// Validate and sync the current file/directory on restart. This reconciles the
	// durable state before enabling writes; it does not automatically migrate it.
	err = file.Sync()
	closeErr := file.Close()
	if err != nil || closeErr != nil {
		return nil, errMetadataStorage
	}
	lockFD, err := unix.Openat(fd, e.base+".lock", unix.O_RDWR|unix.O_CREAT|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return nil, errMetadataStorage
	}
	e.lock = os.NewFile(uintptr(lockFD), e.base+".lock")
	lockInfo, err := e.lock.Stat()
	if err != nil || !safeEditorFile(lockInfo) {
		return nil, errMetadataStorage
	}
	if err := unix.Flock(lockFD, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return nil, errors.New("metadata editor is already locked or unavailable")
	}
	if err := e.directory.Sync(); err != nil {
		return nil, errMetadataStorage
	}
	// Re-read after acquiring lock: a cooperating writer may have completed while
	// startup was validating. Never start with an earlier unconfirmed view.
	data, file, err = e.readFile()
	if err != nil {
		return nil, err
	}
	metadata, err = parseServiceMetadata(data)
	syncErr := file.Sync()
	closeErr = file.Close()
	if err != nil || syncErr != nil || closeErr != nil || e.directory.Sync() != nil {
		return nil, errMetadataStorage
	}
	if _, err := rand.Read(e.key[:]); err != nil {
		return nil, errMetadataStorage
	}
	e.view.Store(&serviceMetadataEditorView{metadata: metadata, bytes: data, revision: e.digest("file", data)})
	success = true
	return e, nil
}

func safeEditorDirectory(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && info.IsDir() && int(stat.Uid) == os.Geteuid() && info.Mode().Perm()&0o700 == 0o700 && info.Mode().Perm()&0o022 == 0 && info.Mode()&(os.ModeSetuid|os.ModeSetgid) == 0
}
func safeEditorFile(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && info.Mode().IsRegular() && stat.Nlink == 1 && int(stat.Uid) == os.Geteuid() && info.Mode().Perm()&0o600 == 0o600 && info.Mode().Perm()&0o022 == 0 && info.Mode()&(os.ModeSetuid|os.ModeSetgid) == 0
}
func validateEditorAncestors(path string) error {
	for current := path; ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errMetadataStorage
		}
		// Sticky system temporary directories are acceptable ancestors, never the
		// dedicated editor directory itself. No permission repair is attempted.
		if info.Mode().Perm()&0o022 != 0 && info.Mode()&os.ModeSticky == 0 {
			return errMetadataStorage
		}
		if current == filepath.Dir(current) {
			break
		}
	}
	return nil
}

func (e *ServiceMetadataEditor) validateDirectoryAndLock() error {
	if e.directory == nil {
		return errMetadataStorage
	}
	if err := validateEditorAncestors(filepath.Dir(e.path)); err != nil {
		return err
	}
	held, err := e.directory.Stat()
	current, currentErr := os.Lstat(filepath.Dir(e.path))
	if err != nil || currentErr != nil || !safeEditorDirectory(current) || !os.SameFile(held, current) {
		return errMetadataStorage
	}
	if e.lock != nil {
		held, err = e.lock.Stat()
		current, currentErr = os.Lstat(e.path + ".lock")
		if err != nil || currentErr != nil || !safeEditorFile(current) || !os.SameFile(held, current) {
			return errMetadataStorage
		}
	}
	return nil
}

func (e *ServiceMetadataEditor) readFile() ([]byte, *os.File, error) {
	if err := e.validateDirectoryAndLock(); err != nil {
		return nil, nil, err
	}
	fd, err := unix.Openat(int(e.directory.Fd()), e.base, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, nil, errMetadataStorage
	}
	file := os.NewFile(uintptr(fd), e.base)
	info, err := file.Stat()
	if err != nil || !safeEditorFile(info) || info.Size() > maxServiceMetadataBytes {
		_ = file.Close()
		return nil, nil, errMetadataStorage
	}
	data, err := io.ReadAll(io.LimitReader(file, maxServiceMetadataBytes+1))
	if err != nil || len(data) > maxServiceMetadataBytes {
		_ = file.Close()
		return nil, nil, errMetadataStorage
	}
	return data, file, nil
}

func (e *ServiceMetadataEditor) checkDisk(view *serviceMetadataEditorView) error {
	data, file, err := e.readFile()
	if err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return errMetadataStorage
	}
	if !bytes.Equal(data, view.bytes) {
		return errMetadataConflict
	}
	return nil
}

func (e *ServiceMetadataEditor) Close() error {
	if e == nil {
		return nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.disabled = true
	var err error
	if e.lock != nil {
		err = e.lock.Close()
		e.lock = nil
	} // Never unlink/replace lock inode.
	if e.directory != nil {
		closeErr := e.directory.Close()
		e.directory = nil
		if err == nil {
			err = closeErr
		}
	}
	return err
}

func (e *ServiceMetadataEditor) digest(purpose string, value []byte) string {
	mac := hmac.New(sha256.New, e.key[:])
	_, _ = mac.Write([]byte(purpose + "\x00"))
	_, _ = mac.Write(value)
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (e *ServiceMetadataEditor) requestData(data *CacheData) *CacheData {
	if e == nil || data == nil {
		return data
	}
	local := *data
	local.ServiceMetadata = e.view.Load().metadata
	return &local
}

func mutateServiceMetadata(document serviceMetadataDocument, id int, name, link, icon string, reset bool) (serviceMetadataDocument, error) {
	if !reset {
		if name != "" {
			if _, err := validateServiceMetadataName(name); err != nil {
				return serviceMetadataDocument{}, err
			}
		}
		if link != "" {
			if _, err := validateServiceMetadataURL(link); err != nil {
				return serviceMetadataDocument{}, err
			}
		}
		if icon != "" && !validServiceIcon(icon) {
			return serviceMetadataDocument{}, errors.New("invalid icon")
		}
	}
	updated := serviceMetadataDocument{Version: serviceMetadataVersionV3, Services: make([]serviceMetadataEntry, 0, len(document.Services)+1)}
	selected := serviceMetadataEntry{ProxyHostID: id}
	for _, entry := range document.Services {
		if entry.ProxyHostID == id {
			selected = entry
		} else {
			updated.Services = append(updated.Services, entry)
		}
	}
	selected.Name, selected.URL, selected.Icon = nil, nil, nil
	selected.NameNull, selected.URLNull = false, false
	if !reset {
		if name != "" {
			selected.Name = &name
		}
		if link != "" {
			selected.URL = &link
		}
		if icon != "" {
			selected.Icon = &icon
		}
	}
	if selected.Name != nil || selected.URL != nil || selected.Icon != nil || selected.Category != nil || selected.Order != nil {
		updated.Services = append(updated.Services, selected)
	}
	sort.Slice(updated.Services, func(i, j int) bool { return updated.Services[i].ProxyHostID < updated.Services[j].ProxyHostID })
	return updated, nil
}

// commit runs under the writer mutex. finalCheck uses only the current in-memory
// complete authorization snapshot, immediately before rename. No cache lock or
// upstream call participates, and no transactional revocation guarantee exists.
func (e *ServiceMetadataEditor) commit(expected string, document serviceMetadataDocument, finalCheck func() error) error {
	if e.disabled {
		return errMetadataUncertain
	}
	old := e.view.Load()
	if !hmac.Equal([]byte(expected), []byte(old.revision)) {
		return errMetadataConflict
	}
	if err := e.checkDisk(old); err != nil {
		return err
	}
	contents, err := serializeServiceMetadataDocumentV3(document)
	if err != nil {
		return err
	}
	metadata, err := parseServiceMetadata(contents)
	if err != nil {
		return err
	}
	hit := func(stage string) error {
		if e.failpoint != nil {
			return e.failpoint(stage)
		}
		return nil
	}
	if err := hit("create"); err != nil {
		return errMetadataStorage
	}
	// The dedicated directory was validated and is owned by this process user.
	// CreateTemp is exclusive and 0600; existing file/parent modes are never fixed.
	temporary, err := os.CreateTemp(filepath.Dir(e.path), "."+e.base+".tmp-*")
	if err != nil {
		return errMetadataStorage
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	defer temporary.Close()
	if err := hit("write"); err != nil {
		return errMetadataStorage
	}
	if _, err := temporary.Write(contents); err != nil {
		return errMetadataStorage
	}
	if err := hit("filesync"); err != nil {
		return errMetadataStorage
	}
	if err := temporary.Sync(); err != nil {
		return errMetadataStorage
	}
	if err := hit("close"); err != nil {
		return errMetadataStorage
	}
	if err := temporary.Close(); err != nil {
		return errMetadataStorage
	}
	if err := hit("recheck"); err != nil {
		return errMetadataStorage
	}
	if err := e.checkDisk(old); err != nil {
		return err
	}
	if err := hit("rename"); err != nil {
		return errMetadataStorage
	}
	if err := finalCheck(); err != nil {
		return err
	}
	// Use held directory descriptor so replacement stays adjacent even if a path
	// is interfered with; checkDisk also verifies the held directory/lock identity.
	if err := unix.Renameat(int(e.directory.Fd()), filepath.Base(temporaryPath), int(e.directory.Fd()), e.base); err != nil {
		return errMetadataStorage
	}
	if err := hit("dirsync"); err != nil {
		e.disabled = true
		return errMetadataUncertain
	}
	if err := e.directory.Sync(); err != nil {
		e.disabled = true
		return errMetadataUncertain
	}
	e.view.Store(&serviceMetadataEditorView{metadata: metadata, bytes: contents, revision: e.digest("file", contents)})
	return nil
}

// Only target/default fields affect the opaque form revision, not timestamps,
// route-state/health or unrelated catalog entries. Backend fields never leave API.
func (e *ServiceMetadataEditor) targetRevision(host ProxyHost) string {
	bytes, _ := json.Marshal(struct {
		ID      int
		Domains []string
		Scheme  string
		Host    string
		Port    int
		Enabled bool
	}{host.ID, host.DomainNames, host.ForwardScheme, host.ForwardHost, host.ForwardPort, host.Enabled})
	return e.digest("target", bytes)
}

func editorErrorIsConflict(err error) bool { return errors.Is(err, errMetadataConflict) }

// No serialized generation/fingerprint, tombstone or separate store exists.
// Positive NPM IDs remain a presentation association; operators must remove
// obsolete entries when deleting/repurposing upstream IDs.
