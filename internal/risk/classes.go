package risk

import (
	"fmt"
	"path"
	"strings"

	"github.com/cjrdz/githints/internal/config"
	"github.com/cjrdz/githints/internal/index"
)

// A class is what a file is, by its path. Its level is fixed here, in code:
// repository rules choose classes, never levels.
type class struct {
	name  string
	level Level
}

var classes = map[string]class{
	"docs":       {"docs", Low},
	"test":       {"test", Low},
	"code":       {"code", Medium},
	"security":   {"security", High},
	"ci":         {"ci", High},
	"infra":      {"infra", High},
	"migration":  {"migration", High},
	"hooks":      {"hooks", High},
	"governance": {"governance", High},
}

// ClassNames lists the classes a review rule may name.
func ClassNames() []string {
	return []string{"docs", "test", "code", "security", "ci", "infra", "migration", "hooks", "governance"}
}

type rule struct {
	match string
	class class
}

// builtinRules classify by file name or directory. Order does not matter:
// every matching rule applies and the highest level wins.
var builtinRules = func() []rule {
	var out []rule
	for name, patterns := range map[string][]string{
		"docs": {"*.md", "*.mdx", "*.rst", "*.adoc", "LICENSE*", "NOTICE*", "AUTHORS*", "docs/**", "doc/**"},
		"security": {".env*", "*.pem", "*.key", "*.p12", "*.pfx", "*.jks", "*.keystore", "id_rsa*", "id_ed25519*",
			"*.kdbx", ".npmrc", ".pypirc", ".netrc"},
		"ci": {".github/workflows/**", ".github/actions/**", ".gitlab-ci.yml", ".gitlab/**", ".circleci/**",
			"Jenkinsfile*", "azure-pipelines.yml", ".buildkite/**", ".drone.yml", "bitbucket-pipelines.yml",
			".woodpecker/**", ".woodpecker.yml", "cloudbuild.yaml"},
		"infra": {"Dockerfile*", "*.Dockerfile", "Containerfile*", "docker-compose*.yml", "docker-compose*.yaml",
			"compose.yml", "compose.yaml", "*.tf", "*.tfvars", "*.tofu", "*.hcl", "Chart.yaml", "*.nomad",
			"k8s/**", "kubernetes/**", "helm/**", "charts/**", "deploy/**", "deployment/**", "ansible/**",
			"playbooks/**", "terraform/**", "infra/**", "Vagrantfile", "Procfile", "fly.toml", "vercel.json",
			"netlify.toml", "nginx.conf", "*.service", "Caddyfile"},
		"migration": {"*.sql", "migrations/**", "migration/**", "db/migrate/**", "alembic/**"},
		"hooks":     {".githooks/**", ".husky/**", ".pre-commit-config.yaml", "lefthook.yml", ".lefthook.yml"},
		"governance": {"CODEOWNERS", "go.mod", "go.sum", "package.json", "package-lock.json", "npm-shrinkwrap.json",
			"yarn.lock", "pnpm-lock.yaml", "bun.lockb", "Cargo.toml", "Cargo.lock", "requirements*.txt",
			"pyproject.toml", "poetry.lock", "Pipfile*", "uv.lock", "Gemfile*", "composer.json", "composer.lock",
			"pom.xml", "build.gradle*", "settings.gradle*", "gradle-wrapper.properties", "*.csproj", "packages.lock.json",
			"pubspec.yaml", "pubspec.lock", "Package.swift", "Package.resolved", "vcpkg.json", "conanfile.*",
			".githints/config.json", ".githintsignore", "SECURITY.md"},
	} {
		for _, p := range patterns {
			out = append(out, rule{p, classes[name]})
		}
	}
	return out
}()

