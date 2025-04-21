package scanner

import (
	"math"

	"github.com/prequel-dev/prequel-logmatch/internal/pkg/pool"
	"github.com/prequel-dev/prequel-logmatch/pkg/entry"

	"github.com/rs/zerolog/log"
)

type LogEntry = entry.LogEntry

const (
	MaxRecordSize = pool.MaxRecordSize
	pageSize      = 4096
)

type ParseFuncT func([]byte) (LogEntry, error)
type ScanFuncT func(entry LogEntry) bool
type ErrFuncT func([]byte, error) error
type FlushFuncT func() bool
type ScanOptT func(*scanOpt)

type scanOpt struct {
	maxSz      int
	start      int64
	stop       int64
	mark       int64
	processors []ScanProcessorI
}

func parseOpts(opts []ScanOptT) scanOpt {
	o := scanOpt{
		maxSz: MaxRecordSize,
		stop:  math.MaxInt64, // Default to scan to end of file; fixup for reverse scan since less common
	}

	for _, opt := range opts {
		opt(&o)
	}

	return o
}

func WithMaxSize(maxSz int) ScanOptT {
	return func(o *scanOpt) {
		// Allocate buffer large enough to hold the requested payload,
		// plus an extra 25% to account for formatting overhead in the
		// original payload.  When used in bufio.Scan(), size dictates
		// the maximum line size allowed, so must be large enough to
		// accommodate overhead of a line that post processing is less
		// than or equal to maxSize.
		bufSz := maxSz + (maxSz / 4)

		// Check for underflow/overflow
		if bufSz <= 0 || bufSz > MaxRecordSize {
			bufSz = MaxRecordSize
		}

		o.maxSz = bufSz
	}
}

func WithStart(start int64) ScanOptT {
	return func(o *scanOpt) {
		o.start = start
	}
}

func WithStop(stop int64) ScanOptT {
	return func(o *scanOpt) {
		o.stop = stop
	}
}

func WithMark(mark int64) ScanOptT {
	return func(o *scanOpt) {
		o.mark = mark
	}
}

func WithProcessor(processor ScanProcessorI) ScanOptT {
	return func(o *scanOpt) {
		o.processors = append(o.processors, processor)
	}
}

func defaultErrFunc(line []byte, err error) error {
	// Tolerate badly formed lines
	log.Debug().
		Err(err).
		Str("line", string(line)).
		Msg("Fail parse.  Continue...")
	return nil
}

func defaultFlushFunc() bool {
	return false
}

func (o *scanOpt) bindProcessors(scanF ScanFuncT) ScanProcessorT {
	chain := ScanProcessorT{
		ErrF:   defaultErrFunc,
		ScanF:  scanF,
		FlushF: defaultFlushFunc,
	}

	// Stack in order of processor registration
	// to allow for chaining of processors.
	for _, p := range o.processors {
		chain = p.Chain(chain)
	}

	return chain
}

type ScanProcessorT struct {
	ErrF   ErrFuncT
	ScanF  ScanFuncT
	FlushF FlushFuncT
}

type ScanProcessorI interface {
	Chain(ScanProcessorT) ScanProcessorT
}
