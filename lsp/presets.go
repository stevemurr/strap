package lsp

import "encoding/json"

// DefaultConfig enables the built-in server presets. Binaries must be installed
// separately; constructing the configuration neither discovers nor launches them.
// JavaScript and TypeScript share one server configuration and process per root.
func DefaultConfig() Config {
	c := GoConfig()
	for _, preset := range []Config{RustConfig(), PythonConfig(), TypeScriptConfig(), BashConfig()} {
		c.Servers = append(c.Servers, preset.Servers...)
	}
	return c
}

// RustConfig supports Cargo projects and rust-project.json workspaces.
// Set explicit Roots to share one instance across members of a Cargo workspace.
func RustConfig() Config {
	return Config{Servers: []ServerConfig{{
		ID: "rust-analyzer", Command: []string{"rust-analyzer"},
		Languages:   map[string]string{".rs": "rust"},
		RootMarkers: []string{"Cargo.toml", "rust-project.json"},
	}}}
}

// PythonConfig uses Pyright. Interpreter and import-path settings can be supplied
// through Settings (python.pythonPath, python.analysis) or pyright project files.
func PythonConfig() Config {
	return Config{Servers: []ServerConfig{{
		ID: "pyright", Command: []string{"pyright-langserver", "--stdio"},
		Languages:         map[string]string{".py": "python", ".pyi": "python", ".pyw": "python"},
		RootMarkers:       []string{"pyrightconfig.json", "pyproject.toml", "setup.py", "setup.cfg", "requirements.txt"},
		FallbackToSession: true,
		Settings:          json.RawMessage(`{"python":{"analysis":{"diagnosticMode":"openFilesOnly"}}}`),
	}}}
}

// TypeScriptConfig handles both JavaScript and TypeScript, including JSX/TSX
// and CommonJS/ES module suffixes. Do not configure two overlapping instances
// for JavaScript and TypeScript files in the same project.
func TypeScriptConfig() Config {
	return Config{Servers: []ServerConfig{{
		ID: "typescript-language-server", Command: []string{"typescript-language-server", "--stdio"},
		Languages: map[string]string{
			".js": "javascript", ".jsx": "javascriptreact", ".mjs": "javascript", ".cjs": "javascript",
			".ts": "typescript", ".tsx": "typescriptreact", ".mts": "typescript", ".cts": "typescript",
		},
		RootMarkers:       []string{"tsconfig.json", "jsconfig.json", "package.json"},
		FallbackToSession: true,
	}}}
}

// BashConfig handles shell scripts and common Bash startup files. ShellCheck is
// optional, but must be installed for its lint diagnostics. No scripts are run.
func BashConfig() Config {
	return Config{Servers: []ServerConfig{{
		ID: "bash-language-server", Command: []string{"bash-language-server", "start"},
		Languages: map[string]string{
			".sh": "shellscript", ".bash": "shellscript", ".bashrc": "shellscript",
			".bash_profile": "shellscript", ".bash_login": "shellscript", ".bash_logout": "shellscript",
		},
		RootMarkers: []string{".git"}, FallbackToSession: true,
	}}}
}
