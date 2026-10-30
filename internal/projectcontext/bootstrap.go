package projectcontext

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/lazyun/devbridge/internal/module"
	"github.com/lazyun/devbridge/internal/observability"
	"github.com/lazyun/devbridge/internal/repository"
)

const (
	maxGuidanceBytes       = 1 << 20
	maxManifestBytes       = 256 << 10
	maxRepresentativeBytes = 64 << 10
	maxDirectoryEntries    = 512
	maxProjectModules      = 100
	maxContextDiagnostics  = 256
)

type RepositoryList interface {
	List(context.Context) ([]repository.Repository, error)
}

// topologyRepositoryList is deliberately optional for compatibility with
// in-memory/bootstrap-only repository lists. The production repository
// authority supplies this revision so a discovery made before an unlink cannot
// publish itself as the current Project Context afterwards.
type topologyRepositoryList interface {
	TopologyRevision(context.Context, string) (int64, error)
}

type ModuleMap interface {
	ReplaceContextMap(context.Context, string, []module.ContextModuleInput) ([]module.Module, error)
	ListContextMapByProject(context.Context, string) ([]module.Module, error)
}

// Bootstrapper owns the bounded, production-only Project Context bootstrap.
// It never invokes test/build/lint/static-analysis commands and never walks a
// repository recursively. A cheap structural fingerprint avoids rebuilding a
// valid Module Map for replacement AI sessions and ordinary source commits.
type Bootstrapper struct {
	contexts     *Service
	repositories RepositoryList
	modules      ModuleMap
}

func NewBootstrapper(contexts *Service, repositories RepositoryList, modules ModuleMap) (*Bootstrapper, error) {
	if contexts == nil || repositories == nil || modules == nil {
		return nil, errors.New("Project Context bootstrap dependencies are incomplete")
	}
	return &Bootstrapper{contexts: contexts, repositories: repositories, modules: modules}, nil
}

