package main

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Diretórios ignorados fora de um repositório git — dentro de um, quem manda é
// o .gitignore.
var skipDirs = map[string]bool{
	".git": true, "node_modules": true, "vendor": true, "dist": true,
	"build": true, ".next": true, "target": true, ".cache": true,
	".venv": true, "__pycache__": true,
}

// Arquivos que marcam a raiz de um projeto. O micro usa o próprio cwd como
// rootUri do LSP, então o editor precisa abrir a partir daqui — de dentro de um
// subdiretório, gopls e tsgo não resolvem os imports.
var rootMarkers = []string{"go.mod", "go.work", "package.json", "tsconfig.json", "deno.json", ".git"}

// errCancelled indica que a pessoa saiu do seletor (Esc ou Ctrl-C).
var errCancelled = errors.New("cancelado")

func runBrowser(dir string) error {
	root, err := resolveRoot(dir)
	if err != nil {
		return err
	}
	editor := editorCommand()

	for {
		dirs, err := listDirs(root, 3)
		if err != nil {
			return err
		}
		chosen, err := pick(dirs, "pasta", root, "Enter entra · Esc encerra", previewDirCmd(root))
		if errors.Is(err, errCancelled) {
			return nil
		}
		if err != nil {
			return err
		}

		target := root
		if chosen != "." {
			target = filepath.Join(root, chosen)
		}

		for {
			files, err := listFiles(target)
			if err != nil {
				return err
			}
			if len(files) == 0 {
				fmt.Fprintf(os.Stderr, "nani: nenhum arquivo em %s\n", target)
				break
			}
			file, err := pick(files, "arquivo", target, "Enter edita · Esc volta às pastas", previewFileCmd(target))
			if errors.Is(err, errCancelled) {
				break // volta ao seletor de pastas, não encerra
			}
			if err != nil {
				return err
			}
			path := filepath.Join(target, file)
			if err := openEditor(editor, path); err != nil {
				return err
			}
		}
	}
}

func resolveRoot(dir string) (string, error) {
	if dir == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		// Chamado direto do home, o diretório de trabalho é o alvo mais provável.
		if home, err := os.UserHomeDir(); err == nil && cwd == home {
			if work := filepath.Join(home, "work"); isDir(work) {
				return work, nil
			}
		}
		return cwd, nil
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	if !isDir(abs) {
		return "", fmt.Errorf("não é um diretório: %s", dir)
	}
	return abs, nil
}

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// gitFiles devolve o que o git mostraria dentro de dir: arquivos rastreados mais
// os não rastreados, menos tudo que o .gitignore (e o exclude global) descarta.
// O segundo retorno é false quando dir não está num repositório.
func gitFiles(dir string) ([]string, bool) {
	cmd := exec.Command("git", "ls-files", "--cached", "--others", "--exclude-standard", "-z")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return nil, false
	}
	var files []string
	for _, name := range strings.Split(string(out), "\x00") {
		if name != "" {
			files = append(files, name)
		}
	}
	return files, true
}

// dirsFromFiles deriva a lista de pastas a partir dos arquivos visíveis. Uma
// pasta inteira ignorada, como node_modules, simplesmente não aparece.
func dirsFromFiles(files []string, maxDepth int) []string {
	seen := map[string]bool{}
	for _, file := range files {
		dir := filepath.Dir(file)
		for dir != "." && dir != string(filepath.Separator) {
			if strings.Count(dir, string(filepath.Separator))+1 <= maxDepth {
				seen[dir] = true
			}
			dir = filepath.Dir(dir)
		}
	}
	out := []string{"."}
	for dir := range seen {
		out = append(out, dir)
	}
	sort.Strings(out)
	return out
}

