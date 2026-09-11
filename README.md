# nani

Navegador de pastas e editor leve de terminal, com LSP de verdade para **Go** e
**TypeScript** — sem virar um Neovim.

A ideia é simples: listar as pastas, escolher uma, escolher um arquivo, editar,
voltar para a lista. O editor é o [micro](https://micro-editor.github.io/), que
não é modal: `Ctrl+S` salva, `Ctrl+Q` sai, `Ctrl+C`/`Ctrl+V` copiam e colam, as
setas andam pelo texto e a barra de atalhos fica no rodapé, como no nano.

```
 pasta ❯ gosample                    │  main.go
 ──────────────────────────────────  │  ────────────────────────────────
 > gosample                          │   1  package main
   tssample                          │   2
   .                                 │   3  import "fmt"
```

## O que ele resolve

O nano não fala LSP — não tem sistema de plugins nem canal de comunicação com um
language server, só realce de sintaxe por regex. O micro tem, mas o plugin LSP
dele só entende diagnósticos **push** (`textDocument/publishDiagnostics`).

O `tsgo`, o TypeScript reescrito em Go, entrega diagnósticos por **pull**
(`textDocument/diagnostic`) e ainda bloqueia esperando respostas a requests que o
plugin do micro não responde. Por isso o `nani` embute uma ponte LSP:

- responde `workspace/configuration` e `client/registerCapability` no lugar do
  editor, que é o que destrava o `tsgo`;
- anuncia ao servidor as capabilities de pull que o editor não anuncia;
- a cada abertura, edição ou save, pede `textDocument/diagnostic` e converte a
  resposta em `publishDiagnostics`, que o plugin sabe desenhar.

Com isso o erro aparece marcado na linha certa, dentro do editor, com o
TypeScript nativo. O `gopls` passa pelo mesmo caminho e funciona igual.

## Instalação

Baixe o binário da [release](https://github.com/JohnnyBoySou/nani/releases) e
rode o setup:

```bash
chmod +x nani-linux-amd64
./nani-linux-amd64 setup
```

O setup copia o próprio binário para `~/.local/bin/nani` — daí em diante o comando
é só `nani`, de qualquer diretório.

O `nani setup` é idempotente — rodar de novo só preenche o que falta. Ele instala:

| o quê | onde |
| --- | --- |
| o próprio `nani` | `~/.local/bin/nani` |
| micro (binário estático da última release) | `~/.local/bin/micro` |
| plugins `lsp`, `filemanager`, `fzf`, `detectindent`, `editorconfig` | `~/.config/micro/plug/` |
| `gopls` (via `go install`) | `~/go/bin/gopls` |
| `tsgo` (via `npm`, em prefixo próprio) | `~/.local/lib/tsgo`, link em `~/.local/bin/tsgo` |
| `settings.json` e `bindings.json` | `~/.config/micro/` |

O `tsgo` vai para um prefixo separado de propósito: não mexe nos pacotes globais
do npm da máquina.

Para compilar em vez de baixar:

```bash
go build -o ~/.local/bin/nani .
```

## Uso

```bash
nani            # navega a partir da pasta atual (ou de ~/work, se chamado do home)
nani ~/projetos # navega a partir de uma pasta específica
```

Enter entra na pasta e depois abre o arquivo. `Esc` na lista de arquivos volta
para as pastas; `Esc` nas pastas encerra. Ao fechar o editor você cai de volta na
lista, para abrir o próximo arquivo.

Com `fzf` instalado, a busca é fuzzy e com preview (`bat` para arquivos, `eza`
para árvores de pasta). Sem `fzf`, ele cai num seletor numerado que funciona em
qualquer lugar.

### Atalhos no editor

| tecla | ação |
| --- | --- |
| `Ctrl+S` / `Ctrl+Q` | salvar / sair |
| `Ctrl+Space` | autocomplete do LSP |
| `Alt+k` | hover (tipo, assinatura, doc) |
| `Alt+d` ou `Alt+g` | ir para a definição |
| `Alt+r` | referências |
| `Alt+f` | formatar (também roda ao salvar) |
| `Ctrl+p` | abrir arquivo por nome |
| `Alt+t` | árvore de arquivos lateral |

## Por que o editor abre na raiz do projeto

O plugin LSP do micro usa o diretório de trabalho do editor como `rootUri`. Se o
micro abrir de dentro de um subdiretório, o `gopls` e o `tsgo` não resolvem os
imports. Por isso o `nani` sobe a partir do arquivo até achar `go.mod`, `go.work`,
`package.json`, `tsconfig.json`, `deno.json` ou `.git`, e abre o editor a partir
dali.

## Quando algo não funcionar

O proxy LSP grava log se você apontar um caminho:

```bash
NANI_LSP_LOG=/tmp/nani-lsp.log nani
```

O log mostra o handshake, os requests do servidor que foram respondidos e quantos
diagnósticos vieram em cada consulta:

```
-> pedindo diagnósticos de file:///.../index.ts (id nani-diag-1)
<- 1 diagnóstico(s) para file:///.../index.ts
```

Nenhum diagnóstico chegando costuma ser uma destas causas:

- **o arquivo está fora de um projeto** — sem `go.mod` ou `tsconfig.json` por
  perto, o servidor não monta o workspace;
- **`typescript` 7 instalado como global do npm** — o `typescript-language-server`
  procura `lib/tsserver.js`, que não existe mais no pacote da versão 7. O `nani` usa
  o `tsgo` justamente para não depender disso;
- **binário fora do PATH** — confira `which gopls tsgo micro`.

## Variáveis

| variável | efeito |
| --- | --- |
| `NANI_EDITOR` | editor a abrir, no lugar do micro |
| `NANI_LSP_LOG` | caminho do log do proxy LSP |

## Estrutura

```
main.go      subcomandos da CLI
browser.go   navegação de pastas e arquivos, seleção e abertura do editor
lsp.go       ponte LSP: pull -> push, respostas aos requests do servidor
setup.go     instalação do editor, plugins, language servers e configuração
```
