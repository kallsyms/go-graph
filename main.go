package main

import (
	"flag"
	"go/ast"
	"go/token"
	"go/types"
	"os"
	"reflect"
	"runtime/pprof"
	"sync"

	"github.com/kallsyms/go-graph/coordination"
	"github.com/kallsyms/go-graph/schema"
	"github.com/schollz/progressbar/v3"
	"github.com/sirupsen/logrus"
	"golang.org/x/tools/go/ast/astutil"
	"golang.org/x/tools/go/callgraph"
	"golang.org/x/tools/go/callgraph/cha"
	"golang.org/x/tools/go/cfg"
	"golang.org/x/tools/go/packages"
	"golang.org/x/tools/go/ssa"

	gbackend "github.com/kallsyms/go-graph/backend"
)

func createGraphVars(pkg *packages.Package) map[*types.Var]*schema.Variable {
	graphVarMap := map[*types.Var]*schema.Variable{}
	for ident, typ := range pkg.TypesInfo.Defs {
		switch typ := typ.(type) {
		// TODO: tuple?
		// TODO: const
		case *types.Var:
			// TODO: check typ.IsField since struct members are Vars
			gVar := &schema.Variable{
				Name: ident.Name,
				Type: typ.Type().String(),
			}
			graphVarMap[typ] = gVar
		}
	}

	return graphVarMap
}

type CFGBlockSet map[*cfg.Block]interface{}

func CFGBlockSetFrom(l []*cfg.Block) CFGBlockSet {
	s := CFGBlockSet{}
	for _, blk := range l {
		s.Add(blk)
	}
	return s
}

func (set CFGBlockSet) Add(block *cfg.Block) {
	set[block] = struct{}{}
}

func (set CFGBlockSet) Pop() *cfg.Block {
	for k := range set {
		delete(set, k)
		return k
	}

	return nil
}

func (set CFGBlockSet) Contains(block *cfg.Block) bool {
	_, found := set[block]
	return found
}

func (set CFGBlockSet) Union(s2 CFGBlockSet) {
	for k := range s2 {
		set[k] = struct{}{}
	}
}

func (set CFGBlockSet) Intersect(s2 CFGBlockSet) {
	for k := range set {
		if _, found := s2[k]; !found {
			delete(set, k)
		}
	}
}

func (set CFGBlockSet) Copy() CFGBlockSet {
	n := CFGBlockSet{}
	for b := range set {
		n.Add(b)
	}
	return n
}

func (set CFGBlockSet) Equals(s2 CFGBlockSet) bool {
	if len(set) != len(s2) {
		return false
	}
	for k := range set {
		if !s2.Contains(k) {
			return false
		}
	}
	for k := range s2 {
		if !set.Contains(k) {
			return false
		}
	}
	return true
}

// find and remove "empty" blocks in funcCFG.
// some blocks in the cfg have no statements (acting just as a fallthrough)
// but these are not useful at all for us.
func pruneEmpty(funcCFG *cfg.CFG, graphFirstStmtMap map[*cfg.Block]*schema.Statement) {
	nonEmptyBBs := []*cfg.Block{}
	successors := map[*cfg.Block]CFGBlockSet{}

	// it's probably safe to do this all in one pass but idk
	for _, bb := range funcCFG.Blocks {
		if _, ok := graphFirstStmtMap[bb]; ok {
			nonEmptyBBs = append(nonEmptyBBs, bb)
		}

		//fmt.Printf("%v %v\n", bb, bb.Succs)
		// Keep track so we don't infinitely loop if an empty bb succeeds itself directly or indirectly
		seen := CFGBlockSet{}
		nonEmpty := CFGBlockSet{}

		queue := bb.Succs
		for len(queue) > 0 {
			succ := queue[0]
			queue = queue[1:]

			if _, ok := graphFirstStmtMap[succ]; ok {
				nonEmpty.Add(succ)
			} else {
				if seen.Contains(succ) {
					continue
				}
				queue = append(queue, succ.Succs...)
			}

			seen.Add(succ)
		}

		successors[bb] = nonEmpty
	}

	funcCFG.Blocks = nonEmptyBBs

	for _, bb := range funcCFG.Blocks {
		succs := []*cfg.Block{}
		for succ := range successors[bb] {
			succs = append(succs, succ)
		}
		bb.Succs = succs
		//fmt.Printf("%v %v\n", bb, bb.Succs)
	}
}