func (b *Bootstrapper) Ensure(ctx context.Context, projectID string) (ProjectContext, error) {
	ctx = observability.WithContext(ctx, observability.Correlation{ProjectID: projectID})
	observability.Info(ctx, "project_context", "project_context.bootstrap.requested", "Project Context bootstrap requested", nil)
	current, err := b.contexts.EnsureRequired(ctx, projectID)
	if err != nil {
		observability.Error(ctx, "project_context", "project_context.bootstrap.failed", "Project Context bootstrap failed", map[string]any{"reasonCode": "ENSURE_REQUIRED_FAILED", "errorSummary": err.Error()})
		return ProjectContext{}, err
	}
	if current.Status == StatusRunning {
		// An existing asynchronous acquisition owns this lifecycle. Do not race
		// it with a second bootstrap or turn project_open into a polling loop.
		observability.Info(ctx, "project_context", "project_context.bootstrap.blocked", "Project Context acquisition is already running", map[string]any{"reasonCode": "ACQUISITION_RUNNING"})
		return current, nil
	}
	repositories, err := b.projectRepositories(ctx, projectID)
	if err != nil {
		observability.Error(ctx, "project_context", "project_context.bootstrap.failed", "Project repositories could not be listed", map[string]any{"reasonCode": "REPOSITORY_LIST_FAILED", "errorSummary": err.Error()})
		return ProjectContext{}, err
	}
	if len(repositories) == 0 {
		// Project selection and Feature Area capture remain valid before Repository
		// registration. Review readiness stays REQUIRED until a map is possible.
		observability.Info(ctx, "project_context", "project_context.bootstrap.blocked", "Project Context awaits a repository", map[string]any{"reasonCode": "NO_REPOSITORIES", "repositoryCount": 0})
		return current, nil
	}
	topologyRevision, err := b.repositoryTopologyRevision(ctx, projectID)
	if err != nil {
		observability.Error(ctx, "project_context", "project_context.bootstrap.failed", "Project repository topology could not be read", map[string]any{"reasonCode": "REPOSITORY_TOPOLOGY_READ_FAILED", "errorSummary": err.Error()})
		return ProjectContext{}, err
	}
	fingerprint, guidance, err := inspectBootstrapInputs(repositories)
	if err != nil {
		observability.Error(ctx, "project_context", "project_context.bootstrap.failed", "Project Context inputs could not be inspected", map[string]any{"reasonCode": "INPUT_INSPECTION_FAILED", "repositoryCount": len(repositories), "errorSummary": err.Error()})
		return ProjectContext{}, err
	}
	if (current.Status == StatusReady || current.Status == StatusNoGuidance) && current.ContextFingerprint == fingerprint {
		values, listErr := b.modules.ListContextMapByProject(ctx, projectID)
		if listErr == nil && len(values) != 0 && validateExistingMap(repositories, values) == nil {
			observability.Info(ctx, "project_context", "project_context.reused", "existing Project Context reused", map[string]any{"repositoryCount": len(repositories), "moduleCount": len(values), "structuralFingerprint": fingerprint, "guidanceState": current.Status})
			return current, nil
		}
	}
	observability.Info(ctx, "project_context", "project_context.rebuild.required", "Project Context rebuild required", map[string]any{"reasonCode": "STRUCTURAL_CHANGE_OR_INVALID_MAP", "repositoryCount": len(repositories), "structuralFingerprint": fingerprint})
	observability.Info(ctx, "project_context", "project_context.bootstrap.started", "Project Context bootstrap started", map[string]any{"repositoryCount": len(repositories), "structuralFingerprint": fingerprint})

	inputs := make([]module.ContextModuleInput, 0)
	diagnostics := make([]string, 0)
	repositoryPrefixes := stableRepositoryPrefixes(repositories)
	for _, value := range repositories {
		discovered, repositoryDiagnostics, discoverErr := discoverRepository(value, guidance[value.ID], repositoryPrefixes[value.ID])
		if discoverErr != nil {
			observability.Error(ctx, "project_context", "project_context.bootstrap.failed", "Project Context discovery failed", map[string]any{"reasonCode": "DISCOVERY_FAILED", "repositoryId": value.ID, "errorSummary": discoverErr.Error()})
			return ProjectContext{}, fmt.Errorf("discover production structure for %s: %w", value.Name, discoverErr)
		}
		inputs = append(inputs, discovered...)
		diagnostics = append(diagnostics, repositoryDiagnostics...)
	}
	if len(inputs) == 0 {
		// An empty Repository has no truthful production boundary to publish.
		// Keep context pending rather than inventing a repository-wide module.
		observability.Info(ctx, "project_context", "project_context.bootstrap.blocked", "Project Context has no truthful production boundary", map[string]any{"reasonCode": "NO_PRODUCTION_BOUNDARY", "repositoryCount": len(repositories)})
		return current, nil
	}
	merged := mergeModuleInputs(inputs)
	if len(merged) > maxProjectModules {
		merged = merged[:maxProjectModules]
		diagnostics = append(diagnostics, "Project Module Map was bounded to the first 100 stable production module keys.")
	}
	if writer, ok := b.modules.(topologyModuleMap); ok && topologyRevision > 0 {
		if _, err := writer.ReplaceContextMapAtTopology(ctx, projectID, merged, topologyRevision); err != nil {
			observability.Error(ctx, "project_context", "project_context.bootstrap.failed", "Project Context module map could not be persisted", map[string]any{"reasonCode": "MODULE_MAP_PERSIST_FAILED", "moduleCount": len(merged), "errorSummary": err.Error()})
			return ProjectContext{}, err
		}
	} else if _, err := b.modules.ReplaceContextMap(ctx, projectID, merged); err != nil {
		observability.Error(ctx, "project_context", "project_context.bootstrap.failed", "Project Context module map could not be persisted", map[string]any{"reasonCode": "MODULE_MAP_PERSIST_FAILED", "moduleCount": len(merged), "errorSummary": err.Error()})
		return ProjectContext{}, err
	}
	status := StatusNoGuidance
	identityParts := make([]string, 0)
	preserved := strings.Builder{}
	for _, value := range repositories {
		item := guidance[value.ID]
		if item.Identity == "" {
			diagnostics = append(diagnostics, fmt.Sprintf("No project-level guidance was found for Repository %q; a lightweight production map was created.", value.Name))
			continue
		}
		status = StatusReady
		identityParts = append(identityParts, value.ID+"="+item.Identity)
		fmt.Fprintf(&preserved, "Repository: %s\n%s\n", value.Name, item.Content)
	}
	sort.Strings(identityParts)
	identity := ""
	if len(identityParts) != 0 {
		sum := sha256.Sum256([]byte(strings.Join(identityParts, "\n")))
		identity = "sha256:" + hex.EncodeToString(sum[:])
	}
	diagnostics = uniqueSorted(diagnostics)
	if len(diagnostics) > maxContextDiagnostics {
		diagnostics = append(diagnostics[:maxContextDiagnostics-1], "Additional Project Context diagnostics were omitted by the bounded projection.")
	}
	if committer, ok := b.contexts.store.(topologyContextCommitter); ok && topologyRevision > 0 {
		result, err := committer.MarkBootstrappedAtTopology(ctx, projectID, status, fingerprint, identity, boundedString(preserved.String(), maxGuidanceBytes), diagnostics, topologyRevision, b.contexts.now().UTC())
		if err != nil {
			observability.Error(ctx, "project_context", "project_context.bootstrap.failed", "Project Context readiness could not be persisted", map[string]any{"reasonCode": "READY_PERSIST_FAILED", "errorSummary": err.Error()})
			return ProjectContext{}, err
		}
		observability.Info(ctx, "project_context", "project_context.bootstrap.ready", "Project Context bootstrap ready", map[string]any{"repositoryCount": len(repositories), "moduleCount": len(merged), "structuralFingerprint": fingerprint, "guidanceState": status})
		return result, nil
	}
	result, err := b.contexts.MarkBootstrapped(ctx, projectID, status, fingerprint, identity, boundedString(preserved.String(), maxGuidanceBytes), diagnostics)
	if err != nil {
		observability.Error(ctx, "project_context", "project_context.bootstrap.failed", "Project Context readiness could not be persisted", map[string]any{"reasonCode": "READY_PERSIST_FAILED", "errorSummary": err.Error()})
		return ProjectContext{}, err
	}
	observability.Info(ctx, "project_context", "project_context.bootstrap.ready", "Project Context bootstrap ready", map[string]any{"repositoryCount": len(repositories), "moduleCount": len(merged), "structuralFingerprint": fingerprint, "guidanceState": status})
	return result, nil
}

