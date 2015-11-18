package fileserver

import (
	"errors"

	"github.com/joushou/g9p/protocol"
)

type locker interface {
	RLock()
	RUnlock()
	Lock()
	Unlock()
}

type Element interface {
	locker

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

func setStat(user string, e Element, parent Element, nstat protocol.Stat) error {
	ostat := e.Stat()

	write := parent.Open(user, protocol.OWRITE) == nil
	writeToParent := false
	if parent != nil {
		writeToParent = parent.Open(user, protocol.OWRITE) == nil
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
