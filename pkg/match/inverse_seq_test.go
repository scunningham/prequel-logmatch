package match

import (
	"testing"
	"time"

	"github.com/prequel-dev/prequel-logmatch/pkg/entry"
	"github.com/rs/zerolog"
)

//********
// --A--B--C-----
// -----------D--

// Should fire *ONLY* {A,D},
// not {A,D}, {B,D}, {C,D}

func TestSeqInverseBadReset(t *testing.T) {
	var (
		window int64 = 10

		resets = []ResetT{
			{
				Term:   makeRaw("Shutdown initiated"),
				Anchor: 11, // Bad anchor
			},
		}
	)

	_, err := NewInverseSeq(window, makeTermsA("alpha", "beta"), resets)
	if err != ErrAnchorRange {
		t.Fatalf("Expected err == ErrAnchorRange, got %v", err)
	}
}

func TestSeqInverse(t *testing.T) {
	type step = stepT[InverseSeq]

	var tests = map[string]struct {
		clock  int64
		window int64
		terms  []string
		reset  []ResetT
		steps  []step
	}{
		"SingleTerm": {
			// -A---------------- alpha
			window: 10,
			terms:  []string{"alpha"},
			steps: []step{
				{line: "alpha", cb: matchStamps(1)},
			},
		},

		"SingleTermResetHit": {
			// -A---------------- alpha
			// ------------------ reset
			terms: []string{"alpha"},
			reset: []ResetT{
				{
					Window: 10,
					Term:   makeRaw("reset"),
				},
			},
			steps: []step{
				{line: "alpha"},
				{line: "NOOP", stamp: 10},                      // fire slightly early
				{line: "reset", stamp: 12, cb: matchStamps(1)}, // Fire reset late
			},
		},

		"SingleTermResetMiss": {
			// -A---------------- alpha
			// -----------B------ reset
			terms: []string{"alpha"},
			reset: []ResetT{
				{
					Window: 10,
					Term:   makeRaw("reset"),
				},
			},
			steps: []step{
				{line: "alpha"},
				{line: "reset", stamp: 11},
				{line: "NOOP", stamp: 1000},
			},
		},

		"SingleTermDupeTimestampReset": {
			//	-1---------------- alpha
			//	-2---------------- reset
			// An event with a dupe timestamp at the end of the reset window should not fire.
			window: 10,
			terms:  []string{"alpha"},
			reset: []ResetT{{
				Term: makeRaw("reset"),
			}}, // Simple relative reset
			steps: []step{
				{line: "alpha", stamp: 1},
				{line: "reset", stamp: 1},
			},
		},

		"DupeTimestamps": {
			// -A----------------
			// -B----------------
			// -C----------------
			// Dupe timestamps are tolerated; not enforcing strict ordering.
			window: 5,
			terms:  []string{"alpha", "beta", "gamma"},
			steps: []step{
				{line: "alpha", stamp: 1},
				{line: "beta", stamp: 1},
				{line: "gamma", stamp: 1, cb: matchStamps(1, 1, 1)},
			},
		},

		"DupeTimestampOnEndOfResetWindow": {
			//	-1---------------- alpha
			//	--2---------------- beta
			//	--3---------------- reset
			// An even with a dupe timestamp at the end of the reset window should not fire.
			window: 10,
			terms:  []string{"alpha", "beta"},
			reset: []ResetT{{
				Term: makeRaw("reset"),
			}}, // Simple relative reset
			steps: []step{
				{line: "alpha", stamp: 1},
				{line: "beta", stamp: 2},
				{line: "reset", stamp: 2},
			},
		},

		"SimpleWindowMatchWithAbsoluteReset": {
			// --A----------
			// ----------B--
			// Fire B inside window, should delay until past window.
			window: 10,
			terms:  []string{"alpha", "beta"},
			reset: []ResetT{
				{
					Term:     makeRaw("reset"),
					Window:   50,
					Absolute: true,
				},
			},
			steps: []step{
				{line: "alpha", stamp: 1},
				{line: "beta", stamp: 1 + 10},                             // alpha stamp + window + 1
				{line: "NOOP", stamp: 1 + 50},                             // still in absolute reset window},
				{line: "NOOP", stamp: 1 + 50 + 1, cb: matchStamps(1, 11)}, // alpha stamp + window + reset window + 1
			},
		},

		"SimpleWindowMatchHitWithAbsoluteResetAndBigJump": {
			// --A----------
			// ---B--------
			// Fire B inside window, should delay until past reset window.
			window: 10,
			terms:  []string{"alpha", "beta"},
			reset: []ResetT{
				{
					Term:     makeRaw("reset"),
					Window:   50,
					Absolute: true,
				},
			},
			steps: []step{
				{line: "alpha"},
				{line: "beta"},
				{line: "NOOP", stamp: 10000, cb: matchStamps(1, 2)}, // way out of reset window
			},
		},

		"SimpleWindowMatchMissWithAbsoluteReset": {
			// --A----------
			// ----------B--
			// Fire B outside of window, should delay until past window.
			window: 10,
			terms:  []string{"alpha", "beta"},
			reset: []ResetT{
				{
					Term:     makeRaw("reset"),
					Window:   50,
					Absolute: true,
				},
			},
			steps: []step{
				{line: "alpha", stamp: 1},
				{line: "beta", stamp: 1 + 10 + 1}, // out of the window
				{line: "NOOP", stamp: 10000},      // way out of reset window
			},
		},

		"OverFire": {
			// --A--B--C-----
			// -----------D--
			window: 10,
			terms:  []string{"alpha", "beta"},
			steps: []step{
				{line: "alpha"},
				{line: "alpha"},
				{line: "alpha"},
				{line: "beta", cb: matchStamps(1, 4)},
			},
		},

		"SlideLeft": {
			// -------23----- alpha
			// ---------4---- beta
			// -1------------ reset
			// Fire a reset with a left shift,
			// should deny {2,4} on winow, but allow {3,4}
			window: 5,
			terms:  []string{"alpha", "beta"},
			reset: []ResetT{
				{
					Term:     makeRaw("reset"),
					Slide:    -5,
					Window:   20,
					Absolute: true,
				},
			},
			steps: []step{
				{line: "reset"},
				{line: "alpha", stamp: 6}, // reset window [1,21], no fire
				{line: "alpha"},           // reset window [2,22], should fire after 22
				{line: "beta"},
				{line: "noop", stamp: 22}, // no fire until outside reset window
				{line: "noop", cb: matchStamps(7, 8)},
			},
		},

		"SlideRight": {
			// ---12---------- alpha
			// -----3--------- beta
			// --------------4- reset
			// Fire a reset with a right shift,
			// should deny {2,4} on winow, but allow {3,4}
			window: 5,
			terms:  []string{"alpha", "beta"},
			reset: []ResetT{
				{
					Term:     makeRaw("reset"),
					Slide:    5,
					Window:   20,
					Absolute: true,
				},
			},
			steps: []step{
				{line: "alpha"},            // reset window [6,26], no fire
				{line: "alpha", stamp: 22}, // reset window [27,47], should fire after 47
				{line: "reset"},            // reset ignored because abs window is slide right
				{line: "beta"},
				{line: "reset", stamp: 26}, // right edge of line 1 window
				{line: "noop", stamp: 47},  // right edge of line 2 window
				{line: "noop", cb: matchStamps(22, 24)},
				{line: "noop", stamp: 1000}, // way out of reset window, should not fire
			},
		},

		"RelativeResetWindowMiss": {
			// -A-------------
			// --B------------
			// ---C-----------
			// -------------R-
			// Setup a relative reset window, and assert reset at end of window.
			// Should not fire.
			window: 3,
			terms:  []string{"alpha", "beta", "gamma"},
			reset: []ResetT{
				{
					Term:   makeRaw("reset"),
					Window: 10,
				},
			},
			steps: []step{
				{line: "alpha"},
				{line: "beta"},
				{line: "gamma"},                // should fire but delay on on reset window [1,13]
				{line: "noop", stamp: 13},      // Noop on edge of window, but can't fire until 13+1
				{line: "reset", stamp: 3 + 10}, // reset on right edge window
				{line: "noop", stamp: 1000},    // way out of reset window, should not fire
			},
		},

		"SlideAnchor": {
			// -1------------ alpha
			// ----2--------- beta
			window: 3,
			terms:  []string{"alpha", "beta"},
			reset: []ResetT{
				{
					Term:     makeRaw("reset"),
					Window:   10,
					Absolute: true,
					Anchor:   1,
				},
			},
			steps: []step{
				{line: "alpha"},
				{line: "beta", stamp: 1 + 3}, //  clock + window, reset window [4, 14]
				{line: "noop", stamp: 14},    // no fire until after window
				{line: "noop", cb: matchStamps(1, 4)},
			},
		},

		"AbsSlideResetContinue": {
			// -A-----------
			// ---B---------
			// ----C--DE----
			// --R----------
			// Anchor absolute reset window with neg slide on line 2.
			// Should disallow [A,B,C] and [A,B,D] but [A,B,E] should fire.
			window: 10,
			terms:  []string{"alpha", "beta", "gamma"},
			reset: []ResetT{
				{
					Term:     makeRaw("reset"),
					Window:   5,
					Absolute: true,
					Anchor:   2,
					Slide:    -5,
				},
			},
			steps: []step{
				{line: "alpha"},
				{line: "reset"},
				{line: "beta"},
				{line: "gamma"},           // reset window [-1, 4], no fire
				{line: "gamma", stamp: 7}, // reset window [2, 7], no fire
				{line: "gamma", stamp: 8}, // reset window [3, 8], should fire on 9
				{line: "noop", stamp: 9, cb: matchStamps(1, 3, 8)},
			},
		},

		"Relative": {
			// -1-3---8-A--- alpha
			// --2--56-9-B-- beta
			// ----4-------- reset1
			// ----------C-- reset2
			window: 50,
			terms:  []string{"alpha", "beta"},
			reset: []ResetT{
				{Term: makeRaw("reset1")},
				{Term: makeRaw("reset2")},
			},
			steps: []step{
				{line: "alpha"},
				{line: "beta"}, // Should match, but cannot fire until next event due to reset
				{line: "alpha", cb: matchStamps(1, 2)},
				{line: "reset1"},
				{line: "beta"},
				{line: "beta"},
				{line: "noop"},
				{line: "alpha"},
				{line: "beta"}, // Should match, but cannot fire until next event due to reset
				{line: "alpha", cb: matchStamps(8, 9)},
				{line: "beta"},
				{line: "reset2", stamp: 11}, // same timestamp as 11, should deny [10,11]
				{line: "noop", stamp: 1000},
			},
		},

		"AbsoluteHit": {
			window: 50,
			terms:  []string{"alpha", "beta"},
			reset: []ResetT{
				{Term: makeRaw("reset1")},
				{Term: makeRaw("reset2")},
				{
					Term:     makeRaw("reset3"),
					Window:   100,
					Absolute: true,
				},
			},
			steps: []step{
				{line: "alpha"},
				{line: "beta", stamp: 1 + 50},                      // clock + window, reset1 [1, 51], reset2 [1, 51], reset3 [1, 101]
				{line: "NOOP", stamp: 101},                         // no fire until after window
				{line: "NOOP", stamp: 102, cb: matchStamps(1, 51)}, // fire after reset window
			},
		},

		"AbsoluteMiss": {
			window: 50,
			terms:  []string{"alpha", "beta"},
			reset: []ResetT{
				{Term: makeRaw("reset1")},
				{Term: makeRaw("reset2")},
				{
					Term:     makeRaw("reset3"),
					Window:   100,
					Absolute: true,
				},
			},
			steps: []step{
				{line: "alpha"},
				{line: "beta", stamp: 1 + 50}, // clock + window, reset1 [1, 51], reset2 [1, 51], reset3 [1, 101]
				{line: "reset3", stamp: 101},  // reset at edge of window
				{line: "NOOP", stamp: 1000},   // no fire
			},
		},

		"ManualEval": {
			window: 10,
			terms:  []string{"alpha", "beta"},
			reset: []ResetT{
				{
					Term:     makeRaw("reset"),
					Window:   20,
					Absolute: true,
				},
			},
			steps: []step{
				{line: "alpha"},
				{line: "beta"},
				{line: "gamma"}, // Cannot fire until after reset window
				{postF: checkEval[*InverseSeq](21, checkNoFire)},
				{postF: checkEval[*InverseSeq](22, matchStamps(1, 2))},
			},
		},

		"PosRelativeOffset": {
			window: 10,
			terms:  []string{"alpha", "beta"},
			reset: []ResetT{
				{Term: makeRaw("reset1")},
				{Term: makeRaw("reset2")},
				{
					Term:     makeRaw("reset3"),
					Absolute: false,
					Window:   5,
				},
			},
			steps: []step{
				{line: "alpha"},
				{line: "beta"}, // reset1: [1,2] reset2: [1,2] reset3: [1,7]; cannot fire until after reset3
				{line: "noop", stamp: 7},
				{line: "noop", stamp: 8, cb: matchStamps(1, 2)},
				{line: "noop", stamp: 1000},
			},
		},

		"Dupes": {
			// --1----3--4-5-6-----
			// --1----3--4-5-6-----
			// --1----3--4 5-6-----
			// ----2-----------7--8
			// Because we are using a duplicate term, there is a possibility
			// of overlapping fire events.  This test should ensure that
			// the sequence matcher is able to handle this case.
			// Above should fire {1,3,4,7} and {3,4,5,8}
			window: 10,
			terms: []string{
				"alpha",
				"alpha",
				"alpha",
				"beta",
			},
			steps: []step{
				{line: "alpha"},
				{line: "beta"},
				{line: "alpha"},
				{line: "alpha"},
				{line: "alpha"},
				{line: "alpha"},
				{line: "beta", cb: matchStamps(1, 3, 4, 7)},
				{line: "beta", cb: matchStamps(3, 4, 5, 8)},
			},
		},

		"DupesWithResetHit": {
			// -123---------
			// -123---------
			// -123---------
			// ----4----56--
			window: 10,
			terms: []string{
				"alpha",
				"alpha",
				"alpha",
				"beta",
			},
			reset: []ResetT{
				{
					Term:     makeRaw("reset"),
					Window:   20,
					Absolute: true,
				},
			},
			steps: []step{
				{line: "alpha"},
				{line: "alpha"},
				{line: "alpha"},
				{line: "beta"},
				{line: "beta", stamp: 21},
				{line: "beta", stamp: 22, cb: matchStamps(1, 2, 3, 4)},
				{line: "noop", stamp: 1000},
			},
		},

		"DupesWithResetMiss": {
			// -123--------- alpha
			// -123--------- alpha
			// -123--------- alpha
			// ----4-----6-- beta
			// ---------5--- reset
			window: 10,
			terms: []string{
				"alpha",
				"alpha",
				"alpha",
				"beta",
			},
			reset: []ResetT{
				{
					Term:     makeRaw("reset"),
					Window:   20,
					Absolute: true,
				},
			},
			steps: []step{
				{line: "alpha"},
				{line: "alpha"},
				{line: "alpha"},
				{line: "beta"},
				{line: "reset", stamp: 21},
				{line: "beta", stamp: 22},
				{line: "noop", stamp: 1000},
			},
		},

		"DupesWithResetMissOnAnchor": {
			// -123--------- alpha
			// -123--------- alpha
			// -123--------- alpha
			// ----4-----6-- beta
			// ---------5--- reset
			window: 10,
			terms: []string{
				"alpha",
				"alpha",
				"alpha",
				"beta",
			},
			reset: []ResetT{
				{
					Term:     makeRaw("reset"),
					Window:   20,
					Anchor:   2,
					Absolute: true,
				},
			},
			steps: []step{
				{line: "alpha1"},
				{line: "alpha2"},
				{line: "alpha3"},
				{line: "beta1"},
				{line: "reset", stamp: 21},
				{line: "beta2", stamp: 22},
				{line: "noop", stamp: 1000},
			},
		},

		"GCOldTerms": {
			// -1------4--------------10----------
			// ---2--3----------8---9-----11----
			// ----------5--6-7---------------12-
			// Should fire {1,2,5}, {4,8,12}
			window: 50,
			terms:  []string{"alpha", "beta", "gamma"},
			steps: []step{
				{line: "alpha"},
				{line: "beta"},
				{line: "beta"},
				{line: "alpha"},
				{line: "gamma", cb: matchStamps(1, 2, 5)},
				{line: "gamma"},
				{line: "gamma"},
				{line: "beta"},
				{line: "beta"},
				{line: "alpha"},
				{line: "beta"},
				{line: "gamma", cb: matchStamps(4, 8, 12)},
				{postF: garbageCollect[*InverseSeq](12 + 50 + 1)}, // clock + window + 1
				{postF: checkActive[InverseSeq](0)},
			},
		},

		"ResetsIgnoreOnNoMatch": {
			window: 10,
			terms:  []string{"alpha", "beta", "gamma"},
			reset:  []ResetT{{Term: makeRaw("reset")}},
			steps: []step{
				{line: "reset"},
				{line: "reset"},
				{line: "reset"},
				{postF: checkResets[InverseSeq](0, 0)},
			},
		},

		"NegativesAreGCed": {
			window: 50,
			terms:  []string{"alpha", "beta", "gamma"},
			reset: []ResetT{
				{
					Term:   makeRaw("reset"),
					Slide:  -10,
					Window: 20,
				},
			},
			steps: []step{
				{line: "reset"},
				{line: "reset"},
				{line: "reset", postF: checkResets[InverseSeq](0, 3)},                    // Reset terms with nothing hot w/o lookback have been optimized out.
				{line: "NOOP", stamp: 1 + 50 + 20, postF: checkResets[InverseSeq](0, 3)}, // Emit noop at full GC window (see calcGCWindow)}, should have some negative terms
				{line: "NOOP", postF: checkResets[InverseSeq](0, 2)},                     // Emit noop right after window, should have peeled off one term
				{line: "NOOP", postF: checkResets[InverseSeq](0, 1)},                     // Emit noop right after window, should have peeled off one term
				{line: "NOOP", postF: checkResets[InverseSeq](0, 0)},                     // Emit noop right after window, should have peeled off one term
				{postF: checkGCMark[InverseSeq](disableGC)},
			},
		},

		"IgnoreOutOfOrder": {
			// -2------ alpha
			// --1----- beta
			window: 10,
			terms:  []string{"alpha", "beta"},
			steps: []step{
				{line: "beta", stamp: 2},
				{line: "alpha", stamp: 1},
			},
		},

		"FireMultiplesProperlyWithWindow": {
			// -123------------------------ alpha
			// --23------------------------ beta
			// ---3------------------------ gamma
			// -------4-------------------- delta
			window: 3,
			terms:  []string{"dupe", "dupe", "dupe", "fire"},
			steps: []step{
				{line: "dupe"}, //window [1,4]
				{line: "dupe"},
				{line: "dupe"},
				{line: "fire", stamp: 6}, // Should not fire  cause out of window.
			},
		},

		"FireMultiplesProperlyWithWindowMiss": {
			// -12345------------ dupe
			// --2345------------ dupe
			// ---345------------ dupe
			// ----------8------- fire
			window: 4,
			terms:  []string{"dupe", "dupe", "dupe", "fire"},
			steps: []step{
				{line: "dupe"}, //window [1,5]
				{line: "dupe"}, //window [2,6]
				{line: "dupe"}, //window [3,7]
				{line: "dupe"},
				{line: "dupe"},
				{line: "fire", stamp: 8}, // Should not fire  cause out of window.
			},
		},

		"FireMultiplesProperlyWithWindowHit": {
			// -1234567----------- dupe
			// --234567----------- dupe
			// ---34567----------- dupe
			// --------8---------- fire
			// Should fire {5,6,7,8}
			window: 3,
			terms:  []string{"dupe", "dupe", "dupe", "fire"},
			steps: []step{
				{line: "dupe1"},
				{line: "dupe2"},
				{line: "dupe3"},
				{line: "dupe4"},
				{line: "dupe5"},
				{line: "dupe6"},
				{line: "dupe7"},
				{line: "fire", stamp: 8, cb: matchLines("dupe5", "dupe6", "dupe7", "fire")},
			},
		},

		"FireMultiplesProperlyWithWindowHitSameTimestamp": {
			// -1234567----------- dupe
			// --234567----------- dupe
			// ---34567----------- dupe
			// --------89---------- fire
			// Should fire {1,2,3,8},{2,3,4,9} due to same timestamp
			window: 3,
			terms:  []string{"dupe", "dupe", "dupe", "fire"},
			steps: []step{
				{line: "dupe1", stamp: 1},
				{line: "dupe2", stamp: 1},
				{line: "dupe3", stamp: 1},
				{line: "dupe4", stamp: 1},
				{line: "dupe5", stamp: 1},
				{line: "dupe6", stamp: 1},
				{line: "dupe7", stamp: 1},
				{line: "fire1", stamp: 1, cb: matchLines("dupe1", "dupe2", "dupe3", "fire1")},
				{line: "fire2", stamp: 2, cb: matchLines("dupe2", "dupe3", "dupe4", "fire2")},
			},
		},

		"FireDisjointMultiples": {
			// -12-456-89---------- dupe
			// --2-456-89---------- dupe
			// ---3---7------------ disjoint
			// ----456-89---------- dupe
			// ----456-89---------- dupe
			// ----------A--------- fire
			// Should fire {5,6,7,8,9,A}
			window: 5,
			terms:  []string{"dupe", "dupe", "disjoint", "dupe", "dupe", "fire"},
			steps: []step{
				{line: "1_dupe"},
				{line: "2_dupe"},
				{line: "3_disjoint"},
				{line: "4_dupe"},
				{line: "5_dupe"},
				{line: "6_dupe"},
				{line: "7_disjoint"},
				{line: "8_dupe"},
				{line: "9_dupe"},
				{line: "A_fire", cb: matchLines("5_dupe", "6_dupe", "7_disjoint", "8_dupe", "9_dupe", "A_fire")},
			},
		},

		"FireDisjointMultiplesSecondNotDuped": {
			// -12-456-89---------- dupe
			// --2-456-89---------- dupe
			// ---3---7------------ disjoint
			// ----456-89---------- dupe
			// ----456-89---------- dupe
			// ----------A--------- fire
			// Should fire {5,6,7,8,9,A}
			window: 5,
			terms:  []string{"dupe", "dupe", "disjoint", "dupe", "dupe", "fire"},
			steps: []step{
				{line: "1_dupe"},
				{line: "2_dupe"},
				{line: "3_disjoint"},
				{line: "4_dupe"},
				{line: "5_dupe"},
				{line: "6_dupe"},
				{line: "7_disjoint"},
				{line: "8_dupe"},
				{line: "9_dupe"},
				{line: "A_fire", cb: matchLines("5_dupe", "6_dupe", "7_disjoint", "8_dupe", "9_dupe", "A_fire")},
			},
		},

		"FireDistinctMultiplesMiss": {
			// --1234----- alpha
			// ---234----- alpha
			// ------56--- beta
			// -------6--- beta
			// ---------A- fire
			// Should not fire; alpha line 2 is out of window.
			window: 4,
			terms:  []string{"alpha", "alpha", "beta", "beta", "fire"},
			steps: []step{
				{line: "1_alpha"},
				{line: "2_alpha"},
				{line: "3_alpha"},
				{line: "4_alpha"},
				{line: "5_beta"},
				{line: "6_beta"},
				{line: "8_fire", stamp: 8},
			},
		},

		"FireDistinctMultiplesHit": {
			// --12345----- alpha
			// ---2345----- alpha
			// -------678-- beta
			// --------78-- beta
			// ---------8-- fire
			// Should fire {3,4,6,7,8}.
			window: 5,
			terms:  []string{"alpha", "alpha", "beta", "beta", "fire"},
			steps: []step{
				{line: "1_alpha"},
				{line: "2_alpha"},
				{line: "3_alpha"},
				{line: "4_alpha"},
				{line: "5_alpha"},
				{line: "6_beta"},
				{line: "7_beta"},
				{line: "8_beta"},
				{line: "8_fire", stamp: 8, cb: matchLines("3_alpha", "4_alpha", "6_beta", "7_beta", "8_fire")},
			},
		},

		"SimpleResetWindow": {
			window: 2,
			terms:  []string{"alpha", "beta"},
			reset: []ResetT{
				{
					Term: makeRaw("reset"),
				},
			},
			steps: []step{
				{line: "alpha"},
				{line: "reset", stamp: 1},
				{line: "NOOP", stamp: 3, postF: checkResets[InverseSeq](0, 1)},
				{line: "NOOP", stamp: 4, postF: checkResets[InverseSeq](0, 0)},
			},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			sm, err := NewInverseSeq(tc.window, makeTerms(tc.terms), tc.reset)
			if err != nil {
				t.Fatalf("Expected err == nil, got %v", err)
			}

			clock := tc.clock

			for idx, step := range tc.steps {

				clock += 1
				stamp := clock
				if step.stamp != 0 {
					stamp = step.stamp
					clock = stamp
				}

				if step.line != "" {
					var (
						entry = entry.LogEntry{Timestamp: stamp, Line: step.line}
						hits  = sm.Scan(entry)
					)

					if step.cb == nil {
						checkNoFire(t, idx+1, hits)
					} else {
						step.cb(t, idx+1, hits)
					}
				}

				if step.postF != nil {
					step.postF(t, idx+1, sm)
				}
			}
		})
	}
}

