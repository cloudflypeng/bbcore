package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"

	"github.com/cloudflypeng/bbcore/internal/bili"
	"github.com/cloudflypeng/bbcore/internal/jsonrpc"
)

func (s *Service) registerUser() {
	s.add("user.me", "Current user profile (nav)", s.userMe)
	s.add("user.info", "User space profile (params: mid)", s.userInfo)
	s.add("user.stat", "User follower/archives counts (params: mid)", s.userStat)
	s.add("user.card", "User card (params: mid)", s.userCard)
	s.add("user.videos", "Uploader videos (params: mid, pn, ps, keyword, order)", s.userVideos)
	s.add("user.history", "Watch history (params: type=archive, ps, max, view_at)", s.userHistory)
	s.add("user.later", "Watch later list (params: pn, ps)", s.userLater)
	s.add("user.later.add", "Add to watch later (params: bvid or aid)", s.userLaterAdd)
	s.add("user.later.del", "Remove from watch later (params: aids)", s.userLaterDel)
	s.add("user.later.clear", "Clear watch later", s.userLaterClear)
	s.add("user.followings", "Following list (params: vmid, pn, ps)", s.userFollowings)
	s.add("user.followers", "Follower list (params: vmid, pn, ps)", s.userFollowers)
	s.add("user.follow", "Follow/unfollow (params: fid, follow)", s.userFollow)
	s.add("user.blacklist", "Blacklist (params: pn, ps)", s.userBlacklist)
	s.add("user.folders", "Created favourite folders (params: mid default self)", s.userFolders)
	s.add("user.sub_folders", "Collected favourite folders (params: mid default self)", s.userSubFolders)
}

