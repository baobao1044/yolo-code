package scope

import "testing"

// deterministicRollout rewards LevelEdit with 1.0 and every other level with
// 0.3, the stub suggested in the task spec. It is a pure function of the state,
// so results are fully deterministic.
func deterministicRollout(s ScopeState) float64 {
	if s.Level == LevelEdit {
		return 1.0
	}
	return 0.3
}

// TestMCTS_TreeBuildsChildren verifies that after Search the root has a
// populated set of children (the tree was expanded).
func TestMCTS_TreeBuildsChildren(t *testing.T) {
	s := NewScopeTreeSearch(ScopeState{Level: LevelFunction}, 20, deterministicRollout)
	_, child := s.Search()
	if len(s.Root().Children) == 0 {
		t.Fatal("root has no children after Search")
	}
	if child == nil {
		t.Fatal("Search returned nil child")
	}
}

// TestMCTS_UCB1SelectsUnvisitedChild verifies that selectChild prefers an
// unvisited child (UCB1 +inf) over a visited sibling.
func TestMCTS_UCB1SelectsUnvisitedChild(t *testing.T) {
	s := NewScopeTreeSearch(ScopeState{Level: LevelFunction}, 1, nil)
	parent := s.Root()
	visited := &ScopeNode{ID: "visited", Parent: parent, Visits: 5, RewardSum: 1.0}
	unvisited := &ScopeNode{ID: "unvisited", Parent: parent, Visits: 0}
	parent.Children = []*ScopeNode{visited, unvisited}

	got := s.selectChild(parent)
	if got != unvisited {
		t.Errorf("selectChild picked %q, want unvisited child (UCB1 +inf)", got.ID)
	}
}

// TestMCTS_BackpropUpdatesAncestors verifies that backprop increments Visits and
// accumulates RewardSum along the full path from a node to the root.
func TestMCTS_BackpropUpdatesAncestors(t *testing.T) {
	s := NewScopeTreeSearch(ScopeState{Level: LevelFunction}, 1, nil)
	root := s.Root()
	root.Visits = 0 // reset the constructor's initial visit for a clean check

	child := &ScopeNode{ID: "child", Parent: root}
	grandchild := &ScopeNode{ID: "grandchild", Parent: child}
	root.Children = []*ScopeNode{child}
	child.Children = []*ScopeNode{grandchild}

	s.backprop(grandchild, 0.5)

	cases := []struct {
		name   string
		node   *ScopeNode
		visits int
		reward float64
	}{
		{"grandchild", grandchild, 1, 0.5},
		{"child", child, 1, 0.5},
		{"root", root, 1, 0.5},
	}
	for _, c := range cases {
		if c.node.Visits != c.visits {
			t.Errorf("%s.Visits = %d, want %d", c.name, c.node.Visits, c.visits)
		}
		if c.node.RewardSum != c.reward {
			t.Errorf("%s.RewardSum = %v, want %v", c.name, c.node.RewardSum, c.reward)
		}
	}
}

// TestMCTS_RobustChildReturned verifies that when a deterministic rollout
// rewards the root's level, Search returns the Stay action — the robust child
// that keeps the search at the rewarded level — rather than a move away from it.
func TestMCTS_RobustChildReturned(t *testing.T) {
	s := NewScopeTreeSearch(ScopeState{Level: LevelEdit}, 50, deterministicRollout)
	action, child := s.Search()
	if action.Action != ActionStay {
		t.Errorf("robust action = %v, want STAY (stays at rewarded LevelEdit)", action.Action)
	}
	if child == nil || child.Action.Action != ActionStay {
		var got Action = -1
		if child != nil {
			got = child.Action.Action
		}
		t.Errorf("robust child action = %v, want STAY", got)
	}
}

// TestMCTS_BudgetBoundsSimulations verifies that Search runs exactly budget
// simulations, so root.Visits == budget+1 (the +1 being the root's own initial
// visit recorded at construction).
func TestMCTS_BudgetBoundsSimulations(t *testing.T) {
	const budget = 30
	rollout := func(s ScopeState) float64 { return 0.5 }
	s := NewScopeTreeSearch(ScopeState{Level: LevelFunction}, budget, rollout)
	s.Search()
	if got, want := s.Root().Visits, budget+1; got != want {
		t.Errorf("root.Visits = %d, want %d (budget+1)", got, want)
	}
}

// TestMCTS_DefaultRolloutNilSafe verifies that a nil RolloutFn (selecting the
// default heuristic) does not panic and still drives tree expansion.
func TestMCTS_DefaultRolloutNilSafe(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("default rollout panicked with RolloutFn=nil: %v", r)
		}
	}()
	s := NewScopeTreeSearch(ScopeState{Level: LevelFunction}, 10, nil)
	action, child := s.Search()
	if child == nil {
		t.Error("Search returned nil child")
	}
	if len(s.Root().Children) == 0 {
		t.Error("root has no children; default rollout did not drive expansion")
	}
	_ = action
}