// --------------------

func BenchmarkSeqInverseMisses(b *testing.B) {
	sm, err := NewInverseSeq(int64(time.Second), makeTermsA("frank", "burns"), nil)
	if err != nil {
		b.Fatalf("Expected err == nil, got %v", err)
	}

	noop := LogEntry{Line: "NOOP", Timestamp: time.Now().UnixNano()}

	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		noop.Timestamp += 1
		sm.Scan(noop)
	}
}

func BenchmarkSeqInverseMissesWithReset(b *testing.B) {

	resets := []ResetT{
		{
			Term:     makeRaw("badterm"),
			Window:   1000,
			Absolute: true,
		},
	}

	sm, err := NewInverseSeq(int64(time.Second), makeTermsA("frank", "burns"), resets)
	if err != nil {
		b.Fatalf("Expected err == nil, got %v", err)
	}

	noop := LogEntry{Line: "NOOP", Timestamp: time.Now().UnixNano()}

	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		noop.Timestamp += 1
		sm.Scan(noop)
	}
}

func BenchmarkSeqInverseHitSequence(b *testing.B) {
	level := zerolog.GlobalLevel()
	zerolog.SetGlobalLevel(zerolog.Disabled)
	defer zerolog.SetGlobalLevel(level)

	sm, err := NewInverseSeq(int64(time.Second), makeTermsA("frank", "burns"), nil)
	if err != nil {
		b.Fatalf("Expected err == nil, got %v", err)
	}

	ts := time.Now().UnixNano()
	ev1 := LogEntry{Line: "Let's be frank"}
	ev2 := LogEntry{Line: "Mr burns I am"}

	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		ev1.Timestamp = ts
		ev2.Timestamp = ts + 1
		ts += 2
		sm.Scan(ev1)
		m := sm.Scan(ev2)
		if m.Cnt != 1 {
			b.FailNow()
		}
	}
}

