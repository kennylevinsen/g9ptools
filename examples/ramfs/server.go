package main

import (
	"bytes"
	"fmt"
	"log"
	"net"
	"sync"

	"github.com/joushou/g9p"
	"github.com/joushou/g9p/protocol"
	"github.com/joushou/g9ptools/examples/tree"
)

const (
	DefaultMaxSize = (1024 * 1024 * 1024)
)

type State struct {
	sync.RWMutex
	location tree.ElementSlice

	open     bool
	mode     protocol.OpenMode
	service  string
	username string
}

type RamFS struct {
	sync.RWMutex
	root    *tree.Tree
	maxsize uint32
	fids    map[protocol.Fid]*State
}

func (rfs *RamFS) Version(r *protocol.VersionRequest) (*protocol.VersionResponse, error) {
	log.Printf("-> Version request")
	rfs.RLock()
	defer rfs.RUnlock()
	if r.MaxSize < DefaultMaxSize {
		rfs.maxsize = r.MaxSize
	} else {
		rfs.maxsize = DefaultMaxSize
	}

	proto := "9P2000"
	if r.Version != "9P2000" {
		proto = "unknown"
	}

	resp := &protocol.VersionResponse{
		MaxSize: rfs.maxsize,
		Version: proto,
	}

	return resp, nil
}

func (rfs *RamFS) Auth(*protocol.AuthRequest) (*protocol.AuthResponse, error) {
	log.Printf("-> Auth request")
	return nil, fmt.Errorf("auth not supported")
}

func (rfs *RamFS) Attach(r *protocol.AttachRequest) (*protocol.AttachResponse, error) {
	log.Printf("-> Attach request")
	rfs.Lock()
	defer rfs.Unlock()

	if _, ok := rfs.fids[r.Fid]; ok {
		return nil, fmt.Errorf("fid already in use")
	}

	s := &State{
		service:  r.Service,
		username: r.Username,
		location: tree.ElementSlice{rfs.root},
	}

	rfs.fids[r.Fid] = s

	resp := &protocol.AttachResponse{
		Qid: s.location.Last().Qid(),
	}

	return resp, nil
}

func (rfs *RamFS) Flush(r *protocol.FlushRequest) (*protocol.FlushResponse, error) {
	// TODO(kl): Handle flush!
	return nil, g9p.ErrFlushed
}

func (rfs *RamFS) Walk(r *protocol.WalkRequest) (*protocol.WalkResponse, error) {
	log.Printf("-> Walk request")
	rfs.Lock()
	defer rfs.Unlock()

	s, ok := rfs.fids[r.Fid]
	if !ok {
		return nil, fmt.Errorf("no such fid")
	}

	s.Lock()
	defer s.Unlock()

	if _, ok = rfs.fids[r.NewFid]; ok {
		return nil, fmt.Errorf("fid already in use")
	}

	if len(r.Names) == 0 {
		x := &State{
			service:  s.service,
			username: s.username,
			location: s.location,
		}
		rfs.fids[r.NewFid] = x

		resp := &protocol.WalkResponse{}
		return resp, nil
	}

	root, ok := s.location.Last().(*tree.Tree)
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
			goto write
		}

		var d tree.Element
		var istree bool

		addToLoc := true
		name := r.Names[i]
		switch name {
		case ".":
			// This is a nop, but we should still report the result
			d = root
			addToLoc = false
			_, istree = d.(*tree.Tree)
		case "..":
			// Go one directory up, or nop if we're at /
			d = newloc.Parent()
			if len(newloc) > 1 {
				newloc = newloc[:len(newloc)-1]
				addToLoc = false
			}
			_, istree = d.(*tree.Tree)
		default:
			// Try to find the file
			d = root.Find(name)
			_, istree = d.(*tree.Tree)
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
			rfs.fids[r.NewFid] = s
		}
		if !istree {
			goto write
		}

		root = d.(*tree.Tree)

	}

write:
	resp := &protocol.WalkResponse{
		Qids: qids,
	}

	return resp, nil
}

func (rfs *RamFS) Open(r *protocol.OpenRequest) (*protocol.OpenResponse, error) {
	log.Printf("-> Open request")
	rfs.RLock()
	defer rfs.RUnlock()

	s, ok := rfs.fids[r.Fid]
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
	resp := &protocol.OpenResponse{
		Qid: l.Qid(),
	}

	return resp, nil

}

func (rfs *RamFS) Create(r *protocol.CreateRequest) (*protocol.CreateResponse, error) {
	log.Printf("-> Create request")
	rfs.RLock()
	defer rfs.RUnlock()

	s, ok := rfs.fids[r.Fid]
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

	t, ok := s.location.Last().(*tree.Tree)
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

	var l tree.Element
	if r.Permissions&protocol.DMDIR != 0 {
		l = tree.NewTree(r.Name, r.Permissions, s.username)
	} else {
		l = tree.NewFile(r.Name, r.Permissions, s.username)
	}
	t.Add(l)

	s.location = append(s.location, l)

	if err := l.Open(s.username, r.Mode); err != nil {
		return nil, err
	}

	s.open = true
	s.mode = r.Mode
	resp := &protocol.CreateResponse{
		Qid: l.Qid(),
	}

	return resp, nil
}