// securityWords are directory or file-name words (split on / . _ -) that mark
// security code: "internal/auth/token.go" has two, "tokenizer.go" none.
var securityWords = map[string]bool{
	"auth": true, "authn": true, "authz": true, "authentication": true, "authorization": true,
	"oauth": true, "oauth2": true, "oidc": true, "saml": true, "sso": true, "jwt": true, "jwks": true,
	"crypto": true, "cryptography": true, "cipher": true, "encrypt": true, "encryption": true, "decrypt": true,
	"hmac": true, "signing": true, "signature": true, "signatures": true, "tls": true, "x509": true, "cert": true, "certs": true,
	"session": true, "sessions": true, "token": true, "tokens": true, "secret": true, "secrets": true,
	"password": true, "passwords": true, "passwd": true, "credential": true, "credentials": true,
	"permission": true, "permissions": true, "acl": true, "rbac": true, "abac": true, "iam": true, "policy": true, "policies": true,
	"login": true, "logout": true, "signin": true, "signup": true, "mfa": true, "totp": true, "otp": true,
	"csrf": true, "cors": true, "sanitize": true, "sanitizer": true, "salt": true, "keyring": true, "vault": true,
}

// classify returns every class that applies to p. The built-in rules decide
// first, including the default (a file no rule names is code); repository
// rules are applied after and can only add to that, so config.json cannot
// lower a level by naming code or auth files "docs".
func classify(p string, repoRules []rule) []class {
	seen := map[string]bool{}
	var out []class
	addClass := func(c class) {
		if !seen[c.name] {
			seen[c.name] = true
			out = append(out, c)
		}
	}
	for _, r := range builtinRules {
		if Match(r.match, p) {
			addClass(r.class)
		}
	}
	// The words are a heuristic for code. A page of documentation about
	// authentication is still documentation.
	if !seen["docs"] {
		for _, w := range strings.FieldsFunc(strings.ToLower(p), func(r rune) bool {
			return r == '/' || r == '.' || r == '_' || r == '-'
		}) {
			if securityWords[w] {
				addClass(classes["security"])
				break
			}
		}
	}
	if index.IsTestFile(p) {
		addClass(classes["test"])
	}
	if len(out) == 0 {
		addClass(classes["code"])
	}
	for _, r := range repoRules {
		if Match(r.match, p) {
			addClass(r.class)
		}
	}
	return out
}

// Limits on repository rules. config.json arrives in clones, so what it can
// make every assessment do is bounded.
const (
	maxRules       = 100
	maxRulePattern = 200
)

// compileRules turns config rules into rules, skipping (with a warning) any
// that name an unknown class or are not a valid pattern. A rule naming a class
// added by a newer githints must not break this one, least of all on the
// commit path.
func compileRules(cfg []config.ReviewRule) ([]rule, []string) {
	var out []rule
	var warnings []string
	if len(cfg) > maxRules {
		warnings = append(warnings, fmt.Sprintf("review.rules has %d entries; only the first %d are used", len(cfg), maxRules))
		cfg = cfg[:maxRules]
	}
	for i, r := range cfg {
		if err := CheckRule(r); err != nil {
			warnings = append(warnings, fmt.Sprintf("review.rules[%d] skipped: %v", i, err))
			continue
		}
		out = append(out, rule{r.Match, classes[r.Class]})
	}
	return out, warnings
}

// CheckRule reports what is wrong with one review rule, for compileRules and
// for doctor, which is strict where the hook path is lenient.
func CheckRule(r config.ReviewRule) error {
	if _, ok := classes[r.Class]; !ok {
		return fmt.Errorf("unknown class %q (known: %s)", r.Class, strings.Join(ClassNames(), ", "))
	}
	if r.Match == "" || len(r.Match) > maxRulePattern {
		return fmt.Errorf("match must be 1 to %d bytes", maxRulePattern)
	}
	if _, err := path.Match(strings.TrimSuffix(r.Match, "/**"), ""); err != nil {
		return fmt.Errorf("match %q: %v", r.Match, err)
	}
	return nil
}
