package main

import (
	"flag"
	"fmt"

	"github.com/VKCOM/tl/internal/tllsp"
)

func main() {
	version := flag.Bool("version", false, "print language server version")
	flag.Parse()
	if *version {
		fmt.Println("tl2lsp 0.6.0")
		return
	}
	tllsp.Main()
}
