package fileserver

import (
	"bytes"
	"fmt"
	"log"
	"sync"

	"github.com/joushou/g9p"
	"github.com/joushou/g9p/protocol"
)

const (
	DefaultMaxSize = (1024 * 1024 * 1024)
)

type State struct {
	sync.RWMutex
	location ElementSlice

	open     bool
	mode     protocol.OpenMode
	service  string
	username string
}

type FileServer struct {
	sync.RWMutex
	Roots  map[string]Dir
	Root   Dir
	Chatty bool

	MaxSize uint32
	fidLock sync.RWMutex
	Fids    map[protocol.Fid]*State
	tagLock sync.Mutex
	tags    map[protocol.Tag]bool
}

func (fs *FileServer) register(d protocol.Message) error {
	fs.tagLock.Lock()
	defer fs.tagLock.Unlock()

	t := d.GetTag()
	if _, ok := fs.tags[t]; ok {
		return fmt.Errorf("tag already in use")
	}

	fs.tags[t] = true
	return nil
}

func (fs *FileServer) flush(t protocol.Tag) {
	fs.tagLock.Lock()
	defer fs.tagLock.Unlock()

	if _, ok := fs.tags[t]; ok {
		delete(fs.tags, t)
	}
}

func (fs *FileServer) flushed(d protocol.Message) bool {
	fs.tagLock.Lock()
	defer fs.tagLock.Unlock()

	t := d.GetTag()
	if _, ok := fs.tags[t]; ok {
		delete(fs.tags, t)
		return false
	}
	return true
}

func (fs *FileServer) Version(r *protocol.VersionRequest) (resp *protocol.VersionResponse, err error) {
	fs.register(r)
	defer func() {
		if fs.flushed(r) {
			resp = nil
			err = g9p.ErrFlushed
		}
	}()

	if fs.Chatty {
		log.Printf("-> Version request")
	}

	fs.Lock()
	defer fs.Unlock()
	if r.MaxSize < DefaultMaxSize {
		fs.MaxSize = r.MaxSize
	} else {
		fs.MaxSize = DefaultMaxSize
	}

	proto := "9P2000"
	if r.Version != "9P2000" {
		proto = "unknown"
	}

	resp = &protocol.VersionResponse{
		MaxSize: fs.MaxSize,
		Version: proto,
	}

	return resp, nil
}

func (fs *FileServer) Auth(r *protocol.AuthRequest) (resp *protocol.AuthResponse, err error) {
	fs.register(r)
	defer func() {
		if fs.flushed(r) {
			resp = nil
			err = g9p.ErrFlushed
		}
	}()

	if fs.Chatty {
		log.Printf("-> Auth request")
	}

	return nil, fmt.Errorf("auth not supported")
}

func (fs *FileServer) Attach(r *protocol.AttachRequest) (resp *protocol.AttachResponse, err error) {
	fs.register(r)
	defer func() {
		if fs.flushed(r) {
			resp = nil
			err = g9p.ErrFlushed
		}
	}()

	if fs.Chatty {
		log.Printf("-> Attach request: %s, %s", r.Username, r.Service)
	}
	fs.fidLock.Lock()
	defer fs.fidLock.Unlock()

	if _, ok := fs.Fids[r.Fid]; ok {
		return nil, fmt.Errorf("fid already in use")
	}

	root := fs.Root

	if root == nil {
		if x, ok := fs.Roots[r.Service]; ok {
			root = x
		} else {
			return nil, fmt.Errorf("no such service")
		}
	}

	s := &State{
		service:  r.Service,
		username: r.Username,
		location: ElementSlice{root},
	}

	fs.Fids[r.Fid] = s

	resp = &protocol.AttachResponse{
		Qid: s.location.Last().Qid(),
	}

	return resp, nil
}

func (fs *FileServer) Flush(r *protocol.FlushRequest) (resp *protocol.FlushResponse, err error) {
	fs.register(r)
	defer func() {
		if fs.flushed(r) {
			resp = nil
			err = g9p.ErrFlushed
		}
	}()

	if fs.Chatty {
		log.Printf("-> Flush request: %d", r.OldTag)
	}

	fs.flush(r.OldTag)

	resp = &protocol.FlushResponse{}

	return resp, nil
}

