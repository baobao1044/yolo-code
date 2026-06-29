package scope

import (
	"math"
	"strconv"
)

// ScopeNode is one node of the scope MCTS tree (SWE-Search / Moatless style,
// adapted to the scope ladder). Each node holds the scope state reached by
// taking its Action from its Parent, plus the MCTS statistics (Visits and
// RewardSum) used by UCB1 selection and backpropagation.
type ScopeNode struct {
	ID        string
	State     ScopeState
	Action    ScopeAction
	Parent    *ScopeNode
	Children  []*ScopeNode
	Visits    int
	RewardSum float64
}

// ScopeState is the state captured at a node: the current Level plus the
// scope.State (allowed files/functions and hypotheses). The MCTS searches over
// ScopeStates; the rollout evaluates one.
type ScopeState struct {
	Level Level
	State State
}

// ScopeAction names an action edge in the tree. It reuses the scope.Action kind
// (NoOp/Expand/Contract/Stay) so the search speaks the same vocabulary as the
// transition logic, and carries a human-readable Reason.
type ScopeAction struct {
	Action Action
	Reason string
}

// RolloutFn evaluates a leaf state and returns a reward in [0,1] (1 = good
// outcome, e.g. a verify pass reachable; 0 = dead end / loop guard tripped). A
// nil RolloutFn selects the search's deterministic default heuristic.
type RolloutFn func(s ScopeState) float64

// ScopeTreeSearch runs MCTS from a root scope state, bounded by a rollout
// budget, to find the most promising next ScopeAction. Each simulation performs
// the four MCTS phases — selection (UCB1), expansion, simulation (rollout),
// and backpropagation — and the best action is the root's robust child (the
// child of root with the most visits).
type ScopeTreeSearch struct {
	root      *ScopeNode
	budget    int
	rollout   RolloutFn
	c         float64 // UCB1 exploration constant; defaults to sqrt(2) when zero
	memory    *Memory // optional anti-loop memory consulted by the default rollout
	idCounter int
}

// NewScopeTreeSearch builds a search rooted at root, with the given rollout
// budget and an optional RolloutFn (nil → default heuristic). The root counts as
// visited once, so after budget simulations root.Visits == budget+1.
func NewScopeTreeSearch(root ScopeState, budget int, rollout RolloutFn) *ScopeTreeSearch {
	return &ScopeTreeSearch{
		root:    &ScopeNode{ID: "0", State: root, Visits: 1},
		budget:  budget,
		rollout: rollout,
	}
}

// WithMemory wires an anti-loop Memory so the default rollout can consult
// Memory.LoopGuard (returning 0.0 when it would trip). It is optional and
// nil-safe: the default rollout skips the guard entirely when no memory is
// wired, so a search built with NewScopeTreeSearch never panics.
func (s *ScopeTreeSearch) WithMemory(m *Memory) *ScopeTreeSearch {
	s.memory = m
	return s
}

// Root returns the root node, for inspection of the built tree.
func (s *ScopeTreeSearch) Root() *ScopeNode { return s.root }

// Search runs the MCTS loop for budget simulations and returns the robust
// child's action and the child node itself. If the root never expanded (no
// budget or no candidate actions), it returns a zero ScopeAction and the root.
func (s *ScopeTreeSearch) Search() (ScopeAction, *ScopeNode) {
	if s.c == 0 {
		s.c = math.Sqrt(2)
	}
	for i := 0; i < s.budget; i++ {
		node := s.selectNode()
		if s.hasUntried(node) {
			child := s.expand(node)
			s.backprop(child, s.simulate(child.State))
		} else {
			// Terminal leaf: simulate from the node itself.
			s.backprop(node, s.simulate(node.State))
		}
	}
	if len(s.root.Children) == 0 {
		return ScopeAction{}, s.root
	}
	// Robust child: the child of root with the most visits. Ties break by
	// creation order (the first such child in Children), which is stable.
	var best *ScopeNode
	for _, c := range s.root.Children {
		if best == nil || c.Visits > best.Visits {
			best = c
		}
	}
	return best.Action, best
}

// selectNode descends from the root via UCB1 until it reaches a node that still
// has an untried action (to expand) or a terminal leaf (to simulate).
func (s *ScopeTreeSearch) selectNode() *ScopeNode {
	node := s.root
	for {
		if s.hasUntried(node) {
			return node
		}
		if len(node.Children) == 0 {
			return node
		}
		node = s.selectChild(node)
	}
}

// selectChild returns the child of node with the highest UCB1 value. Ties are
// broken by creation order (the first child in node.Children), which is stable
// and deterministic; unvisited children score +inf and are always explored
// before any visited sibling.
func (s *ScopeTreeSearch) selectChild(node *ScopeNode) *ScopeNode {
	var best *ScopeNode
	var bestVal float64
	for _, c := range node.Children {
		val := s.ucb1(c, node)
		if best == nil || val > bestVal {
			best = c
			bestVal = val
		}
	}
	return best
}

// ucb1 computes the UCB1 score of child relative to parent:
//
//	exploit + c*sqrt(ln(parent.Visits)/child.Visits)
//
// where exploit = child.RewardSum/child.Visits. Unvisited children (Visits==0)
// score +inf so they are explored once before being exploited.
func (s *ScopeTreeSearch) ucb1(child, parent *ScopeNode) float64 {
	if child.Visits == 0 {
		return math.Inf(1)
	}
	exploit := child.RewardSum / float64(child.Visits)
	explore := s.c * math.Sqrt(math.Log(float64(parent.Visits))/float64(child.Visits))
	return exploit + explore
}

