package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/cloudflypeng/bbcore/internal/jsonrpc"
)

func (s *Service) registerLive() {
	s.add("live.list", "Recommended live rooms (params: page, page_size)", s.liveList)
	s.add("live.following", "Followed live rooms (params: page)", s.liveFollowing)
	s.add("live.room_info", "Room play info (params: room_id, qn, only_audio)", s.liveRoomInfo)
	s.add("live.room_info_h5", "Room H5 info (params: room_id)", s.liveRoomInfoH5)
	s.add("live.info_user", "Room info for the current user (params: room_id)", s.liveInfoByUser)
	s.add("live.danmaku_info", "Danmaku server token (params: room_id)", s.liveDanmakuInfo)
	s.add("live.history", "Recent room danmaku (params: room_id)", s.liveHistory)
	s.add("live.super_chat", "Super chat messages (params: room_id)", s.liveSuperChat)
	s.add("live.send_msg", "Send a danmaku to a room (params: room_id, msg, mode, color)", s.liveSendMsg)
	s.add("live.connect", "Subscribe to a room's danmaku (params: room_id); events via notifications", s.liveConnect)
	s.add("live.disconnect", "Stop a danmaku subscription (params: room_id)", s.liveDisconnect)
	s.add("live.rooms", "List active danmaku subscriptions", s.liveRooms)
}