func (s *Service) userMe(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	p, rpcErr := jsonrpc.ParseParams(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	return s.rawRequest(ctx, bili.Spec{
		Base:   bili.BaseAPI,
		Method: http.MethodGet,
		Path:   bili.NavEndpoint,
		Sign:   bili.SignNone,
	}, accountRef(p), false)
}

func (s *Service) userInfo(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	p, rpcErr := jsonrpc.ParseParams(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	mid, ok := p.ID("mid")
	if !ok || mid <= 0 {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "mid is required")
	}
	result, err := s.endpointRequest(ctx, "memberInfo", requestOptions{
		account: accountRef(p),
		query:   url.Values{"mid": {stringify(mid)}},
		headers: map[string]string{
			"origin":  "https://space.bilibili.com",
			"referer": "https://space.bilibili.com/" + stringify(mid) + "/dynamic",
		},
	})
	return result, err
}

func (s *Service) userStat(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	p, rpcErr := jsonrpc.ParseParams(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	mid, ok := p.ID("mid")
	if !ok || mid <= 0 {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "mid is required")
	}
	return s.endpointQuery(ctx, "userStat", p, url.Values{"vmid": {stringify(mid)}})
}

func (s *Service) userCard(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	p, rpcErr := jsonrpc.ParseParams(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	mid, ok := p.ID("mid")
	if !ok || mid <= 0 {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "mid is required")
	}
	return s.endpointQuery(ctx, "memberCardInfo", p, url.Values{"mid": {stringify(mid)}, "photo": {"false"}})
}

func (s *Service) userVideos(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	p, rpcErr := jsonrpc.ParseParams(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	mid, ok := p.ID("mid")
	if !ok || mid <= 0 {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "mid is required")
	}
	query := values(map[string]any{
		"mid": mid, "ps": p.IntDefault("ps", 30), "tid": p.IntDefault("tid", 0),
		"pn": p.IntDefault("pn", 1), "order": p.StringDefault("order", "pubdate"),
		"platform": "web", "web_location": 333.1387, "order_avoided": "true",
		"dm_img_list": "[]", "dm_img_str": randomB64(16), "dm_cover_img_str": randomB64(32),
		"dm_img_inter": `{"ds":[],"wh":[0,0,0],"of":[0,0,0]}`,
	})
	if keyword := p.String("keyword"); keyword != "" {
		query.Set("keyword", keyword)
	}
	if special := p.String("special_type"); special != "" {
		query.Set("special_type", special)
	}
	return s.endpointRequest(ctx, "searchArchive", requestOptions{
		account: accountRef(p),
		query:   query,
		headers: map[string]string{
			"origin":  "https://space.bilibili.com",
			"referer": "https://space.bilibili.com/" + stringify(mid),
		},
	})
}

func (s *Service) userHistory(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	p, rpcErr := jsonrpc.ParseParams(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	query := values(map[string]any{
		"type":    p.StringDefault("type", "archive"),
		"ps":      p.IntDefault("ps", 20),
		"max":     p.IntDefault("max", 0),
		"view_at": p.IntDefault("view_at", 0),
	})
	if business := p.String("business"); business != "" {
		query.Set("business", business)
	}
	return s.endpointQuery(ctx, "historyList", p, query)
}

func (s *Service) userLater(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	p, rpcErr := jsonrpc.ParseParams(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	return s.endpointQuery(ctx, "seeYouLater", p, values(map[string]any{
		"pn": p.IntDefault("pn", 1), "ps": p.IntDefault("ps", 20),
		"viewed": p.IntDefault("viewed", 0), "key": p.StringDefault("key", ""),
		"asc": p.Bool("asc", false), "need_split": "true", "web_location": 333.881,
	}))
}

func (s *Service) userLaterAdd(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	p, rpcErr := jsonrpc.ParseParams(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	form := url.Values{}
	if bvid := p.String("bvid"); bvid != "" {
		form.Set("bvid", bvid)
	}
	if aid, ok := p.ID("aid"); ok && aid > 0 {
		form.Set("aid", stringify(aid))
	}
	if len(form) == 0 {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "bvid or aid is required")
	}
	return s.endpointRequest(ctx, "toViewLater", requestOptions{
		account: accountRef(p),
		method:  http.MethodPost,
		form:    form,
	})
}

func (s *Service) userLaterDel(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	p, rpcErr := jsonrpc.ParseParams(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	aids := joinList(p.Value("aids"))
	if aids == "" {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "aids is required")
	}
	return s.endpointRequest(ctx, "toViewDel", requestOptions{
		account: accountRef(p),
		method:  http.MethodPost,
		form:    url.Values{"resources": {aids}},
	})
}

func (s *Service) userLaterClear(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	p, rpcErr := jsonrpc.ParseParams(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	return s.endpointRequest(ctx, "toViewClear", requestOptions{
		account: accountRef(p),
		method:  http.MethodPost,
		form:    url.Values{"jsonp": {"jsonp"}},
	})
}

func (s *Service) userFollowings(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	return s.userRelationList(ctx, raw, "followings")
}

func (s *Service) userFollowers(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	return s.userRelationList(ctx, raw, "fans")
}

func (s *Service) userRelationList(ctx context.Context, raw json.RawMessage, endpoint string) (any, *jsonrpc.Error) {
	p, rpcErr := jsonrpc.ParseParams(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	mid, ok := p.ID("vmid")
	if !ok || mid <= 0 {
		mid = p.IntDefault("mid", 0)
	}
	if mid <= 0 {
		mid = s.opt.Store.ActiveMid()
	}
	if mid <= 0 {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "vmid is required (no active account)")
	}
	return s.endpointQuery(ctx, endpoint, p, values(map[string]any{
		"vmid": mid, "pn": p.IntDefault("pn", 1), "ps": p.IntDefault("ps", 20),
		"order": "desc", "order_type": p.StringDefault("order_type", ""),
	}))
}

func (s *Service) userFollow(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	p, rpcErr := jsonrpc.ParseParams(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	fid, ok := p.ID("fid")
	if !ok || fid <= 0 {
		fid, ok = p.ID("mid")
	}
	if !ok || fid <= 0 {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "fid is required")
	}
	act := p.IntDefault("act", 0)
	if act == 0 {
		if p.Bool("follow", true) {
			act = 1
		} else {
			act = 2
		}
	}
	return s.endpointRequest(ctx, "relationMod", requestOptions{
		account: accountRef(p),
		method:  http.MethodPost,
		query: values(map[string]any{
			"statistics":             `{"appId":100,"platform":5}`,
			"x-bili-device-req-json": `{"platform":"web","device":"pc","spmid":"333.1387"}`,
		}),
		form: values(map[string]any{
			"fid": fid, "act": act, "re_src": p.IntDefault("re_src", 11),
			"gaia_source": "web_main", "spmid": "333.1387",
			"extend_content": `{"entity":"user","entity_id":` + stringify(fid) + `,"fp":"web"}`,
		}),
	})
}

func (s *Service) userBlacklist(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	p, rpcErr := jsonrpc.ParseParams(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	return s.endpointQuery(ctx, "blackList", p, values(map[string]any{
		"pn": p.IntDefault("pn", 1), "ps": p.IntDefault("ps", 50),
		"re_version": 0, "jsonp": "jsonp",
	}))
}

func (s *Service) userFolders(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	p, rpcErr := jsonrpc.ParseParams(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	mid, ok := p.ID("mid")
	if !ok || mid <= 0 {
		mid = s.opt.Store.ActiveMid()
	}
	if mid <= 0 {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "mid is required (no active account)")
	}
	return s.endpointQuery(ctx, "userFavFolder", p, values(map[string]any{
		"pn": p.IntDefault("pn", 1), "ps": p.IntDefault("ps", 20), "up_mid": mid,
	}))
}

func (s *Service) userSubFolders(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	p, rpcErr := jsonrpc.ParseParams(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	mid, ok := p.ID("mid")
	if !ok || mid <= 0 {
		mid = s.opt.Store.ActiveMid()
	}
	if mid <= 0 {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "mid is required (no active account)")
	}
	return s.endpointQuery(ctx, "userSubFolder", p, values(map[string]any{
		"pn": p.IntDefault("pn", 1), "ps": p.IntDefault("ps", 20),
		"up_mid": mid, "platform": "web",
	}))
}