func (fs *FileServer) Walk(r *protocol.WalkRequest) (resp *protocol.WalkResponse, err error) {
	fs.register(r)
	defer func() {
		if fs.flushed(r) {
			resp = nil
			err = g9p.ErrFlushed
		}
	}()

	if fs.Chatty {
		log.Printf("-> Walk request: %v", r.Names)
	}

	fs.fidLock.Lock()
	defer fs.fidLock.Unlock()
	s, ok := fs.Fids[r.Fid]
	if !ok {
		return nil, fmt.Errorf("no such fid")
	}

	s.Lock()
	defer s.Unlock()

	if _, ok = fs.Fids[r.NewFid]; ok {
		return nil, fmt.Errorf("fid already in use")
	}

	if len(r.Names) == 0 {
		x := &State{
			service:  s.service,
			username: s.username,
			location: s.location,
		}
		fs.Fids[r.NewFid] = x

		resp := &protocol.WalkResponse{}
		return resp, nil
	}

	root, ok := s.location.Last().(Dir)
	if !ok {
		return nil, fmt.Errorf("fid not dir")
	}

	newloc := s.location

	var qids []protocol.Qid
	for i := range r.Names {
		// This can cause multiple RLock's being held on the same tree, but that
		// doesn't matter, and they should all be unlocked at the end by the
		// defer.
		root.RLock()
		defer root.RUnlock()

		if err := root.Open(s.username, protocol.OEXEC); err != nil {
			log.Printf("Open failed: %v", err)
			goto write
		}

		var d Element
		var istree bool

		addToLoc := true
		name := r.Names[i]
		switch name {
		case ".":
			// This is a nop, but we should still report the result
			d = root
			addToLoc = false
			_, istree = d.(Dir)
		case "..":
			// Go one directory up, or nop if we're at /
			d = newloc.Parent()
			if len(newloc) > 1 {
				newloc = newloc[:len(newloc)-1]
				addToLoc = false
			}
			_, istree = d.(Dir)
		default:
			// Try to find the file
			d = root.Find(name)
			_, istree = d.(Dir)
			if d == nil {
				goto write
			}
		}

		if addToLoc {
			newloc = append(newloc, d)
		}
		qids = append(qids, d.Qid())

		if i >= len(r.Names)-1 {
			s := &State{
				service:  s.service,
				username: s.username,
				location: newloc,
			}
			fs.Fids[r.NewFid] = s
		}
		if !istree {
			goto write
		}

		root = d.(Dir)

	}

write:
	resp = &protocol.WalkResponse{
		Qids: qids,
	}

	return resp, nil
}

func (fs *FileServer) Open(r *protocol.OpenRequest) (resp *protocol.OpenResponse, err error) {
	fs.register(r)
	defer func() {
		if fs.flushed(r) {
			resp = nil
			err = g9p.ErrFlushed
		}
	}()

	if fs.Chatty {
		log.Printf("-> Open request")
	}

	fs.fidLock.RLock()
	defer fs.fidLock.RUnlock()
	s, ok := fs.Fids[r.Fid]
	if !ok {
		return nil, fmt.Errorf("no such fid")
	}

	s.Lock()
	defer s.Unlock()

	if s.open {
		return nil, fmt.Errorf("already open")
	}

	l := s.location.Last()
	if err := l.Open(s.username, r.Mode); err != nil {
		return nil, err
	}
	s.open = true
	s.mode = r.Mode
	resp = &protocol.OpenResponse{
		Qid: l.Qid(),
	}

	return resp, nil

}