func (s *Service) liveList(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	p, rpcErr := jsonrpc.ParseParams(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	return s.endpointQuery(ctx, "liveList", p, values(map[string]any{
		"page": p.IntDefault("page", 1), "page_size": p.IntDefault("page_size", 30),
		"platform": "web",
	}))
}

func (s *Service) liveFollowing(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	p, rpcErr := jsonrpc.ParseParams(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	return s.endpointQuery(ctx, "liveFollow", p, values(map[string]any{
		"page": p.IntDefault("page", 1), "page_size": 9,
		"ignoreRecord": 1, "hit_ab": "true",
	}))
}

func (s *Service) liveRoomInfo(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	p, rpcErr := jsonrpc.ParseParams(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	roomID, ok := p.ID("room_id")
	if !ok || roomID <= 0 {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "room_id is required")
	}
	query := values(map[string]any{
		"room_id": roomID, "protocol": "0,1", "format": "0,1,2", "codec": "0,1,2",
		"qn": p.IntDefault("qn", 0), "platform": "web", "ptype": 8,
		"dolby": 5, "panorama": 1, "web_location": 444.8,
	})
	if p.Bool("only_audio", false) {
		query.Set("only_audio", "1")
	}
	return s.endpointRequest(ctx, "liveRoomInfo", requestOptions{
		account: accountRef(p),
		query:   query,
		headers: map[string]string{"referer": fmt.Sprintf("https://live.bilibili.com/%d", roomID)},
	})
}

func (s *Service) liveRoomInfoH5(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	p, rpcErr := jsonrpc.ParseParams(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	roomID, ok := p.ID("room_id")
	if !ok || roomID <= 0 {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "room_id is required")
	}
	return s.endpointRequest(ctx, "liveRoomInfoH5", requestOptions{
		account: accountRef(p),
		query:   url.Values{"room_id": {stringify(roomID)}},
		headers: map[string]string{"referer": fmt.Sprintf("https://live.bilibili.com/%d", roomID)},
	})
}

func (s *Service) liveInfoByUser(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	p, rpcErr := jsonrpc.ParseParams(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	roomID, ok := p.ID("room_id")
	if !ok || roomID <= 0 {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "room_id is required")
	}
	return s.endpointRequest(ctx, "getLiveInfoByUser", requestOptions{
		account: accountRef(p),
		query: values(map[string]any{
			"room_id": roomID, "from": 0, "not_mock_enter_effect": 1, "web_location": 444.8,
		}),
	})
}

func (s *Service) liveDanmakuInfo(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	p, rpcErr := jsonrpc.ParseParams(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	roomID, ok := p.ID("room_id")
	if !ok || roomID <= 0 {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "room_id is required")
	}
	return s.endpointRequest(ctx, "liveRoomDmToken", requestOptions{
		account: accountRef(p),
		query:   values(map[string]any{"id": roomID, "web_location": 444.8}),
		headers: map[string]string{"referer": fmt.Sprintf("https://live.bilibili.com/%d", roomID)},
		sign:    "wbi",
	})
}

func (s *Service) liveHistory(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	p, rpcErr := jsonrpc.ParseParams(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	roomID, ok := p.ID("room_id")
	if !ok || roomID <= 0 {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "room_id is required")
	}
	return s.endpointRequest(ctx, "liveRoomDmPrefetch", requestOptions{
		account: accountRef(p),
		query:   values(map[string]any{"roomid": roomID}),
		headers: map[string]string{"referer": fmt.Sprintf("https://live.bilibili.com/%d", roomID)},
	})
}

func (s *Service) liveSuperChat(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	p, rpcErr := jsonrpc.ParseParams(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	roomID, ok := p.ID("room_id")
	if !ok || roomID <= 0 {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "room_id is required")
	}
	return s.endpointQuery(ctx, "superChatMsg", p, url.Values{"room_id": {stringify(roomID)}})
}

func (s *Service) liveSendMsg(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	p, rpcErr := jsonrpc.ParseParams(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	roomID, ok := p.ID("room_id")
	if !ok || roomID <= 0 {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "room_id is required")
	}
	msg := p.String("msg")
	if msg == "" {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "msg is required")
	}
	csrf := s.csrfFor(accountRef(p))
	form := values(map[string]any{
		"bubble": 0, "msg": msg, "color": p.IntDefault("color", 16777215),
		"mode": p.IntDefault("mode", 1), "dm_type": p.IntDefault("dm_type", 0),
		"room_type": 0, "jumpfrom": 0, "reply_mid": p.IntDefault("reply_mid", 0),
		"reply_attr": 0, "replay_dmid": p.StringDefault("replay_dmid", ""),
		"statistics": `{"appId":100,"platform":5}`, "reply_type": 0, "reply_uname": "",
		"fontsize": 25, "rnd": time.Now().UnixMilli() / 1000, "roomid": roomID,
		"csrf": csrf, "csrf_token": csrf,
	})
	return s.endpointRequest(ctx, "sendLiveMsg", requestOptions{
		account: accountRef(p),
		method:  http.MethodPost,
		query:   url.Values{"web_location": {"444.8"}},
		form:    form,
		headers: map[string]string{"referer": fmt.Sprintf("https://live.bilibili.com/%d", roomID)},
	})
}

func (s *Service) liveConnect(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	p, rpcErr := jsonrpc.ParseParams(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	roomID, ok := p.ID("room_id")
	if !ok || roomID <= 0 {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "room_id is required")
	}
	if s.opt.Live == nil {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInternalError, "live manager is not available")
	}
	cred, credErr := s.credential(accountRef(p))
	if credErr != nil {
		return nil, credErr
	}
	info, err := s.opt.Live.Connect(ctx, roomID, cred)
	if err != nil {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInternalError, "%v", err)
	}
	return info, nil
}

func (s *Service) liveDisconnect(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	p, rpcErr := jsonrpc.ParseParams(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	roomID, ok := p.ID("room_id")
	if !ok || roomID <= 0 {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "room_id is required")
	}
	if s.opt.Live == nil {
		return map[string]any{"disconnected": false}, nil
	}
	return map[string]any{"disconnected": s.opt.Live.Disconnect(roomID)}, nil
}

func (s *Service) liveRooms(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	if s.opt.Live == nil {
		return map[string]any{"rooms": []int64{}}, nil
	}
	return map[string]any{"rooms": s.opt.Live.Rooms()}, nil
}
