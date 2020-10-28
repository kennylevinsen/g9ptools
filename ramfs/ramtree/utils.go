package ramtree

import (
	"sync"

	"github.com/kennylevinsen/g9p/protocol"
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