func (fs *FileServer) Create(r *protocol.CreateRequest) (resp *protocol.CreateResponse, err error) {
	fs.register(r)
	defer func() {
		if fs.flushed(r) {
			resp = nil
			err = g9p.ErrFlushed
		}
	}()

	if fs.Chatty {
		log.Printf("-> Create request: %v", r.Name)
	}

	fs.fidLock.RLock()
	defer fs.fidLock.RUnlock()
	s, ok := fs.Fids[r.Fid]
	if !ok {
		return nil, fmt.Errorf("no such fid")
	}

	s.Lock()
	defer s.Unlock()

	if s.open {
		return nil, fmt.Errorf("already open")
	}

	if r.Name == "." || r.Name == ".." {
		return nil, fmt.Errorf("illegal name")
	}

	t, ok := s.location.Last().(Dir)
	if !ok {
		return nil, fmt.Errorf("not a directory")
	}

	t.Lock()
	defer t.Unlock()

	if d := t.Find(r.Name); d != nil {
		return nil, fmt.Errorf("file already exists")
	}

	if err := t.Open(s.username, protocol.OWRITE); err != nil {
		return nil, fmt.Errorf("could not open directory for writing")
	}

	l, err := t.Create(r.Name, r.Permissions)
	if err != nil {
		return nil, err
	}

	s.location = append(s.location, l)

	if err := l.Open(s.username, r.Mode); err != nil {
		return nil, err
	}

	s.open = true
	s.mode = r.Mode
	resp = &protocol.CreateResponse{
		Qid: l.Qid(),
	}

	return resp, nil
}

func (fs *FileServer) Read(r *protocol.ReadRequest) (resp *protocol.ReadResponse, err error) {
	fs.register(r)
	defer func() {
		if fs.flushed(r) {
			resp = nil
			err = g9p.ErrFlushed
		}
	}()

	if fs.Chatty {
		log.Printf("-> Read request")
	}

	fs.fidLock.RLock()
	defer fs.fidLock.RUnlock()
	s, ok := fs.Fids[r.Fid]
	if !ok {
		return nil, fmt.Errorf("no such fid")
	}

	s.RLock()
	defer s.RUnlock()

	if !s.open {
		return nil, fmt.Errorf("file not open")
	}

	if s.mode != protocol.OREAD && s.mode != protocol.ORDWR {
		return nil, fmt.Errorf("file not opened for reading")
	}

	var data []byte

	switch x := s.location.Last().(type) {
	case Dir:
		buf := new(bytes.Buffer)
		x.RLock()
		defer x.RUnlock()
		x.Walk(func(e Element) {
			y := e.Stat()
			y.Encode(buf)
		})
		data = buf.Bytes()
	case File:
		x.RLock()
		defer x.RUnlock()
		data = x.Content()
	default:
		return nil, fmt.Errorf("unexpected error")
	}

	var max uint64
	if r.Offset > uint64(len(data)) {
		data = nil
		goto write
	}
	max = uint64(len(data)) - r.Offset
	if uint64(r.Count) < max {
		max = uint64(r.Count)
	}

	data = data[r.Offset : r.Offset+max]
write:
	resp = &protocol.ReadResponse{
		Data: data,
	}

	// Ensure that we obey the negotiated maxsize!
	if resp.EncodedLength()+protocol.HeaderSize > int(fs.MaxSize) {
		diff := r.EncodedLength() + protocol.HeaderSize - int(fs.MaxSize)
		resp.Data = resp.Data[:len(resp.Data)-diff]
	}

	return resp, nil
}

func (fs *FileServer) Write(r *protocol.WriteRequest) (resp *protocol.WriteResponse, err error) {
	fs.register(r)
	defer func() {
		if fs.flushed(r) {
			resp = nil
			err = g9p.ErrFlushed
		}
	}()

	if fs.Chatty {
		log.Printf("-> Write request")
	}

	fs.fidLock.RLock()
	defer fs.fidLock.RUnlock()
	s, ok := fs.Fids[r.Fid]
	if !ok {
		return nil, fmt.Errorf("no such fid")
	}

	s.RLock()
	defer s.RUnlock()

	if !s.open {
		return nil, fmt.Errorf("file not open")
	}

	if s.mode != protocol.OWRITE && s.mode != protocol.ORDWR {
		return nil, fmt.Errorf("file not opened for writing")
	}

	switch x := s.location.Last().(type) {
	case Dir:
		return nil, fmt.Errorf("cannot write to directory")
	case File:
		x.Lock()
		defer x.Unlock()
		c := x.Content()
		offset := r.Offset
		if x.Permissions()&1<<30 != 0 {
			offset = uint64(len(c) - 1)
		}

		if offset+uint64(len(r.Data)) > uint64(cap(c)) {
			old := c
			c = make([]byte, offset+uint64(len(r.Data)))
			x.SetContent(c)

			// Copy, but don't copy data we're about to override
			copy(c, old[:offset])
		}

		copy(c[offset:], r.Data)

		resp = &protocol.WriteResponse{
			Count: uint32(len(r.Data)),
		}

		return resp, nil
	}

	return nil, fmt.Errorf("unexpected error")
}

