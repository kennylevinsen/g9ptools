package ramtree

import (
	"errors"
	"sync"
	"time"

	"github.com/joushou/g9p/protocol"
	"github.com/joushou/g9ptools/fileserver"
)

var (
	globalIDLock sync.Mutex
	globalID     uint64 = 0
)

func nextID() uint64 {
	globalIDLock.Lock()
	defer globalIDLock.Unlock()
	id := globalID
	globalID++
	return id
}

func permCheck(owner bool, permissions protocol.FileMode, mode protocol.OpenMode) bool {
	var offset uint8
	if owner {
		offset = 6
	}

	switch mode & 3 {
	case protocol.OREAD:
		return permissions&(1<<(2+offset)) != 0
	case protocol.OWRITE:
		return permissions&(1<<(1+offset)) != 0
	case protocol.ORDWR:
		return (permissions&(1<<(2+offset)) != 0) && (permissions&(1<<(1+offset)) != 0)
	case protocol.OEXEC:
		return permissions&(1<<offset) != 0
	default:
		return false
	}
}

type RAMTree struct {
	sync.RWMutex
	subtrees    []*RAMTree
	subfiles    []*RAMFile
	id          uint64
	name        string
	user        string
	group       string
	muser       string
	version     uint32
	atime       time.Time
	mtime       time.Time
	permissions protocol.FileMode
}

func (t *RAMTree) Qid() protocol.Qid {
	return protocol.Qid{
		Type:    protocol.QTDIR,
		Version: t.version,
		Path:    t.id,
	}
}

func (t *RAMTree) Name() string {
	if t.name == "" {
		return "/"
	}
	return t.name
}

func (t *RAMTree) ApplyStat(s protocol.Stat) error {
	t.name = s.Name
	t.user = s.UID
	t.group = s.GID
	t.permissions = s.Mode
	t.atime = time.Now()
	t.mtime = time.Now()
	t.version++
	return nil
}

func (t *RAMTree) Stat() protocol.Stat {
	return protocol.Stat{
		Qid:   t.Qid(),
		Mode:  t.permissions | protocol.DMDIR,
		Name:  t.Name(),
		UID:   t.user,
		GID:   t.group,
		MUID:  t.muser,
		Atime: uint32(t.mtime.Unix()),
		Mtime: uint32(t.mtime.Unix()),
	}
}

func (t *RAMTree) Permissions() protocol.FileMode {
	return t.permissions
}

func (t *RAMTree) Open(user string, mode protocol.OpenMode) error {
	owner := t.user == user

	if !permCheck(owner, t.permissions, mode) {
		return errors.New("access denied")
	}

	t.atime = time.Now()

	return nil
}

func (t *RAMTree) Empty() bool {
	return (len(t.subtrees) + len(t.subfiles)) == 0
}

func (t *RAMTree) Create(name string, perms protocol.FileMode) (fileserver.Element, error) {
	if t.Find(name) != nil {
		return nil, errors.New("file already exists")
	}

	var d fileserver.Element
	if perms&protocol.DMDIR != 0 {
		perms = perms & (^protocol.FileMode(0777) | (t.permissions & 0777))
		x := NewRAMTree(name, perms, t.user, t.group)
		t.subtrees = append(t.subtrees, x)
		d = x
	} else {
		perms = perms & (^protocol.FileMode(0666) | (t.permissions & 0666))
		x := NewRAMFile(name, perms, t.user, t.group)
		t.subfiles = append(t.subfiles, x)
		d = x
	}

	t.mtime = time.Now()
	t.atime = t.mtime
	t.version++
	return d, nil
}

func (t *RAMTree) Remove(other fileserver.Element) error {
	switch other := other.(type) {
	case *RAMTree:
		for i := range t.subtrees {
			if t.subtrees[i] == other {
				t.subtrees = append(t.subtrees[:i], t.subtrees[i+1:]...)
				t.mtime = time.Now()
				t.atime = t.mtime
				t.version++
				return nil
			}
		}
	case *RAMFile:
		for i := range t.subfiles {
			if t.subfiles[i] == other {
				t.subfiles = append(t.subfiles[:i], t.subfiles[i+1:]...)
				t.mtime = time.Now()
				t.atime = t.mtime
				t.version++
				return nil
			}
		}
	}
	return errors.New("no such file")
}

