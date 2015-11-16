package tree

import (
	"errors"
	"sync"

	"github.com/joushou/g9p/protocol"
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

func SetStat(user string, e Element, parent Element, nstat protocol.Stat) error {
	ostat := e.Stat()

	write := permCheck(user == ostat.UID, e.Permissions(), protocol.OWRITE)
	writeToParent := false
	if parent != nil {
		s := parent.Stat()
		writeToParent = permCheck(user == s.UID, parent.Permissions(), protocol.OWRITE)
	}

	needWrite := false
	needParentWrite := false

	if nstat.Type != ^uint16(0) && nstat.Type != ostat.Type {
		return errors.New("it is illegal to modify type")
	}
	if nstat.Dev != ^uint32(0) && nstat.Dev != ostat.Dev {
		return errors.New("it is illegal to modify dev")
	}
	if nstat.Mode != ^protocol.FileMode(0) && nstat.Mode != ostat.Mode {
		// TODO Ensure we don't flip DMDIR
		if user != ostat.UID {
			return errors.New("only owner can change mode")
		}
		ostat.Mode = ostat.Mode&protocol.DMDIR | nstat.Mode & ^protocol.DMDIR
	}
	if nstat.Atime != ^uint32(0) && nstat.Atime != ostat.Atime {
		return errors.New("it is illegal to modify atime")
	}
	if nstat.Mtime != ^uint32(0) && nstat.Mtime != ostat.Mtime {
		if user != ostat.UID {
			return errors.New("only owner can change mtime")
		}
		needWrite = true
		ostat.Mtime = nstat.Mtime
	}
	if nstat.Length != ^uint64(0) && nstat.Length != ostat.Length {
		return errors.New("change of not permitted")
	}
	if nstat.Name != "" && nstat.Name != ostat.Name {
		if parent != nil {
			parent := parent.(*Tree)
			if e := parent.Find(nstat.Name); e != nil {
				return errors.New("name already taken")
			}
			ostat.Name = nstat.Name
		} else {
			return errors.New("it is illegal to rename root")
		}
		needParentWrite = true
	}
	if nstat.UID != "" && nstat.UID != ostat.UID {
		// NOTE: It is normally illegal to change the file owner, but we are a bit more relaxed.
		ostat.UID = nstat.UID
		needWrite = true
	}
	if nstat.GID != "" && nstat.GID != ostat.GID {
		ostat.GID = nstat.GID
		needWrite = true
	}
	if nstat.MUID != "" && nstat.MUID != ostat.MUID {
		return errors.New("it is illegal to modify muid")
	}

	if needParentWrite && !writeToParent {
		return errors.New("write permissions required to parent directory")
	}

	if needWrite && !write {
		return errors.New("write permissions required to element")
	}

	x := e.(interface {
		ApplyStat(protocol.Stat) error
	})

	return x.ApplyStat(ostat)
}

type Tree struct {
	sync.RWMutex
	subtrees    []*Tree
	subfiles    []*File
	id          uint64
	name        string
	user        string
	group       string
	muser       string
	permissions protocol.FileMode
}

func (t *Tree) Qid() protocol.Qid {
	return protocol.Qid{
		Type:    protocol.QTDIR,
		Version: 0,
		Path:    t.id,
	}
}

func (t *Tree) Name() string {
	if t.name == "" {
		return "/"
	}
	return t.name
}

func (t *Tree) ApplyStat(s protocol.Stat) error {
	t.name = s.Name
	t.user = s.UID
	t.group = s.GID
	t.permissions = s.Mode
	return nil
}

func (t *Tree) Stat() protocol.Stat {
	return protocol.Stat{
		Qid:  t.Qid(),
		Mode: t.permissions | protocol.DMDIR,
		Name: t.Name(),
		UID:  t.user,
		GID:  t.group,
		MUID: t.muser,
	}
}

func (t *Tree) Permissions() protocol.FileMode {
	return t.permissions
}

func (t *Tree) Open(user string, mode protocol.OpenMode) error {
	owner := t.user == user

	if !permCheck(owner, t.permissions, mode) {
		return errors.New("access denied")
	}

	return nil
}

func (t *Tree) Empty() bool {
	return (len(t.subtrees) + len(t.subfiles)) == 0
}

func (t *Tree) Add(other Element) error {
	switch other := other.(type) {
	case *Tree:
		t.subtrees = append(t.subtrees, other)
	case *File:
		t.subfiles = append(t.subfiles, other)
	default:
		return errors.New("unknown type")
	}
	return nil
}

func (t *Tree) Remove(other Element) bool {
	switch other := other.(type) {
	case *Tree:
		for i := range t.subtrees {
			if t.subtrees[i] == other {
				t.subtrees = append(t.subtrees[:i], t.subtrees[i+1:]...)
				return true
			}
		}
	case *File:
		for i := range t.subfiles {
			if t.subfiles[i] == other {
				t.subfiles = append(t.subfiles[:i], t.subfiles[i+1:]...)
				return true
			}
		}
	}
	return false
}

func (t *Tree) Walk(cb func(Element)) {
	for i := range t.subtrees {
		cb(t.subtrees[i])
	}
	for i := range t.subfiles {
		cb(t.subfiles[i])
	}
}

func (t *Tree) Find(name string) (d Element) {
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

func NewTree(name string, permissions protocol.FileMode, user string) *Tree {
	return &Tree{
		name:        name,
		permissions: permissions,
		user:        user,
		group:       user,
		muser:       user,
		id:          nextID(),
	}
}

type File struct {
	sync.RWMutex
	content     []byte
	id          uint64
	name        string
	user        string
	group       string
	muser       string
	permissions protocol.FileMode
}

func (f *File) Name() string {
	return f.name
}

func (f *File) Qid() protocol.Qid {
	return protocol.Qid{
		Type:    protocol.QTFILE,
		Version: 0,
		Path:    f.id,
	}
}

func (f *File) ApplyStat(s protocol.Stat) error {
	f.name = s.Name
	f.user = s.UID
	f.group = s.GID
	f.permissions = s.Mode
	return nil
}

func (f *File) Stat() protocol.Stat {
	return protocol.Stat{
		Qid:    f.Qid(),
		Mode:   f.permissions,
		Name:   f.Name(),
		Length: uint64(len(f.content)),
		UID:    f.user,
		GID:    f.user,
		MUID:   f.user,
	}
}

func (f *File) Permissions() protocol.FileMode {
	return f.permissions
}

func (f *File) Open(user string, mode protocol.OpenMode) error {
	owner := f.user == user
	if !permCheck(owner, f.permissions, mode) {
		return errors.New("access denied")
	}

	return nil
}

func (f *File) Content() []byte {
	return f.content
}

func (f *File) SetContent(b []byte) {
	f.content = b
}

func NewFile(name string, permissions protocol.FileMode, user string) *File {
	return &File{
		name:        name,
		permissions: permissions,
		user:        user,
		group:       user,
		muser:       user,
		id:          nextID(),
	}
}

type Element interface {
	Name() string
	Qid() protocol.Qid
	Stat() protocol.Stat
	Permissions() protocol.FileMode
	Open(user string, mode protocol.OpenMode) error
}

type ElementSlice []Element

func (es ElementSlice) Last() Element {
	if len(es) == 0 {
		return nil
	}
	return es[len(es)-1]
}

func (es ElementSlice) Parent() Element {
	if len(es) == 0 {
		return nil
	}
	if len(es) == 1 {
		return es[len(es)-1]
	}
	return es[len(es)-2]
}
