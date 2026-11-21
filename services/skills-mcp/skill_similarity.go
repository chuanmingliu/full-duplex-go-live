package main

import (
	"container/heap"
	"context"
	"sort"
)

type skillNeighbor struct {
	id       int
	score    float64
	priority int
}

func betterNeighbor(a, b skillNeighbor) bool {
	if a.score != b.score {
		return a.score > b.score
	}
	if a.priority != b.priority {
		return a.priority > b.priority
	}
	return a.id < b.id
}

// The least relevant retained neighbor is at the root. A scan uses O(k)
// additional memory and O(n*d + n*log(k)) time for k requested results.
type neighborHeap []skillNeighbor

func (h neighborHeap) Len() int           { return len(h) }
func (h neighborHeap) Less(i, j int) bool { return betterNeighbor(h[j], h[i]) }
func (h neighborHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *neighborHeap) Push(v any)        { *h = append(*h, v.(skillNeighbor)) }
func (h *neighborHeap) Pop() any {
	old := *h
	v := old[len(old)-1]
	*h = old[:len(old)-1]
	return v
}

func (x *skillIndex) semanticSearch(ctx context.Context, in skillRequest, text string, limit, budget int) (skillReply, error) {
	if x.pool != nil {
		return x.pool.search(ctx, in, text, limit, budget)
	}
	if x.semantic.dimension == 0 {
		return x.searchReply(nil, nil, limit, budget, 0, "semantic", ""), nil
	}
	query, err := x.semantic.query(ctx, text)
	if err != nil {
		return skillReply{}, err
	}
	minimum := 0.3
	if in.MinSimilarity != nil {
		minimum = *in.MinSimilarity
	}
	best := &neighborHeap{}
	total := 0
	consider := func(id int) {
		if !x.searchEligible(id, in) {
			return
		}
		vector := x.semantic.vectors[id]
		if vector == nil {
			return
		}
		var score float64
		for i, value := range vector {
			score += float64(value) * float64(query[i])
		}
		// Rounding in float32 normalization can produce values just outside [-1,1].
		score = min(1, max(-1, score))
		if score < minimum {
			return
		}
		total++
		candidate := skillNeighbor{id, score, x.items[id].skill.Priority}
		if best.Len() < limit {
			heap.Push(best, candidate)
		} else if betterNeighbor(candidate, (*best)[0]) {
			(*best)[0] = candidate
			heap.Fix(best, 0)
		}
	}
	if in.Event != "" {
		for _, id := range x.events[in.Event] {
			if err := ctx.Err(); err != nil {
				return skillReply{}, err
			}
			consider(id)
		}
	} else {
		for id := range x.items {
			if err := ctx.Err(); err != nil {
				return skillReply{}, err
			}
			consider(id)
		}
	}
	sort.Slice(*best, func(i, j int) bool { return betterNeighbor((*best)[i], (*best)[j]) })
	ids := make([]int, 0, best.Len())
	scores := make(map[int]float64, best.Len())
	for _, candidate := range *best {
		ids = append(ids, candidate.id)
		scores[candidate.id] = candidate.score
	}
	return x.searchReply(ids, scores, limit, budget, total, "semantic", ""), nil
}
