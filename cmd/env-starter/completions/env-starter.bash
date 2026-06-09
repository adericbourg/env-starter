# bash completion for env-starter
# Install: source this file from ~/.bashrc, or copy to /etc/bash_completion.d/
#
#   source <(env-starter completion bash)
#   # or persistently:
#   env-starter completion bash > ~/.local/share/bash-completion/completions/env-starter

_env_starter_complete() {
    local output directive
    local -a lines candidates

    # Pass the words typed so far (after the program name) to the Go helper.
    # COMP_WORDS[@]:1:$COMP_CWORD gives COMP_CWORD elements starting at index 1,
    # which is exactly the subcommand + args up to and including the current partial.
    output=$(env-starter __complete "${COMP_WORDS[@]:1:$COMP_CWORD}" 2>/dev/null) || return

    mapfile -t lines <<< "$output"

    # The last line is the directive (:0 default, :1 file completion).
    directive="${lines[-1]}"
    unset 'lines[-1]'

    if [[ "$directive" == ":1" ]]; then
        COMPREPLY=( $(compgen -f -- "${COMP_WORDS[$COMP_CWORD]}") )
        return
    fi

    # Extract the value part (before the tab-separated description).
    # The Go helper already filtered by prefix, so no further filtering needed.
    for line in "${lines[@]}"; do
        [[ -n "$line" ]] && candidates+=( "${line%%	*}" )
    done

    COMPREPLY=("${candidates[@]}")
}

complete -F _env_starter_complete -o nosort env-starter
