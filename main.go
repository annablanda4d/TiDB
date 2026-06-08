package main

import (
	"errors"
	"fmt"
	"sync"
)

// Table represents a database table with a schema version.
type Table struct {
	ID            int64
	Name          string
	SchemaVersion int64
	Columns       []string
	Indices       []string
}

// InfoSchema represents the schema information.
type InfoSchema struct {
	tables map[string]*Table
	mu     sync.RWMutex
}

func NewInfoSchema() *InfoSchema {
	return &InfoSchema{
		tables: make(map[string]*Table),
	}
}

func (is *InfoSchema) GetTable(name string) (*Table, bool) {
	is.mu.RLock()
	defer is.mu.RUnlock()
	t, exists := is.tables[name]
	return t, exists
}

func (is *InfoSchema) AddTable(t *Table) {
	is.mu.Lock()
	defer is.mu.Unlock()
	is.tables[t.Name] = t
}

// Plan represents a cached query plan.
type Plan struct {
	SQL              string
	ExecutionPath    string
	TableVersions    map[int64]int64 // TableID -> SchemaVersion at plan generation
	DependentTables  []int64
}

// PlanCache is a thread-safe cache for query plans.
type PlanCache struct {
	mu    sync.RWMutex
	cache map[string]*Plan
}

func NewPlanCache() *PlanCache {
	return &PlanCache{
		cache: make(map[string]*Plan),
	}
}

func (pc *PlanCache) Get(sql string, is *InfoSchema) (*Plan, bool) {
	pc.mu.Lock() // Lock for write because we might evict
	defer pc.mu.Unlock()

	plan, exists := pc.cache[sql]
	if !exists {
		return nil, false
	}

	// Validate schema versions of dependent tables
	is.mu.RLock()
	defer is.mu.RUnlock()

	for _, tableID := range plan.DependentTables {
		// Find the table in the current InfoSchema
		var currentTable *Table
		for _, t := range is.tables {
			if t.ID == tableID {
				currentTable = t
				break
			}
		}

		// If table no longer exists or schema version has changed, invalidate
		if currentTable == nil || currentTable.SchemaVersion > plan.TableVersions[tableID] {
			// Evict from cache
			delete(pc.cache, sql)
			return nil, false
		}
	}

	return plan, true
}

func (pc *PlanCache) Put(sql string, plan *Plan) {
	pc.mu.Lock()
	defer pc.mu.Unlock()
	pc.cache[sql] = plan
}

// Session represents a client session.
type Session struct {
	is        *InfoSchema
	planCache *PlanCache
}

func NewSession(is *InfoSchema, pc *PlanCache) *Session {
	return &Session{
		is:        is,
		planCache: pc,
	}
}

// Optimize simulates the query optimizer.
func (s *Session) Optimize(sql string) (*Plan, error) {
	t, exists := s.is.GetTable("t")
	if !exists {
		return nil, errors.New("table t not found")
	}

	hasIdx := false
	for _, idx := range t.Indices {
		if idx == "idx" {
			hasIdx = true
			break
		}
	}

	var path string
	if hasIdx && sql == "select * from t use index(idx) where a = ?" {
		path = "IndexScan(idx)"
	} else {
		path = "TableScan"
	}

	plan := &Plan{
		SQL:           sql,
		ExecutionPath: path,
		TableVersions: map[int64]int64{
			t.ID: t.SchemaVersion,
		},
		DependentTables: []int64{t.ID},
	}

	return plan, nil
}

// Execute executes a query, utilizing the plan cache.
func (s *Session) Execute(sql string) (string, error) {
	plan, hit := s.planCache.Get(sql, s.is)
	if hit {
		fmt.Printf("Cache Hit! Executing plan: %s\n", plan.ExecutionPath)
		return plan.ExecutionPath, nil
	}

	fmt.Println("Cache Miss! Optimizing query...")
	newPlan, err := s.Optimize(sql)
	if err != nil {
		return "", err
	}

	s.planCache.Put(sql, newPlan)
	return newPlan.ExecutionPath, nil
}

func main() {
	is := NewInfoSchema()
	pc := NewPlanCache()

	// 1. Create table t with index idx
	t := &Table{
		ID:            1,
		Name:          "t",
		SchemaVersion: 1,
		Columns:       []string{"a", "b"},
		Indices:       []string{"idx"},
	}
	is.AddTable(t)

	s := NewSession(is, pc)

	sql := "select * from t use index(idx) where a = ?"

	// First execution: Cache Miss, should optimize and use IndexScan
	path1, err := s.Execute(sql)
	if err != nil {
		panic(err)
	}
	fmt.Printf("Result 1: %s\n", path1)

	// Second execution: Cache Hit, should use IndexScan
	path2, err := s.Execute(sql)
	if err != nil {
		panic(err)
	}
	fmt.Printf("Result 2: %s\n", path2)

	// Perform DDL: Drop index idx (updates schema version)
	fmt.Println("\n--- Performing DDL: Drop index idx ---")
	is.mu.Lock()
	t.Indices = []string{}
	t.SchemaVersion++
	is.mu.Unlock()

	// Third execution: Cache Miss (invalidated due to schema version change),
	// should rebuild plan and use TableScan
	path3, err := s.Execute(sql)
	if err != nil {
		panic(err)
	}
	fmt.Printf("Result 3: %s\n", path3)
}