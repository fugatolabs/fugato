package fugato

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/fugatolabs/fugato/packages/fugato/internal/control"
)

const (
	prepareMaxEntries           = 10000
	prepareMaxReportBytes       = 2 << 20
	prepareMaxFileBytes   int64 = 64 << 20
	prepareMaxTotalBytes  int64 = 256 << 20
)

var prepareDefaultNames = []string{"node_modules", ".venv", "venv", "__pycache__", ".next", ".nuxt", ".cache", ".gradle", ".mypy_cache", ".pytest_cache", ".ruff_cache", ".tox", ".nox"}

type prepareItem struct {
	name  string
	info  os.FileInfo
	entry int
}

type preparePlan struct {
	ctx            context.Context
	source, target *os.Root
	cfg            PrepareConfig
	report         *control.PrepareReport
	exclude, allow []*regexp.Regexp
	items          []prepareItem
	seen           map[string]int
	visited        int
	bytes          int64
	blocked        bool
}

// PrepareFiles copies only the target project's declared local files. Planning
// completes before any write; each published file is complete and never replaces
// an existing destination. A failure report is returned alongside its error.
func PrepareFiles(ctx context.Context, targetPath, sourcePath string, dryRun bool) (control.PrepareReport, error) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	report := control.PrepareReport{SchemaVersion: 1, DryRun: dryRun, Entries: []control.PrepareEntry{}}
	for _, name := range prepareDefaultNames {
		report.DefaultExcludes = append(report.DefaultExcludes, "**/"+name)
	}
	fail := func(err error) (control.PrepareReport, error) {
		if contextErr := ctx.Err(); contextErr != nil {
			err = contextErr
		}
		report.OK = false
		report.Error = err.Error()
		if !prepareBoundReport(&report) {
			report.Error += "; report truncated at 2 MiB; narrow prepare.files or exclusions"
		}
		sort.Slice(report.Entries, func(i, j int) bool { return report.Entries[i].Path < report.Entries[j].Path })
		return report, err
	}
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	if strings.TrimSpace(sourcePath) == "" {
		return fail(errors.New("prepare requires an explicit source project directory"))
	}
	target, err := discoverProjectInputs(ctx, targetPath, "", false, true)
	if err != nil {
		// YAML type errors may quote input values. The report never echoes file contents.
		return fail(errors.New("cannot discover target project; check its Git worktree and .fugato.yaml configuration"))
	}
	report.ProjectPath = target.Dir
	source, err := filepath.Abs(sourcePath)
	if err != nil {
		return fail(err)
	}
	source, err = filepath.EvalSymlinks(source)
	if err != nil {
		return fail(errors.New("prepare source must be an existing project directory"))
	}
	report.SourcePath = source
	info, err := os.Stat(source)
	if err != nil || !info.IsDir() {
		return fail(errors.New("prepare source must be an existing project directory"))
	}
	// Git identity, rather than remote URLs, proves this is the same local
	// repository and also permits linked worktrees. Source config is irrelevant.
	common, err := gitOutput(ctx, source, "rev-parse", "--git-common-dir")
	if err != nil {
		return fail(errors.New("prepare source must be within a Git worktree"))
	}
	if !filepath.IsAbs(common) {
		common = filepath.Join(source, common)
	}
	common, err = filepath.EvalSymlinks(common)
	if err != nil || common != target.Repository {
		return fail(errors.New("prepare source and target must belong to the same Git repository"))
	}
	sourceWorktree, err := gitOutput(ctx, source, "rev-parse", "--show-toplevel")
	if err != nil {
		return fail(errors.New("prepare source must be within a Git worktree"))
	}
	sourceWorktree, err = filepath.EvalSymlinks(sourceWorktree)
	if err != nil || !within(sourceWorktree, source) {
		return fail(errors.New("prepare source escaped its Git worktree"))
	}
	if gitMetadataPath(sourceWorktree, source) || gitMetadataPath(target.Worktree, target.Dir) {
		return fail(errors.New("prepare project directory must not be Git metadata"))
	}
	targetInfo, err := os.Lstat(target.Dir)
	if err != nil || !targetInfo.IsDir() {
		return fail(errors.New("prepare target directory identity changed"))
	}
	src, err := os.OpenRoot(source)
	if err != nil {
		return fail(err)
	}
	defer src.Close()
	dst, err := os.OpenRoot(target.Dir)
	if err != nil {
		return fail(err)
	}
	defer dst.Close()
	openedSource, sourceErr := src.Stat(".")
	openedTarget, targetErr := dst.Stat(".")
	if sourceErr != nil || targetErr != nil || !os.SameFile(info, openedSource) || !os.SameFile(targetInfo, openedTarget) {
		return fail(errors.New("prepare project directory identity changed"))
	}
	if err := prepareRootIdentity(src, source); err != nil {
		return fail(err)
	}
	if err := prepareRootIdentity(dst, target.Dir); err != nil {
		return fail(err)
	}
	plan := preparePlan{ctx: ctx, source: src, target: dst, cfg: target.Config.Prepare, report: &report, seen: map[string]int{}}
	for _, pattern := range plan.cfg.Exclude {
		re, e := preparePattern(pattern)
		if e != nil {
			return fail(e)
		}
		plan.exclude = append(plan.exclude, re)
	}
	for _, pattern := range plan.cfg.Allow {
		re, e := preparePattern(pattern)
		if e != nil {
			return fail(e)
		}
		plan.allow = append(plan.allow, re)
	}
	for _, file := range plan.cfg.Files {
		if err := plan.visit(file.Path, file.required(), true); err != nil {
			plan.add(file.Path, "blocked", file.required(), err.Error())
			plan.blocked = true
			if ctx.Err() != nil {
				return fail(ctx.Err())
			}
		}
	}
	if err := prepareRootIdentity(src, source); err != nil {
		return fail(err)
	}
	if err := prepareRootIdentity(dst, target.Dir); err != nil {
		return fail(err)
	}
	if !prepareBoundReport(&report) {
		return fail(errors.New("prepare report exceeds 2 MiB; narrow prepare.files or exclusions"))
	}
	if plan.blocked {
		return fail(errors.New("preparation blocked; no files were copied"))
	}
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	if !dryRun {
		for _, item := range plan.items {
			if err := ctx.Err(); err != nil {
				return fail(err)
			}
			if err := prepareRootIdentity(src, source); err != nil {
				return fail(err)
			}
			if err := prepareRootIdentity(dst, target.Dir); err != nil {
				return fail(err)
			}
			status, copyErr := preparePublish(ctx, src, dst, item)
			entry := &report.Entries[item.entry]
			entry.Status = status
			if copyErr != nil {
				entry.Status = "blocked"
				entry.Reason = copyErr.Error()
				return fail(copyErr)
			}
		}
	}
	sort.Slice(report.Entries, func(i, j int) bool { return report.Entries[i].Path < report.Entries[j].Path })
	report.OK = true
	return report, nil
}

