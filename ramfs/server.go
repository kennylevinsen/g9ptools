package main

import (
	"fmt"
	"log"
	"net"
	"os"

	"github.com/joushou/g9p"
	"github.com/joushou/g9ptools/fileserver"
	"github.com/joushou/g9ptools/ramfs/ramtree"
)

func main() {
	if len(os.Args) < 4 {
		fmt.Printf("Too few arguments\n")
		fmt.Printf("%s UID GID address\n", os.Args[0])
		fmt.Printf("UID and GID are the user/group that owns /\n")
		return
	}

	user := os.Args[1]
	group := os.Args[2]
	addr := os.Args[3]

	root := ramtree.NewRAMTree("/", 0777, user, group)
	l, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("Unable to listen: %v", err)
	}

	h := func() g9p.Handler {
		return fileserver.NewFileServer(root, 10*1024*1024, true)
	}

	log.Printf("Starting ramfs at %s", addr)
	g9p.ServeListener(l, h)
}
