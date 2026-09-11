package main

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// runSetup deixa a máquina pronta: editor, plugins, language servers e config.
// É idempotente — rodar de novo só preenche o que falta.
func runSetup() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	binDir := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return err
	}

	step("o próprio nani")
	naniPath, err := installSelf(binDir)
	if err != nil {
		return err
	}
	okf("nani em %s", naniPath)

	step("editor micro")
	microPath, err := ensureMicro(binDir)
	if err != nil {
		return fmt.Errorf("instalando o micro: %w", err)
	}
	okf("micro em %s", microPath)

	step("plugins do micro")
	if err := ensurePlugins(microPath); err != nil {
		warnf("não consegui instalar todos os plugins: %v", err)
	} else {
		okf("lsp, filemanager, fzf, detectindent, editorconfig")
	}

	step("language server de Go (gopls)")
	if path, err := ensureGopls(); err != nil {
		warnf("%v", err)
	} else {
		okf("gopls em %s", path)
	}

	step("TypeScript nativo (tsgo)")
	if path, err := ensureTsgo(home, binDir); err != nil {
		warnf("%v", err)
	} else {
		okf("tsgo em %s", path)
	}

	step("configuração do micro")
	if err := writeMicroConfig(home, naniPath); err != nil {
		return err
	}
	okf("settings.json e bindings.json escritos")

	fmt.Println()
	fmt.Println("pronto. use `nani` para navegar e editar.")
	if !inPath(binDir) {
		fmt.Printf("\natenção: %s não está no PATH. adicione ao seu shell:\n", binDir)
		fmt.Printf("  bash/zsh:  export PATH=\"%s:$PATH\"\n", binDir)
		fmt.Printf("  fish:      fish_add_path -g %s\n", binDir)
	}
	return nil
}

func step(name string)                   { fmt.Printf("\n== %s\n", name) }
func okf(f string, a ...any)             { fmt.Printf("   ok: "+f+"\n", a...) }
func warnf(f string, a ...any)           { fmt.Printf("   aviso: "+f+"\n", a...) }
func inPath(dir string) bool             { return strings.Contains(os.Getenv("PATH"), dir) }
func haveCmd(name string) (string, bool) { p, err := exec.LookPath(name); return p, err == nil }

func run(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	return cmd.Run()
}

// installSelf copia o binário para o PATH, para que baixar o arquivo da release
// e rodar o setup baste — sem passo manual de instalação.
func installSelf(binDir string) (string, error) {
	self, err := os.Executable()
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(self); err == nil {
		self = resolved
	}
	dest := filepath.Join(binDir, "nani")
	if self == dest {
		return dest, nil // já é o binário instalado
	}

	src, err := os.Open(self)
	if err != nil {
		return "", err
	}
	defer src.Close()

	// Grava ao lado e renomeia: sobrescrever direto falha quando a versão
	// antiga está em execução.
	tmp, err := os.CreateTemp(binDir, ".nani-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	if _, err := io.Copy(tmp, src); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if err := os.Chmod(tmp.Name(), 0o755); err != nil {
		return "", err
	}
	if err := os.Rename(tmp.Name(), dest); err != nil {
		return "", err
	}
	return dest, nil
}

// ensureMicro devolve o caminho do micro, baixando o binário estático da última
// release quando ele ainda não existe.
func ensureMicro(binDir string) (string, error) {
	if path, ok := haveCmd("micro"); ok {
		return path, nil
	}
	target := filepath.Join(binDir, "micro")
	if _, err := os.Stat(target); err == nil {
		return target, nil
	}

	asset, err := microAssetURL()
	if err != nil {
		return "", err
	}
	fmt.Printf("   baixando %s\n", asset)
	resp, err := http.Get(asset)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download falhou: %s", resp.Status)
	}
	if err := extractMicro(resp.Body, target); err != nil {
		return "", err
	}
	return target, nil
}

// microAssetURL escolhe o arquivo da última release para o sistema atual.
func microAssetURL() (string, error) {
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get("https://api.github.com/repos/zyedidia/micro/releases/latest")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var release struct {
		Assets []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return "", err
	}

	var want string
	switch runtime.GOOS + "/" + runtime.GOARCH {
	case "linux/amd64":
		want = "linux64-static.tar.gz"
	case "linux/arm64":
		want = "linux-arm64.tar.gz"
	case "darwin/amd64":
		want = "osx.tar.gz"
	case "darwin/arm64":
		want = "macos-arm64.tar.gz"
	default:
		return "", fmt.Errorf("sem binário pronto para %s/%s: instale o micro pelo gerenciador de pacotes", runtime.GOOS, runtime.GOARCH)
	}
	for _, a := range release.Assets {
		if strings.HasSuffix(a.Name, want) {
			return a.URL, nil
		}
	}
	return "", fmt.Errorf("não achei o arquivo %s na última release do micro", want)
}