func gitMetadataPath(root, name string) bool {
	rel, err := filepath.Rel(root, name)
	if err != nil {
		return true
	}
	for _, part := range strings.Split(filepath.ToSlash(rel), "/") {
		if strings.EqualFold(part, ".git") {
			return true
		}
	}
	return false
}

func prepareRootIdentity(root *os.Root, name string) error {
	opened, err := root.Stat(".")
	if err != nil {
		return errors.New("prepare directory identity changed")
	}
	current, err := os.Lstat(name)
	if err != nil || current.Mode()&os.ModeSymlink != 0 || !os.SameFile(opened, current) {
		return errors.New("prepare directory identity changed")
	}
	return nil
}

func (p *preparePlan) add(name, status string, required bool, reason string) int {
	if index, ok := p.seen[name]; ok {
		entry := &p.report.Entries[index]
		entry.Required = entry.Required || required
		if status == "blocked" {
			entry.Status, entry.Reason = status, reason
		}
		return index
	}
	index := len(p.report.Entries)
	p.seen[name] = index
	p.report.Entries = append(p.report.Entries, control.PrepareEntry{Path: name, Status: status, Required: required, Reason: reason})
	return index
}

func prepareMatches(patterns []*regexp.Regexp, name string) bool {
	for _, re := range patterns {
		if re.MatchString(name) {
			return true
		}
	}
	return false
}

