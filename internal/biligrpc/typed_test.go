package biligrpc

import (
	"reflect"
	"testing"
)

func TestEncodeDmSegMobileReq(t *testing.T) {
	raw := EncodeDmSegMobileReq(43750131546, 2, 1)
	r := &protoReader{buf: raw}
	got := map[int]int64{}
	for !r.done() {
		field, wire, ok := r.tag()
		if !ok || wire != wireVarint {
			t.Fatalf("unexpected field %d wire %d", field, wire)
		}
		value, ok := r.varint()
		if !ok {
			t.Fatalf("invalid varint for field %d", field)
		}
		got[field] = int64(value)
	}
	want := map[int]int64{2: 43750131546, 3: 1, 4: 2}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("fields = %v, want %v", got, want)
	}
}

func TestDecodeDmSegMobileReply(t *testing.T) {
	elem := &protoWriter{}
	elem.Int(1, 2248799144733219072)
	elem.Int(2, 5752)
	elem.Int(3, 1)
	elem.String(7, "hello")
	elem.String(12, "2248799144733219072")

	reply := &protoWriter{}
	reply.Message(1, elem.Bytes())
	reply.Message(1, elem.Bytes())
	reply.Int(2, 1)

	decoded, err := DecodeDmSegMobileReply(reply.Bytes())
	if err != nil {
		t.Fatalf("DecodeDmSegMobileReply: %v", err)
	}
	elems, ok := decoded["elems"].([]any)
	if !ok || len(elems) != 2 {
		t.Fatalf("elems = %v", decoded["elems"])
	}
	first := elems[0].(map[string]any)
	if first["id"] != int64(2248799144733219072) {
		t.Errorf("id = %v", first["id"])
	}
	if first["progress"] != int64(5752) || first["mode"] != int64(1) {
		t.Errorf("progress/mode = %v/%v", first["progress"], first["mode"])
	}
	if first["content"] != "hello" || first["id_str"] != "2248799144733219072" {
		t.Errorf("content/id_str = %v/%v", first["content"], first["id_str"])
	}
	if decoded["state"] != int64(1) {
		t.Errorf("state = %v", decoded["state"])
	}
}

func TestEncodeMainListReq(t *testing.T) {
	raw := EncodeMainListReq(117644908825124, 1, 3, "cursor-1", 20)
	r := &protoReader{buf: raw}
	fields := map[int]int64{}
	for !r.done() {
		field, wire, ok := r.tag()
		if !ok {
			t.Fatal("invalid tag")
		}
		switch field {
		case 1, 2, 9:
			if wire != wireVarint {
				t.Fatalf("field %d wire %d", field, wire)
			}
			value, _ := r.varint()
			fields[field] = int64(value)
		case 10:
			if wire != wireBytes {
				t.Fatalf("field 10 wire %d", field)
			}
			payload, ok := r.bytesValue()
			if !ok {
				t.Fatal("invalid pagination")
			}
			pr := &protoReader{buf: payload}
			pageFields := map[int]any{}
			for !pr.done() {
				pf, pw, ok := pr.tag()
				if !ok {
					t.Fatal("invalid pagination tag")
				}
				switch pf {
				case 1:
					if pw != wireVarint {
						t.Fatalf("pagination field 1 wire %d", pw)
					}
					v, _ := pr.varint()
					pageFields[1] = int64(v)
				case 2:
					if pw != wireBytes {
						t.Fatalf("pagination field 2 wire %d", pw)
					}
					v, _ := pr.bytesValue()
					pageFields[2] = string(v)
				default:
					if !pr.skip(pw) {
						t.Fatal("cannot skip pagination field")
					}
				}
			}
			if pageFields[1] != int64(20) || pageFields[2] != "cursor-1" {
				t.Fatalf("pagination = %v", pageFields)
			}
		default:
			if !r.skip(wire) {
				t.Fatalf("cannot skip field %d", field)
			}
		}
	}
	if fields[1] != 117644908825124 || fields[2] != 1 || fields[9] != 3 {
		t.Fatalf("fields = %v", fields)
	}
}
