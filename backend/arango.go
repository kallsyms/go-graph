package backend

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	driver "github.com/arangodb/go-driver"
	"github.com/arangodb/go-driver/http"
	"github.com/sirupsen/logrus"

	"github.com/kallsyms/go-graph/coordination"
	"github.com/kallsyms/go-graph/schema"
)

type idGenerator struct {
	prefix  string
	counter uint64
}

func (g *idGenerator) Next() string {
	return fmt.Sprintf("%s-%x", g.prefix, atomic.AddUint64(&g.counter, 1))
}

type ArangoBackend struct {
	client driver.Client
	dbName string
	db     driver.Database
	graph  driver.Graph
	idGen  idGenerator
}

func NewArangoBackend(connStr string, dbName string) (*ArangoBackend, error) {
	conn, err := http.NewConnection(http.ConnectionConfig{
		Endpoints: []string{connStr},
		ConnLimit: -1,
	})
	if err != nil {
		return nil, err
	}
	client, err := driver.NewClient(driver.ClientConfig{
		Connection: conn,
	})
	if err != nil {
		return nil, err
	}

	db, err := client.Database(nil, dbName)
	if driver.IsNotFound(err) {
		db, err = client.CreateDatabase(nil, dbName, nil)
	}
	if err != nil {
		return nil, err
	}

	// Don't check for errors so that this works with -init
	graph, _ := db.Graph(nil, "code")

	return &ArangoBackend{client, dbName, db, graph, idGenerator{}}, nil
}

func (backend *ArangoBackend) DropAll() error {
	if err := backend.db.Remove(nil); err != nil {
		return err
	}

	db, err := backend.client.CreateDatabase(nil, backend.dbName, nil)
	backend.db = db

	return err
}

func (backend *ArangoBackend) CreateSchema() error {
	_, err := backend.db.CreateCollection(nil, "idgen", nil)
	if err != nil {
		return err
	}
	graph, err := backend.db.CreateGraphV2(nil, "code", &driver.CreateGraphOptions{
		EdgeDefinitions: []driver.EdgeDefinition{
			{
				Collection: "Functions",
				From:       []string{"package"},
				To:         []string{"function"},
			},
			{
				Collection: "Statement",
				From:       []string{"function"},
				To:         []string{"statement"},
			},
			{
				Collection: "Calls",
				From:       []string{"function"},
				To:         []string{"functioncall"},
			},
			{
				Collection: "Callee",
				From:       []string{"functioncall"},
				To:         []string{"function"},
			},
			{
				Collection: "References",
				From:       []string{"statement"},
				To:         []string{"variable"},
			},
			{
				Collection: "Assigns",
				From:       []string{"statement"},
				To:         []string{"variable"},
			},
			{
				Collection: "Next",
				From:       []string{"statement"},
				To:         []string{"statement"},
			},
			{
				Collection: "FirstStatement",
				From:       []string{"functioncall"},
				To:         []string{"statement"},
			},
			{
				Collection: "CallSiteStatement",
				From:       []string{"functioncall"},
				To:         []string{"statement"},
			},
		},
	})
	if err != nil {
		return err
	}

	backend.graph = graph

	pkgCol, err := graph.VertexCollection(nil, "package")
	if err != nil {
		// programming error
		panic(err)
	}
	_, _, err = pkgCol.EnsurePersistentIndex(nil, []string{"SourceURL", "Version"}, &driver.EnsurePersistentIndexOptions{
		Unique: true,
	})
	if err != nil {
		return err
	}

	functionCol, err := graph.VertexCollection(nil, "function")
	if err != nil {
		// programming error
		panic(err)
	}
	_, _, err = functionCol.EnsurePersistentIndex(nil, []string{"Name"}, nil)
	if err != nil {
		return err
	}

	return nil
}