func (p *preparePlan) exclusion(name string) string {
	for _, part := range strings.Split(name, "/") {
		if strings.EqualFold(part, ".git") {
			return "Git metadata is prohibited"
		}
	}
	if prepareMatches(p.exclude, name) {
		return "excluded by prepare.exclude"
	}
	if prepareMatches(p.allow, name) {
		return ""
	}
	for _, part := range strings.Split(name, "/") {
		for _, excluded := range prepareDefaultNames {
			if part == excluded {
				return "excluded by default dependency/cache filter"
			}
		}
	}
	return ""
}

func (p *preparePlan) visit(name string, required, declared bool) error {
	if err := p.ctx.Err(); err != nil {
		return err
	}
	p.visited++
	if p.visited > prepareMaxEntries {
		return errors.New("prepare exceeds 10000 entries")
	}
	if index, seen := p.seen[name]; seen {
		entry := &p.report.Entries[index]
		entry.Required = entry.Required || required
		if declared && required && (entry.Status == "missing" || entry.Status == "skipped") {
			p.blocked = true
			if entry.Status == "skipped" {
				entry.Status = "blocked"
			}
		}
		return nil
	}
	if reason := p.exclusion(name); reason != "" {
		if declared && required {
			p.add(name, "blocked", true, reason)
			p.blocked = true
			return nil
		}
		p.add(name, "skipped", required, reason)
		// A narrower allow can reach children of a default-excluded directory.
		if reason != "excluded by default dependency/cache filter" || len(p.allow) == 0 {
			return nil
		}
		info, err := prepareLstat(p.source, name)
		if err != nil || !info.IsDir() {
			return nil
		}
		return p.children(name, required)
	}
	targetInfo, targetErr := prepareLstat(p.target, name)
	if targetErr != nil && !errors.Is(targetErr, os.ErrNotExist) {
		return errors.New("unsafe or unreadable target path")
	}
	if targetErr == nil && targetInfo.Mode().IsRegular() {
		sourceInfo, sourceErr := prepareLstat(p.source, name)
		if sourceErr == nil && sourceInfo.IsDir() {
			return errors.New("source directory conflicts with existing target file")
		}
		if sourceErr != nil && !errors.Is(sourceErr, os.ErrNotExist) {
			return errors.New("unsafe or unreadable source path")
		}
		p.add(name, "existing", required, "")
		return nil
	}
	if targetErr == nil && !targetInfo.IsDir() {
		return errors.New("target path must be a regular file or directory")
	}
	info, err := prepareLstat(p.source, name)
	if errors.Is(err, os.ErrNotExist) {
		if targetErr == nil {
			p.add(name, "existing", required, "")
			return nil
		}
		p.add(name, "missing", required, "source path does not exist")
		if required {
			p.blocked = true
		}
		return nil
	}
	if err != nil {
		return errors.New("unsafe or unreadable source path")
	}
	if !info.Mode().IsRegular() && !info.IsDir() {
		return errors.New("source path must be a regular file or directory")
	}
	if targetErr == nil && targetInfo.IsDir() && !info.IsDir() {
		return errors.New("source and target path types conflict")
	}
	if info.Mode().IsRegular() {
		if info.Size() > prepareMaxFileBytes {
			return errors.New("prepare file exceeds 64 MiB")
		}
		p.bytes += info.Size()
		if p.bytes > prepareMaxTotalBytes {
			return errors.New("prepare exceeds 256 MiB total")
		}
	}
	status := "would_copy"
	if targetErr == nil {
		status = "existing"
	}
	index := p.add(name, status, required, "")
	if targetErr != nil {
		p.items = append(p.items, prepareItem{name: name, info: info, entry: index})
	}
	if info.IsDir() {
		return p.children(name, required)
	}
	return nil
}

