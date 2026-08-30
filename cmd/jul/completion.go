// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package main

import (
	"flag"
	"fmt"
	"sort"
	"strings"
)

// completionScripts maps a shell name to a completion script for the `jul`
// command. The scripts complete the subcommand verbs and common diagnostic
// options; file completion covers path-valued arguments. They are static (no
// runtime introspection) so `jul completion <shell>` is a pure, fast emit.
var completionScripts = map[string]string{
	"bash": bashCompletion,
	"zsh":  zshCompletion,
	"fish": fishCompletion,
	// PowerShell is offered under both its canonical name and the pwsh alias.
	"powershell": powershellCompletion,
	"pwsh":       powershellCompletion,
}

// cmdCompletion writes a shell completion script for the requested shell to
// stdout. Exit codes: 0 = ok, 2 = usage error (missing or unsupported shell).
func cmdCompletion(args []string) int {
	fs := flag.NewFlagSet("completion", flag.ContinueOnError)
	fs.SetOutput(stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintf(stderr, "usage: jul completion <%s>\n", strings.Join(completionShells(), "|"))
		return 2
	}
	shell := strings.ToLower(fs.Arg(0))
	script, ok := completionScripts[shell]
	if !ok {
		fmt.Fprintf(stderr, "error: unsupported shell %q (want one of: %s)\n",
			fs.Arg(0), strings.Join(completionShells(), ", "))
		return 2
	}
	fmt.Fprint(stdout, script)
	return 0
}

