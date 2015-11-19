package ramtree

import (
	"bytes"
	"errors"
	"sync"
	"time"

	"github.com/joushou/g9p/protocol"
	"github.com/joushou/g9ptools/fileserver"
)

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
	for _, i := range ot.t.tree {
		y, err := i.Stat()
		if err != nil {
			return err
		}
		y.Encode(buf)
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
	ot.t.Lock()
	defer ot.t.Unlock()
	ot.t.opens--
	ot.t = nil
	return nil
}

type RAMTree struct {
	sync.RWMutex
	parent fileserver.File
	tree        map[string]fileserver.File
	id          uint64
	name        string
	user        string
	group       string
	muser       string
	version     uint32
	atime       time.Time
	mtime       time.Time
	permissions protocol.FileMode
	opens       uint
}

func (t *RAMTree) Parent() (fileserver.File, error) {
	if t.parent == nil {
		return t, nil
	}
	return t.parent, nil
}

func (t *RAMTree) Qid() (protocol.Qid, error) {
	return protocol.Qid{
		Type:    protocol.QTDIR,
		Version: t.version,
		Path:    t.id,
	}, nil
}

func (t *RAMTree) Name() (string, error) {
	t.tree.Lock()
	defer t.tree.Unlock()
	if t.name == "" {
		return "/", nil
	}
	return t.name, nil
}

func (t *RAMTree) WriteStat(s protocol.Stat) error {
	t.tree.Lock()
	defer t.tree.Unlock()
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
	t.tree.Lock()
	defer t.tree.Unlock()
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
	t.tree.Lock()
	defer t.tree.Unlock()
	owner := t.user == user

	if !permCheck(owner, t.permissions, mode) {
		return nil, errors.New("access denied")
	}

	t.atime = time.Now()
	t.Lock()
	defer t.Unlock()
	t.opens++
	return &RAMOpenTree{t: t}, nil
}

func (t *RAMTree) Empty() (bool, error) {
	return len(t.tree) == 0, nil
}

func (t *RAMTree) Create(user, name string, perms protocol.FileMode) (fileserver.File, error) {
	t.tree.Lock()
	defer t.tree.Unlock()
	owner := t.user == user
	if !permCheck(owner, t.permissions, protocol.OWRITE) {
		return nil, errors.New("access denied")
	}

	_, ok := t.tree[name]
	if ok {
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

	t.tree[name] = d

	t.mtime = time.Now()
	t.atime = t.mtime
	t.version++
	return d, nil
}

func (t *RAMTree) Add(name string, f fileserver.File) error {
	t.tree.Lock()
	defer t.tree.Unlock()
	_, ok := t.tree[name]
	if ok {
		return errors.New("file already exists")
	}
	t.tree[name] = f
	t.mtime = time.Now()
	t.atime = t.mtime
	t.version++
	return nil
}

func (t *RAMTree) Rename(user, oldname, newname string) error {
	t.Lock()
	defer t.Unlock()
	f, ok := t.tree[oldname]
	if !ok {
		return errors.New("file not found")
	}
	_, ok = t.tree[newname]
	if !ok {
		return errors.New("file already exists")
	}

	owner := t.user == user
	if !permCheck(owner, t.permissions, protocol.OWRITE) {
		return errors.New("access denied")
	}

	t.tree[newname] = t.tree[oldname]
	delete(t.tree, oldname)
}

func (t *RAMTree) Remove(user string, other fileserver.File) error {
	t.tree.Lock()
	defer t.tree.Unlock()
	owner := t.user == user
	if !permCheck(owner, t.permissions, protocol.OWRITE) {
		return errors.New("access denied")
	}

	for i := range t.tree {
		if t.tree[i] == other {
			d, err := other.IsDir()
			if err != nil {
				return err
			}
			if d {
				e, err := other.(fileserver.Dir).Empty()
				if err != nil {
					return err
				}
				if !e {
					return errors.New("directory not empty")
				}
			}
			delete(t.tree, i)
			t.mtime = time.Now()
			t.atime = t.mtime
			t.version++
			return nil
		}
	}
	return errors.New("no such file")
}


func (t *RAMTree) Walk(user string, name string) (fileserver.File, error) {
	t.tree.Lock()
	defer t.tree.Unlock()
	owner := t.user == user
	if !permCheck(owner, t.permissions, protocol.OEXEC) {
		return nil, errors.New("access denied")
	}

	t.atime = time.Now()
	for i := range t.tree {
		if i == name {
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
