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

// runSetup gets the machine ready: editor, plugins, language servers and config.
// It is idempotent — running again only fills in what is missing.
func runSetup() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	binDir := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return err
	}

	step("nani itself")
	naniPath, err := installSelf(binDir)
	if err != nil {
		return err
	}
	okf("nani at %s", naniPath)

	step("micro editor")
	microPath, err := ensureMicro(binDir)
	if err != nil {
		return fmt.Errorf("installing micro: %w", err)
	}
	okf("micro at %s", microPath)

	step("micro plugins")
	if err := ensurePlugins(microPath); err != nil {
		warnf("could not install every plugin: %v", err)
	} else {
		okf("lsp, filemanager, fzf, detectindent, editorconfig")
	}

	step("Go language server (gopls)")
	if path, err := ensureGopls(); err != nil {
		warnf("%v", err)
	} else {
		okf("gopls at %s", path)
	}

	step("native TypeScript (tsgo)")
	if path, err := ensureTsgo(home, binDir); err != nil {
		warnf("%v", err)
	} else {
		okf("tsgo at %s", path)
	}

	step("micro configuration")
	if err := writeMicroConfig(home, naniPath); err != nil {
		return err
	}
	okf("settings.json and bindings.json written")

	fmt.Println()
	fmt.Println("done. run `nani` to browse and edit.")
	if !inPath(binDir) {
		fmt.Printf("\nwarning: %s is not in your PATH. add it to your shell:\n", binDir)
		fmt.Printf("  bash/zsh:  export PATH=\"%s:$PATH\"\n", binDir)
		fmt.Printf("  fish:      fish_add_path -g %s\n", binDir)
	}
	return nil
}

func step(name string)                   { fmt.Printf("\n== %s\n", name) }
func okf(f string, a ...any)             { fmt.Printf("   ok: "+f+"\n", a...) }
func warnf(f string, a ...any)           { fmt.Printf("   warning: "+f+"\n", a...) }
func inPath(dir string) bool             { return strings.Contains(os.Getenv("PATH"), dir) }
func haveCmd(name string) (string, bool) { p, err := exec.LookPath(name); return p, err == nil }

func run(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	return cmd.Run()
}

// installSelf copies the binary into the PATH, so downloading the release file
// and running setup is enough — no manual install step.
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
		return dest, nil // already the installed binary
	}

	src, err := os.Open(self)
	if err != nil {
		return "", err
	}
	defer src.Close()

	// Write alongside and rename: overwriting in place fails while the old
	// version is running.
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

// ensureMicro returns micro's path, downloading the static binary from the latest
// release when it is not there yet.
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
	fmt.Printf("   downloading %s\n", asset)
	resp, err := http.Get(asset)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download failed: %s", resp.Status)
	}
	if err := extractMicro(resp.Body, target); err != nil {
		return "", err
	}
	return target, nil
}

// microAssetURL picks the file from the latest release for the current system.
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
		return "", fmt.Errorf("no prebuilt binary for %s/%s: install micro from your package manager", runtime.GOOS, runtime.GOARCH)
	}
	for _, a := range release.Assets {
		if strings.HasSuffix(a.Name, want) {
			return a.URL, nil
		}
	}
	return "", fmt.Errorf("could not find %s in the latest micro release", want)
}

// extractMicro pulls the executable out of the .tar.gz and writes it to dest.
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
			return fmt.Errorf("executable not found in the downloaded archive")
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
		return "", fmt.Errorf("go not found; install Go to get gopls")
	}
	fmt.Println("   go install golang.org/x/tools/gopls@latest (takes a while)")
	if err := run("go", "install", "golang.org/x/tools/gopls@latest"); err != nil {
		return "", fmt.Errorf("go install of gopls failed: %w", err)
	}
	if path, ok := haveCmd("gopls"); ok {
		return path, nil
	}
	// Installed, but GOBIN is not in this session's PATH yet.
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "go", "bin", "gopls"), nil
}

// ensureTsgo installs native TypeScript under its own prefix, without touching
// the machine's global npm packages.
func ensureTsgo(home, binDir string) (string, error) {
	if path, ok := haveCmd("tsgo"); ok {
		return path, nil
	}
	if _, ok := haveCmd("npm"); !ok {
		return "", fmt.Errorf("npm not found; install Node to get tsgo")
	}
	prefix := filepath.Join(home, ".local", "lib", "tsgo")
	if err := os.MkdirAll(prefix, 0o755); err != nil {
		return "", err
	}
	fmt.Println("   npm i @typescript/native-preview")
	if err := run("npm", "i", "--prefix", prefix, "@typescript/native-preview"); err != nil {
		return "", fmt.Errorf("npm install of tsgo failed: %w", err)
	}
	real := filepath.Join(prefix, "node_modules", "@typescript", "native-preview", "bin", "tsgo")
	link := filepath.Join(binDir, "tsgo")
	_ = os.Remove(link)
	if err := os.Symlink(real, link); err != nil {
		return "", err
	}
	return link, nil
}

// writeMicroConfig writes the options without discarding what was already there.
func writeMicroConfig(home, naniPath string) error {
	configDir := filepath.Join(home, ".config", "micro")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		return err
	}

	// The servers run behind the proxy: it answers the requests that block tsgo
	// and converts diagnostics from pull to push.
	lspServer := fmt.Sprintf("go=%s lsp gopls,typescript=%s lsp tsgo --lsp -stdio,javascript=%s lsp tsgo --lsp -stdio",
		naniPath, naniPath, naniPath)

	settings := map[string]any{
		"colorscheme":             "dracula-tc",
		"lsp.server":              lspServer,
		"lsp.formatOnSave":        true,
		"lsp.autocompleteDetails": false,
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
		"statusformatr":           "$(bind:ToggleHelp): help | Ctrl-p: files | Alt-t: tree",
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

// mergeJSON applies the new keys on top of the existing file.
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