// for each block A, compute the set of blocks which when traversed to from A, means a back edge was taken
func backEdges(funcCFG *cfg.CFG) map[*cfg.Block]CFGBlockSet {
	if len(funcCFG.Blocks) == 0 {
		return nil
	}

	// https://pages.cs.wisc.edu/~fischer/cs701.f14/finding.loops.html
	predecessors := map[*cfg.Block]CFGBlockSet{}

	for _, block := range funcCFG.Blocks {
		for _, succ := range block.Succs {
			if predecessors[succ] == nil {
				predecessors[succ] = CFGBlockSet{}
			}
			predecessors[succ].Add(block)
		}
	}

	dominators := map[*cfg.Block]CFGBlockSet{}
	queue := CFGBlockSet{}

	dominators[funcCFG.Blocks[0]] = CFGBlockSetFrom([]*cfg.Block{funcCFG.Blocks[0]})

	for _, block := range funcCFG.Blocks[1:] {
		dominators[block] = CFGBlockSetFrom(funcCFG.Blocks)
		queue.Add(block)
	}

	for len(queue) > 0 {
		block := queue.Pop()

		first := false
		newDoms := CFGBlockSet{}
		for pred := range predecessors[block] {
			if !first {
				newDoms = dominators[pred].Copy()
				first = true
				continue
			}
			newDoms.Intersect(dominators[pred])
		}
		newDoms.Add(block)

		if !newDoms.Equals(dominators[block]) {
			dominators[block] = newDoms

			for _, succ := range block.Succs {
				queue.Add(succ)
			}
		}
	}

	cfgBackEdges := map[*cfg.Block]CFGBlockSet{}
	for _, block := range funcCFG.Blocks {
		cfgBackEdges[block] = CFGBlockSet{}
		for _, succ := range block.Succs {
			if dominators[block].Contains(succ) {
				cfgBackEdges[block].Add(succ)
			}
		}
	}

	return cfgBackEdges
}

func resolveIdents(node ast.Node, pkg *packages.Package, graphVarMap map[*types.Var]*schema.Variable) []*schema.Variable {
	var vars []*schema.Variable

	astutil.Apply(node, func(stmtCur *astutil.Cursor) bool {
		if ident, ok := stmtCur.Node().(*ast.Ident); ok {
			if varType, ok := pkg.TypesInfo.Defs[ident].(*types.Var); ok {
				// This can happen in the case of `var X struct {...}` (e.g. https://sourcegraph.com/github.com/golang/go/-/blob/src/internal/cpu/cpu.go?L26:5#tab=references)
				if graphVarMap[varType] != nil {
					vars = append(vars, graphVarMap[varType])
				}
			} else if varType, ok := pkg.TypesInfo.Uses[ident].(*types.Var); ok {
				if graphVarMap[varType] != nil {
					vars = append(vars, graphVarMap[varType])
				}
			}
		}

		return true
	}, nil)

	return vars
}

// simple encapsulating struct used to match ssa instructions to ast statements by location in source
type stmtWithLoc struct {
	start token.Pos
	end   token.Pos
	gStmt *schema.Statement
}

