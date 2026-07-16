# Shell completion

`env-starter` supports shell completion for **zsh**, **bash**, and **fish**.

Completion covers:
- Subcommand names (`run`, `stop`, `list`, …)
- Flag names (`--config`, `--config-overlay`, `--timeout`, …)
- Environment names (from your config, e.g. `env-starter run <TAB>`)
- Command names (from your config, e.g. `env-starter command restart <TAB>`)
- File-path completion for `--config` and `--config-overlay` values

## Installation

### zsh

**Recommended — install to `$fpath`** (persists across sessions):

```zsh
mkdir -p ~/.zsh/completions
env-starter completion zsh > ~/.zsh/completions/_env-starter
```

Add this to your `.zshrc`, **before** `compinit` is called, if not already present:

```zsh
fpath=(~/.zsh/completions $fpath)
```

**Alternative — load inline from your `.zshrc`**:

```zsh
source <(env-starter completion zsh)
```

Reload your shell or run `exec zsh` to activate.

### bash

**Recommended — install to bash-completion directory** (persists across sessions):

```bash
env-starter completion bash > ~/.local/share/bash-completion/completions/env-starter
```

**Alternative — load inline from your `.bashrc`**:

```bash
source <(env-starter completion bash)
```

### fish

```fish
env-starter completion fish > ~/.config/fish/completions/env-starter.fish
```

Completion is active immediately in new fish sessions.

## How it works

The completion scripts are thin shell wrappers that delegate all logic to the
hidden `env-starter __complete` subcommand. That command receives the words typed
so far, loads your config **offline** (no daemon required), and prints completion
candidates followed by a directive line (`:0` for normal candidates, `:1` to
signal the shell to fall back to file-path completion).

Environment and command names come from the same config that `env-starter list`
reads, so they always reflect your local configuration without needing a
running daemon.
