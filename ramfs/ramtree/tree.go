package ramtree

import (
	"bytes"
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

type RAMOpenTree struct {
	t      *RAMTree
	buffer []byte
	offset uint64
}

func (ot *RAMOpenTree) update() error {
	ot.t.RLock()
	defer ot.t.RUnlock()
	buf := new(bytes.Buffer)
	var e error
	ot.t.Walk(func(f fileserver.File) {
		if e != nil {
			return
		}
		y, err := f.Stat()
		if err != nil {
			e = err
			return
		}
		y.Encode(buf)
	})

	if e != nil {
		return e
	}
	ot.buffer = buf.Bytes()
	return nil
}

func (ot *RAMOpenTree) Seek(offset uint64) error {
	if ot.t == nil {
		return errors.New("file not open")
	}
	ot.t.RLock()
	defer ot.t.RUnlock()
	if offset != 0 && offset != ot.offset {
		return errors.New("can only seek to 0 on directory")
	}
	ot.offset = offset
	ot.update()
	ot.t.atime = time.Now()
	return nil
}

func (ot *RAMOpenTree) Read(p []byte) (int, error) {
	if ot.t == nil {
		return 0, errors.New("file not open")
	}
	ot.t.RLock()
	defer ot.t.RUnlock()
	rlen := uint64(len(p))
	if rlen > uint64(len(ot.buffer))-ot.offset {
		rlen = uint64(len(ot.buffer)) - ot.offset
	}
	copy(p, ot.buffer[ot.offset:rlen+ot.offset])
	ot.offset += rlen
	ot.t.atime = time.Now()
	return int(rlen), nil
}

func (ot *RAMOpenTree) Write(p []byte) (int, error) {
	return 0, errors.New("cannot write to directory")
}

func (ot *RAMOpenTree) Close() error {
	ot.t = nil
	return nil
}

type RAMTree struct {
	sync.RWMutex
	tree        []fileserver.File
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

func (t *RAMTree) Qid() (protocol.Qid, error) {
	return protocol.Qid{
		Type:    protocol.QTDIR,
		Version: t.version,
		Path:    t.id,
	}, nil
}

func (t *RAMTree) Name() (string, error) {
	if t.name == "" {
		return "/", nil
	}
	return t.name, nil
}

func (t *RAMTree) WriteStat(s protocol.Stat) error {
	t.name = s.Name
	t.user = s.UID
	t.group = s.GID
	t.permissions = s.Mode
	t.atime = time.Now()
	t.mtime = time.Now()
	t.version++
	return nil
}

func (t *RAMTree) Stat() (protocol.Stat, error) {
	q, err := t.Qid()
	if err != nil {
		return protocol.Stat{}, err
	}
	n, err := t.Name()
	if err != nil {
		return protocol.Stat{}, err
	}
	return protocol.Stat{
		Qid:   q,
		Mode:  t.permissions | protocol.DMDIR,
		Name:  n,
		UID:   t.user,
		GID:   t.group,
		MUID:  t.muser,
		Atime: uint32(t.atime.Unix()),
		Mtime: uint32(t.mtime.Unix()),
	}, nil
}

func (t *RAMTree) Open(user string, mode protocol.OpenMode) (fileserver.OpenFile, error) {
	owner := t.user == user

	if !permCheck(owner, t.permissions, mode) {
		return nil, errors.New("access denied")
	}

	t.atime = time.Now()
	return &RAMOpenTree{t: t}, nil
}

func (t *RAMTree) Empty() (bool, error) {
	return len(t.tree) == 0, nil
}

func (t *RAMTree) Create(name string, perms protocol.FileMode) (fileserver.File, error) {
	_, err := t.Find(name)
	if err != nil {
		return nil, errors.New("file already exists")
	}

	var d fileserver.File
	if perms&protocol.DMDIR != 0 {
		perms = perms & (^protocol.FileMode(0777) | (t.permissions & 0777))
		d = NewRAMTree(name, perms, t.user, t.group)
	} else {
		perms = perms & (^protocol.FileMode(0666) | (t.permissions & 0666))
		d = NewRAMFile(name, perms, t.user, t.group)
	}

	t.tree = append(t.tree, d)

	t.mtime = time.Now()
	t.atime = t.mtime
	t.version++
	return d, nil
}

func (t *RAMTree) Add(f fileserver.File) error {
	t.tree = append(t.tree, f)
	t.mtime = time.Now()
	t.atime = t.mtime
	t.version++
	return nil
}

func (t *RAMTree) Remove(other fileserver.File) error {
	for i := range t.tree {
		if t.tree[i] == other {
			t.tree = append(t.tree[:i], t.tree[i+1:]...)
			t.mtime = time.Now()
			t.atime = t.mtime
			t.version++
			return nil
		}
	}
	return errors.New("no such file")
}

func (t *RAMTree) Walk(cb func(fileserver.File)) error {
	t.atime = time.Now()
	for i := range t.tree {
		cb(t.tree[i])
	}
	return nil
}

func (t *RAMTree) Find(name string) (fileserver.File, error) {
	t.atime = time.Now()
	for i := range t.tree {
		n, err := t.tree[i].Name()
		if err != nil {
			return nil, err
		}
		if n == name {
			return t.tree[i], nil
		}
	}
	return nil, nil
}

func (t *RAMTree) IsDir() (bool, error) {
	return true, nil
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
	offset uint64
	f      *RAMFile
}

func (of *RAMOpenFile) Seek(offset uint64) error {
	if of.f == nil {
		return errors.New("file not open")
	}
	of.f.RLock()
	defer of.f.RUnlock()
	if offset > uint64(len(of.f.content)) {
		return errors.New("seek past length")
	}
	of.offset = uint64(offset)
	of.f.atime = time.Now()
	return nil
}

func (of *RAMOpenFile) Read(p []byte) (int, error) {
	if of.f == nil {
		return 0, errors.New("file not open")
	}
	of.f.RLock()
	defer of.f.RUnlock()
	maxRead := uint64(len(p))
	if maxRead > uint64(len(of.f.content))-of.offset {
		maxRead = uint64(len(of.f.content)) - of.offset
	}

	copy(p, of.f.content[of.offset:maxRead+of.offset])
	of.offset += maxRead
	of.f.atime = time.Now()
	return int(maxRead), nil
}

func (of *RAMOpenFile) Write(p []byte) (int, error) {
	if of.f == nil {
		return 0, errors.New("file not open")
	}

	// TODO(kl): handle append-only
	wlen := uint64(len(p))

	if wlen+of.offset > uint64(len(of.f.content)) {
		b := make([]byte, wlen+of.offset)
		copy(b, of.f.content[:of.offset])
		of.f.content = b
	}

	copy(of.f.content[of.offset:], p)

	of.offset += wlen
	of.f.mtime = time.Now()
	of.f.atime = of.f.mtime
	of.f.version++
	return int(wlen), nil
}

func (of *RAMOpenFile) Close() error {
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

func (f *RAMFile) Name() (string, error) {
	return f.name, nil
}

func (f *RAMFile) Qid() (protocol.Qid, error) {
	return protocol.Qid{
		Type:    protocol.QTFILE,
		Version: f.version,
		Path:    f.id,
	}, nil
}

func (f *RAMFile) WriteStat(s protocol.Stat) error {
	f.name = s.Name
	f.user = s.UID
	f.group = s.GID
	f.permissions = s.Mode
	f.mtime = time.Now()
	f.atime = f.mtime
	f.version++
	return nil
}

func (f *RAMFile) Stat() (protocol.Stat, error) {
	q, err := f.Qid()
	if err != nil {
		return protocol.Stat{}, err
	}
	n, err := f.Name()
	if err != nil {
		return protocol.Stat{}, err
	}
	return protocol.Stat{
		Qid:    q,
		Mode:   f.permissions,
		Name:   n,
		Length: uint64(len(f.content)),
		UID:    f.user,
		GID:    f.user,
		MUID:   f.user,
		Atime:  uint32(f.atime.Unix()),
		Mtime:  uint32(f.mtime.Unix()),
	}, nil
}

func (f *RAMFile) Open(user string, mode protocol.OpenMode) (fileserver.OpenFile, error) {
	owner := f.user == user
	if !permCheck(owner, f.permissions, mode) {
		return nil, errors.New("access denied")
	}

	f.atime = time.Now()

	return &RAMOpenFile{f: f}, nil
}

func (f *RAMFile) IsDir() (bool, error) {
	return false, nil
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