func processPackage(pkgDir string, backend gbackend.Backend) {
	// Minimal load to check if this is already done and get imports
	config := &packages.Config{
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedModule | packages.NeedImports |
			packages.NeedDeps | packages.NeedTypes | packages.NeedSyntax | packages.NeedTypesInfo,
		Logf: func(format string, args ...interface{}) {
			logrus.Tracef(format, args...)
		},
		Dir: pkgDir,
	}

	pkgs, err := packages.Load(config, "./...")
	if err != nil {
		logrus.Errorf("Error loading %q: %v", pkgDir, err)
		return
	}

	logrus.Infof("Processing %q", pkgDir)

	fCache := make(fileCache)

	vertices := make(chan schema.Vertex, 32768)
	verticesCount := 0
	var vertexWG *sync.WaitGroup

	if noProgressBar {
		vertexWG = backend.AddVStream(vertices, func(done []schema.Vertex) {
			verticesCount += len(done)
		})
	} else {
		progress := progressbar.Default(-1)
		vertexWG = backend.AddVStream(vertices, func(done []schema.Vertex) {
			verticesCount += len(done)
			progress.Add(len(done))
		})
	}

	fallbackVersion := coordination.MakeFallbackVersion(pkgDir)

	edges := []schema.Edge{}

	// maps that will be built up during package walking, and used in callgraph processing
	pkgIsNew := map[string]bool{}
	pkgGraphStatements := map[string][]stmtWithLoc{}
	graphFuncMap := map[*ast.FuncDecl]*schema.Function{}

	// GlobalDebug allows us to go from ssa function to ast funcdecl
	ssaProg := ssa.NewProgram(token.NewFileSet(), ssa.GlobalDebug)

	// Visit over the import tree like ssautil.Packages does
	// Do it ourselves though, so we can get a handle to the packages.Package and the more detailed module information
	// that comes with it
	packages.Visit(pkgs, nil, func(pkg *packages.Package) {
		if pkg.Types == nil || pkg.IllTyped {
			return
		}

		tup := coordination.NewPackageTuple(pkg, fallbackVersion)

		graphPkg, found := backend.GetPackage(tup)
		if !found {
			graphPkg, err = backend.CreatePackage(tup)
			if err != nil {
				logrus.Fatalf("Error creating package %v: %v", tup, err)
			}
		}
		pkgIsNew[pkg.PkgPath] = !found

		// Create the package in the SSA program.
		// We'll use the results of this later to determine who calls what.
		// This has to be done regardless of whether the package is new so that the call graph building works
		ssaProg.CreatePackage(pkg.Types, pkg.Syntax, pkg.TypesInfo, true)

		if found {
			// populate graphFuncMap with the functions already in DB
			alreadyPresentFuncs, err := backend.PackageFunctions(graphPkg)
			if err != nil {
				logrus.Errorf("Error retrieving functions for package %v", tup)
				return
			}

			for _, root := range pkg.Syntax {
				astutil.Apply(root, func(cur *astutil.Cursor) bool {
					funcDecl, ok := cur.Node().(*ast.FuncDecl)
					if !ok {
						return true
					}

					if funcDecl.Body == nil {
						return false
					}

					gf, ok := alreadyPresentFuncs[funcDecl.Name.String()]
					if !ok {
						logrus.Warnf("AST function %q (in %q) not found in DB despite existing package?", funcDecl.Name.String(), pkg.PkgPath)
						return false
					}

					gf.Package = graphPkg
					graphFuncMap[funcDecl] = gf

					return false
				}, nil)
			}

			// done with this pkg now
			return
		}

		logrus.Debugf("Processing new package %q", pkg.PkgPath)

		graphVarMap := createGraphVars(pkg)
		for _, gVar := range graphVarMap {
			vertices <- gVar
		}

		// Extract all function declarations from the package
		for _, root := range pkg.Syntax {
			astutil.Apply(root, func(cur *astutil.Cursor) bool {
				funcDecl, ok := cur.Node().(*ast.FuncDecl)
				if !ok {
					return true
				}

				if funcDecl.Body == nil {
					return false
				}

				logrus.Tracef("Processing function %q", funcDecl.Name.String())

				gFunc := &schema.Function{
					Name:    funcDecl.Name.String(),
					Package: graphPkg,
				}
				graphFuncMap[funcDecl] = gFunc
				vertices <- gFunc
				edges = append(edges, schema.Edge{
					Source: gFunc.Package,
					Label:  "Functions",
					Target: gFunc,
				})

				// And create a CFG for them
				// TODO: Either copypasta or somehow call the ctrlflow pass to actually determine callMayReturn
				// https://cs.opensource.google/go/x/tools/+/refs/tags/v0.1.8:go/analysis/passes/ctrlflow/ctrlflow.go;l=185;bpv=0;bpt=1
				funcCFG := cfg.New(funcDecl.Body, func(_ *ast.CallExpr) bool { return true })

				logrus.Trace("Created CFG")

				// Create all statements, keeping track of the first and last in each BB
				var funcFirstGStatement *schema.Statement
				graphFirstStmtMap := map[*cfg.Block]*schema.Statement{}
				graphLastStmtMap := map[*cfg.Block]*schema.Statement{}
				for _, bb := range funcCFG.Blocks {
					var prevGStmt *schema.Statement

					for _, node := range bb.Nodes {
						// statement isn't really an apt name, since at this point things like if conditional exprs
						// have already been broken out
						file, offset, text := fCache.readNodeSource(pkg.Fset, node /*limit=*/, 1024)
						gStmt := &schema.Statement{
							File:    file,
							Offset:  offset,
							Text:    text,
							ASTType: reflect.TypeOf(node).Elem().Name(),
						}
						vertices <- gStmt
						edges = append(edges, schema.Edge{
							Source: gFunc,
							Label:  "Statement",
							Target: gStmt,
						})
						pkgGraphStatements[pkg.PkgPath] = append(pkgGraphStatements[pkg.PkgPath], stmtWithLoc{node.Pos(), node.End(), gStmt})

						if prevGStmt != nil {
							edges = append(edges, schema.Edge{
								Source: prevGStmt,
								Label:  "Next",
								Target: gStmt,
								Properties: map[string]interface{}{
									"isBackEdge": false,
								},
							})
						}
						prevGStmt = gStmt

						// is this the first statement in the entire function?
						if funcFirstGStatement == nil {
							funcFirstGStatement = gStmt
							edges = append(edges, schema.Edge{
								Source: gFunc,
								Label:  "FirstStatement",
								Target: gStmt,
							})
						}

						// Find all variables that anything under this stmt references (or defines)
						refGVars := resolveIdents(node, pkg, graphVarMap)

						// And all vars that this stmt may assign
						var assignGVars []*schema.Variable
						astutil.Apply(node, func(stmtCur *astutil.Cursor) bool {
							switch node := stmtCur.Node().(type) {
							case *ast.AssignStmt:
								// a := 1 or a = 1
								for _, child := range node.Lhs {
									assignGVars = append(assignGVars, resolveIdents(child, pkg, graphVarMap)...)
								}
							case *ast.ValueSpec:
								// var a int = 1
								for _, child := range node.Names {
									switch def := pkg.TypesInfo.Defs[child].(type) {
									// TODO *types.Const
									case *types.Var:
										assignGVars = append(assignGVars, graphVarMap[def])
									}
								}
							}
							return true
						}, nil)

						for _, refGvar := range refGVars {
							edges = append(edges, schema.Edge{
								Source: gStmt,
								Label:  "References",
								Target: refGvar,
							})
						}

						for _, assignGvar := range assignGVars {
							edges = append(edges, schema.Edge{
								Source: gStmt,
								Label:  "Assigns",
								Target: assignGvar,
							})
						}

						if graphFirstStmtMap[bb] == nil {
							graphFirstStmtMap[bb] = gStmt
						}
						graphLastStmtMap[bb] = gStmt
					}
				}
				logrus.Trace("Created first/last statement maps")

				pruneEmpty(funcCFG, graphFirstStmtMap)

				cfgBackEdges := backEdges(funcCFG)
				logrus.Trace("Created back-edge map")

				// Link the edges between the last instruction in each BB and all possible successor BB's first statements
				for _, bb := range funcCFG.Blocks {
					for _, succ := range bb.Succs {
						isBackEdge := cfgBackEdges[bb].Contains(succ)

						edges = append(edges, schema.Edge{
							Source: graphLastStmtMap[bb],
							Label:  "Next",
							Target: graphFirstStmtMap[succ],
							Properties: map[string]interface{}{
								"isBackEdge": isBackEdge,
							},
						})
					}
				}

				return false
			}, nil)
		}
	})

	ssaProg.Build()

	// use RTA instead? would require the "global graph" to be almost flow-sensitive, but would remove unreachable
	// interface calls
	logrus.Trace("Computing callgraph")
	cg := cha.CallGraph(ssaProg)
	// this seems to stall on some packages? /repos/mlabouardy/komiser seems to be an example
	//cg.DeleteSyntheticNodes()
	logrus.Trace("Created callgraph")

	// create the calls edges (and FunctionCall intermediate vertices)
	callgraph.GraphVisitEdges(cg, func(edge *callgraph.Edge) error {
		callerNode, ok := edge.Caller.Func.Syntax().(*ast.FuncDecl)
		if !ok {
			return nil
		}

		// only insert if the call is in a new (not in DB) package
		pkgName := edge.Site.Parent().Pkg.Pkg.Path()
		if !pkgIsNew[pkgName] {
			return nil
		}

		calleeNode, ok := edge.Callee.Func.Syntax().(*ast.FuncDecl)
		if !ok {
			return nil
		}

		if caller, found := graphFuncMap[callerNode]; found {
			if callee, found := graphFuncMap[calleeNode]; found {
				fc := &schema.FunctionCall{
					Caller: caller,
					Callee: callee,
				}
				vertices <- fc
				edges = append(
					edges,
					schema.Edge{Source: fc.Caller, Label: "Calls", Target: fc},
					schema.Edge{Source: fc, Label: "Callee", Target: fc.Callee},
				)

				pkgName := edge.Site.Parent().Pkg.Pkg.Path()
				for _, stmt := range pkgGraphStatements[pkgName] {
					if stmt.start <= edge.Site.Pos() && stmt.end > edge.Site.Pos() {
						edges = append(
							edges,
							schema.Edge{Source: fc, Label: "CallSiteStatement", Target: stmt.gStmt},
						)

						break
					}
				}
			}
		}

		return nil
	})
	logrus.Trace("Callgraph nodes created")

	close(vertices)
	vertexWG.Wait()

	logrus.Infof("Added %d vertices", verticesCount)

	if noProgressBar {
		backend.AddEBulk(edges, func(doneEdges []schema.Edge) {})
	} else {
		progress := progressbar.Default(int64(len(edges)))
		backend.AddEBulk(edges, func(doneEdges []schema.Edge) {
			progress.Add(len(doneEdges))
		})
	}
}

