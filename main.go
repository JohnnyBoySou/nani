package main

import (
	"fmt"
	"os"
)

const version = "0.4.0"

const usage = `nani — folder browser and lightweight editor with LSP

usage:
  nani [dir]              browse folders and open files in micro
  nani setup              install and configure micro, plugins, gopls and tsgo
  nani lsp <cmd> [args]   LSP bridge for servers that only do pull diagnostics
  nani version            show the version

environment:
  NANI_EDITOR             editor to open instead of micro
  NANI_LSP_LOG            path to the LSP proxy log
`

func main() {
	args := os.Args[1:]
	if len(args) == 0 {
		if err := runBrowser(""); err != nil {
			fail(err)
		}
		return
	}

	switch args[0] {
	case "lsp":
		if len(args) < 2 {
			fail(fmt.Errorf("nani lsp needs the server command. e.g.: nani lsp tsgo --lsp -stdio"))
		}
		if err := runLSPProxy(args[1], args[2:]); err != nil {
			fail(err)
		}
	case "setup":
		if err := runSetup(); err != nil {
			fail(err)
		}
	case "version", "--version", "-v":
		fmt.Println("nani " + version)
	case "help", "--help", "-h":
		fmt.Print(usage)
	default:
		if err := runBrowser(args[0]); err != nil {
			fail(err)
		}
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "nani: "+err.Error())
	os.Exit(1)
}
