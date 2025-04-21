package scanner

// WARNING: Does not work with reverse scans.

import (
	"strings"
	"unicode/utf8"
)

type FoldProcessor struct {
}

func NewFoldProcessor() *FoldProcessor {
	return &FoldProcessor{}
}

func (p *FoldProcessor) Chain(chain ScanProcessorT) (nChain ScanProcessorT) {
	// Chain the processor to the scan function
	// and return the new chain.

	var (
		pending LogEntry
		builder strings.Builder
	)

	nChain.ScanF = func(entry LogEntry) (done bool) {
		// Cache on first spin
		if pending.Timestamp == 0 {
			pending = entry
			return
		}

		if builder.Len() > 0 {
			pending.Line = builder.String()
			builder.Reset()
		}

		// Scan pending entry
		switch done = chain.ScanF(pending); done {
		case true:
			// Scan done; avoid emit on flush
			pending.Timestamp = 0
		default:
			// Promote current entry to pending
			pending = entry
		}

		return
	}

	// On error, append line to pending entry
	nChain.ErrF = func(line []byte, err error) error {
		switch {
		case !utf8.Valid(line):
			// Data is not valid UTF8; ignore.
			// Occasionally binary garbage is seen in the log stream.
		case builder.Len() > 0:
			// We've already appended to the builder, append.
			builder.Write(line)
		case pending.Timestamp == 0:
			// No pending line, ignore the unparsable line
		default:
			builder.WriteString(pending.Line)
			builder.Write(line)
		}

		return chain.ErrF(line, err)
	}

	// On final flush, emit pending entry if exists
	nChain.FlushF = func() (done bool) {
		if pending.Timestamp != 0 {
			if builder.Len() > 0 {
				pending.Line = builder.String()
				builder.Reset()
			}
			done = chain.ScanF(pending)
		}
		if !done {
			done = chain.FlushF()
		}
		return
	}

	return
}
