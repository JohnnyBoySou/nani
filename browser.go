package main

import (
	"bufio"
	"errors"
	"fmt"
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

// upEntry é a linha que sobe um nível, equivalente à seta para a esquerda.
const upEntry = "../"

type entry struct {
	label string // como aparece na lista: "src/" para pasta, "main.go" para arquivo
	path  string // caminho absoluto
	isDir bool
}

func runBrowser(dir string) error {
	cwd, err := resolveRoot(dir)
	if err != nil {
		return err
	}
	editor := editorCommand()

	// Ao subir, o cursor volta posicionado na pasta de onde se saiu.
	cameFrom := ""

	for {
		entries, err := listEntries(cwd)
		if err != nil {
			return err
		}
		choice, key, err := pick(entries, cwd, cameFrom)
		if errors.Is(err, errCancelled) {
			return nil
		}
		if err != nil {
			return err
		}
		cameFrom = ""

		if key == "left" || choice.label == upEntry {
			cwd, cameFrom = parentOf(cwd)
			continue
		}
		if choice.isDir {
			cwd, cameFrom = choice.path, ""
			continue
		}
		if err := openEditor(editor, choice.path); err != nil {
			return err
		}
	}
}

// parentOf sobe um nível e devolve de onde veio, para reposicionar o cursor.
func parentOf(dir string) (string, string) {
	parent := filepath.Dir(dir)
	if parent == dir {
		return dir, "" // já está na raiz do sistema de arquivos
	}
	return parent, filepath.Base(dir) + "/"
}

// listEntries devolve o conteúdo imediato do diretório: pastas primeiro, depois
// arquivos. Dentro de um repositório, respeita o .gitignore.
func listEntries(dir string) ([]entry, error) {
	dirs, files, err := childrenOf(dir)
	if err != nil {
		return nil, err
	}
	sort.Strings(dirs)
	sort.Strings(files)

	out := make([]entry, 0, len(dirs)+len(files)+1)
	if parent := filepath.Dir(dir); parent != dir {
		out = append(out, entry{label: upEntry, path: parent, isDir: true})
	}
	for _, name := range dirs {
		out = append(out, entry{label: name + "/", path: filepath.Join(dir, name), isDir: true})
	}
	for _, name := range files {
		out = append(out, entry{label: name, path: filepath.Join(dir, name), isDir: false})
	}
	return out, nil
}

// childrenOf separa pastas e arquivos do nível imediato.
func childrenOf(dir string) (dirs, files []string, err error) {
	if tracked, ok := gitFiles(dir); ok {
		seenDir := map[string]bool{}
		for _, rel := range tracked {
			if name, _, isNested := strings.Cut(rel, string(filepath.Separator)); isNested {
				if !seenDir[name] {
					seenDir[name] = true
					dirs = append(dirs, name)
				}
				continue
			}
			files = append(files, rel)
		}
		return dirs, files, nil
	}

	// Fora de um repositório: lê o diretório e aplica a lista fixa.
	items, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil, err
	}
	for _, item := range items {
		if item.IsDir() {
			if !skipDirs[item.Name()] {
				dirs = append(dirs, item.Name())
			}
			continue
		}
		files = append(files, item.Name())
	}
	return dirs, files, nil
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
		return "", fmt.Errorf("not a folder: %s", dir)
	}
	return abs, nil
}

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// previewCmd mostra árvore para pastas e conteúdo para arquivos.
func previewCmd(dir string) string {
	target := shellQuote(dir) + "/{}"
	tree := "ls -A"
	if _, err := exec.LookPath("eza"); err == nil {
		// --git-ignore para o preview não mostrar o que a listagem já esconde.
		tree = "eza --tree --level=2 --icons --color=always --git-ignore"
	}
	file := "head -500"
	if _, err := exec.LookPath("bat"); err == nil {
		file = "bat --style=numbers --color=always --line-range :500"
	}
	return fmt.Sprintf(`p=%s; if [ -d "$p" ]; then %s "$p" 2>/dev/null | head -300; else %s "$p" 2>/dev/null; fi`,
		target, tree, file)
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// pick mostra o conteúdo da pasta e devolve o item escolhido mais a tecla usada
// ("left" para voltar, "" para Enter, "right" para entrar).
func pick(entries []entry, cwd, cameFrom string) (entry, string, error) {
	if _, err := exec.LookPath("fzf"); err == nil {
		return pickFzf(entries, cwd, cameFrom)
	}
	return pickPlain(entries, cwd)
}

func pickFzf(entries []entry, cwd, cameFrom string) (entry, string, error) {
	labels := make([]string, len(entries))
	for i, e := range entries {
		labels[i] = e.label
	}

	args := []string{
		"--prompt=" + filepath.Base(cwd) + " ❯ ",
		"--header=" + cwd + "\n→ in · ← back · enter open · esc quit",
		"--preview=" + previewCmd(cwd),
		"--preview-window=right,55%",
		"--height=100%",
		// Explícito para não depender do FZF_DEFAULT_OPTS de cada máquina.
		"--layout=reverse",
		// As setas deixam de andar pela busca e passam a navegar pelas pastas.
		"--expect=right,left",
	}
	// Ao voltar, o cursor começa na pasta de onde se saiu.
	if cameFrom != "" {
		for i, label := range labels {
			if label == cameFrom {
				args = append(args, "--sync", fmt.Sprintf("--bind=start:pos(%d)", i+1))
				break
			}
		}
	}

	cmd := exec.Command("fzf", args...)
	cmd.Stdin = strings.NewReader(strings.Join(labels, "\n"))
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			// 1 = nada casou, 130 = interrompido pela pessoa.
			if exit.ExitCode() == 1 || exit.ExitCode() == 130 {
				return entry{}, "", errCancelled
			}
		}
		return entry{}, "", err
	}

	// Com --expect, a primeira linha é a tecla (vazia no Enter) e a segunda é a
	// escolha.
	lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	if len(lines) < 2 {
		return entry{}, "", errCancelled
	}
	key, label := lines[0], lines[1]
	for _, e := range entries {
		if e.label == label {
			return e, key, nil
		}
	}
	return entry{}, "", errCancelled
}

func pickPlain(entries []entry, cwd string) (entry, string, error) {
	fmt.Printf("\n%s\n\n", cwd)
	for i, e := range entries {
		fmt.Printf("  %3d  %s\n", i+1, e.label)
	}
	fmt.Print("\nnumber opens · empty goes up · q quits: ")

	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		return entry{}, "", errCancelled
	}
	switch line = strings.TrimSpace(line); line {
	case "":
		return entry{}, "left", nil
	case "q":
		return entry{}, "", errCancelled
	}
	n, err := strconv.Atoi(line)
	if err != nil || n < 1 || n > len(entries) {
		return entry{}, "", fmt.Errorf("invalid choice: %s", line)
	}
	return entries[n-1], "", nil
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