func (backend *ArangoBackend) GetPackages() ([]*schema.Package, error) {
	cursor, err := backend.db.Query(nil, "FOR p IN package RETURN p", nil)
	if err != nil {
		return nil, err
	}
	defer cursor.Close()

	pkgs := []*schema.Package{}
	for {
		var p schema.Package
		_, err := cursor.ReadDocument(nil, &p)
		if driver.IsNoMoreDocuments(err) {
			break
		} else if err != nil {
			return nil, err
		}

		pkgs = append(pkgs, &p)
	}

	return pkgs, nil
}

func (backend *ArangoBackend) GetPackage(tup coordination.PackageTuple) (*schema.Package, bool) {
	pkg := &schema.Package{
		SourceURL: tup.Name,
		Version:   tup.Version,
	}

	cursor, err := backend.db.Query(nil, "FOR p IN package FILTER p.SourceURL == @SourceURL AND p.Version == @Version RETURN p", pkg.Properties())
	if err != nil {
		panic(err)
	}
	defer cursor.Close()

	meta, err := cursor.ReadDocument(nil, &pkg)
	if driver.IsNoMoreDocuments(err) {
		return nil, false
	} else if err != nil {
		panic(err)
	}

	pkg.SetBackendMeta(meta)
	return pkg, true
}

func (backend *ArangoBackend) CreatePackage(tup coordination.PackageTuple) (*schema.Package, error) {
	pkg := &schema.Package{
		SourceURL: tup.Name,
		Version:   tup.Version,
	}

	col, err := backend.graph.VertexCollection(nil, "package")
	if err != nil {
		panic(err)
	}

	meta, err := col.CreateDocument(nil, pkg)
	if err != nil {
		return nil, err
	}
	pkg.SetBackendMeta(meta)

	return pkg, nil
}

func (backend *ArangoBackend) PackageFunctions(pkg *schema.Package) (map[string]*schema.Function, error) {
	cursor, err := backend.db.Query(nil, "FOR f IN OUTBOUND @pkg Functions RETURN f", map[string]interface{}{
		"pkg": pkg.GetBackendMeta().(driver.DocumentMeta).ID,
	})
	if err != nil {
		return nil, err
	}
	defer cursor.Close()

	functions := map[string]*schema.Function{}
	for {
		var f schema.Function
		meta, err := cursor.ReadDocument(nil, &f)
		if driver.IsNoMoreDocuments(err) {
			break
		} else if err != nil {
			return nil, err
		}

		f.Package = pkg
		f.SetBackendMeta(meta)
		functions[f.Name] = &f
	}

	return functions, nil
}

const VERTEX_BATCH_SIZE = 1000
const EDGE_BATCH_SIZE = 1000
const BULK_WORKERS = 20

func (backend *ArangoBackend) flushVBulk(label string, vertices []schema.Vertex) error {
	col, err := backend.graph.VertexCollection(nil, label)
	if err != nil {
		return fmt.Errorf("Error getting vertex collection: %q: %v", label, err)
	}

	pMaps := make([]map[string]interface{}, len(vertices))
	for j, v := range vertices {
		pMaps[j] = v.Properties()
		key := backend.idGen.Next()

		v.SetBackendMeta(driver.DocumentMeta{
			Key: key,
			ID:  driver.DocumentID(label + "/" + key),
		})
		pMaps[j]["_key"] = key
	}

	var details []string
	stats, err := col.ImportDocuments(
		driver.WithImportDetails(nil, &details),
		pMaps,
		&driver.ImportDocumentOptions{
			// we guarantee all inserts are unique - this is just so that if we have to retry
			// due to arango OOMing or something and only some docs were persisted, those don't
			// cause the batch to error.
			// TODO: is this actually necessary?
			OnDuplicate: driver.ImportOnDuplicateReplace,
		},
	)
	if err != nil {
		return fmt.Errorf("Error inserting vertices: %v", err)
	}
	if stats.Errors > 0 {
		logrus.Error(details)
		return fmt.Errorf("Errors while adding vertices: %d/%d created, %d errors, %d empty", stats.Created, len(pMaps), stats.Errors, stats.Empty)
	}

	return nil
}

