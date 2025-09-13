package match

import (
	"slices"

	"github.com/prequel-dev/prequel-logmatch/pkg/entry"
	"github.com/rs/zerolog/log"
)

type InverseSeq struct {
	clock   int64
	window  int64
	gcMark  int64
	gcLeft  int64
	gcRight int64
	nActive int
	terms   []termT
	resets  []resetT
	dupeMap map[int]int
}

func NewInverseSeq(window int64, seqTerms []TermT, resetTerms []ResetT) (*InverseSeq, error) {

	terms, dupeMap, err := buildSeqTerms(seqTerms...)
	if err != nil {
		return nil, err
	}

	var resets []resetT
	if len(resetTerms) > 0 {
		resets = make([]resetT, 0, len(resetTerms))

		for _, term := range resetTerms {
			m, err := term.Term.NewMatcher()
			switch {
			case err != nil:
				return nil, err
			case int(term.Anchor) >= len(seqTerms):
				return nil, ErrAnchorRange
			}

			resets = append(resets, resetT{
				matcher:  m,
				window:   term.Window,
				slide:    term.Slide,
				anchor:   term.Anchor,
				absolute: term.Absolute,
			})
		}
	}
	gcLeft, gcRight := calcGCWindow(window, resets)

	return &InverseSeq{
		window:  window,
		gcLeft:  gcLeft,
		gcRight: gcRight,
		gcMark:  disableGC,
		terms:   terms,
		resets:  resets,
		dupeMap: dupeMap,
	}, nil
}

func (r *InverseSeq) Scan(e entry.LogEntry) (hits Hits) {
	if e.Timestamp < r.clock {
		log.Warn().
			Str("line", e.Line).
			Int64("stamp", e.Timestamp).
			Int64("clock", r.clock).
			Msg("MatchSeq: Out of order event.")
		return
	}
	r.clock = e.Timestamp

	r.maybeGC(e.Timestamp)

	// Zero match optimization to avoid resets if no lookback is needed.
	var zeroMatch bool
	switch {
	case r.nActive > 0:
	case r.gcLeft > 0:
	case !r.terms[r.nActive].matcher(e.Line):
		return
	default:
		zeroMatch = true
	}

	// Run resets
	for i, reset := range r.resets {
		if reset.matcher(e.Line) {
			r.resets[i].resets = append(reset.resets, e.Timestamp)
			r.resetGcMark(e.Timestamp + r.gcLeft + r.gcRight)
		}
	}

	// Run the active terms
	for i := range r.nActive {
		if r.terms[i].matcher(e.Line) {
			r.terms[i].asserts = append(r.terms[i].asserts, e)
		}
	}

	if r.nActive < len(r.terms) {

		switch {
		case zeroMatch:
		case !r.terms[r.nActive].matcher(e.Line):
			return // No match on active term; NOOP.
		}

		r.terms[r.nActive].asserts = append(r.terms[r.nActive].asserts, e)

		r.resetGcMark(e.Timestamp + r.gcRight)

		if dupeCnt := r.dupeMap[r.nActive] + 1; len(r.terms[r.nActive].asserts) < dupeCnt {
			// Not enough dupes yet; cannot move to next activeTerm
			return
		}

		if r.nActive += 1; r.nActive < len(r.terms) {
			return
		}
	}

	return r.Eval(e.Timestamp)
}

// Assert clock, may used to close out matcher
func (r *InverseSeq) Eval(clock int64) (hits Hits) {
	var nTerms = len(r.terms)

	for r.nActive == nTerms {

		var (
			drop    = anchorT{term: -1}
			stopIdx = r.dupeMap[nTerms-1]
			tStart  = r.terms[0].asserts[0].Timestamp
			tStop   = r.terms[nTerms-1].asserts[stopIdx].Timestamp
		)

		if tStop-tStart > r.window {
			drop.term = 0
		} else if r.resets != nil {
			anchor := r.checkReset(clock)

			switch {
			case anchor.ValidTerm():
				drop = anchor
			case anchor.clock > 0:
				// We have a match that is too recent; we must wait.
				return
			}
		}

		if drop.ValidTerm() {
			// We have a negative match;
			// remove the offending term assert and continue.
			if _, ok := r.dupeMap[drop.term]; !ok {
				shiftLeft(r.terms, drop.term, 1)
			} else {
				shiftAnchor(r.terms, drop)
			}

		} else {
			// Fire hit and prune first assert from each term.
			hits.Cnt += 1
			if hits.Logs == nil {
				hits.Logs = make([]LogEntry, 0, nTerms+r.dupeMap[-1])
			}

			for i, term := range r.terms {
				hitCnt := r.dupeMap[i] + 1
				hits.Logs = append(hits.Logs, term.asserts[:hitCnt]...)

				// Only remove the first item; leave remaining dupes for next match.
				shiftLeft(r.terms, i, 1)
			}
		}

		// Fixup state
		r.miniGC()
	}

	return
}

