package dag

import (
	"fmt"

	"github.com/a-matson/workflow-orchestrator/backend/internal/models"
)

// Graph represents the Directed Acyclic Graph of tasks
type Graph struct {
	Nodes    map[string]*Node
	Edges    map[string][]string // nodeID -> []dependentIDs
	InDegree map[string]int
}

// Node wraps a TaskDefinition in the graph
type Node struct {
	Task         *models.TaskDefinition
	Dependencies []*Node
	Dependents   []*Node
}

// ValidationError holds DAG validation failures
type ValidationError struct {
	Issues []string
}

func (e *ValidationError) Error() string {
	msg := "DAG validation failed:"
	for _, issue := range e.Issues {
		msg += "\n  - " + issue
	}
	return msg
}

// Parse builds a Graph from a WorkflowDefinition and validates it
func Parse(def *models.WorkflowDefinition) (*Graph, error) {
	g := &Graph{
		Nodes:    make(map[string]*Node),
		Edges:    make(map[string][]string),
		InDegree: make(map[string]int),
	}

	// Register all nodes
	for i := range def.Tasks {
		task := &def.Tasks[i]
		if _, exists := g.Nodes[task.ID]; exists {
			return nil, fmt.Errorf("duplicate task ID: %s", task.ID)
		}
		g.Nodes[task.ID] = &Node{Task: task}
		g.InDegree[task.ID] = 0
	}

	// Wire dependencies
	for i := range def.Tasks {
		task := &def.Tasks[i]
		for _, depID := range task.Dependencies {
			depNode, exists := g.Nodes[depID]
			if !exists {
				return nil, fmt.Errorf("task %s depends on unknown task %s", task.ID, depID)
			}
			g.Nodes[task.ID].Dependencies = append(g.Nodes[task.ID].Dependencies, depNode)
			depNode.Dependents = append(depNode.Dependents, g.Nodes[task.ID])
			g.Edges[depID] = append(g.Edges[depID], task.ID)
			g.InDegree[task.ID]++
		}
	}

	if err := g.validate(); err != nil {
		return nil, err
	}

	return g, nil
}

// validate checks for cycles and other structural problems
func (g *Graph) validate() error {
	var issues []string

	// Kahn's algorithm to detect cycles
	order, err := g.TopologicalSort()
	if err != nil {
		issues = append(issues, err.Error())
	} else if len(order) != len(g.Nodes) {
		issues = append(issues, "cycle detected in task dependency graph")
	}

	if len(issues) > 0 {
		return &ValidationError{Issues: issues}
	}
	return nil
}

// TopologicalSort returns tasks in a safe execution order using Kahn's algorithm
func (g *Graph) TopologicalSort() ([]string, error) {
	inDegree := make(map[string]int)
	for id, deg := range g.InDegree {
		inDegree[id] = deg
	}

	// Start with all root nodes (no dependencies)
	var queue []string
	for id, deg := range inDegree {
		if deg == 0 {
			queue = append(queue, id)
		}
	}

	var order []string
	for len(queue) > 0 {
		// Stable sort for determinism in tests
		nodeID := queue[0]
		queue = queue[1:]
		order = append(order, nodeID)

		for _, dependentID := range g.Edges[nodeID] {
			inDegree[dependentID]--
			if inDegree[dependentID] == 0 {
				queue = append(queue, dependentID)
			}
		}
	}

	if len(order) != len(g.Nodes) {
		return nil, fmt.Errorf("cycle detected: only %d/%d nodes resolved", len(order), len(g.Nodes))
	}

	return order, nil
}