type topologyModuleMap interface {
	ReplaceContextMapAtTopology(context.Context, string, []module.ContextModuleInput, int64) ([]module.Module, error)
}

type topologyContextCommitter interface {
	MarkBootstrappedAtTopology(context.Context, string, Status, string, string, string, []string, int64, time.Time) (ProjectContext, error)
}

func (b *Bootstrapper) repositoryTopologyRevision(ctx context.Context, projectID string) (int64, error) {
	reader, ok := b.repositories.(topologyRepositoryList)
	if !ok {
		return 0, nil
	}
	return reader.TopologyRevision(ctx, projectID)
}

func (b *Bootstrapper) projectRepositories(ctx context.Context, projectID string) ([]repository.Repository, error) {
	values, err := b.repositories.List(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]repository.Repository, 0)
	for _, value := range values {
		if value.ProjectID == projectID {
			result = append(result, value)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result, nil
}

type guidanceInput struct {
	Identity string
	Content  string
}

func inspectBootstrapInputs(repositories []repository.Repository) (string, map[string]guidanceInput, error) {
	hash := sha256.New()
	guidance := make(map[string]guidanceInput, len(repositories))
	for _, value := range repositories {
		fmt.Fprintf(hash, "repository:%s\n", value.ID)
		item, err := readRootGuidance(value.Path)
		if err != nil {
			return "", nil, err
		}
		guidance[value.ID] = item
		fmt.Fprintf(hash, "guidance:%s\n", item.Identity)
		entries, err := boundedReadDir(value.Path)
		if err != nil {
			return "", nil, err
		}
		for _, entry := range entries {
			name := entry.Name()
			if entry.Type()&os.ModeSymlink != 0 || ignoredProductionName(name) || excludedProjectContextPath(name) {
				continue
			}
			fmt.Fprintf(hash, "top:%s:%t\n", name, entry.IsDir())
			if entry.IsDir() && isContainerRoot(name) {
				children, readErr := boundedReadDir(filepath.Join(value.Path, name))
				if readErr != nil {
					return "", nil, readErr
				}
				for _, child := range children {
					relative := filepath.ToSlash(filepath.Join(name, child.Name()))
					if child.Type()&os.ModeSymlink != 0 || ignoredProductionName(child.Name()) || excludedProjectContextPath(relative) {
						continue
					}
					fmt.Fprintf(hash, "boundary:%s:%t\n", relative, child.IsDir())
				}
			}
		}
		for _, manifest := range knownManifests {
			path := filepath.Join(value.Path, manifest)
			contents, readErr := readRegularBounded(path, maxManifestBytes)
			if errors.Is(readErr, os.ErrNotExist) {
				continue
			}
			if readErr != nil {
				return "", nil, readErr
			}
			fmt.Fprintf(hash, "manifest:%s:%x\n", manifest, sha256.Sum256(contents))
		}
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), guidance, nil
}

var knownManifests = []string{"Cargo.toml", "go.mod", "package.json", "pom.xml", "pyproject.toml", "settings.gradle", "settings.gradle.kts"}

func readRootGuidance(root string) (guidanceInput, error) {
	contents, err := readRegularBounded(filepath.Join(root, "AGENTS.md"), maxGuidanceBytes)
	if errors.Is(err, os.ErrNotExist) {
		return guidanceInput{}, nil
	}
	if err != nil {
		return guidanceInput{}, fmt.Errorf("read root AGENTS.md: %w", err)
	}
	sum := sha256.Sum256(contents)
	return guidanceInput{Identity: "sha256:" + hex.EncodeToString(sum[:]), Content: string(contents)}, nil
}

type discoveredRoot struct {
	Path           string
	Key            string
	Name           string
	Entrypoints    []string
	Responsibility string
	Source         module.ResponsibilitySource
	Imports        []string
}

func discoverRepository(value repository.Repository, guidance guidanceInput, repositoryPrefix string) ([]module.ContextModuleInput, []string, error) {
	entries, err := boundedReadDir(value.Path)
	if err != nil {
		return nil, nil, err
	}
	roots := make([]discoveredRoot, 0)
	for _, entry := range entries {
		name := entry.Name()
		if entry.Type()&os.ModeSymlink != 0 || ignoredProductionName(name) || excludedProjectContextPath(name) {
			continue
		}
		if entry.IsDir() {
			if isContainerRoot(name) {
				children, readErr := boundedReadDir(filepath.Join(value.Path, name))
				if readErr != nil {
					return nil, nil, readErr
				}
				for _, child := range children {
					relative := filepath.ToSlash(filepath.Join(name, child.Name()))
					if child.Type()&os.ModeSymlink != 0 || ignoredProductionName(child.Name()) || excludedProjectContextPath(relative) {
						continue
					}
					if child.IsDir() || productionSourceName(child.Name()) {
						roots = append(roots, inspectDiscoveredRoot(value.Path, relative, guidance.Content))
					}
				}
				continue
			}
			roots = append(roots, inspectDiscoveredRoot(value.Path, name, guidance.Content))
			continue
		}
		if productionArtifactName(name) {
			roots = append(roots, inspectDiscoveredRoot(value.Path, name, guidance.Content))
		}
	}
	if len(roots) == 0 {
		for _, manifest := range knownManifests {
			if _, statErr := os.Stat(filepath.Join(value.Path, manifest)); statErr == nil {
				roots = append(roots, inspectDiscoveredRoot(value.Path, manifest, guidance.Content))
				break
			}
		}
	}
	assignStableKeys(roots, repositoryPrefix)
	for i := range roots {
		for _, imported := range roots[i].Imports {
			for _, candidate := range roots {
				if candidate.Key != roots[i].Key && (strings.HasSuffix(imported, "/"+candidate.Path) || imported == candidate.Path) {
					roots[i].Imports = append(roots[i].Imports, candidate.Key)
				}
			}
		}
		roots[i].Imports = moduleKeysOnly(roots[i].Imports, roots)
	}
	result := make([]module.ContextModuleInput, 0, len(roots))
	for _, root := range roots {
		if _, err := validateRelativeProductionPath(value.Path, root.Path); err != nil {
			return nil, nil, err
		}
		result = append(result, module.ContextModuleInput{
			Key: root.Key, Name: root.Name, Description: root.Responsibility,
			ProductionFacts: []module.ProductionFacts{{RepositoryID: value.ID, RepositoryName: value.Name, Responsibility: root.Responsibility, ResponsibilitySource: root.Source, ProductionRoots: []string{root.Path}, Entrypoints: root.Entrypoints, RelatedModules: root.Imports}},
		})
	}
	diagnostics := guidancePathDiagnostics(value, guidance.Content)
	return result, diagnostics, nil
}

func inspectDiscoveredRoot(repositoryRoot, relative, guidance string) discoveredRoot {
	result := discoveredRoot{Path: filepath.ToSlash(relative), Name: humanName(filepath.Base(relative)), Source: module.ResponsibilityDerived}
	absolute := filepath.Join(repositoryRoot, filepath.FromSlash(relative))
	info, _ := os.Stat(absolute)
	files := []string{relative}
	if info != nil && info.IsDir() {
		entries, _ := boundedReadDir(absolute)
		files = files[:0]
		for _, entry := range entries {
			candidate := filepath.ToSlash(filepath.Join(relative, entry.Name()))
			if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || excludedProjectContextPath(candidate) || !productionSourceName(entry.Name()) {
				continue
			}
			files = append(files, candidate)
			if rootEntrypointName(entry.Name()) {
				result.Entrypoints = append(result.Entrypoints, candidate)
			}
			if len(files) == 8 {
				break
			}
		}
	} else if rootEntrypointName(filepath.Base(relative)) {
		result.Entrypoints = []string{relative}
	}
	for _, line := range strings.Split(guidance, "\n") {
		lower := strings.ToLower(line)
		if strings.Contains(line, relative) && (strings.Contains(lower, "responsib") || strings.Contains(lower, " owns ") || strings.Contains(lower, "module")) {
			result.Responsibility = boundedString(strings.TrimSpace(strings.TrimLeft(line, "#*- ")), 4096)
			result.Source = module.ResponsibilityGuidance
			break
		}
	}
	for _, file := range files {
		contents, err := readRegularBounded(filepath.Join(repositoryRoot, filepath.FromSlash(file)), maxRepresentativeBytes)
		if err != nil {
			continue
		}
		if result.Responsibility == "" {
			if match := packageDocPattern.FindSubmatch(contents); len(match) == 2 {
				result.Responsibility = boundedString(strings.TrimSpace(string(match[1])), 4096)
				result.Source = module.ResponsibilityPackageDoc
			}
		}
		for _, match := range quotedImportPattern.FindAllSubmatch(contents, -1) {
			result.Imports = append(result.Imports, string(match[1]))
		}
	}
	if result.Responsibility == "" {
		result.Responsibility = "Owns production implementation under " + result.Path + "."
	}
	result.Entrypoints = uniqueSorted(result.Entrypoints)
	result.Imports = uniqueSorted(result.Imports)
	return result
}

var (
	packageDocPattern   = regexp.MustCompile(`(?m)^// Package [A-Za-z0-9_]+\s+([^\n]+)`)
	quotedImportPattern = regexp.MustCompile(`["']([^"'\s]+)["']`)
	backtickPathPattern = regexp.MustCompile("`([A-Za-z0-9._-]+(?:/[A-Za-z0-9._-]+)+)`")
)

func guidancePathDiagnostics(value repository.Repository, guidance string) []string {
	result := make([]string, 0)
	for _, match := range backtickPathPattern.FindAllStringSubmatch(guidance, -1) {
		path := match[1]
		if excludedProjectContextPath(path) || ignoredProductionName(strings.Split(path, "/")[0]) {
			continue
		}
		if _, err := validateRelativeProductionPath(value.Path, path); err != nil {
			result = append(result, fmt.Sprintf("Guidance for Repository %q refers to missing or invalid production path %q.", value.Name, path))
		}
	}
	return uniqueSorted(result)
}

func validateExistingMap(repositories []repository.Repository, values []module.Module) error {
	byID := make(map[string]repository.Repository, len(repositories))
	for _, value := range repositories {
		byID[value.ID] = value
	}
	for _, value := range values {
		for _, facts := range value.ProductionFacts {
			repositoryValue, ok := byID[facts.RepositoryID]
			if !ok || len(facts.ProductionRoots) == 0 {
				return errors.New("Project Module Map Repository is unavailable")
			}
			for _, root := range facts.ProductionRoots {
				if _, err := validateRelativeProductionPath(repositoryValue.Path, root); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func validateRelativeProductionPath(root, supplied string) (string, error) {
	if excludedProjectContextPath(supplied) {
		return "", errors.New("Project Context path is excluded")
	}
	if supplied == "" || filepath.IsAbs(supplied) || strings.Contains(supplied, "\\") || strings.ContainsRune(supplied, '\x00') {
		return "", errors.New("invalid production path")
	}
	clean := filepath.Clean(filepath.FromSlash(supplied))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", errors.New("invalid production path")
	}
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	target, err := filepath.EvalSymlinks(filepath.Join(canonicalRoot, clean))
	if err != nil {
		return "", errors.New("production path does not exist")
	}
	relative, err := filepath.Rel(canonicalRoot, target)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", errors.New("production path escapes Repository")
	}
	info, err := os.Stat(target)
	if err != nil || (!info.IsDir() && !info.Mode().IsRegular()) {
		return "", errors.New("production path is not a file or directory")
	}
	return filepath.ToSlash(relative), nil
}

func mergeModuleInputs(values []module.ContextModuleInput) []module.ContextModuleInput {
	byKey := map[string]module.ContextModuleInput{}
	for _, value := range values {
		current, ok := byKey[value.Key]
		if !ok {
			byKey[value.Key] = value
			continue
		}
		current.ProductionFacts = append(current.ProductionFacts, value.ProductionFacts...)
		byKey[value.Key] = current
	}
	keys := make([]string, 0, len(byKey))
	for key := range byKey {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]module.ContextModuleInput, 0, len(keys))
	for _, key := range keys {
		result = append(result, byKey[key])
	}
	return result
}

func assignStableKeys(values []discoveredRoot, repositoryPrefix string) {
	counts := map[string]int{}
	for i := range values {
		base := stableSlug(filepath.Base(values[i].Path), "module")
		values[i].Key = base
		counts[base]++
	}
	for i := range values {
		if counts[values[i].Key] > 1 {
			values[i].Key = stableSlug(strings.ReplaceAll(values[i].Path, "/", "-"), "module")
		}
		if repositoryPrefix != "" {
			values[i].Key = repositoryPrefix + "." + values[i].Key
		}
		values[i].Key = boundedModuleKey(values[i].Key, repositoryPrefix+":"+values[i].Path)
	}
}

func stableRepositoryPrefixes(values []repository.Repository) map[string]string {
	result := make(map[string]string, len(values))
	if len(values) < 2 {
		return result
	}
	counts := map[string]int{}
	for _, value := range values {
		base := stableSlug(value.Name, "repository")
		counts[base]++
	}
	ordinals := map[string]int{}
	for _, value := range values {
		base := stableSlug(value.Name, "repository")
		if counts[base] == 1 {
			result[value.ID] = base
			continue
		}
		ordinals[base]++
		result[value.ID] = fmt.Sprintf("%s-%d", base, ordinals[base])
	}
	return result
}

func moduleKeysOnly(imports []string, roots []discoveredRoot) []string {
	keys := map[string]struct{}{}
	for _, imported := range imports {
		for _, candidate := range roots {
			if imported == candidate.Key {
				keys[imported] = struct{}{}
			}
		}
	}
	result := make([]string, 0, len(keys))
	for key := range keys {
		result = append(result, key)
	}
	sort.Strings(result)
	return result
}

func boundedReadDir(path string) ([]os.DirEntry, error) {
	values, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}
	if len(values) > maxDirectoryEntries {
		values = values[:maxDirectoryEntries]
	}
	return values, nil
}

func readRegularBounded(path string, limit int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("path is not a regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	contents, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(contents)) > limit {
		return nil, errors.New("bounded Project Context input exceeds limit")
	}
	return contents, nil
}

func ignoredProductionName(name string) bool {
	lower := strings.ToLower(name)
	if strings.HasPrefix(lower, ".") {
		return true
	}
	switch lower {
	case "agents.md", "build", "coverage", "dist", "doc", "docs", "examples", "node_modules", "scripts", "target", "testdata", "tools", "vendor":
		return true
	default:
		return false
	}
}

// excludedProjectContextPath preserves the existing bounded Project Context
// discovery heuristic. It is deliberately local to Project Context and is not
// a Feature Area Review authorization rule.
func excludedProjectContextPath(path string) bool {
	normalized := filepath.ToSlash(filepath.Clean(filepath.FromSlash(path)))
	parts := strings.Split(normalized, "/")
	for _, part := range parts {
		switch strings.ToLower(part) {
		case "test", "tests", "__tests__":
			return true
		}
	}
	name := strings.ToLower(parts[len(parts)-1])
	return strings.Contains(name, "_test.") || strings.Contains(name, ".test.") || strings.Contains(name, ".spec.")
}

func isContainerRoot(name string) bool {
	switch strings.ToLower(name) {
	case "app", "cmd", "internal", "lib", "pkg", "src":
		return true
	default:
		return false
	}
}

func productionSourceName(name string) bool {
	if excludedProjectContextPath(name) {
		return false
	}
	switch strings.ToLower(filepath.Ext(name)) {
	case ".c", ".cc", ".cpp", ".cs", ".dart", ".go", ".java", ".js", ".jsx", ".kt", ".kts", ".m", ".mm", ".php", ".py", ".rb", ".rs", ".swift", ".ts", ".tsx":
		return true
	default:
		return rootEntrypointName(name)
	}
}

// productionArtifactName permits bounded root-level production/configuration
// artifacts to form a module boundary even when their format is not a source
// language. Narrative project documents and known test paths stay excluded.
func productionArtifactName(name string) bool {
	if productionSourceName(name) {
		return true
	}
	lower := strings.ToLower(name)
	switch {
	case lower == "readme" || strings.HasPrefix(lower, "readme."),
		lower == "license" || strings.HasPrefix(lower, "license."),
		lower == "changelog" || strings.HasPrefix(lower, "changelog."),
		lower == "contributing" || strings.HasPrefix(lower, "contributing."),
		lower == "code_of_conduct" || strings.HasPrefix(lower, "code_of_conduct."):
		return false
	default:
		return !excludedProjectContextPath(name) && !ignoredProductionName(name)
	}
}

func rootEntrypointName(name string) bool {
	lower := strings.ToLower(name)
	return strings.HasPrefix(lower, "main.") || strings.HasPrefix(lower, "index.") || strings.HasPrefix(lower, "app.") || strings.HasPrefix(lower, "server.")
}

func slug(value string) string {
	var result strings.Builder
	lastSeparator := false
	for _, r := range strings.ToLower(value) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			result.WriteRune(r)
			lastSeparator = false
		} else if !lastSeparator && result.Len() != 0 {
			result.WriteByte('-')
			lastSeparator = true
		}
	}
	return strings.Trim(result.String(), "-")
}