func (r *InverseSeq) maybeGC(clock int64) {

	if clock < r.gcMark {
		return
	}

	r.GarbageCollect(clock)
}

// Remove all terms that are older than the window.
func (r *InverseSeq) GarbageCollect(clock int64) {

	// Special case;
	// If all the terms are hot and we have resets,
	// allow the GC to be handled on the next evaluation.
	// Otherwise, we may GC an valid single term prematurely.
	if r.nActive == len(r.terms) && len(r.resets) > 0 {
		r.gcMark = disableGC
		return
	}

	var (
		cnt      int
		nMark    = disableGC
		m        = r.terms[0].asserts
		deadline = clock - r.gcRight
	)

	// Find the first term that is not older than the window.
	// Binary search?
	for _, term := range m {
		if term.Timestamp >= deadline {
			break
		}
		cnt += 1
	}

	if cnt > 0 {
		shiftLeft(r.terms, 0, cnt)
	}

	r.miniGC()

	if r.nActive > 0 {
		nMark = r.terms[0].asserts[0].Timestamp + r.gcRight
	}

	// Adjust the deadline for the reset terms
	deadline -= r.gcLeft

	// Clean up the reset terms
	for i, reset := range r.resets {

		var (
			m = reset.resets
		)

		if len(m) == 0 {
			continue
		}

		cnt, _ := slices.BinarySearch(m, deadline)

		if cnt > 0 {
			r.resets[i].resets = m[cnt:]
		}

		if len(r.resets[i].resets) > 0 {
			v := r.resets[i].resets[0] + r.gcLeft + r.gcRight
			if v < nMark {
				nMark = v
			}
		}
	}

	r.gcMark = nMark
}

// Find the first term in the sequence.
// Remove each term older than that, it cannot be in sequence.
// Update the nActive count.

func (r *InverseSeq) miniGC() {

	if len(r.terms[0].asserts) == 0 {
		r.reset()
		return
	}

	var (
		nActive     = 0
		forceClear  bool
		zeroMatch   int64
		zeroAsserts = r.terms[0].asserts
		zeroDupes   = r.dupeMap[0]
	)

	if len(zeroAsserts) < zeroDupes+1 {
		forceClear = true
	} else {
		zeroMatch = zeroAsserts[zeroDupes].Timestamp
		nActive += 1
	}

	// For remaining active terms, find the first term that is not older than the window.
	for i := 1; i < r.nActive; i++ {

		if forceClear {
			resetTerm(r.terms, i)
			continue
		}

		var (
			cnt int
			m   = r.terms[i].asserts
		)
	TERMLOOP:
		for _, term := range m {
			switch {
			case term.Timestamp < zeroMatch:
			default:
				break TERMLOOP
			}
			cnt += 1
		}

		if cnt > 0 {
			shiftLeft(r.terms, i, cnt)
		}

		if len(r.terms[i].asserts) > r.dupeMap[i] {
			nActive++
		} else {
			forceClear = true
		}
	}

	r.nActive = nActive

}

func (r *InverseSeq) reset() {
	for i := range r.terms {
		resetTerm(r.terms, i)
	}
	r.nActive = 0
}

func (r *InverseSeq) checkReset(clock int64) anchorT {
	// 'stamps'  escapes;  annoying.
	// TODO: consider avoiding by using s.terms[0].asserts[0].Timestamp directly
	var (
		nTerms  = len(r.terms)
		nDupes  = r.dupeMap[-1]
		anchors = make([]anchorT, 0, nTerms+nDupes)
	)

	// Gather timestamps from match
	for i, term := range r.terms {
		cnt := r.dupeMap[i] + 1
		for j := range cnt {
			anchors = append(anchors, anchorT{
				clock:  term.asserts[j].Timestamp,
				term:   i,
				offset: j,
			})
		}
	}

	// Iterate across the resets; determine if we have a negative match.
	for _, reset := range r.resets {
		start, stop := reset.calcWindow(anchors)

		// Check if we have a negative term in the reset window.
		// TODO: Binary search?
		for _, ts := range reset.resets {
			if ts >= start && ts <= stop {
				return anchors[reset.anchor]
			}
		}

		// If the reset window is in the future, we cannot come to a conclusion.
		// We must wait until the reset window is in the past due to events with
		// duplicate timestamps.  Thus must wait until one tick past the reset window.
		if stop >= clock {
			return anchorT{
				term:  -1,
				clock: stop - clock + 1,
			}
		}
	}

	return anchorT{term: -1}
}

func (r *InverseSeq) resetGcMark(nMark int64) {
	if nMark < r.gcMark {
		r.gcMark = nMark
	}
}
