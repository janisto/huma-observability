package obs

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

var (
	githubRESTCaller = regexp.MustCompile(
		`\bgh[[:space:]]+api\b|` +
			`\b(?:github|octokit)\.(?:rest\b|request[[:space:]]*\(|paginate[[:space:]]*\()|` +
			`https?://api\.github\.com\b`,
	)
	lockedGitHubHeader = regexp.MustCompile(
		`X-GitHub-Api-Version["']?[[:space:]]*[:=[:space:]]` +
			`[[:space:]]*["']?2026-03-10\b`,
	)
	githubAPIHeaderName = regexp.MustCompile(`(?i)X-GitHub-Api-Version`)
	githubClientAlias   = regexp.MustCompile(
		`\b(?:const[[:space:]]+|let[[:space:]]+|var[[:space:]]+)?` +
			`([A-Za-z_][A-Za-z0-9_]*)[[:space:]]*(?::=|=)[[:space:]]*` +
			`(?:new[[:space:]]+)?[^\n]*(?:Octokit|GitHub|Github|octokit|github)\b`,
	)
)

var automatedPolicyExtensions = map[string]bool{
	".bash": true, ".cjs": true, ".go": true, ".js": true, ".json": true,
	".mjs": true, ".py": true, ".rs": true, ".sh": true, ".toml": true,
	".ts": true, ".yaml": true, ".yml": true, ".zsh": true,
}

var skippedPolicyDirectories = map[string]bool{
	".git": true, ".venv": true, "artifacts": true, "coverage": true,
	"dist": true, "mutants": true, "node_modules": true, "target": true,
}

func isAutomatedPolicyPath(path string) bool {
	normalized := filepath.ToSlash(path)
	return normalized != "github_api_policy_test.go" &&
		!strings.HasSuffix(normalized, ".md") &&
		(automatedPolicyExtensions[filepath.Ext(normalized)] || filepath.Base(normalized) == "Justfile")
}

func githubAPIPolicyViolations(files map[string]string) []string {
	var violations []string
	for path, content := range files {
		if !isAutomatedPolicyPath(path) {
			continue
		}
		lines := strings.Split(content, "\n")
		aliases := githubAPICallerAliases(content)
		for index, line := range lines {
			if !isGitHubRESTCaller(line, aliases) {
				continue
			}
			limit := min(len(lines), index+12)
			end := index + 1
			for end < limit && !isGitHubRESTCaller(lines[end], aliases) {
				end++
			}
			block := strings.Join(lines[index:end], "\n")
			if len(githubAPIHeaderName.FindAllString(block, -1)) != 1 || !lockedGitHubHeader.MatchString(block) {
				violations = append(violations, fmt.Sprintf("%s:%d", path, index+1))
			}
		}
	}
	sort.Strings(violations)
	return violations
}

func githubAPICallerAliases(content string) []string {
	matches := githubClientAlias.FindAllStringSubmatch(content, -1)
	aliases := make([]string, 0, len(matches))
	for _, match := range matches {
		aliases = append(aliases, match[1])
	}
	return aliases
}

func isGitHubRESTCaller(line string, aliases []string) bool {
	if githubRESTCaller.MatchString(line) {
		return true
	}
	for _, alias := range aliases {
		for _, method := range []string{".rest", ".request(", ".paginate("} {
			if strings.Contains(line, alias+method) {
				return true
			}
		}
	}
	return false
}

func repositoryPolicyFiles(t *testing.T) map[string]string {
	t.Helper()
	files := map[string]string{}
	err := filepath.WalkDir(".", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if path != "." && skippedPolicyDirectories[entry.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if !entry.Type().IsRegular() || !isAutomatedPolicyPath(path) {
			return nil
		}
		//nolint:gosec // WalkDir supplied this regular file beneath the repository root.
		content, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		files[filepath.ToSlash(strings.TrimPrefix(path, "."+string(filepath.Separator)))] = string(content)
		return nil
	})
	if err != nil {
		t.Fatalf("scan repository policy files: %v", err)
	}
	return files
}

func TestGitHubAPIPolicyFixtures(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		files    map[string]string
		expected []string
	}{
		{
			name: "zero automated callers and human documentation",
			files: map[string]string{
				"README.md": "Use `gh api` with the locally installed CLI.",
			},
		},
		{
			name: "exact header",
			files: map[string]string{
				"workflow.yml": "github.request(\"GET /repo\", {\n" +
					"headers: {\"X-GitHub-Api-Version\": \"2026-03-10\"}\n})",
			},
		},
		{
			name:     "missing header",
			files:    map[string]string{"client.go": "github.request(\"GET /repo\")"},
			expected: []string{"client.go:1"},
		},
		{
			name:     "dynamic header",
			files:    map[string]string{"client.go": "github.request(\"GET /repo\", header=VERSION)"},
			expected: []string{"client.go:1"},
		},
		{
			name: "different header",
			files: map[string]string{
				"client.go": "github.request(\"GET /repo\", " +
					"header=\"X-GitHub-Api-Version: 2022-11-28\")",
			},
			expected: []string{"client.go:1"},
		},
		{
			name: "mixed callers",
			files: map[string]string{
				"client.go": "github.request(\"GET /one\", " +
					"header=\"X-GitHub-Api-Version: 2026-03-10\")\n" +
					"github.request(\"GET /two\")",
			},
			expected: []string{"client.go:2"},
		},
		{
			name: "conflicting versions in one caller",
			files: map[string]string{
				"client.go": "github.request(\"GET /one\", " +
					"header=\"X-GitHub-Api-Version: 2026-03-10\")\n" +
					"header=\"X-GitHub-Api-Version: 2022-11-28\"",
			},
			expected: []string{"client.go:1"},
		},
		{
			name: "aliased octokit caller",
			files: map[string]string{
				"client.go": "const client = new Octokit()\nclient.request(\"GET /repo\")",
			},
			expected: []string{"client.go:2"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			actual := githubAPIPolicyViolations(test.files)
			if !slicesEqual(actual, test.expected) {
				t.Fatalf("violations = %v, want %v", actual, test.expected)
			}
		})
	}
}

func TestRepositoryHasNoUnpinnedAutomatedGitHubRESTCaller(t *testing.T) {
	t.Parallel()
	violations := githubAPIPolicyViolations(repositoryPolicyFiles(t))
	if len(violations) != 0 {
		t.Fatalf("unpinned automated GitHub REST callers: %v", violations)
	}
}

func slicesEqual(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
