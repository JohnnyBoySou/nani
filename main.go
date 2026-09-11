package main

import (
	"fmt"
	"os"
)

const version = "0.1.0"

const usage = `ff — navegador de pastas e editor leve com LSP

uso:
  ff [dir]              navega pastas e abre arquivos no micro
  ff setup              instala e configura micro, plugins, gopls e tsgo
  ff lsp <cmd> [args]   ponte LSP para servidores que só fazem pull diagnostics
  ff version            mostra a versão

variáveis:
  FF_LSP_LOG            caminho de log do proxy LSP (diagnóstico de problemas)
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
			fail(fmt.Errorf("ff lsp precisa do comando do servidor. ex: ff lsp tsgo --lsp -stdio"))
		}
		if err := runLSPProxy(args[1], args[2:]); err != nil {
			fail(err)
		}
	case "setup":
		if err := runSetup(); err != nil {
			fail(err)
		}
	case "version", "--version", "-v":
		fmt.Println("ff " + version)
	case "help", "--help", "-h":
		fmt.Print(usage)
	default:
		if err := runBrowser(args[0]); err != nil {
			fail(err)
		}
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "ff: "+err.Error())
	os.Exit(1)
}