// completionShells returns the canonical supported shell names, sorted and with
// the pwsh alias folded into powershell so help text lists each shell once.
func completionShells() []string {
	seen := map[string]bool{}
	var out []string
	for name := range completionScripts {
		if name == "pwsh" {
			continue // alias of powershell
		}
		if !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

const bashCompletion = `# jul bash completion. Load with:  source <(jul completion bash)
_jul() {
    local cur subcmds
    cur="${COMP_WORDS[COMP_CWORD]}"
    subcmds="serve check doctor support-bundle lint fmt run healthcheck import version capabilities completion"
    if [ "${COMP_CWORD}" -eq 1 ]; then
        COMPREPLY=( $(compgen -W "${subcmds} -config -check -version -help" -- "${cur}") )
        return 0
    fi
    case "${COMP_WORDS[1]}" in
        completion)
            COMPREPLY=( $(compgen -W "bash zsh fish powershell" -- "${cur}") )
            ;;
        version|capabilities)
            COMPREPLY=( $(compgen -W "-json" -- "${cur}") )
            ;;
        doctor)
            COMPREPLY=( $(compgen -W "-config -json -strict -check-network -timeout -per-check-timeout" -- "${cur}") )
            ;;
        support-bundle)
            COMPREPLY=( $(compgen -W "-config -output -json -check-network -include-logs -log-tail-bytes -timeout -per-collector-timeout -max-artifact-bytes -max-uncompressed-bytes -max-compressed-bytes" -- "${cur}") )
            ;;
        *)
            COMPREPLY=( $(compgen -f -- "${cur}") )
            ;;
    esac
}
complete -F _jul jul
`

const zshCompletion = `#compdef jul
# jul zsh completion. Load with:  source <(jul completion zsh)
_jul() {
    local -a subcmds
    subcmds=(serve check doctor support-bundle lint fmt run healthcheck import version capabilities completion)
    if (( CURRENT == 2 )); then
        _describe -t commands 'jul command' subcmds
        return
    fi
    case ${words[2]} in
        completion)           compadd bash zsh fish powershell ;;
        version|capabilities) compadd -- -json ;;
        doctor)               compadd -- -config -json -strict -check-network -timeout -per-check-timeout ;;
        support-bundle)       compadd -- -config -output -json -check-network -include-logs -log-tail-bytes -timeout -per-collector-timeout -max-artifact-bytes -max-uncompressed-bytes -max-compressed-bytes ;;
        *)                    _files ;;
    esac
}
compdef _jul jul
`

const fishCompletion = `# jul fish completion. Load with:  jul completion fish | source
complete -c jul -f
complete -c jul -n __fish_use_subcommand -a serve          -d 'Run the server (explicit form)'
complete -c jul -n __fish_use_subcommand -a check          -d 'Full runtime preflight check'
complete -c jul -n __fish_use_subcommand -a doctor         -d 'Run deterministic read-only diagnostics'
complete -c jul -n __fish_use_subcommand -a support-bundle -d 'Write a bounded secret-safe local archive'
complete -c jul -n __fish_use_subcommand -a lint           -d 'Validate and report best-practice warnings'
complete -c jul -n __fish_use_subcommand -a fmt            -d 'Rewrite the config in canonical TOML'
complete -c jul -n __fish_use_subcommand -a run            -d 'Run a zero-config server'
complete -c jul -n __fish_use_subcommand -a healthcheck    -d 'Probe the admin health endpoint'
complete -c jul -n __fish_use_subcommand -a import         -d 'Translate an NGINX config'
complete -c jul -n __fish_use_subcommand -a version        -d 'Print version and build metadata'
complete -c jul -n __fish_use_subcommand -a capabilities   -d 'Report compiled features and exit-code contract'
complete -c jul -n __fish_use_subcommand -a completion     -d 'Generate a shell completion script'
complete -c jul -n '__fish_seen_subcommand_from completion' -a 'bash zsh fish powershell'
complete -c jul -n '__fish_seen_subcommand_from version capabilities' -l json -d 'Emit JSON'
complete -c jul -n '__fish_seen_subcommand_from doctor' -l config -r -d 'Configuration file'
complete -c jul -n '__fish_seen_subcommand_from doctor' -l json -d 'Emit JSON'
complete -c jul -n '__fish_seen_subcommand_from doctor' -l strict -d 'Fail on warnings'
complete -c jul -n '__fish_seen_subcommand_from doctor' -l check-network -d 'Enable network-capable checks'
complete -c jul -n '__fish_seen_subcommand_from support-bundle' -l config -r -d 'Configuration file'
complete -c jul -n '__fish_seen_subcommand_from support-bundle' -l output -r -d 'Output archive'
complete -c jul -n '__fish_seen_subcommand_from support-bundle' -l json -d 'Emit JSON status'
complete -c jul -n '__fish_seen_subcommand_from support-bundle' -l include-logs -d 'Include bounded configured access-log tail'
complete -c jul -n '__fish_seen_subcommand_from support-bundle' -l check-network -d 'Enable network-capable doctor checks'
`

const powershellCompletion = `# jul PowerShell completion. Load with:  jul completion powershell | Out-String | Invoke-Expression
Register-ArgumentCompleter -Native -CommandName jul -ScriptBlock {
    param($wordToComplete, $commandAst, $cursorPosition)
    $verbs = @('serve','check','doctor','support-bundle','lint','fmt','run','healthcheck','import','version','capabilities','completion')
    $elements = $commandAst.CommandElements
    if ($elements.Count -le 2) {
        $verbs | Where-Object { $_ -like "$wordToComplete*" } | ForEach-Object {
            [System.Management.Automation.CompletionResult]::new($_, $_, 'ParameterValue', $_)
        }
        return
    }
    $sub = $elements[1].Value
    $candidates = switch ($sub) {
        'completion'     { @('bash','zsh','fish','powershell') }
        'version'        { @('-json') }
        'capabilities'   { @('-json') }
        'doctor'         { @('-config','-json','-strict','-check-network','-timeout','-per-check-timeout') }
        'support-bundle' { @('-config','-output','-json','-check-network','-include-logs','-log-tail-bytes','-timeout','-per-collector-timeout','-max-artifact-bytes','-max-uncompressed-bytes','-max-compressed-bytes') }
        default          { @() }
    }
    $candidates | Where-Object { $_ -like "$wordToComplete*" } | ForEach-Object {
        [System.Management.Automation.CompletionResult]::new($_, $_, 'ParameterValue', $_)
    }
}
`
