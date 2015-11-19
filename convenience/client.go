package convenience

import (
	"bytes"
	"errors"
	"io"
	"log"
	"net"
	"path"
	"strings"
	"time"

	"github.com/joushou/g9p"
	"github.com/joushou/g9p/protocol"
)

const (
	DefaultMaxSize = 128 * 1024
	Version        = "9P2000"
)

var (
	ErrUnknownProtocol  = errors.New("unknown protocol")
	ErrClientNotStarted = errors.New("client not started")
	ErrNoSuchFile       = errors.New("no such file")
	ErrNotADirectory    = errors.New("not a directory")
)

type Client struct {
	c       *g9p.Client
	maxSize uint32
	root    protocol.Fid
	nextFid protocol.Fid
}

func (c *Client) getFid() protocol.Fid {
	// We need to skip NOFID (highest value) and 0 (our root)
	if c.nextFid == protocol.NOFID {
		c.nextFid++
	}
	if c.nextFid == 0 {
		c.nextFid++
	}
	f := c.nextFid
	c.nextFid++
	return f
}

func (c *Client) setup(username, servicename string) error {
	if c.c == nil {
		return ErrClientNotStarted
	}

	vreq := &protocol.VersionRequest{
		Tag:     protocol.NOTAG,
		MaxSize: DefaultMaxSize,
		Version: Version,
	}

	vresp, err := c.c.Version(vreq)
	if err != nil {
		c.c.Stop()
		c.c = nil
		return err
	}

	if vresp.Version != "9P2000" {
		return ErrUnknownProtocol
	}

	c.maxSize = vresp.MaxSize

	areq := &protocol.AttachRequest{
		Tag:      c.c.NextTag(),
		Fid:      c.root,
		AuthFid:  protocol.NOFID,
		Username: username,
		Service:  servicename,
	}
	_, err = c.c.Attach(areq)
	if err != nil {
		c.c.Stop()
		c.c = nil
		return err
	}

	return nil
}

func (c *Client) readAll(fid protocol.Fid) ([]byte, error) {
	var b []byte

	for {
		rreq := &protocol.ReadRequest{
			Tag:    c.c.NextTag(),
			Fid:    fid,
			Offset: uint64(len(b)),
			Count:  c.maxSize - 9, // The size of a response
		}

		rresp, err := c.c.Read(rreq)
		if err != nil {
			return nil, err
		}
		if len(rresp.Data) == 0 {
			break
		}
		b = append(b, rresp.Data...)
	}

	return b, nil
}

func (c *Client) writeAll(fid protocol.Fid, data []byte) error {
	var offset uint64
	for {
		count := int(c.maxSize - 20)
		if len(data[offset:]) < count {
			count = len(data[offset:])
		}
		wreq := &protocol.WriteRequest{
			Tag:    c.c.NextTag(),
			Fid:    fid,
			Offset: offset,
			Data:   data[offset : offset+uint64(count)],
		}

		wresp, err := c.c.Write(wreq)
		if err != nil {
			return err
		}
		offset += uint64(wresp.Count)
	}

	return nil
}

func (c *Client) walkTo(file string) (protocol.Fid, protocol.Qid, error) {
	s := strings.Split(file, "/")

	var strs []string
	for _, str := range s {
		if str != "" {
			strs = append(strs, str)
		}
	}
	s = strs

	wreq := &protocol.WalkRequest{
		Tag:    c.c.NextTag(),
		Fid:    c.root,
		NewFid: c.getFid(),
		Names:  s,
	}
	wresp, err := c.c.Walk(wreq)
	if err != nil {
		return protocol.NOFID, protocol.Qid{}, err
	}

	if len(wresp.Qids) != len(wreq.Names) {
		return protocol.NOFID, protocol.Qid{}, ErrNoSuchFile
	}

	q := protocol.Qid{}
	if len(wresp.Qids) > 0 {
		end := len(wresp.Qids) - 1
		for i, q := range wresp.Qids {
			if i == end {
				break
			}
			if q.Type&protocol.QTDIR == 0 {
				return protocol.NOFID, protocol.Qid{}, ErrNotADirectory
			}
		}
		q = wresp.Qids[end]
	}

	return wreq.NewFid, q, nil
}

func (c *Client) clunk(fid protocol.Fid) {
	creq := &protocol.ClunkRequest{
		Tag: c.c.NextTag(),
		Fid: fid,
	}
	c.c.Clunk(creq)
}

