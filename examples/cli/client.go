package main

import (
	"bytes"
	"log"
	"net"

	"github.com/joushou/g9p"
	"github.com/joushou/g9p/protocol"
)

func main() {
	conn, err := net.Dial("tcp", "localhost:8080")
	if err != nil {
		log.Printf("error: %v", err)
		return
	}
	c := g9p.NewClient(conn)
	go c.Start()

	_, err = c.Version(&protocol.VersionRequest{
		Tag:     c.NextTag(),
		MaxSize: 1024,
		Version: "9P2000",
	})
	_, err = c.Attach(&protocol.AttachRequest{
		Tag:      c.NextTag(),
		Fid:      1,
		AuthFid:  protocol.NOFID,
		Username: "none",
		Service:  "something",
	})
	_, err = c.Open(&protocol.OpenRequest{
		Tag:  c.NextTag(),
		Fid:  1,
		Mode: protocol.OEXEC,
	})
	v4, err := c.Read(&protocol.ReadRequest{
		Tag:    c.NextTag(),
		Fid:    1,
		Offset: 0,
		Count:  10 * 1024,
	})

	buf := bytes.NewBuffer(v4.Data)

	log.Printf("Length: %v", buf.Len())
	var ss []protocol.Stat
	for buf.Len() > 0 {
		s := protocol.Stat{}
		s.Decode(buf)
		ss = append(ss, s)
	}

	for _, s := range ss {
		log.Printf("Stat: %v", s.Name)
	}
}
