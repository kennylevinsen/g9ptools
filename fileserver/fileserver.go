package fileserver

import (
	"errors"
	"fmt"
	"io"
	"log"
	"sync"
	"time"

	"github.com/joushou/g9p"
	"github.com/joushou/g9p/protocol"
)

type Verbosity int

const (
	Quiet Verbosity = iota
	Chatty
	Loud
	Obnoxious
	Debug
)

const (
	DefaultMaxSize = (1024 * 1024 * 1024)
)

type State struct {
	sync.RWMutex
	location FilePath

	open     OpenFile
	mode     protocol.OpenMode
	service  string
	username string
}

type FileServer struct {
	sync.RWMutex
	Roots  map[string]Dir
	Root   Dir
	Chatty Verbosity

	MaxSize uint32
	fidLock sync.RWMutex
	Fids    map[protocol.Fid]*State
	tagLock sync.Mutex
	tags    map[protocol.Tag]bool
}

func (fs *FileServer) logreq(d protocol.Message) {
	switch fs.Chatty {
	case Chatty, Loud:
		log.Printf("-> %T", d)
	case Obnoxious, Debug:
		log.Printf("-> %T    \t%+v", d, d)
	}
}

func (fs *FileServer) logresp(d protocol.Message, err error) {
	switch fs.Chatty {
	case Loud:
		if err != nil {
			log.Printf("<- *protocol.ErrorResponse")
		} else {
			log.Printf("<- %T", d)
		}
	case Obnoxious, Debug:
		if err != nil {
			log.Printf("<- *protocol.ErrorResponse    \t%s", err)
		} else {
			log.Printf("<- %T    \t%+v", d, d)
		}
	}
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

		fs.logresp(resp, err)
	}()

	fs.logreq(r)

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

		fs.logresp(resp, err)
	}()

	fs.logreq(r)

	return nil, fmt.Errorf("auth not supported")
}

