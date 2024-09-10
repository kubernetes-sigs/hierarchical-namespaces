package loader

import (
	"errors"
	"fmt"
	"go/ast"
	"go/build"
	"go/parser"
	"go/scanner"
	"go/token"
	"go/types"
	"go/version"
	"os"
	"runtime"
	"strings"
	"time"

	"honnef.co/go/tools/config"
	"honnef.co/go/tools/lintcmd/cache"

	"golang.org/x/tools/go/gcexportdata"
	"golang.org/x/tools/go/packages"
)

const MaxFileSize = 50 * 1024 * 1024 // 50 MB

var errMaxFileSize = errors.New("file exceeds max file size")

type PackageSpec struct {
	ID      string
	Name    string
	PkgPath string
	// Errors that occurred while building the import graph. These will
	// primarily be parse errors or failure to resolve imports, but
	// may also be other errors.
	Errors          []packages.Error
	GoFiles         []string
	CompiledGoFiles []string
	OtherFiles      []string
	ExportFile      string
	Imports         map[string]*PackageSpec
	TypesSizes      types.Sizes
	Hash            cache.ActionID
	Module          *packages.Module

	Config config.Config
}

func (spec *PackageSpec) String() string {
	return spec.ID
}

type Package struct {
	*PackageSpec

	// Errors that occurred while loading the package. These will
	// primarily be parse or type errors, but may also be lower-level
	// failures such as file-system ones.
	Errors    []packages.Error
	Types     *types.Package
	Fset      *token.FileSet
	Syntax    []*ast.File
	TypesInfo *types.Info
}

// Graph resolves patterns and returns packages with all the
// information required to later load type information, and optionally
// syntax trees.
//
// The provided config can set any setting with the exception of Mode.
func Graph(c *cache.Cache, cfg *packages.Config, patterns ...string) ([]*PackageSpec, error) {
	var dcfg packages.Config
	if cfg != nil {
		dcfg = *cfg
	}
	dcfg.Mode = packages.NeedName |
		packages.NeedImports |
		packages.NeedDeps |
		packages.NeedExportFile |
		packages.NeedFiles |
		packages.NeedCompiledGoFiles |
		packages.NeedTypesSizes |
		packages.NeedModule
	pkgs, err := packages.Load(&dcfg, patterns...)
	if err != nil {
		return nil, err
	}

	m := map[*packages.Package]*PackageSpec{}
	packages.Visit(pkgs, nil, func(pkg *packages.Package) {
		spec := &PackageSpec{
			ID:              pkg.ID,
			Name:            pkg.Name,
			PkgPath:         pkg.PkgPath,
			Errors:          pkg.Errors,
			GoFiles:         pkg.GoFiles,
			CompiledGoFiles: pkg.CompiledGoFiles,
			OtherFiles:      pkg.OtherFiles,
			ExportFile:      pkg.ExportFile,
			Imports:         map[string]*PackageSpec{},
			TypesSizes:      pkg.TypesSizes,
			Module:          pkg.Module,
		}
		for path, imp := range pkg.Imports {
			spec.Imports[path] = m[imp]
		}
		if cdir := config.Dir(pkg.GoFiles); cdir != "" {
			cfg, err := config.Load(cdir)
			if err != nil {
				spec.Errors = append(spec.Errors, convertError(err)...)
			}
			spec.Config = cfg
		} else {
			spec.Config = config.DefaultConfig
		}
		spec.Hash, err = computeHash(c, spec)
		if err != nil {
			spec.Errors = append(spec.Errors, convertError(err)...)
		}
		m[pkg] = spec
	})
	out := make([]*PackageSpec, 0, len(pkgs))
	for _, pkg := range pkgs {
		if len(pkg.CompiledGoFiles) == 0 && len(pkg.Errors) == 0 && pkg.PkgPath != "unsafe" {
			// If a package consists only of test files, then
			// go/packages incorrectly(?) returns an empty package for
			// the non-test variant. Get rid of those packages. See
			// #646.
			//
			// Do not, however, skip packages that have errors. Those,
			// too, may have no files, but we want to print the
			// errors.
			continue
		}
		out = append(out, m[pkg])
	}

	return out, nil
}

type program struct {
	fset     *token.FileSet
	packages map[string]*types.Package
	options  *Options
}

type Stats struct {
	Source time.Duration
	Export map[*PackageSpec]time.Duration
}

type Options struct {
	// The Go language version to use for the type checker. If unset, or if set
	// to "module", it will default to the Go version specified in the module;
	// if there is no module, it will default to the version of Go the
	// executable was built with.
	GoVersion string
}