func (t *RAMTree) Walk(cb func(fileserver.Element)) {
	t.atime = time.Now()
	for i := range t.subtrees {
		cb(t.subtrees[i])
	}
	for i := range t.subfiles {
		cb(t.subfiles[i])
	}
}

func (t *RAMTree) Find(name string) (d fileserver.Element) {
	t.atime = time.Now()
	for i := range t.subtrees {
		if t.subtrees[i].name == name {
			return t.subtrees[i]
		}
	}

	for i := range t.subfiles {
		if t.subfiles[i].name == name {
			return t.subfiles[i]
		}
	}

	return nil
}

func NewRAMTree(name string, permissions protocol.FileMode, user, group string) *RAMTree {
	return &RAMTree{
		name:        name,
		permissions: permissions,
		user:        user,
		group:       group,
		muser:       user,
		id:          nextID(),
		atime:       time.Now(),
		mtime:       time.Now(),
	}
}

type RAMOpenFile struct {
	offset int
	f      *RAMFile
}

func (of *RAMOpenFile) Seek(offset uint64) error {
	if of.f == nil {
		return errors.New("file not open")
	}
	of.f.RLock()
	defer of.f.RUnlock()
	if offset > len(f.content) {
		return errors.New("seek past length")
	}
	of.offset = int(offset)
}

func (of *RAMOpenFile) Read(p []byte) (int, error) {
	if of.f == nil {
		return errors.New("file not open")
	}
	of.f.RLock()
	defer of.f.RUnlock()
	maxRead := len(p)
	if maxRead > len(f.content)-of.offset {
		maxRead = len(f.content) - of.offset
	}

	copy(p, f.content[offset:maxRead+offset])
	of.offset += maxRead
	return maxRead, nil
}

func (of *RAMOpenFile) Write(p []byte) (int, error) {
	if of.f == nil {
		return errors.New("file not open")
	}

	// TODO(kl): handle append-only
	wlen := len(p)
	if wlen+of.offset > len(f.content) {
		b := make([]byte, wlen+of.offset)
		copy(b, f.content[:of.offset])
	}

	copy(b[of.offset], p)
	return wlen, nil
}

func (of *RAMOpenClose) Close() error {
	of.f = nil
	return nil
}

type RAMFile struct {
	sync.RWMutex
	content     []byte
	id          uint64
	name        string
	user        string
	group       string
	muser       string
	atime       time.Time
	mtime       time.Time
	version     uint32
	permissions protocol.FileMode
}

func (f *RAMFile) Name() string {
	return f.name
}

func (f *RAMFile) Qid() protocol.Qid {
	return protocol.Qid{
		Type:    protocol.QTFILE,
		Version: f.version,
		Path:    f.id,
	}
}

func (f *RAMFile) ApplyStat(s protocol.Stat) error {
	f.name = s.Name
	f.user = s.UID
	f.group = s.GID
	f.permissions = s.Mode
	f.mtime = time.Now()
	f.atime = f.mtime
	f.version++
	return nil
}

func (f *RAMFile) Stat() protocol.Stat {
	return protocol.Stat{
		Qid:    f.Qid(),
		Mode:   f.permissions,
		Name:   f.Name(),
		Length: uint64(len(f.content)),
		UID:    f.user,
		GID:    f.user,
		MUID:   f.user,
		Atime:  uint32(f.atime.Unix()),
		Mtime:  uint32(f.mtime.Unix()),
	}
}

func (f *RAMFile) Permissions() protocol.FileMode {
	return f.permissions
}

func (f *RAMFile) Open(user string, mode protocol.OpenMode) error {
	owner := f.user == user
	if !permCheck(owner, f.permissions, mode) {
		return errors.New("access denied")
	}

	f.atime = time.Now()

	return nil
}

func (f *RAMFile) Content() []byte {
	f.atime = time.Now()
	return f.content
}

func (f *RAMFile) SetContent(b []byte) {
	f.mtime = time.Now()
	f.atime = f.mtime
	f.version++
	f.content = b
}

func NewRAMFile(name string, permissions protocol.FileMode, user, group string) *RAMFile {
	return &RAMFile{
		name:        name,
		permissions: permissions,
		user:        user,
		group:       group,
		muser:       user,
		id:          nextID(),
		atime:       time.Now(),
		mtime:       time.Now(),
	}
}
