package main

import (
	"fmt"
	"log"
	"net"
	"os"

	"github.com/joushou/g9p"
	"github.com/joushou/g9ptools/exportfs/proxytree"
	"github.com/joushou/g9ptools/fileserver"
)

func main() {
	if len(os.Args) < 3 {
		fmt.Printf("Too few arguments\n")
		return
	}

	path := os.Args[1]
	addr := os.Args[2]

	root := proxytree.NewProxyTree(path)
	l, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("Unable to listen: %v", err)
	}

	h := func() g9p.Handler {
		m := make(map[string]fileserver.Dir)
		m["proxyfs"] = root
		return fileserver.NewFileServer(nil, m, 10*1024*1024, true)
	}

	log.Printf("Starting proxy at %s", addr)
	g9p.ServeListener(l, h)
}
