package tree

import (
	"errors"
	"sync"

	"github.com/joushou/g9p/protocol"
)

type Element interface {
	RLock()
	RUnlock()
	Lock()
	Unlock()
	Name() string
	Qid() protocol.Qid
	ApplyStat(protocol.Stat) error
	Stat() protocol.Stat
	Permissions() protocol.FileMode
	Open(user string, mode protocol.OpenMode) error
}

type File interface {
	Element
	SetContent([]byte)
	Content() []byte
}

type Dir interface {
	Element
	Empty() bool
	Create(name string, perms protocol.FileMode) (Element, error)
	Remove(Element) error
	Walk(func(Element))
	Find(name string) Element
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
			parent := parent.(Dir)
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

	return e.ApplyStat(ostat)
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
	permissions protocol.FileMode
}

func (t *RAMTree) Qid() protocol.Qid {
	return protocol.Qid{
		Type:    protocol.QTDIR,
		Version: 0,
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
	return nil
}

func (t *RAMTree) Stat() protocol.Stat {
	return protocol.Stat{
		Qid:  t.Qid(),
		Mode: t.permissions | protocol.DMDIR,
		Name: t.Name(),
		UID:  t.user,
		GID:  t.group,
		MUID: t.muser,
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

	return nil
}

func (t *RAMTree) Empty() bool {
	return (len(t.subtrees) + len(t.subfiles)) == 0
}

func (t *RAMTree) Create(name string, perms protocol.FileMode) (Element, error) {
	if t.Find(name) != nil {
		return nil, errors.New("file already exists")
	}

	var d Element
	if perms&protocol.DMDIR != 0 {
		perms = perms & (^protocol.FileMode(0777) | (t.permissions & 0777))
		d = NewRAMTree(name, perms, t.user, t.group)
	} else {
		perms = perms & (^protocol.FileMode(0666) | (t.permissions & 0666))
		d = NewRAMFile(name, perms, t.user, t.group)
	}

	t.Add(d)
	return d, nil
}

func (t *RAMTree) Add(other Element) error {
	switch other := other.(type) {
	case *RAMTree:
		t.subtrees = append(t.subtrees, other)
	case *RAMFile:
		t.subfiles = append(t.subfiles, other)
	default:
		return errors.New("unknown type")
	}
	return nil
}

func (t *RAMTree) Remove(other Element) error {
	switch other := other.(type) {
	case *RAMTree:
		for i := range t.subtrees {
			if t.subtrees[i] == other {
				t.subtrees = append(t.subtrees[:i], t.subtrees[i+1:]...)
				return nil
			}
		}
	case *RAMFile:
		for i := range t.subfiles {
			if t.subfiles[i] == other {
				t.subfiles = append(t.subfiles[:i], t.subfiles[i+1:]...)
				return nil
			}
		}
	}
	return errors.New("no such file")
}

func (t *RAMTree) Walk(cb func(Element)) {
	for i := range t.subtrees {
		cb(t.subtrees[i])
	}
	for i := range t.subfiles {
		cb(t.subfiles[i])
	}
}

func (t *RAMTree) Find(name string) (d Element) {
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
	}
}

type RAMFile struct {
	sync.RWMutex
	content     []byte
	id          uint64
	name        string
	user        string
	group       string
	muser       string
	permissions protocol.FileMode
}

func (f *RAMFile) Name() string {
	return f.name
}

func (f *RAMFile) Qid() protocol.Qid {
	return protocol.Qid{
		Type:    protocol.QTFILE,
		Version: 0,
		Path:    f.id,
	}
}

func (f *RAMFile) ApplyStat(s protocol.Stat) error {
	f.name = s.Name
	f.user = s.UID
	f.group = s.GID
	f.permissions = s.Mode
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

	return nil
}

func (f *RAMFile) Content() []byte {
	return f.content
}

func (f *RAMFile) SetContent(b []byte) {
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
	}
}
