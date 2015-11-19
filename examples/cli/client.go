package main

import (
	"fmt"
	"os"

	"github.com/joushou/g9ptools/convenience"
)

func main() {
	if len(os.Args) < 5 {
		fmt.Printf("Too few arguments\n")
		return
	}

	addr := os.Args[1]
	user := os.Args[2]
	service := os.Args[3]

	c := &convenience.Client{}
	err := c.Dial("tcp", addr, user, service)
	if err != nil {
		fmt.Printf("Connect failed: %v\n", err)
		return
	}

	switch os.Args[4] {
	case "ls":
		p := "/"
		if len(os.Args) >= 6 {
			p = os.Args[5]
		}
		strs, err := c.List(p)
		if err != nil {
			fmt.Printf("cmd failed: %v\n", err)
			return
		}
		fmt.Printf("%v\n", strs)
	case "cat":
		if len(os.Args) < 6 {
			fmt.Printf("not enough arguments\n")
		}
		p := os.Args[5]
		strs, err := c.Read(p)
		if err != nil {
			fmt.Printf("cmd failed: %v\n", err)
			return
		}
		fmt.Printf("%v\n", strs)
	case "touch":
		if len(os.Args) < 6 {
			fmt.Printf("not enough arguments\n")
		}
		p := os.Args[5]
		err := c.Create(p, false)
		if err != nil {
			fmt.Printf("cmd failed: %v\n", err)
			return
		}
	case "mkdir":
		if len(os.Args) < 6 {
			fmt.Printf("not enough arguments\n")
		}
		p := os.Args[5]
		err := c.Create(p, true)
		if err != nil {
			fmt.Printf("cmd failed: %v\n", err)
			return
		}
	case "rm":
		if len(os.Args) < 6 {
			fmt.Printf("not enough arguments\n")
		}
		p := os.Args[5]
		err := c.Remove(p)
		if err != nil {
			fmt.Printf("cmd failed: %v\n", err)
			return
		}
	case "testflush":
		c.TestFlush()
	}
}
