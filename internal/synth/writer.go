package synth

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// fillerBlock is the padding every generated record is bulked out with. It is
// printable ASCII that no reader could mistake for prose, and it is built once
// so padding a 68 MiB log costs no allocation at all: writers slice it.
var fillerBlock = strings.Repeat("synthetic-filler-payload-", 512)

// fillerSlice returns n bytes of filler, wrapping when n exceeds one block.
func fillerSlice(n int) string {
	if n <= len(fillerBlock) {
		return fillerBlock[:n]
	}
	return strings.Repeat(fillerBlock, n/len(fillerBlock)+1)[:n]
}

// timeLayout is the timestamp form all three harnesses record: RFC 3339 with
// milliseconds and a literal Z, which time.Parse(time.RFC3339, ...) accepts.
const timeLayout = "2006-01-02T15:04:05.000Z"

// logWriter streams one JSONL log to disk.
//
// Records are formatted into a scratch buffer that is reused for the life of
// the file and flushed through a fixed-size writer, so memory use is bounded by
// the largest single record rather than by the log — the same property the
// readers under test must have, and one a fixture generator that built files in
// a bytes.Buffer could never help prove.
//
// Errors are sticky rather than returned per call: a record is a dozen appends,
// and checking each one would bury the record shapes this package exists to
// express. close reports the first failure.
type logWriter struct {
	path    string
	f       *os.File
	bw      *bufio.Writer
	buf     []byte
	size    int64
	records int
	err     error
}

// createLog opens a log for streaming, creating parent directories.
func createLog(path string) (*logWriter, error) {
	if err := os.MkdirAll(filepath.Dir(path), dirPerm); err != nil {
		return nil, fmt.Errorf("synth: create directory for %s: %w", path, err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, filePerm)
	if err != nil {
		return nil, fmt.Errorf("synth: create %s: %w", path, err)
	}
	return &logWriter{
		path: path,
		f:    f,
		bw:   bufio.NewWriterSize(f, logBufferBytes),
		buf:  make([]byte, 0, 8<<10),
	}, nil
}

// open starts a new record.
func (w *logWriter) open() *logWriter {
	w.buf = w.buf[:0]
	return w
}

// raw appends verbatim JSON text.
func (w *logWriter) raw(s string) *logWriter {
	w.buf = append(w.buf, s...)
	return w
}

// str appends s as a JSON string, quotes included.
func (w *logWriter) str(s string) *logWriter {
	w.buf = appendJSONString(w.buf, s)
	return w
}

// num appends a JSON number.
func (w *logWriter) num(v int64) *logWriter {
	w.buf = strconv.AppendInt(w.buf, v, 10)
	return w
}

// id appends "<prefix><n>" as a JSON string. Record identifiers are the one
// thing every record in a hot generation loop needs, so they are formatted by
// appending rather than by fmt: a 68 MiB log is a hundred thousand records, and
// one small allocation each would make the generator's own memory use scale
// with the corpus it is meant to prove can be streamed.
func (w *logWriter) id(prefix string, n int) *logWriter {
	w.buf = append(w.buf, '"')
	w.buf = append(w.buf, prefix...)
	w.buf = strconv.AppendInt(w.buf, int64(n), 10)
	w.buf = append(w.buf, '"')
	return w
}

// ts appends t as a JSON string in the harnesses' timestamp form.
func (w *logWriter) ts(t time.Time) *logWriter {
	w.buf = append(w.buf, '"')
	w.buf = t.UTC().AppendFormat(w.buf, timeLayout)
	w.buf = append(w.buf, '"')
	return w
}

// part flushes the record built so far without ending it, which is how a record
// larger than the scratch buffer is written: prefix, streamed payload, suffix.
func (w *logWriter) part() *logWriter {
	if w.err == nil && len(w.buf) > 0 {
		n, err := w.bw.Write(w.buf)
		w.size += int64(n)
		w.err = err
	}
	w.buf = w.buf[:0]
	return w
}

// fill streams n bytes of filler straight through the buffered writer, never
// materializing them.
func (w *logWriter) fill(n int64) *logWriter {
	w.part()
	for n > 0 && w.err == nil {
		chunk := int64(len(fillerBlock))
		if chunk > n {
			chunk = n
		}
		written, err := w.bw.WriteString(fillerBlock[:chunk])
		w.size += int64(written)
		w.err = err
		n -= chunk
	}
	return w
}

// end terminates the record with a newline and counts it.
func (w *logWriter) end() {
	w.buf = append(w.buf, '\n')
	w.part()
	w.records++
}

// tear ends the record with no newline, leaving the log as a reader finds it
// while a harness is still writing.
func (w *logWriter) tear() {
	w.part()
	w.records++
}

// close flushes, closes, and reports the first error seen.
func (w *logWriter) close() error {
	if err := w.bw.Flush(); err != nil && w.err == nil {
		w.err = err
	}
	if err := w.f.Close(); err != nil && w.err == nil {
		w.err = err
	}
	if w.err != nil {
		return fmt.Errorf("synth: write %s: %w", w.path, w.err)
	}
	return nil
}

// appendJSONString appends s as a JSON string literal. Only the three classes
// JSON actually requires are escaped, and multi-byte UTF-8 passes through
// unchanged; a fixture generator that emitted invalid JSON while claiming to
// generate well-formed records would defeat its own purpose.
func appendJSONString(dst []byte, s string) []byte {
	const hexDigits = "0123456789abcdef"
	dst = append(dst, '"')
	for i := range len(s) {
		switch c := s[i]; {
		case c == '"' || c == '\\':
			dst = append(dst, '\\', c)
		case c < 0x20:
			dst = append(dst, '\\', 'u', '0', '0', hexDigits[c>>4], hexDigits[c&0xf])
		default:
			dst = append(dst, c)
		}
	}
	return append(dst, '"')
}

// recordShape is one harness's record skeleton around a free-text payload,
// which is all the defect writer needs in order to emit damage shaped like the
// harness itself produced it.
type recordShape struct {
	// open starts a record and writes everything up to and including the
	// opening quote of its text payload.
	open func(w *logWriter, t time.Time)
	// closing is the JSON following the payload's closing quote.
	closing string
}

// writeDefects appends a log's planned damage, in the only order it can occur:
// a malformed record is not JSON in any harness and therefore has no shape, an
// oversized record is a well-formed record with an enormous payload, and a torn
// record is necessarily last, because the writer that tore it is the writer
// that stopped.
func writeDefects(w *logWriter, d Defects, start time.Time, shape recordShape) {
	for i := range d.MalformedRecords {
		w.open().raw("{synthetic-malformed-record-").num(int64(i))
		w.end()
	}
	for range d.OversizedRecords {
		shape.open(w, start.Add(time.Duration(w.records)*time.Second))
		w.fill(OversizedRecordBytes)
		w.raw(shape.closing)
		w.end()
	}
	if d.TornFinalLine {
		shape.open(w, start.Add(time.Duration(w.records)*time.Second))
		w.raw("synthetic torn")
		w.tear()
	}
}