func (backend *ArangoBackend) AddVStream(vertices chan schema.Vertex, progressCb func([]schema.Vertex)) *sync.WaitGroup {
	// init idGenerator, doesn't really matter if this is racey
	if backend.idGen.prefix == "" {
		idCol, err := backend.db.Collection(nil, "idgen")
		if err != nil {
			panic(err)
		}

		meta, err := idCol.CreateDocument(nil, &struct{}{})
		if err != nil {
			panic(err)
		}
		backend.idGen.prefix = meta.Key
	}

	var wg sync.WaitGroup
	for i := 0; i < BULK_WORKERS; i++ {
		wg.Add(1)
		go func() {
			vByLabel := map[string][]schema.Vertex{}

			for {
				if v, ok := <-vertices; ok {
					label := v.Label()
					stuff := append(vByLabel[label], v)

					if len(vByLabel[label]) > VERTEX_BATCH_SIZE {
						for {
							err := backend.flushVBulk(label, stuff)
							if err == nil {
								break
							}
							logrus.Error(err)
							time.Sleep(3 * time.Second)
						}

						progressCb(stuff)
						stuff = stuff[:0]
					}

					vByLabel[label] = stuff
				} else {
					break
				}
			}

			for label, stuff := range vByLabel {
				backend.flushVBulk(label, stuff)
				progressCb(stuff)
			}

			wg.Done()
		}()
	}

	return &wg
}

type ArangoEdge struct {
	From driver.DocumentID `json:"_from"`
	To   driver.DocumentID `json:"_to"`
}

func (backend *ArangoBackend) flushEBulk(label string, edges []schema.Edge) error {
	col, _, err := backend.graph.EdgeCollection(nil, label)
	if err != nil {
		return fmt.Errorf("Error getting edge collection %q: %v", label, err)
	}

	aEdges := make([]ArangoEdge, len(edges))
	for j, edge := range edges {
		aEdges[j] = ArangoEdge{
			From: edge.Source.GetBackendMeta().(driver.DocumentMeta).ID,
			To:   edge.Target.GetBackendMeta().(driver.DocumentMeta).ID,
		}
	}

	var details []string
	stats, err := col.ImportDocuments(
		driver.WithImportDetails(nil, &details),
		aEdges,
		&driver.ImportDocumentOptions{
			OnDuplicate: driver.ImportOnDuplicateReplace,
		},
	)
	if err != nil {
		return fmt.Errorf("Error inserting edges: %v", err)
	}
	if stats.Errors > 0 {
		return fmt.Errorf("Errors while adding edges: %d/%d created, %d errors, %d empty", stats.Created, len(aEdges), stats.Errors, stats.Empty)
	}

	return nil
}

func (backend *ArangoBackend) AddEBulk(allEdges []schema.Edge, progressCb func([]schema.Edge)) {
	logrus.Infof("Adding %d edges", len(allEdges))

	work := make(chan schema.Edge)

	var wg sync.WaitGroup
	for i := 0; i < BULK_WORKERS; i++ {
		wg.Add(1)
		go func() {
			eByLabel := map[string][]schema.Edge{}

			for {
				if e, ok := <-work; ok {
					stuff := append(eByLabel[e.Label], e)

					if len(eByLabel[e.Label]) > EDGE_BATCH_SIZE {
						for {
							err := backend.flushEBulk(e.Label, stuff)
							if err == nil {
								break
							}
							logrus.Error(err)
							time.Sleep(3 * time.Second)
						}

						progressCb(stuff)
						stuff = stuff[:0]
					}

					eByLabel[e.Label] = stuff
				} else {
					break
				}
			}

			for label, stuff := range eByLabel {
				backend.flushEBulk(label, stuff)
				progressCb(stuff)
			}

			wg.Done()
		}()
	}

	for _, e := range allEdges {
		work <- e
	}

	close(work)
	wg.Wait()
}