func listDirs(root string, maxDepth int) ([]string, error) {
	if files, ok := gitFiles(root); ok {
		return dirsFromFiles(files, maxDepth), nil
	}
	out := []string{"."}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // uma pasta ilegível não interrompe a navegação
		}
		if !d.IsDir() || path == root {
			return nil
		}
		if skipDirs[d.Name()] {
			return filepath.SkipDir
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return nil
		}
		if strings.Count(rel, string(filepath.Separator))+1 > maxDepth {
			return filepath.SkipDir
		}
		out = append(out, rel)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(out)
	return out, nil
}

func listFiles(dir string) ([]string, error) {
	if files, ok := gitFiles(dir); ok {
		sort.Strings(files)
		return files, nil
	}
	var out []string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if skipDirs[d.Name()] && path != dir {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err == nil {
			out = append(out, rel)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(out)
	return out, nil
}

func previewDirCmd(root string) string {
	if _, err := exec.LookPath("eza"); err == nil {
		// --git-ignore para o preview não mostrar o que a listagem já esconde.
		return "eza --tree --level=2 --icons --color=always --git-ignore " + shellQuote(root) + "/{} 2>/dev/null | head -300"
	}
	return "ls -A " + shellQuote(root) + "/{} 2>/dev/null | head -300"
}

func previewFileCmd(dir string) string {
	if _, err := exec.LookPath("bat"); err == nil {
		return "bat --style=numbers --color=always --line-range :500 " + shellQuote(dir) + "/{} 2>/dev/null"
	}
	return "head -500 " + shellQuote(dir) + "/{} 2>/dev/null"
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// pick mostra a lista no fzf; sem fzf instalado, usa um seletor numerado.
func pick(items []string, kind, header, hint, preview string) (string, error) {
	if _, err := exec.LookPath("fzf"); err == nil {
		return pickFzf(items, kind, header, hint, preview)
	}
	return pickPlain(items, kind, header, hint)
}

func pickFzf(items []string, kind, header, hint, preview string) (string, error) {
	cmd := exec.Command("fzf",
		"--prompt="+kind+" ❯ ",
		"--header="+header+"\n"+hint,
		"--preview="+preview,
		"--preview-window=right,60%",
		"--height=100%",
	)
	cmd.Stdin = strings.NewReader(strings.Join(items, "\n"))
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			// 1 = nada casou, 130 = interrompido pela pessoa.
			if exit.ExitCode() == 1 || exit.ExitCode() == 130 {
				return "", errCancelled
			}
		}
		return "", err
	}
	choice := strings.TrimSpace(string(out))
	if choice == "" {
		return "", errCancelled
	}
	return choice, nil
}

func pickPlain(items []string, kind, header, hint string) (string, error) {
	fmt.Printf("\n%s\n%s\n\n", header, hint)
	for i, item := range items {
		fmt.Printf("  %3d  %s\n", i+1, item)
	}
	fmt.Printf("\nescolha %s (número, vazio para voltar): ", kind)

	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		return "", errCancelled
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return "", errCancelled
	}
	n, err := strconv.Atoi(line)
	if err != nil || n < 1 || n > len(items) {
		return "", fmt.Errorf("escolha inválida: %s", line)
	}
	return items[n-1], nil
}

func editorCommand() string {
	if env := os.Getenv("NANI_EDITOR"); env != "" {
		return env
	}
	if _, err := exec.LookPath("micro"); err == nil {
		return "micro"
	}
	if env := os.Getenv("EDITOR"); env != "" {
		return env
	}
	return "nano"
}

// openEditor abre o arquivo com o cwd na raiz do projeto, que é de onde o LSP
// enxerga o workspace.
func openEditor(editor, path string) error {
	cmd := exec.Command(editor, path)
	cmd.Dir = projectRoot(path)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func projectRoot(path string) string {
	dir := filepath.Dir(path)
	for {
		for _, marker := range rootMarkers {
			if _, err := os.Stat(filepath.Join(dir, marker)); err == nil {
				return dir
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return filepath.Dir(path)
		}
		dir = parent
	}
}
