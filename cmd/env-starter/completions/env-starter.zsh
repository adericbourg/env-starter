#compdef env-starter
# zsh completion for env-starter
# Install:
#   mkdir -p ~/.zsh/completions
#   env-starter completion zsh > ~/.zsh/completions/_env-starter
#   # then add to .zshrc, before compinit is called:
#   #   fpath=(~/.zsh/completions $fpath)
# or for lazy-loading via compinit (e.g. Oh My Zsh):
#   source <(env-starter completion zsh)

_env_starter() {
    local output directive
    local -a lines descs

    # words[2,$CURRENT] are the args after the program name, up to and including
    # the current partial word.  The leading (@ ) flag ensures each element is
    # passed as a separate argument even when words contain spaces.
    output=$(env-starter __complete "${(@)words[2,$CURRENT]}" 2>/dev/null) || return

    # Split the output on newlines.
    lines=("${(@f)output}")

    # Last element is the directive.
    directive="${lines[-1]}"
    lines=("${(@)lines[1,-2]}")

    if [[ "$directive" == ":1" ]]; then
        _files
        return
    fi

    # Convert "value\tdescription" to zsh _describe format "value:description".
    for line in "${lines[@]}"; do
        [[ -z "$line" ]] && continue
        if [[ "$line" == *$'\t'* ]]; then
            descs+=("${line//$'\t'/:}")
        else
            descs+=("$line")
        fi
    done

    _describe 'candidates' descs
}

# Register the completion function.
# When installed as a file in $fpath (named _env-starter), the #compdef tag
# above handles registration automatically.  When loaded via `source <(...)`,
# compdef registers it explicitly.
compdef _env_starter env-starter
