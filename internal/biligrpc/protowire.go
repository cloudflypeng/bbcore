package biligrpc

import "encoding/binary"

// Minimal protobuf wire helpers so gRPC metadata and danmaku messages can be
// encoded/decoded without generated code.

const (
	wireVarint = 0
	wireByte64 = 1
	wireBytes  = 2
	wireByte32 = 5
)

type protoWriter struct {
	buf []byte
}

func (w *protoWriter) varint(v uint64) {
	var tmp [10]byte
	n := binary.PutUvarint(tmp[:], v)
	w.buf = append(w.buf, tmp[:n]...)
}

func (w *protoWriter) tag(field, wire int) {
	w.varint(uint64(field)<<3 | uint64(wire))
}

func (w *protoWriter) Int(field int, v int64) {
	w.tag(field, wireVarint)
	w.varint(uint64(v))
}

func (w *protoWriter) Uint(field int, v uint64) {
	w.tag(field, wireVarint)
	w.varint(v)
}

func (w *protoWriter) Bool(field int, v bool) {
	if v {
		w.Uint(field, 1)
	}
}

func (w *protoWriter) String(field int, s string) {
	if s == "" {
		return
	}
	w.tag(field, wireBytes)
	w.varint(uint64(len(s)))
	w.buf = append(w.buf, s...)
}

func (w *protoWriter) Message(field int, payload []byte) {
	if len(payload) == 0 {
		w.tag(field, wireBytes)
		w.varint(0)
		return
	}
	w.tag(field, wireBytes)
	w.varint(uint64(len(payload)))
	w.buf = append(w.buf, payload...)
}

func (w *protoWriter) Bytes() []byte { return w.buf }

type protoReader struct {
	buf []byte
	pos int
}

func (r *protoReader) done() bool { return r.pos >= len(r.buf) }

func (r *protoReader) varint() (uint64, bool) {
	v, n := binary.Uvarint(r.buf[r.pos:])
	if n <= 0 {
		return 0, false
	}
	r.pos += n
	return v, true
}

func (r *protoReader) tag() (field, wire int, ok bool) {
	v, ok := r.varint()
	if !ok {
		return 0, 0, false
	}
	return int(v >> 3), int(v & 7), true
}

func (r *protoReader) bytesValue() ([]byte, bool) {
	length, ok := r.varint()
	if !ok || int(length) > len(r.buf)-r.pos {
		return nil, false
	}
	value := r.buf[r.pos : r.pos+int(length)]
	r.pos += int(length)
	return value, true
}

func (r *protoReader) skip(wire int) bool {
	switch wire {
	case wireVarint:
		_, ok := r.varint()
		return ok
	case wireByte64:
		if r.pos+8 > len(r.buf) {
			return false
		}
		r.pos += 8
		return true
	case wireBytes:
		_, ok := r.bytesValue()
		return ok
	case wireByte32:
		if r.pos+4 > len(r.buf) {
			return false
		}
		r.pos += 4
		return true
	default:
		return false
	}
}