var noProgressBar bool

func main() {
	verbose := flag.Bool("verbose", false, "Verbose?")
	trace := flag.Bool("trace", false, "Very verbose?")
	noBar := flag.Bool("no-bar", false, "No progress bar")

	profile := flag.Bool("profile", false, "Save cpu and mem profiles")

	conn := flag.String("db", "ws://localhost:8182", "DB connection string")

	flag.Parse()

	if *verbose {
		logrus.SetLevel(logrus.DebugLevel)
		noProgressBar = true
	}
	if *trace {
		logrus.SetLevel(logrus.TraceLevel)
		noProgressBar = true
	}
	if *noBar {
		noProgressBar = true
	}

	if *profile {
		cpu, err := os.Create("cpuprofile")
		if err != nil {
			logrus.Fatal("Error creating cpuprofile: %v", err)
		}
		defer cpu.Close()
		if err := pprof.StartCPUProfile(cpu); err != nil {
			logrus.Fatal("Error starting profiling: %v", err)
		}
		defer pprof.StopCPUProfile()

		mem, err := os.Create("memprofile")
		if err != nil {
			logrus.Fatal("Error creating memprofile: %v", err)
		}
		defer mem.Close()
		defer pprof.WriteHeapProfile(mem)
	}

	var backend gbackend.Backend
	var err error
	backend, err = gbackend.NewArangoBackend(*conn, "go-graph")
	if err != nil {
		logrus.Fatalf("Error creating backend: %v", err)
	}

	for i := 0; i < flag.NArg(); i++ {
		processPackage(flag.Arg(i), backend)
	}
}
