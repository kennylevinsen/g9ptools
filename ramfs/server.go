package main

import (
	"fmt"
	"log"
	"net"
	"os"

	"github.com/kennylevinsen/g9p"
	"github.com/kennylevinsen/g9ptools/fileserver"
	"github.com/kennylevinsen/g9ptools/ramfs/ramtree"
)

func main() {
	if len(os.Args) < 5 {
		fmt.Printf("Too few arguments\n")
		fmt.Printf("%s service UID GID address\n", os.Args[0])
		fmt.Printf("UID and GID are the user/group that owns /\n")
		return
	}

	service := os.Args[1]
	user := os.Args[2]
	group := os.Args[3]
	addr := os.Args[4]

	root := ramtree.NewRAMTree("/", 0777, user, group)
	l, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("Unable to listen: %v", err)
	}

	h := func() g9p.Handler {
		m := make(map[string]fileserver.Dir)
		m[service] = root
		return fileserver.NewFileServer(nil, m, 10*1024*1024, fileserver.Debug)
	}

	log.Printf("Starting ramfs at %s", addr)
	g9p.ServeListener(l, h)
}