func (fs *FileServer) Attach(r *protocol.AttachRequest) (resp *protocol.AttachResponse, err error) {
	fs.register(r)
	defer func() {
		if fs.flushed(r) {
			resp = nil
			err = g9p.ErrFlushed
		}

		fs.logresp(resp, err)
	}()

	fs.logreq(r)
	fs.fidLock.Lock()
	defer fs.fidLock.Unlock()

	if _, ok := fs.Fids[r.Fid]; ok {
		return nil, fmt.Errorf("fid already in use")
	}

	var root Dir
	if x, ok := fs.Roots[r.Service]; ok {
		root = x
	} else if fs.Root != nil {
		root = fs.Root
	}

	if root == nil {
		return nil, fmt.Errorf("no such service")
	}

	s := &State{
		service:  r.Service,
		username: r.Username,
		location: FilePath{root},
	}

	fs.Fids[r.Fid] = s

	q, err := s.location.Current().Qid()
	if err != nil {
		return nil, err
	}

	resp = &protocol.AttachResponse{
		Qid: q,
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

		fs.logresp(resp, err)
	}()

	fs.logreq(r)

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

		fs.logresp(resp, err)
	}()

	fs.logreq(r)

	fs.fidLock.Lock()
	defer fs.fidLock.Unlock()
	s, ok := fs.Fids[r.Fid]
	if !ok {
		return nil, fmt.Errorf("unknown fid")
	}

	s.Lock()
	defer s.Unlock()

	if s.open != nil {
		return nil, fmt.Errorf("fid cannot be open for walk")
	}

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

	cur := s.location.Current()
	d, err := cur.IsDir()
	if err != nil {
		return nil, err
	}
	if !d {
		return nil, fmt.Errorf("walk -- in a non-directory")
	}
	root := cur

	newloc := s.location
	first := true
	var qids []protocol.Qid
	for i := range r.Names {
		x, err := root.Open(s.username, protocol.OEXEC)
		if err != nil {
			goto write
		}
		x.Close()

		addToLoc := true
		name := r.Names[i]
		switch name {
		case ".":
			// This is a nop, but we should still report the result
			addToLoc = false
		case "..":
			// Go one directory up, or nop if we're at /
			root = newloc.Parent()
			if len(newloc) > 1 {
				newloc = newloc[:len(newloc)-1]
				addToLoc = false
			}
		default:
			istree, err := root.IsDir()
			if err != nil {
				return nil, err
			}
			if !istree {
				goto write
			}

			d := root.(Dir)
			root, err = d.Walk(s.username, name)
			if err != nil {
				goto write
			}
			if root == nil {
				if first {
					return nil, errors.New("file does not exist")
				}
				goto write
			}
		}

		if addToLoc {
			newloc = append(newloc, root)
		}
		q, err := root.Qid()
		if err != nil {
			return nil, err
		}
		qids = append(qids, q)

		if i >= len(r.Names)-1 {
			s := &State{
				service:  s.service,
				username: s.username,
				location: newloc,
			}
			fs.Fids[r.NewFid] = s
		}

		first = false
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

		fs.logresp(resp, err)
	}()

	fs.logreq(r)

	fs.fidLock.RLock()
	defer fs.fidLock.RUnlock()
	s, ok := fs.Fids[r.Fid]
	if !ok {
		return nil, fmt.Errorf("unknown fid")
	}

	s.Lock()
	defer s.Unlock()

	if s.open != nil {
		return nil, fmt.Errorf("already open")
	}

	l := s.location.Current()
	q, err := l.Qid()
	if err != nil {
		return nil, err
	}
	x, err := l.Open(s.username, r.Mode)
	if err != nil {
		return nil, err
	}
	s.open = x
	s.mode = r.Mode
	resp = &protocol.OpenResponse{
		Qid: q,
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

		fs.logresp(resp, err)
	}()

	fs.logreq(r)

	fs.fidLock.RLock()
	defer fs.fidLock.RUnlock()
	s, ok := fs.Fids[r.Fid]
	if !ok {
		return nil, fmt.Errorf("unknown fid")
	}

	s.Lock()
	defer s.Unlock()

	if s.open != nil {
		return nil, fmt.Errorf("already open")
	}

	if r.Name == "." || r.Name == ".." {
		return nil, fmt.Errorf("file name syntax")
	}

	cur := s.location.Current()
	isdir, err := cur.IsDir()
	if err != nil {
		return nil, err
	}
	if !isdir {
		return nil, fmt.Errorf("create -- in a non-directory")
	}
	t := cur.(Dir)

	l, err := t.Create(s.username, r.Name, r.Permissions)
	if err != nil {
		return nil, err
	}

	q, err := l.Qid()
	if err != nil {
		return nil, err
	}

	x, err := l.Open(s.username, r.Mode)
	if err != nil {
		return nil, err
	}

	s.location = append(s.location, l)
	s.open = x
	s.mode = r.Mode
	resp = &protocol.CreateResponse{
		Qid:    q,
		IOUnit: 0,
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

		fs.logresp(resp, err)
	}()

	fs.logreq(r)

	fs.fidLock.RLock()
	defer fs.fidLock.RUnlock()
	s, ok := fs.Fids[r.Fid]
	if !ok {
		return nil, fmt.Errorf("unknown fid")
	}

	s.RLock()
	defer s.RUnlock()

	if s.open == nil {
		return nil, fmt.Errorf("file not open")
	}

	if (s.mode&3 != protocol.OREAD) && (s.mode&3) != protocol.ORDWR {
		return nil, fmt.Errorf("file not opened for reading")
	}

	count := int(fs.MaxSize) - (&protocol.ReadResponse{}).EncodedLength() + protocol.HeaderSize
	if count > int(r.Count) {
		count = int(r.Count)
	}

	b := make([]byte, count)

	_, err = s.open.Seek(int64(r.Offset), 0)
	if err != nil {
		return nil, err
	}
	n, err := s.open.Read(b)
	if err == io.EOF {
		n = 0
	} else if err != nil {
		return nil, err
	}
	b = b[:n]
	resp = &protocol.ReadResponse{
		Data: b,
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

		fs.logresp(resp, err)
	}()

	fs.logreq(r)

	fs.fidLock.RLock()
	defer fs.fidLock.RUnlock()
	s, ok := fs.Fids[r.Fid]
	if !ok {
		return nil, fmt.Errorf("unknown fid")
	}

	s.RLock()
	defer s.RUnlock()

	if s.open == nil {
		return nil, fmt.Errorf("file not open")
	}

	if (s.mode&3) != protocol.OWRITE && (s.mode%3) != protocol.ORDWR {
		return nil, fmt.Errorf("file not opened for writing")
	}

	_, err = s.open.Seek(int64(r.Offset), 0)
	if err != nil {
		return nil, err
	}
	n, err := s.open.Write(r.Data)
	if err != nil {
		return nil, err
	}

	resp = &protocol.WriteResponse{
		Count: uint32(n),
	}

	return resp, nil
}

