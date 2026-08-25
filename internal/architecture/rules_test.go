package architecture

import (
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const modulePath = "github.com/marstack-labs/marstack-cloud"

type sourceFile struct {
	pkg     string
	path    string
	imports []string
}

func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}
	return root
}

func loadSources(t *testing.T) []sourceFile {
	t.Helper()

	root := repoRoot(t)
	fset := token.NewFileSet()
	var files []sourceFile

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if name := d.Name(); name != "." && (strings.HasPrefix(name, ".") || name == "bin" || name == "data") {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}

		parsed, parseErr := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if parseErr != nil {
			return parseErr
		}

		rel, relErr := filepath.Rel(root, filepath.Dir(path))
		if relErr != nil {
			return relErr
		}

		sf := sourceFile{
			pkg:  filepath.ToSlash(rel),
			path: filepath.ToSlash(strings.TrimPrefix(path, root+string(filepath.Separator))),
		}
		for _, spec := range parsed.Imports {
			unquoted, unquoteErr := strconv.Unquote(spec.Path.Value)
			if unquoteErr != nil {
				return unquoteErr
			}
			if strings.HasPrefix(unquoted, modulePath+"/") {
				sf.imports = append(sf.imports, strings.TrimPrefix(unquoted, modulePath+"/"))
			}
		}
		files = append(files, sf)
		return nil
	})
	if err != nil {
		t.Fatalf("walk sources: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("no Go sources found")
	}
	return files
}

func TestKernelDoesNotDependOnPlatformOrApp(t *testing.T) {
	for _, f := range loadSources(t) {
		if !strings.HasPrefix(f.pkg, "internal/kernel/") {
			continue
		}
		for _, imp := range f.imports {
			if strings.HasPrefix(imp, "internal/platform/") || imp == "internal/app" || imp == "internal/store" {
				t.Errorf("%s imports %s: kernel must not depend on platform, app, or store", f.path, imp)
			}
		}
	}
}

func TestPlatformModulesDoNotImportEachOther(t *testing.T) {
	for _, f := range loadSources(t) {
		owner, ok := platformModuleOf(f.pkg)
		if !ok {
			continue
		}
		for _, imp := range f.imports {
			target, isPlatform := platformModuleOf(imp)
			if isPlatform && target != owner {
				t.Errorf("%s imports platform module %q: modules must not depend on each other directly", f.path, target)
			}
		}
	}
}

func TestPlatformDoesNotImportApp(t *testing.T) {
	for _, f := range loadSources(t) {
		if _, ok := platformModuleOf(f.pkg); !ok {
			continue
		}
		for _, imp := range f.imports {
			if imp == "internal/app" {
				t.Errorf("%s imports internal/app: the composition root must depend on modules, never the reverse", f.path)
			}
		}
	}
}

func TestAgentDoesNotReachIntoTheControlPlane(t *testing.T) {
	for _, f := range loadSources(t) {
		if !strings.HasPrefix(f.pkg, "internal/agent") {
			continue
		}
		for _, imp := range f.imports {
			if strings.HasPrefix(imp, "internal/platform/") || imp == "internal/app" || imp == "internal/store" {
				t.Errorf("%s imports %s: the agent talks to the control plane over HTTP, not in process", f.path, imp)
			}
		}
	}
}

func TestStoreStaysInfrastructure(t *testing.T) {
	for _, f := range loadSources(t) {
		if f.pkg != "internal/store" {
			continue
		}
		for _, imp := range f.imports {
			if strings.HasPrefix(imp, "internal/platform/") || imp == "internal/app" {
				t.Errorf("%s imports %s: store must not know about platform modules", f.path, imp)
			}
		}
	}
}

func TestCommandOnlyWiresTheCLI(t *testing.T) {
	for _, f := range loadSources(t) {
		if !strings.HasPrefix(f.pkg, "cmd/") {
			continue
		}
		for _, imp := range f.imports {
			if imp != "internal/cli" {
				t.Errorf("%s imports %s: cmd must only wire internal/cli", f.path, imp)
			}
		}
	}
}

func platformModuleOf(pkg string) (string, bool) {
	const prefix = "internal/platform/"
	if !strings.HasPrefix(pkg, prefix) {
		return "", false
	}
	rest := strings.TrimPrefix(pkg, prefix)
	if rest == "" {
		return "", false
	}
	return strings.Split(rest, "/")[0], true
}