func stableSlug(value, fallback string) string {
	if result := slug(value); result != "" {
		return result
	}
	sum := sha256.Sum256([]byte(value))
	return fallback + "-" + hex.EncodeToString(sum[:4])
}

func boundedModuleKey(value, identity string) string {
	if len(value) <= 128 {
		return value
	}
	sum := sha256.Sum256([]byte(identity))
	prefix := strings.TrimRight(value[:119], ".-_")
	return prefix + "-" + hex.EncodeToString(sum[:4])
}

func humanName(value string) string {
	words := strings.FieldsFunc(value, func(r rune) bool { return r == '-' || r == '_' || r == '.' })
	for i := range words {
		if words[i] != "" {
			runes := []rune(words[i])
			runes[0] = unicode.ToUpper(runes[0])
			words[i] = string(runes)
		}
	}
	if len(words) == 0 {
		return "Project Root"
	}
	return strings.Join(words, " ")
}

func uniqueSorted(values []string) []string {
	sort.Strings(values)
	result := values[:0]
	for _, value := range values {
		if value == "" || len(result) != 0 && result[len(result)-1] == value {
			continue
		}
		result = append(result, value)
	}
	return result
}

func boundedString(value string, maximum int) string {
	if len(value) <= maximum {
		return value
	}
	end := maximum
	for end > 0 && !utf8.ValidString(value[:end]) {
		end--
	}
	return value[:end]
}
