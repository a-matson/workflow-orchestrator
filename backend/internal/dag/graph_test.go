package dag_test

import (
	"testing"

	"github.com/workflow-platform/backend/internal/dag"
	"github.com/workflow-platform/backend/internal/models"
)

func makeWorkflow(tasks []models.TaskDefinition) *models.WorkflowDefinition {
	return &models.WorkflowDefinition{
		ID:    "wf-test",
		Name:  "Test Workflow",
		Tasks: tasks,
	}
}

func TestParse_LinearChain(t *testing.T) {
	def := makeWorkflow([]models.TaskDefinition{
		{ID: "a", Name: "A", Type: "generic", Dependencies: []string{}},
		{ID: "b", Name: "B", Type: "generic", Dependencies: []string{"a"}},
		{ID: "c", Name: "C", Type: "generic", Dependencies: []string{"b"}},
	})

	g, err := dag.Parse(def)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if len(g.Nodes) != 3 {
		t.Errorf("expected 3 nodes, got %d", len(g.Nodes))
	}
}

func TestParse_Diamond(t *testing.T) {
	//   a
	//  / \
	// b   c
	//  \ /
	//   d
	def := makeWorkflow([]models.TaskDefinition{
		{ID: "a", Dependencies: []string{}},
		{ID: "b", Dependencies: []string{"a"}},
		{ID: "c", Dependencies: []string{"a"}},
		{ID: "d", Dependencies: []string{"b", "c"}},
	})

	g, err := dag.Parse(def)
	if err != nil {
		t.Fatalf("diamond DAG should be valid, got: %v", err)
	}

	order, err := g.TopologicalSort()
	if err != nil {
		t.Fatalf("topological sort failed: %v", err)
	}
	if len(order) != 4 {
		t.Errorf("expected 4 in topological order, got %d", len(order))
	}
	// 'a' must come before 'b' and 'c'; 'b' and 'c' before 'd'
	pos := func(id string) int {
		for i, v := range order {
			if v == id {
				return i
			}
		}
		return -1
	}
	if pos("a") >= pos("b") { t.Error("a must precede b") }
	if pos("a") >= pos("c") { t.Error("a must precede c") }
	if pos("b") >= pos("d") { t.Error("b must precede d") }
	if pos("c") >= pos("d") { t.Error("c must precede d") }
}

func TestParse_CycleDetected(t *testing.T) {
	def := makeWorkflow([]models.TaskDefinition{
		{ID: "a", Dependencies: []string{"c"}},
		{ID: "b", Dependencies: []string{"a"}},
		{ID: "c", Dependencies: []string{"b"}}, // cycle: a→b→c→a
	})

	_, err := dag.Parse(def)
	if err == nil {
		t.Fatal("expected cycle detection error, got nil")
	}
}

func TestParse_UnknownDependency(t *testing.T) {
	def := makeWorkflow([]models.TaskDefinition{
		{ID: "a", Dependencies: []string{"ghost"}}, // 'ghost' doesn't exist
	})

	_, err := dag.Parse(def)
	if err == nil {
		t.Fatal("expected error for unknown dependency, got nil")
	}
}

func TestParse_DuplicateNodeID(t *testing.T) {
	def := makeWorkflow([]models.TaskDefinition{
		{ID: "a", Dependencies: []string{}},
		{ID: "a", Dependencies: []string{}}, // duplicate
	})

	_, err := dag.Parse(def)
	if err == nil {
		t.Fatal("expected duplicate ID error, got nil")
	}
}

func TestGetReadyTasks_RootNodes(t *testing.T) {
	def := makeWorkflow([]models.TaskDefinition{
		{ID: "a", Dependencies: []string{}},
		{ID: "b", Dependencies: []string{}},
		{ID: "c", Dependencies: []string{"a", "b"}},
	})

	g, _ := dag.Parse(def)
	ready := g.GetReadyTasks(map[string]bool{}, map[string]bool{}, map[string]bool{})

	if len(ready) != 2 {
		t.Errorf("expected 2 ready root tasks, got %d: %v", len(ready), ready)
	}
}

func TestGetReadyTasks_AfterCompletion(t *testing.T) {
	def := makeWorkflow([]models.TaskDefinition{
		{ID: "a", Dependencies: []string{}},
		{ID: "b", Dependencies: []string{}},
		{ID: "c", Dependencies: []string{"a", "b"}},
	})

	g, _ := dag.Parse(def)

	// After a and b complete, c should be ready
	completed := map[string]bool{"a": true, "b": true}
	ready := g.GetReadyTasks(completed, map[string]bool{}, map[string]bool{})

	if len(ready) != 1 || ready[0] != "c" {
		t.Errorf("expected [c], got %v", ready)
	}
}

func TestGetReadyTasks_AlreadyRunning(t *testing.T) {
	def := makeWorkflow([]models.TaskDefinition{
		{ID: "a", Dependencies: []string{}},
		{ID: "b", Dependencies: []string{}},
	})

	g, _ := dag.Parse(def)

	// a is already running — only b should be ready
	running := map[string]bool{"a": true}
	ready := g.GetReadyTasks(map[string]bool{}, running, map[string]bool{})

	if len(ready) != 1 || ready[0] != "b" {
		t.Errorf("expected [b], got %v", ready)
	}
}

func TestCriticalPath(t *testing.T) {
	//  a → b → d
	//  a → c
	// Critical path should be a → b → d (length 3)
	def := makeWorkflow([]models.TaskDefinition{
		{ID: "a", Dependencies: []string{}},
		{ID: "b", Dependencies: []string{"a"}},
		{ID: "c", Dependencies: []string{"a"}},
		{ID: "d", Dependencies: []string{"b"}},
	})

	g, _ := dag.Parse(def)
	path := g.GetCriticalPath()

	if len(path) != 3 {
		t.Errorf("expected critical path length 3, got %d: %v", len(path), path)
	}
	if path[0] != "a" || path[len(path)-1] != "d" {
		t.Errorf("expected path a→…→d, got %v", path)
	}
}

func TestIsAncestor(t *testing.T) {
	def := makeWorkflow([]models.TaskDefinition{
		{ID: "a", Dependencies: []string{}},
		{ID: "b", Dependencies: []string{"a"}},
		{ID: "c", Dependencies: []string{"b"}},
	})

	g, _ := dag.Parse(def)

	if !g.IsAncestor("a", "c") {
		t.Error("a should be an ancestor of c")
	}
	if g.IsAncestor("c", "a") {
		t.Error("c should not be an ancestor of a")
	}
}