func (c *Client) Read(file string) ([]byte, error) {
	fid, _, err := c.walkTo(file)
	if err != nil {
		return nil, err
	}
	defer c.clunk(fid)

	oreq := &protocol.OpenRequest{
		Tag:  c.c.NextTag(),
		Fid:  fid,
		Mode: protocol.OREAD,
	}
	_, err = c.c.Open(oreq)
	if err != nil {
		return nil, err
	}

	return c.readAll(fid)
}

func (c *Client) Write(content []byte, file string) error {
	fid, _, err := c.walkTo(file)
	if err != nil {
		return err
	}
	defer c.clunk(fid)

	oreq := &protocol.OpenRequest{
		Tag:  c.c.NextTag(),
		Fid:  fid,
		Mode: protocol.OWRITE,
	}
	_, err = c.c.Open(oreq)
	if err != nil {
		return err
	}

	return c.writeAll(fid, content)
}

func (c *Client) List(file string) ([]string, error) {
	fid, _, err := c.walkTo(file)
	if err != nil {
		return nil, err
	}
	defer c.clunk(fid)

	oreq := &protocol.OpenRequest{
		Tag:  c.c.NextTag(),
		Fid:  fid,
		Mode: protocol.OREAD,
	}
	_, err = c.c.Open(oreq)
	if err != nil {
		return nil, err
	}

	b, err := c.readAll(fid)
	if err != nil {
		return nil, err
	}

	buf := bytes.NewBuffer(b)
	var strs []string
	for buf.Len() > 0 {
		x := &protocol.Stat{}
		if err := x.Decode(buf); err != nil {
			return nil, err
		}
		if x.Mode&protocol.DMDIR == 0 {
			strs = append(strs, x.Name)
		} else {
			strs = append(strs, x.Name+"/")
		}
	}

	return strs, nil
}

func (c *Client) Create(name string, directory bool) error {
	dir := path.Dir(name)
	file := path.Base(name)

	fid, _, err := c.walkTo(dir)
	if err != nil {
		return err
	}
	defer c.clunk(fid)

	perms := protocol.FileMode(0755)
	if directory {
		perms |= protocol.DMDIR
	}

	creq := &protocol.CreateRequest{
		Tag:         c.c.NextTag(),
		Fid:         fid,
		Name:        file,
		Permissions: perms,
		Mode:        protocol.OREAD,
	}
	_, err = c.c.Create(creq)
	if err != nil {
		return err
	}

	return nil
}

func (c *Client) Remove(name string) error {
	fid, _, err := c.walkTo(name)
	if err != nil {
		return err
	}

	rreq := &protocol.RemoveRequest{
		Tag: c.c.NextTag(),
		Fid: fid,
	}
	_, err = c.c.Remove(rreq)
	if err != nil {
		return err
	}

	return nil
}

type asdf struct {
	resp protocol.Message
	err  error
}

func (c *Client) TestFlush() error {
	t := c.c.NextTag()
	wreq := &protocol.WalkRequest{
		Tag:    t,
		Fid:    c.root,
		NewFid: c.getFid(),
		Names:  []string{"test", "wee", "hello", "thereyougo"},
	}

	freq := &protocol.FlushRequest{
		Tag:    c.c.NextTag(),
		OldTag: t,
	}

	ch := make(chan asdf)

	go func() {
		log.Printf("Flushing")
		time.Sleep(1 * time.Millisecond)
		resp, err := c.c.Flush(freq)
		ch <- asdf{resp, err}
	}()

	log.Printf("Walking")
	resp, err := c.c.Walk(wreq)

	x := <-ch
	log.Printf("Orig req: %v, %v", resp, err)
	log.Printf("Flush req: %v, %v", x.resp, x.err)

	return nil
}

func (c *Client) Dial(network, address, username, servicename string) error {
	conn, err := net.Dial(network, address)
	if err != nil {
		return err
	}

	c.c = g9p.NewClient(conn)
	go c.c.Start()

	err = c.setup(username, servicename)
	if err != nil {
		return err
	}
	return nil
}

func (c *Client) Connect(rw io.ReadWriter, username, servicename string) error {
	c.c = g9p.NewClient(rw)
	go c.c.Start()

	err := c.setup(username, servicename)
	if err != nil {
		return err
	}
	return nil
}