// Load loads the package described in spec. Imports will be loaded
// from export data, while the package itself will be loaded from
// source.
//
// An error will only be returned for system failures, such as failure
// to read export data from disk. Syntax and type errors, among
// others, will only populate the returned package's Errors field.
func Load(spec *PackageSpec, opts *Options) (*Package, Stats, error) {
	if opts == nil {
		opts = &Options{}
	}
	if opts.GoVersion == "" {
		opts.GoVersion = "module"
	}
	prog := &program{
		fset:     token.NewFileSet(),
		packages: map[string]*types.Package{},
		options:  opts,
	}

	stats := Stats{
		Export: map[*PackageSpec]time.Duration{},
	}
	for _, imp := range spec.Imports {
		if imp.PkgPath == "unsafe" {
			continue
		}
		t := time.Now()
		_, err := prog.loadFromExport(imp)
		stats.Export[imp] = time.Since(t)
		if err != nil {
			return nil, stats, err
		}
	}
	t := time.Now()
	pkg, err := prog.loadFromSource(spec)
	if err == errMaxFileSize {
		pkg, err = prog.loadFromExport(spec)
	}
	stats.Source = time.Since(t)
	return pkg, stats, err
}

// loadFromExport loads a package from export data.
func (prog *program) loadFromExport(spec *PackageSpec) (*Package, error) {
	// log.Printf("Loading package %s from export", spec)
	if spec.ExportFile == "" {
		return nil, fmt.Errorf("no export data for %q", spec.ID)
	}
	f, err := os.Open(spec.ExportFile)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	r, err := gcexportdata.NewReader(f)
	if err != nil {
		return nil, err
	}
	tpkg, err := gcexportdata.Read(r, prog.fset, prog.packages, spec.PkgPath)
	if err != nil {
		return nil, err
	}
	pkg := &Package{
		PackageSpec: spec,
		Types:       tpkg,
		Fset:        prog.fset,
	}
	// runtime.SetFinalizer(pkg, func(pkg *Package) {
	// 	log.Println("Unloading package", pkg.PkgPath)
	// })
	return pkg, nil
}

