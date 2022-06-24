package backend

import (
	"sync"

	"github.com/kallsyms/go-graph/coordination"
	"github.com/kallsyms/go-graph/schema"
)

type Backend interface {
	DropAll() error
	CreateSchema() error
	GetPackages() ([]*schema.Package, error)
	GetPackage(coordination.PackageTuple) (*schema.Package, bool)
	CreatePackage(coordination.PackageTuple) (*schema.Package, error)
	PackageFunctions(pkg *schema.Package) (map[string]*schema.Function, error)
	AddVStream(vertices chan schema.Vertex, progressCb func([]schema.Vertex)) *sync.WaitGroup
	AddEBulk(edges []schema.Edge, progressCb func([]schema.Edge))
}