func (fs *FileServer) Clunk(r *protocol.ClunkRequest) (resp *protocol.ClunkResponse, err error) {
	fs.register(r)
	defer func() {
		if fs.flushed(r) {
			resp = nil
			err = g9p.ErrFlushed
		}
	}()

	if fs.Chatty {
		log.Printf("-> Clunk request")
	}

	fs.fidLock.Lock()
	defer fs.fidLock.Unlock()
	s, ok := fs.Fids[r.Fid]
	if !ok {
		return nil, fmt.Errorf("no such fid")
	}

	s.Lock()
	defer s.Unlock()

	delete(fs.Fids, r.Fid)
	return &protocol.ClunkResponse{}, nil
}

func (fs *FileServer) Remove(r *protocol.RemoveRequest) (resp *protocol.RemoveResponse, err error) {
	fs.register(r)
	defer func() {
		if fs.flushed(r) {
			resp = nil
			err = g9p.ErrFlushed
		}
	}()

	if fs.Chatty {
		log.Printf("-> Remove request")
	}

	fs.fidLock.Lock()
	defer fs.fidLock.Unlock()
	s, ok := fs.Fids[r.Fid]
	if !ok {
		return nil, fmt.Errorf("no such fid")
	}
	s.Lock()
	defer s.Unlock()

	var l, p Element

	// We're not going to remove /.
	if len(s.location) <= 1 {
		goto write
	}

	// Attempt to delete it.
	l = s.location.Last()

	if x, ok := l.(Dir); ok {
		if !x.Empty() {
			goto write
		}
	}

	p = s.location.Parent()
	if err := p.Open(s.username, protocol.OWRITE); err != nil {
		goto write
	}
	p.(Dir).Remove(l)

write:
	delete(fs.Fids, r.Fid)
	return &protocol.RemoveResponse{}, nil
}

func (fs *FileServer) Stat(r *protocol.StatRequest) (resp *protocol.StatResponse, err error) {
	fs.register(r)
	defer func() {
		if fs.flushed(r) {
			resp = nil
			err = g9p.ErrFlushed
		}
	}()

	if fs.Chatty {
		log.Printf("-> Stat request")
	}

	fs.fidLock.RLock()
	defer fs.fidLock.RUnlock()
	s, ok := fs.Fids[r.Fid]
	if !ok {
		return nil, fmt.Errorf("no such fid")
	}

	s.RLock()
	defer s.RUnlock()

	l := s.location.Last()
	if l == nil {
		return nil, fmt.Errorf("no such file")
	}

	resp = &protocol.StatResponse{
		Stat: l.Stat(),
	}

	return resp, nil
}

func (fs *FileServer) WriteStat(r *protocol.WriteStatRequest) (resp *protocol.WriteStatResponse, err error) {
	fs.register(r)
	defer func() {
		if fs.flushed(r) {
			resp = nil
			err = g9p.ErrFlushed
		}
	}()

	if fs.Chatty {
		log.Printf("-> WriteStat request")
	}

	fs.fidLock.Lock()
	defer fs.fidLock.Unlock()
	s, ok := fs.Fids[r.Fid]
	if !ok {
		return nil, fmt.Errorf("no such fid")
	}

	s.Lock()
	defer s.Unlock()

	var l, p Element
	l = s.location.Last()
	if l == nil {
		return nil, fmt.Errorf("no such file")
	}

	if len(s.location) > 1 {
		p = s.location.Parent()
	}
	if err := setStat(s.username, l, p, r.Stat); err != nil {
		return nil, err
	}

	return &protocol.WriteStatResponse{}, nil
}

func NewFileServer(root Dir, roots map[string]Dir, maxSize uint32, chatty bool) *FileServer {
	return &FileServer{
		Root:    root,
		Roots:   roots,
		MaxSize: maxSize,
		Chatty:  chatty,
		Fids:    make(map[protocol.Fid]*State),
		tags:    make(map[protocol.Tag]bool),
	}
}