// loadFromSource loads a package from source. All of its dependencies
// must have been loaded already.
func (prog *program) loadFromSource(spec *PackageSpec) (*Package, error) {
	if len(spec.Errors) > 0 {
		panic("LoadFromSource called on package with errors")
	}

	pkg := &Package{
		PackageSpec: spec,
		Types:       types.NewPackage(spec.PkgPath, spec.Name),
		Syntax:      make([]*ast.File, len(spec.CompiledGoFiles)),
		Fset:        prog.fset,
		TypesInfo: &types.Info{
			Types:        make(map[ast.Expr]types.TypeAndValue),
			Defs:         make(map[*ast.Ident]types.Object),
			Uses:         make(map[*ast.Ident]types.Object),
			Implicits:    make(map[ast.Node]types.Object),
			Scopes:       make(map[ast.Node]*types.Scope),
			Selections:   make(map[*ast.SelectorExpr]*types.Selection),
			Instances:    make(map[*ast.Ident]types.Instance),
			FileVersions: make(map[*ast.File]string),
		},
	}
	// runtime.SetFinalizer(pkg, func(pkg *Package) {
	// 	log.Println("Unloading package", pkg.PkgPath)
	// })

	// OPT(dh): many packages have few files, much fewer than there
	// are CPU cores. Additionally, parsing each individual file is
	// very fast. A naive parallel implementation of this loop won't
	// be faster, and tends to be slower due to extra scheduling,
	// bookkeeping and potentially false sharing of cache lines.
	for i, file := range spec.CompiledGoFiles {
		f, err := os.Open(file)
		if err != nil {
			return nil, err
		}
		fi, err := f.Stat()
		if err != nil {
			return nil, err
		}
		if fi.Size() >= MaxFileSize {
			return nil, errMaxFileSize
		}
		af, err := parser.ParseFile(prog.fset, file, f, parser.ParseComments|parser.SkipObjectResolution)
		f.Close()
		if err != nil {
			pkg.Errors = append(pkg.Errors, convertError(err)...)
			return pkg, nil
		}
		pkg.Syntax[i] = af
	}
	importer := func(path string) (*types.Package, error) {
		if path == "unsafe" {
			return types.Unsafe, nil
		}
		if path == "C" {
			// go/packages doesn't tell us that cgo preprocessing
			// failed. When we subsequently try to parse the package,
			// we'll encounter the raw C import.
			return nil, errors.New("cgo preprocessing failed")
		}
		ispecpkg := spec.Imports[path]
		if ispecpkg == nil {
			return nil, fmt.Errorf("trying to import %q in the context of %q returned nil PackageSpec", path, spec)
		}
		ipkg := prog.packages[ispecpkg.PkgPath]
		if ipkg == nil {
			return nil, fmt.Errorf("trying to import %q (%q) in the context of %q returned nil PackageSpec", ispecpkg.PkgPath, path, spec)
		}
		return ipkg, nil
	}
	tc := &types.Config{
		Importer: importerFunc(importer),
		Error: func(err error) {
			pkg.Errors = append(pkg.Errors, convertError(err)...)
		},
	}
	if prog.options.GoVersion == "module" {
		if spec.Module != nil && spec.Module.GoVersion != "" {
			var our string
			rversion := runtime.Version()
			if fields := strings.Fields(rversion); len(fields) > 0 {
				// When using GOEXPERIMENT, the version returned by
				// runtime.Version might look something like "go1.23.0
				// X:boringcrypto", which wouldn't be accepted by
				// version.IsValid even though it's a proper release.
				//
				// When using a development build, the version looks like "devel
				// go1.24-206df8e7ad Tue Aug 13 16:44:16 2024 +0000", and taking
				// the first field of that won't change whether it's accepted as
				// valid or not.
				rversion = fields[0]
			}
			if version.IsValid(rversion) {
				// Staticcheck was built with a released version of Go.
				// runtime.Version() returns something like "go1.22.4" or
				// "go1.23rc1".
				our = rversion
			} else {
				// Staticcheck was built with a development version of Go.
				// runtime.Version() returns something like "devel go1.23-e8ee1dc4f9
				// Sun Jun 23 00:52:20 2024 +0000". Fall back to using ReleaseTags,
				// where the last one will contain the language version of the
				// development version of Go.
				tags := build.Default.ReleaseTags
				our = tags[len(tags)-1]
			}
			if version.Compare("go"+spec.Module.GoVersion, our) == 1 {
				// We don't need this check for correctness, as go/types rejects
				// a GoVersion that's too new. But we can produce a better error
				// message. In Go 1.22, go/types simply says "package requires
				// newer Go version go1.23", without any information about the
				// file, or what version Staticcheck was built with. Starting
				// with Go 1.23, the error seems to be better:
				// "/home/dominikh/prj/src/example.com/foo.go:3:1: package
				// requires newer Go version go1.24 (application built with
				// go1.23)" and we may be able to remove this custom logic once
				// we depend on Go 1.23.
				//
				// Note that if Staticcheck was built with a development version of
				// Go, e.g. "devel go1.23-82c371a307", then we'll say that
				// Staticcheck was built with go1.23, which is the language version
				// of the development build. This matches the behavior of the Go
				// toolchain, which says "go.mod requires go >= 1.23rc1 (running go
				// 1.23; GOTOOLCHAIN=local)".
				//
				// Note that this prevents Go master from working with go1.23rc1,
				// even if master is further ahead. This is currently unavoidable,
				// and matches the behavior of the Go toolchain (see above.)
				return nil, fmt.Errorf(
					"module requires at least go%s, but Staticcheck was built with %s",
					spec.Module.GoVersion, our,
				)
			}
			tc.GoVersion = "go" + spec.Module.GoVersion
		} else {
			tags := build.Default.ReleaseTags
			tc.GoVersion = tags[len(tags)-1]
		}
	} else {
		tc.GoVersion = prog.options.GoVersion
	}
	// Note that the type-checker can return a non-nil error even though the Go
	// compiler has already successfully built this package (which is an
	// invariant of getting to this point), for example because of the Go
	// version passed to the type checker.
	err := types.NewChecker(tc, pkg.Fset, pkg.Types, pkg.TypesInfo).Files(pkg.Syntax)
	return pkg, err
}

func convertError(err error) []packages.Error {
	var errs []packages.Error
	// taken from go/packages
	switch err := err.(type) {
	case packages.Error:
		// from driver
		errs = append(errs, err)

	case *os.PathError:
		// from parser
		errs = append(errs, packages.Error{
			Pos:  err.Path + ":1",
			Msg:  err.Err.Error(),
			Kind: packages.ParseError,
		})

	case scanner.ErrorList:
		// from parser
		for _, err := range err {
			errs = append(errs, packages.Error{
				Pos:  err.Pos.String(),
				Msg:  err.Msg,
				Kind: packages.ParseError,
			})
		}

	case types.Error:
		// from type checker
		errs = append(errs, packages.Error{
			Pos:  err.Fset.Position(err.Pos).String(),
			Msg:  err.Msg,
			Kind: packages.TypeError,
		})

	case config.ParseError:
		errs = append(errs, packages.Error{
			Pos:  fmt.Sprintf("%s:%d:%d", err.Filename, err.Position.Line, err.Position.Col),
			Msg:  fmt.Sprintf("%s (last key parsed: %q)", err.Message, err.LastKey),
			Kind: packages.ParseError,
		})
	default:
		errs = append(errs, packages.Error{
			Pos:  "-",
			Msg:  err.Error(),
			Kind: packages.UnknownError,
		})
	}
	return errs
}

type importerFunc func(path string) (*types.Package, error)

func (f importerFunc) Import(path string) (*types.Package, error) { return f(path) }