// expand adds one child to node for the first candidate action that has not yet
// been tried, and returns the new child. Callers only invoke expand when
// hasUntried(node) is true, so expand always finds an action to try; the
// no-op return is defensive.
func (s *ScopeTreeSearch) expand(node *ScopeNode) *ScopeNode {
	tried := map[Action]bool{}
	for _, c := range node.Children {
		tried[c.Action.Action] = true
	}
	for _, a := range s.actionsFor(node) {
		if tried[a.Action] {
			continue
		}
		target := applyAction(node.State.Level, a)
		state := node.State.State // copy scope.State (slice headers shared, read-only)
		state.Level = target
		child := &ScopeNode{
			ID:     s.nextID(),
			State:  ScopeState{Level: target, State: state},
			Action: a,
			Parent: node,
		}
		node.Children = append(node.Children, child)
		return child
	}
	return node
}

// simulate evaluates a state with the wired RolloutFn, or the default heuristic
// when none was provided.
func (s *ScopeTreeSearch) simulate(state ScopeState) float64 {
	if s.rollout != nil {
		return s.rollout(state)
	}
	return s.defaultRollout(state)
}

// defaultRollout is the deterministic fallback heuristic used when RolloutFn is
// nil. It returns 0.0 when a wired memory's loop guard would trip, 1.0 when the
// state has reached LevelVerify (the sentinel "pass"), and otherwise a mild
// gradient (0.5 + a small bonus favoring LevelEdit/LevelFunction). It is a pure
// function of the state plus the optional memory, so it stays deterministic.
func (s *ScopeTreeSearch) defaultRollout(state ScopeState) float64 {
	if s.memory != nil && s.memory.LoopGuard() {
		return 0.0
	}
	if state.Level == LevelVerify {
		return 1.0
	}
	return 0.5 + levelBonus(state.Level)
}

// levelBonus returns a small, deterministic reward bonus that favors the
// edit/function rungs (the productive middle of the ladder).
func levelBonus(l Level) float64 {
	switch l {
	case LevelEdit, LevelFunction:
		return 0.1
	default:
		return 0.0
	}
}

// backprop walks from node up to the root, incrementing Visits and adding reward
// to RewardSum at every ancestor along the path.
func (s *ScopeTreeSearch) backprop(node *ScopeNode, reward float64) {
	for n := node; n != nil; n = n.Parent {
		n.Visits++
		n.RewardSum += reward
	}
}

// hasUntried reports whether node still has a candidate action with no child.
func (s *ScopeTreeSearch) hasUntried(node *ScopeNode) bool {
	acts := s.actionsFor(node)
	if len(acts) == 0 {
		return false
	}
	tried := map[Action]bool{}
	for _, c := range node.Children {
		tried[c.Action.Action] = true
	}
	for _, a := range acts {
		if !tried[a.Action] {
			return true
		}
	}
	return false
}

// actionsFor returns the deterministic candidate actions at a node. A node
// produced by a Stay/NoOp action (same level as its parent) is a terminal leaf
// with no further actions; every other node offers Stay plus the level-changing
// moves derived from the transition rules.
func (s *ScopeTreeSearch) actionsFor(node *ScopeNode) []ScopeAction {
	if node.Parent != nil && node.State.Level == node.Parent.State.Level {
		return nil
	}
	return candidateActions(node.State.Level)
}

// candidateActions returns the small, fixed, deterministic action set for a
// level, mirroring the W3 transition vocabulary. Stay keeps the current level
// (the resulting child is a terminal leaf); Expand broadens one rung towards
// Task; Contract steps back two rungs (clamped at Task) to re-examine a broader
// context. Actions that would not change the level, or that would duplicate
// Expand's target, are omitted so every child lands on a distinct level.
//
// The moves are monotonic towards the broad end (Task), matching the direction of
// SuggestTransition's contract rule and the task's worked examples (from
// LevelFunction, Expand → LevelFile and Contract → LevelRepo).
func candidateActions(l Level) []ScopeAction {
	acts := []ScopeAction{{Action: ActionStay, Reason: "stay at " + l.String()}}

	expandTarget := l - 1
	if expandTarget >= LevelTask {
		acts = append(acts, ScopeAction{Action: ActionExpand, Reason: "broaden to " + expandTarget.String()})
	}

	contractTarget := l - 2
	if contractTarget < LevelTask {
		contractTarget = LevelTask
	}
	if contractTarget != l && contractTarget != expandTarget {
		acts = append(acts, ScopeAction{Action: ActionContract, Reason: "step back to " + contractTarget.String()})
	}

	return acts
}

// applyAction maps a level and a scope action to the resulting level, using the
// same rule as candidateActions (Expand: l-1; Contract: l-2; Stay/NoOp: l),
// clamped at LevelTask.
func applyAction(l Level, a ScopeAction) Level {
	switch a.Action {
	case ActionExpand:
		t := l - 1
		if t < LevelTask {
			t = LevelTask
		}
		return t
	case ActionContract:
		t := l - 2
		if t < LevelTask {
			t = LevelTask
		}
		return t
	default: // ActionStay, ActionNoOp
		return l
	}
}

// nextID returns a deterministic sequential identifier for a new node.
func (s *ScopeTreeSearch) nextID() string {
	s.idCounter++
	return strconv.Itoa(s.idCounter)
}