func (rfs *RamFS) Read(r *protocol.ReadRequest) (*protocol.ReadResponse, error) {
	log.Printf("-> Read request")
	rfs.RLock()
	defer rfs.RUnlock()

	s, ok := rfs.fids[r.Fid]
	if !ok {
		return nil, fmt.Errorf("no such fid")
	}

	s.RLock()
	defer s.RUnlock()

	if !s.open {
		return nil, fmt.Errorf("file not open")
	}

	if s.mode != protocol.OREAD || s.mode != protocol.ORDWR {
		return nil, fmt.Errorf("file not opened for reading")
	}

	var data []byte

	switch x := s.location.Last().(type) {
	case *tree.Tree:
		buf := new(bytes.Buffer)
		x.Walk(func(e tree.Element) {
			y := e.Stat()
			y.Encode(buf)
		})
		data = buf.Bytes()
	case *tree.File:
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
	resp := &protocol.ReadResponse{
		Data: data,
	}

	// Ensure that we obey the negotiated maxsize!
	if resp.EncodedLength()+protocol.HeaderSize > int(rfs.maxsize) {
		diff := r.EncodedLength() + protocol.HeaderSize - int(rfs.maxsize)
		resp.Data = resp.Data[:len(resp.Data)-diff]
	}

	return resp, nil
}

func (rfs *RamFS) Write(r *protocol.WriteRequest) (*protocol.WriteResponse, error) {
	log.Printf("-> Write request")
	rfs.RLock()
	defer rfs.RUnlock()

	s, ok := rfs.fids[r.Fid]
	if !ok {
		return nil, fmt.Errorf("no such fid")
	}

	s.RLock()
	defer s.RUnlock()

	if !s.open {
		return nil, fmt.Errorf("file not open")
	}

	if s.mode != protocol.OWRITE || s.mode != protocol.ORDWR {
		return nil, fmt.Errorf("file not opened for writing")
	}

	switch x := s.location.Last().(type) {
	case *tree.Tree:
		return nil, fmt.Errorf("cannot write to directory")
	case *tree.File:
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

		resp := &protocol.WriteResponse{
			Count: uint32(len(r.Data)),
		}

		return resp, nil
	}

	return nil, fmt.Errorf("unexpected error")
}

func (rfs *RamFS) Clunk(r *protocol.ClunkRequest) (*protocol.ClunkResponse, error) {
	log.Printf("-> Clunk request")
	rfs.Lock()
	defer rfs.Unlock()
	s, ok := rfs.fids[r.Fid]
	if !ok {
		return nil, fmt.Errorf("no such fid")
	}

	s.Lock()
	defer s.Unlock()

	delete(rfs.fids, r.Fid)
	return &protocol.ClunkResponse{}, nil
}

func (rfs *RamFS) Remove(r *protocol.RemoveRequest) (*protocol.RemoveResponse, error) {
	log.Printf("-> Remove request")
	rfs.Lock()
	defer rfs.Unlock()

	s, ok := rfs.fids[r.Fid]
	if !ok {
		return nil, fmt.Errorf("no such fid")
	}
	s.Lock()
	defer s.Unlock()

	var l, p tree.Element

	// We're not going to remove /.
	if len(s.location) <= 1 {
		goto write
	}

	// Attempt to delete it.
	l = s.location.Last()

	if x, ok := l.(*tree.Tree); ok {
		if !x.Empty() {
			goto write
		}
	}

	p = s.location.Parent()
	if err := p.Open(s.username, protocol.OWRITE); err != nil {
		goto write
	}
	p.(*tree.Tree).Remove(l)

write:
	delete(rfs.fids, r.Fid)
	return &protocol.RemoveResponse{}, nil
}

func (rfs *RamFS) Stat(r *protocol.StatRequest) (*protocol.StatResponse, error) {
	log.Printf("-> Stat request")
	rfs.RLock()
	defer rfs.RUnlock()

	s, ok := rfs.fids[r.Fid]
	if !ok {
		return nil, fmt.Errorf("no such fid")
	}

	s.RLock()
	defer s.RUnlock()

	l := s.location.Last()
	if l == nil {
		return nil, fmt.Errorf("no such file")
	}

	resp := &protocol.StatResponse{
		Stat: l.Stat(),
	}

	return resp, nil
}

func (rfs *RamFS) WriteStat(r *protocol.WriteStatRequest) (*protocol.WriteStatResponse, error) {
	log.Printf("-> WriteStat request")
	rfs.Lock()
	defer rfs.Unlock()

	s, ok := rfs.fids[r.Fid]
	if !ok {
		return nil, fmt.Errorf("no such fid")
	}

	s.Lock()
	defer s.Unlock()

	var l, p tree.Element
	l = s.location.Last()
	if l == nil {
		return nil, fmt.Errorf("no such file")
	}

	if len(s.location) > 1 {
		p = s.location.Parent()
	}
	if err := tree.SetStat(s.username, l, p, r.Stat); err != nil {
		return nil, err
	}

	return &protocol.WriteStatResponse{}, nil
}

func main() {
	root := tree.NewTree("/", 0777, "none")
	l, err := net.Listen("tcp", ":8080")
	if err != nil {
		log.Fatalf("Unable to listen: %v", err)
	}

	h := func() g9p.Handler {
		rfs := &RamFS{
			root:    root,
			maxsize: 1024 * 1024 * 1024,
			fids:    make(map[protocol.Fid]*State),
		}
		return rfs
	}

	g9p.ServeListener(l, h)
}