func BenchmarkSeqInverseHitOverlap(b *testing.B) {
	level := zerolog.GlobalLevel()
	zerolog.SetGlobalLevel(zerolog.Disabled)
	defer zerolog.SetGlobalLevel(level)

	sm, err := NewInverseSeq(10, makeTermsA("frank", "burns"), nil)
	if err != nil {
		b.Fatalf("Expected err == nil, got %v", err)
	}

	var (
		ts  = time.Now().UnixNano()
		ev1 = LogEntry{Line: "Let's be frank"}
		ev2 = LogEntry{Line: "Mr burns I am"}
	)

	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		ev1.Timestamp = ts
		sm.Scan(ev1)
		ts += 1
		ev1.Timestamp = ts
		sm.Scan(ev1)
		ts += 1
		ev1.Timestamp = ts
		sm.Scan(ev1)
		ts += 1
		ev2.Timestamp = ts
		ts += 1
		m := sm.Scan(ev2)
		if m.Cnt != 1 {
			b.FailNow()
		}
	}
}

func BenchmarkSeqInverseRunawayMatch(b *testing.B) {
	level := zerolog.GlobalLevel()
	zerolog.SetGlobalLevel(zerolog.Disabled)
	defer zerolog.SetGlobalLevel(level)

	sm, err := NewInverseSeq(1000000, makeTermsA("frank", "burns"), nil)
	if err != nil {
		b.Fatalf("Expected err == nil, got %v", err)
	}

	var (
		ev1 = LogEntry{Line: "Let's be frank"}
	)

	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		ev1.Timestamp += 1
		sm.Scan(ev1)
	}
}