func (p *preparePlan) children(name string, required bool) error {
	dir, err := prepareOpenDir(p.source, name, false)
	if err != nil {
		return errors.New("source directory identity changed")
	}
	defer dir.Close()
	file, err := dir.Open(".")
	if err != nil {
		return err
	}
	defer file.Close()
	// Read only one more entry than the remaining allowance, including excluded
	// entries, so a huge cache cannot turn a dry-run into unbounded enumeration.
	entries, err := file.ReadDir(prepareMaxEntries - p.visited + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return errors.New("cannot enumerate source directory")
	}
	if len(entries) > prepareMaxEntries-p.visited {
		return errors.New("prepare exceeds 10000 entries")
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, entry := range entries {
		if err := p.visit(path.Join(name, entry.Name()), required, false); err != nil {
			return err
		}
	}
	return nil
}

// Walk one component at a time and pin each opened directory. Comparing the
// no-follow stat to the opened descriptor detects replacement during opening.
func prepareOpenDir(root *os.Root, name string, create bool) (*os.Root, error) {
	current, err := root.OpenRoot(".")
	if err != nil {
		return nil, err
	}
	if name == "." {
		return current, nil
	}
	for _, part := range strings.Split(name, "/") {
		info, e := current.Lstat(part)
		if errors.Is(e, os.ErrNotExist) && create {
			if e = current.Mkdir(part, 0700); e != nil && !errors.Is(e, os.ErrExist) {
				current.Close()
				return nil, e
			}
			info, e = current.Lstat(part)
		}
		if e != nil {
			current.Close()
			return nil, e
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			current.Close()
			return nil, errors.New("path contains a symlink or non-directory")
		}
		next, e := current.OpenRoot(part)
		if e != nil {
			current.Close()
			return nil, e
		}
		opened, e := next.Stat(".")
		after, afterErr := current.Lstat(part)
		current.Close()
		if e != nil || afterErr != nil || !os.SameFile(info, opened) || !os.SameFile(info, after) || after.Mode()&os.ModeSymlink != 0 {
			next.Close()
			return nil, errors.New("directory identity changed")
		}
		current = next
	}
	return current, nil
}

func prepareLstat(root *os.Root, name string) (os.FileInfo, error) {
	parent, err := prepareOpenDir(root, path.Dir(name), false)
	if err != nil {
		return nil, err
	}
	defer parent.Close()
	info, err := parent.Lstat(path.Base(name))
	if err == nil && info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("symlinks are prohibited")
	}
	return info, err
}

