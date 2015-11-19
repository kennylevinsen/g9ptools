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

type File interface {
	locker

	Open(user string, mode protocol.OpenMode) (OpenFile, error)
	Name() (string, error)
	Stat() (protocol.Stat, error)
	WriteStat(protocol.Stat) error
	Qid() (protocol.Qid, error)
	IsDir() (bool, error)
}

type Dir interface {
	File

	Find(name string) (File, error)
	Walk(func(File)) error
	Empty() (bool, error)

	Add(File) error
	Create(name string, perms protocol.FileMode) (File, error)
	Remove(File) error
}

type OpenFile interface {
	Seek(offset uint64) error
	Read(p []byte) (int, error)
	Write(p []byte) (int, error)
	Close() error
}

type FilePath []File

func (fp FilePath) Current() File {
	if len(fp) == 0 {
		return nil
	}
	return fp[len(fp)-1]
}

func (fp FilePath) Parent() File {
	if len(fp) == 0 {
		return nil
	} else if len(fp) == 1 {
		return fp[len(fp)-1]
	}
	return fp[len(fp)-2]
}

func setStat(user string, e File, parent File, nstat protocol.Stat) error {
	ostat, err := e.Stat()
	if err != nil {
		return err
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
			taken, err := parent.Find(nstat.Name)
			if err != nil {
				return err
			}
			if taken != nil {
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

	if needParentWrite {
		if parent != nil {
			x, err := parent.Open(user, protocol.OWRITE)
			if err != nil {
				return err
			}
			x.Close()
		}
	}

	if needWrite {
		x, err := parent.Open(user, protocol.OWRITE)
		if err != nil {
			return err
		}
		x.Close()
	}

	return e.WriteStat(ostat)
}