// extractMicro tira o executável de dentro do .tar.gz e o grava em dest.
func extractMicro(r io.Reader, dest string) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			return fmt.Errorf("executável não encontrado no arquivo baixado")
		}
		if err != nil {
			return err
		}
		if header.Typeflag != tar.TypeReg || filepath.Base(header.Name) != "micro" {
			continue
		}
		out, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
		if err != nil {
			return err
		}
		defer out.Close()
		_, err = io.Copy(out, tr)
		return err
	}
}

func ensurePlugins(microPath string) error {
	plugins := []string{"lsp", "filemanager", "fzf", "detectindent", "editorconfig"}
	args := append([]string{"-plugin", "install"}, plugins...)
	return run(microPath, args...)
}

func ensureGopls() (string, error) {
	if path, ok := haveCmd("gopls"); ok {
		return path, nil
	}
	if _, ok := haveCmd("go"); !ok {
		return "", fmt.Errorf("go não encontrado; instale o Go para ter gopls")
	}
	fmt.Println("   go install golang.org/x/tools/gopls@latest (demora um pouco)")
	if err := run("go", "install", "golang.org/x/tools/gopls@latest"); err != nil {
		return "", fmt.Errorf("go install de gopls falhou: %w", err)
	}
	if path, ok := haveCmd("gopls"); ok {
		return path, nil
	}
	// Instalado, mas o GOBIN ainda não está no PATH desta sessão.
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "go", "bin", "gopls"), nil
}

// ensureTsgo instala o TypeScript nativo em um prefixo próprio, sem mexer nos
// pacotes globais do npm da máquina.
func ensureTsgo(home, binDir string) (string, error) {
	if path, ok := haveCmd("tsgo"); ok {
		return path, nil
	}
	if _, ok := haveCmd("npm"); !ok {
		return "", fmt.Errorf("npm não encontrado; instale o Node para ter o tsgo")
	}
	prefix := filepath.Join(home, ".local", "lib", "tsgo")
	if err := os.MkdirAll(prefix, 0o755); err != nil {
		return "", err
	}
	fmt.Println("   npm i @typescript/native-preview")
	if err := run("npm", "i", "--prefix", prefix, "@typescript/native-preview"); err != nil {
		return "", fmt.Errorf("npm install do tsgo falhou: %w", err)
	}
	real := filepath.Join(prefix, "node_modules", "@typescript", "native-preview", "bin", "tsgo")
	link := filepath.Join(binDir, "tsgo")
	_ = os.Remove(link)
	if err := os.Symlink(real, link); err != nil {
		return "", err
	}
	return link, nil
}

// writeMicroConfig grava as opções sem descartar o que a pessoa já tinha.
func writeMicroConfig(home, naniPath string) error {
	configDir := filepath.Join(home, ".config", "micro")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		return err
	}

	// Os servidores sobem atrás do proxy: ele responde os requests que travam o
	// tsgo e converte os diagnósticos de pull para push.
	lspServer := fmt.Sprintf("go=%s lsp gopls,typescript=%s lsp tsgo --lsp -stdio,javascript=%s lsp tsgo --lsp -stdio",
		naniPath, naniPath, naniPath)

	settings := map[string]any{
		"colorscheme":             "dracula-tc",
		"lsp.server":              lspServer,
		"lsp.formatOnSave":        true,
		"lsp.autocompleteDetails": true,
		"lsp.tabcompletion":       false,
		"autoindent":              true,
		"clipboard":               "external",
		"diffgutter":              true,
		"hlsearch":                true,
		"ignorecase":              true,
		"incsearch":               true,
		"keymenu":                 true,
		"mouse":                   true,
		"rmtrailingws":            true,
		"ruler":                   true,
		"savecursor":              true,
		"scrollbar":               true,
		"softwrap":                true,
		"statusformatl":           "$(filename) $(modified)($(line),$(col)) $(opt:filetype)",
		"statusformatr":           "$(bind:ToggleHelp): ajuda | Ctrl-p: arquivos | Alt-t: arvore",
		"tabsize":                 4,
		"tabstospaces":            true,
		"ft:go":                   map[string]any{"tabstospaces": false, "tabsize": 4},
		"ft:typescript":           map[string]any{"tabsize": 2},
		"ft:javascript":           map[string]any{"tabsize": 2},
		"ft:json":                 map[string]any{"tabsize": 2},
	}
	if err := mergeJSON(filepath.Join(configDir, "settings.json"), settings); err != nil {
		return err
	}

	bindings := map[string]any{
		"Ctrl-p": "command:fzf",
		"Alt-t":  "command:tree",
		"Alt-g":  "command:definition",
		"F2":     "Save",
		"F3":     "Find",
		"F4":     "Quit",
	}
	return mergeJSON(filepath.Join(configDir, "bindings.json"), bindings)
}

// mergeJSON aplica as chaves novas por cima do arquivo existente.
func mergeJSON(path string, values map[string]any) error {
	current := map[string]any{}
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &current)
	}
	for k, v := range values {
		current[k] = v
	}
	data, err := json.MarshalIndent(current, "", "    ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}