func preparePublish(ctx context.Context, source, target *os.Root, item prepareItem) (string, error) {
	if err := ctx.Err(); err != nil {
		return "blocked", err
	}
	if err := prepareRootIdentity(source, source.Name()); err != nil {
		return "blocked", err
	}
	if err := prepareRootIdentity(target, target.Name()); err != nil {
		return "blocked", err
	}
	if item.info.IsDir() {
		dir, err := prepareOpenDir(target, item.name, true)
		if err != nil {
			return "blocked", errors.New("unsafe target directory")
		}
		dir.Close()
		return "copied", nil
	}
	srcParent, err := prepareOpenDir(source, path.Dir(item.name), false)
	if err != nil {
		return "blocked", errors.New("source directory identity changed")
	}
	defer srcParent.Close()
	input, err := srcParent.OpenFile(path.Base(item.name), os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return "blocked", errors.New("source file is no longer available")
	}
	defer input.Close()
	info, err := input.Stat()
	if err != nil || !info.Mode().IsRegular() || !os.SameFile(item.info, info) || info.Size() != item.info.Size() || !info.ModTime().Equal(item.info.ModTime()) {
		return "blocked", errors.New("source file identity changed")
	}
	parent, err := prepareOpenDir(target, path.Dir(item.name), true)
	if err != nil {
		return "blocked", errors.New("unsafe target directory")
	}
	defer parent.Close()
	base := path.Base(item.name)
	if info, err := parent.Lstat(base); err == nil {
		if info.Mode().IsRegular() {
			return "existing", nil
		}
		return "blocked", errors.New("target path is no longer a regular file")
	} else if !errors.Is(err, os.ErrNotExist) {
		return "blocked", err
	}
	temp := ".fugato-prepare-" + rand.Text()
	output, err := parent.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0600|(item.info.Mode().Perm()&0100))
	if err != nil {
		return "blocked", errors.New("cannot create private preparation file")
	}
	defer parent.Remove(temp)
	defer output.Close()
	written, err := io.CopyBuffer(output, &prepareReader{ctx: ctx, reader: io.LimitReader(input, prepareMaxFileBytes+1)}, make([]byte, 32<<10))
	if err != nil {
		return "blocked", err
	}
	if written != item.info.Size() {
		return "blocked", errors.New("source file size changed")
	}
	if err := output.Sync(); err != nil {
		return "blocked", errors.New("cannot sync preparation file")
	}
	if err := ctx.Err(); err != nil {
		return "blocked", err
	}
	after, err := input.Stat()
	current, statErr := prepareLstat(source, item.name)
	if err != nil || statErr != nil || !os.SameFile(info, current) || !os.SameFile(info, after) || after.Size() != info.Size() || !after.ModTime().Equal(info.ModTime()) {
		return "blocked", errors.New("source file identity changed during copy")
	}
	if err := prepareRootIdentity(source, source.Name()); err != nil {
		return "blocked", err
	}
	if err := prepareRootIdentity(target, target.Name()); err != nil {
		return "blocked", err
	}
	// Ensure the pinned parent is still the named destination before publishing.
	currentParent, err := prepareOpenDir(target, path.Dir(item.name), false)
	if err != nil {
		return "blocked", errors.New("target directory identity changed")
	}
	currentInfo, e1 := currentParent.Stat(".")
	pinnedInfo, e2 := parent.Stat(".")
	currentParent.Close()
	if e1 != nil || e2 != nil || !os.SameFile(currentInfo, pinnedInfo) {
		return "blocked", errors.New("target directory identity changed")
	}
	// Link is atomic and fails if the destination exists; unlike Rename it can
	// never clobber a file that appeared after preflight.
	if err := parent.Link(temp, base); err != nil {
		if errors.Is(err, os.ErrExist) {
			if info, e := parent.Lstat(base); e == nil && info.Mode().IsRegular() {
				return "existing", nil
			}
		}
		return "blocked", errors.New("cannot publish preparation file without replacing target")
	}
	return "copied", nil
}

type prepareReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *prepareReader) Read(buffer []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(buffer)
}

// Preparation must run before runtime inputs exist. Resolve the nearest existing
// prefix to retain the worktree boundary without requiring missing leaf paths.
func validatePreparationRuntimePath(root, name string, directory bool) error {
	if !within(root, name) || gitMetadataPath(root, name) {
		return errors.New("runtime input must remain within the Git worktree")
	}
	existing := name
	for {
		_, err := os.Lstat(existing)
		if err == nil {
			resolved, err := filepath.EvalSymlinks(existing)
			if err != nil || !within(root, resolved) {
				return errors.New("runtime input must remain within the Git worktree")
			}
			info, err := os.Stat(resolved)
			if err != nil {
				return errors.New("runtime input is inaccessible")
			}
			if existing != name || directory {
				if !info.IsDir() {
					return errors.New("runtime input parent or host.cwd must be a directory")
				}
			} else if !info.Mode().IsRegular() {
				return errors.New("Compose input must be a regular file")
			}
			return nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return errors.New("runtime input is inaccessible")
		}
		parent := filepath.Dir(existing)
		if parent == existing || !within(root, parent) {
			return errors.New("runtime input must remain within the Git worktree")
		}
		existing = parent
	}
}

// Leave headroom for final error/status fields. Count actual JSON bytes, including
// escaped path characters, without allocating the entire potentially huge report.
func prepareBoundReport(report *control.PrepareReport) bool {
	header := *report
	header.Entries = nil
	encoded, err := json.Marshal(header)
	if err != nil {
		return false
	} // All report fields have concrete JSON types.
	size := len(encoded) - 2 // Replace entries:null with entries:[].
	for i, entry := range report.Entries {
		encoded, err := json.Marshal(entry)
		if err != nil {
			return false
		}
		extra := len(encoded)
		if i > 0 {
			extra++
		}
		if size+extra > prepareMaxReportBytes-4096 {
			report.Entries = report.Entries[:i]
			report.Truncated = true
			return false
		}
		size += extra
	}
	return true
}
