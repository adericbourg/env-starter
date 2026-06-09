# fish completion for env-starter
# Install:
#   env-starter completion fish > ~/.config/fish/completions/env-starter.fish

function __env_starter_complete
    # commandline -opc tokenises the current pipeline up to the cursor.
    # commandline -ct  gives just the current (partial) token at the cursor.
    set -l tokens (commandline -opc)
    set -l cur (commandline -ct)

    # Remove the program name (first token).
    set -e tokens[1]

    # Build the args list: previous tokens + the current partial.
    # When cur is non-empty it is already the last element of tokens, so
    # we only append the empty string when the cursor is at a word boundary.
    set -l args $tokens
    if test -z "$cur"
        set args $args ""
    end

    set -l output (env-starter __complete $args 2>/dev/null)

    test (count $output) -eq 0; and return

    # Last element is the directive.
    set -l directive $output[-1]
    set -e output[-1]

    if test "$directive" = ":1"
        # Delegate file-path completion to fish's built-in helper.
        __fish_complete_path "$cur"
        return
    end

    # Print "value\tdescription" lines; fish renders the description natively.
    for line in $output
        test -n "$line"; and echo $line
    end
end

# Suppress default file completion; our function handles everything.
complete -c env-starter -f -a '(__env_starter_complete)'
