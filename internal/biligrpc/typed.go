package biligrpc

import (
	"encoding/json"
	"fmt"
)

// DmSegMobilePath is the gRPC path for a danmaku segment.
const DmSegMobilePath = "/bilibili.community.service.dm.v1.DM/DmSegMobile"

// MainListPath is the gRPC path for the main comment list.
const MainListPath = "/bilibili.main.community.reply.v1.Reply/MainList"

// EncodeDmSegMobileReq builds the DmSegMobileReq message.
// Field numbers from lib/grpc/bilibili/community/service/dm/v1.pb.dart.
func EncodeDmSegMobileReq(cid, segmentIndex, danmakuType int64) []byte {
	w := &protoWriter{}
	if cid != 0 {
		w.Int(2, cid) // oid
	}
	w.Int(3, danmakuType)
	if segmentIndex != 0 {
		w.Int(4, segmentIndex)
	}
	return w.Bytes()
}

// DecodeDmSegMobileReply converts DmSegMobileReply into JSON-friendly maps.
func DecodeDmSegMobileReply(payload []byte) (map[string]any, error) {
	result := map[string]any{"elems": []any{}}
	r := &protoReader{buf: payload}
	elems := make([]any, 0, 256)
	for !r.done() {
		field, wire, ok := r.tag()
		if !ok {
			return nil, fmt.Errorf("invalid protobuf tag")
		}
		if field == 1 && wire == wireBytes {
			elemBytes, ok := r.bytesValue()
			if !ok {
				return nil, fmt.Errorf("invalid danmaku elem")
			}
			elems = append(elems, decodeDanmakuElem(elemBytes))
			continue
		}
		if wire == wireVarint && (field == 2 || field == 3 || field == 6) {
			value, ok := r.varint()
			if !ok {
				return nil, fmt.Errorf("invalid protobuf varint")
			}
			if field == 2 {
				result["state"] = int64(value)
			}
			continue
		}
		if !r.skip(wire) {
			return nil, fmt.Errorf("invalid protobuf wire type %d", wire)
		}
	}
	result["elems"] = elems
	return result, nil
}

func decodeDanmakuElem(payload []byte) map[string]any {
	elem := map[string]any{}
	r := &protoReader{buf: payload}
	for !r.done() {
		field, wire, ok := r.tag()
		if !ok {
			return elem
		}
		switch field {
		case 1, 2, 3, 4, 5, 8, 9, 11, 13, 15, 24, 25, 26, 27, 100:
			if wire != wireVarint {
				if !r.skip(wire) {
					return elem
				}
				continue
			}
			value, ok := r.varint()
			if !ok {
				return elem
			}
			key := danmakuVarintKey(field)
			if field == 101 {
				elem["is_self"] = value != 0
			} else {
				elem[key] = int64(value)
			}
		case 6, 7, 10, 12, 22, 23:
			if wire != wireBytes {
				if !r.skip(wire) {
					return elem
				}
				continue
			}
			value, ok := r.bytesValue()
			if !ok {
				return elem
			}
			key := danmakuStringKey(field)
			if key != "" {
				elem[key] = string(value)
			}
		default:
			if field == 101 && wire == wireVarint {
				value, ok := r.varint()
				if !ok {
					return elem
				}
				elem["is_self"] = value != 0
				continue
			}
			if !r.skip(wire) {
				return elem
			}
		}
	}
	return elem
}

func danmakuVarintKey(field int) string {
	switch field {
	case 1:
		return "id"
	case 2:
		return "progress"
	case 3:
		return "mode"
	case 4:
		return "fontsize"
	case 5:
		return "color"
	case 8:
		return "ctime"
	case 9:
		return "weight"
	case 11:
		return "pool"
	case 13:
		return "attr"
	case 15:
		return "like_count"
	case 24:
		return "colorful"
	case 25:
		return "type"
	case 26:
		return "oid"
	case 27:
		return "dm_from"
	case 100:
		return "count"
	}
	return "field_" + fmt.Sprint(field)
}

func danmakuStringKey(field int) string {
	switch field {
	case 6:
		return "mid_hash"
	case 7:
		return "content"
	case 10:
		return "action"
	case 12:
		return "id_str"
	case 22:
		return "animation"
	case 23:
		return "extra"
	}
	return ""
}

// EncodeMainListReq builds MainListReq for the reply gRPC service.
// mode: 2 = time, 3 = hot.
func EncodeMainListReq(oid, replyType, mode int64, offset string, pageSize int64) []byte {
	w := &protoWriter{}
	w.Int(1, oid)
	w.Int(2, replyType)
	if mode != 0 {
		w.Int(9, mode)
	}
	pagination := &protoWriter{}
	if pageSize > 0 {
		pagination.Int(1, pageSize)
	}
	pagination.String(2, offset)
	w.Message(10, pagination.Bytes())
	return w.Bytes()
}

// MarshalJSON is a tiny convenience for callers embedding raw results.
func MarshalJSON(v any) string {
	raw, _ := json.Marshal(v)
	return string(raw)
}
