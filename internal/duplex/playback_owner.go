package duplex

// A nil owner is allowed for standalone Player users. Engine metrics never
// resolve one through the current tracker: production segments carry owners.
func (s Segment) validOwner() bool {
	return s.owner == nil || (s.owner.ID == s.TurnID && s.owner.Generation == s.Gen && s.owner.Revision == s.Revision)
}

func (r PlaybackReport) validTurn() *Turn {
	if r.owner == nil || r.owner.ID != r.TurnID || r.owner.Generation != r.Generation || r.owner.Revision != r.Revision {
		return nil
	}
	return r.owner
}

func (p *Player) adoptedOpenerLocked(s Segment) bool {
	return !s.content && s.Kind != SegEnd && p.adoptedFrom != nil && p.adoptedTo != nil && s.owner == p.adoptedFrom &&
		s.TurnID == p.adoptedTo.ID && (p.activeTurn == "" || p.owner == p.adoptedTo) && s.validOwner()
}

func (p *Player) matchesSegmentLocked(s Segment) bool {
	return (p.activeTurn != "" && p.adoptedOpenerLocked(s)) || (p.activeTurn == s.TurnID && p.activeGen == s.Gen && p.activeRevision == s.Revision && p.owner == s.owner)
}

// adoptOpenerLocked transfers only an opener to the revision that explicitly
// inherited it. The caller holds p.mu across the tracker generation change.
// Existing body audio can never be inherited through this path.
func (p *Player) adoptOpenerLocked(from, to *Turn) bool {
	if from == nil || to == nil || from.ID != to.ID || to.Revision <= from.Revision || to.Generation <= from.Generation {
		return false
	}
	if p.activeTurn != "" && (p.owner != from || p.contentStarted) {
		return false
	}
	for _, s := range p.queue {
		if s.owner != from {
			return false
		}
	}
	if p.activeTurn != "" {
		p.owner, p.activeGen, p.activeRevision = to, to.Generation, to.Revision
	}
	if p.adoptedFrom == nil {
		p.adoptedFrom = from
	}
	p.adoptedTo = to
	kept := p.queue[:0]
	for _, s := range p.queue {
		if s.content || s.Kind == SegEnd {
			continue
		}
		s.owner, s.Gen, s.Revision = to, to.Generation, to.Revision
		kept = append(kept, s)
	}
	clear(p.queue[len(kept):])
	p.queue = kept
	return true
}