func (fs *FileServer) Clunk(r *protocol.ClunkRequest) (resp *protocol.ClunkResponse, err error) {
	fs.register(r)
	defer func() {
		if fs.flushed(r) {
			resp = nil
			err = g9p.ErrFlushed
		}

		fs.logresp(resp, err)
	}()

	fs.logreq(r)

	fs.fidLock.Lock()
	defer fs.fidLock.Unlock()
	s, ok := fs.Fids[r.Fid]
	if !ok {
		return nil, fmt.Errorf("unknown fid")
	}

	s.Lock()
	defer s.Unlock()

	if s.open != nil {
		s.open.Close()
		s.open = nil
	}

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

		fs.logresp(resp, err)
	}()

	fs.logreq(r)

	fs.fidLock.Lock()
	defer fs.fidLock.Unlock()
	s, ok := fs.Fids[r.Fid]
	if !ok {
		return nil, fmt.Errorf("unknown fid")
	}
	defer delete(fs.Fids, r.Fid)
	s.Lock()
	defer s.Unlock()

	if s.open != nil {
		s.open.Close()
		s.open = nil
	}

	var cur, p File

	// We're not going to remove /.
	if len(s.location) <= 1 {
		return &protocol.RemoveResponse{}, nil
	}

	// Attempt to delete it, but ignore error.
	cur = s.location.Current()
	p = s.location.Parent()
	n, err := cur.Name()
	if err != nil {
		return nil, err
	}
	p.(Dir).Remove(s.username, n)

	return &protocol.RemoveResponse{}, nil
}

func (fs *FileServer) Stat(r *protocol.StatRequest) (resp *protocol.StatResponse, err error) {
	fs.register(r)
	defer func() {
		if fs.flushed(r) {
			resp = nil
			err = g9p.ErrFlushed
		}

		fs.logresp(resp, err)
	}()

	fs.logreq(r)

	fs.fidLock.RLock()
	defer fs.fidLock.RUnlock()
	s, ok := fs.Fids[r.Fid]
	if !ok {
		return nil, fmt.Errorf("unknown fid")
	}

	s.RLock()
	defer s.RUnlock()

	l := s.location.Current()
	if l == nil {
		return nil, fmt.Errorf("no such file")
	}

	st, err := l.Stat()
	if err != nil {
		return nil, err
	}

	resp = &protocol.StatResponse{
		Stat: st,
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

		fs.logresp(resp, err)
	}()

	fs.logreq(r)

	fs.fidLock.Lock()
	defer fs.fidLock.Unlock()
	s, ok := fs.Fids[r.Fid]
	if !ok {
		return nil, fmt.Errorf("unknown fid")
	}

	s.Lock()
	defer s.Unlock()

	var l File
	var p Dir
	l = s.location.Current()
	if l == nil {
		return nil, fmt.Errorf("no such file")
	}

	if len(s.location) > 1 {
		p = s.location.Parent().(Dir)
	}
	if err := setStat(s.username, l, p, r.Stat); err != nil {
		return nil, err
	}

	return &protocol.WriteStatResponse{}, nil
}

func NewFileServer(root Dir, roots map[string]Dir, maxSize uint32, chat Verbosity) *FileServer {
	fs := &FileServer{
		Root:    root,
		Roots:   roots,
		MaxSize: maxSize,
		Chatty:  chat,
		Fids:    make(map[protocol.Fid]*State),
		tags:    make(map[protocol.Tag]bool),
	}

	if chat == Debug {
		go func() {
			t := time.Tick(10 * time.Second)
			for range t {
				log.Printf("Open fids: %d", len(fs.Fids))
			}
		}()
	}

	return fs
}
