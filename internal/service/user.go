package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

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
	s.add("user.follow_tags", "Following groups with counts", s.userFollowTags)
	s.add("user.follow_tag_members", "Following group members (params: tagid, pn, ps, order_type)", s.userFollowTagMembers)
	s.add("user.follow_search", "Search followings (params: name, pn, ps)", s.userFollowSearch)
	s.add("user.follow", "Follow/unfollow (params: fid, follow)", s.userFollow)
	s.add("user.relation", "Relationship with a user (params: mid or fid)", s.userRelation)
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
	if err != nil {
		return result, err
	}
	if upstreamCode(result) == 0 {
		return result, nil
	}
	// The space info endpoint is periodically challenged by risk control even
	// when the card endpoint succeeds; synthesize a profile-shaped response.
	card, cardErr := s.endpointRequest(ctx, "memberCardInfo", requestOptions{
		account: accountRef(p),
		query:   url.Values{"mid": {stringify(mid)}, "photo": {"false"}},
	})
	if cardErr != nil {
		return result, nil
	}
	if info, ok := memberInfoFromCard(mid, card); ok {
		return info, nil
	}
	return result, nil
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
	result, rpcErr := s.endpointRequest(ctx, "searchArchive", requestOptions{
		account: accountRef(p),
		query:   query,
		headers: map[string]string{
			"origin":  "https://space.bilibili.com",
			"referer": "https://space.bilibili.com/" + stringify(mid),
		},
	})
	if rpcErr != nil {
		return nil, rpcErr
	}
	if upstreamCode(result) == 0 || p.IntDefault("pn", 1) > 1 ||
		p.String("keyword") != "" || p.String("special_type") != "" {
		return result, nil
	}
	// The web archive list is sometimes risk-controlled for anonymous users.
	// The app endpoint serves the first page without that challenge.
	fallback, fallbackErr := s.endpointRequest(ctx, "spaceArchive", requestOptions{
		account: accountRef(p),
		query: values(map[string]any{
			"vmid": mid, "ps": p.IntDefault("ps", 30),
			"order": p.StringDefault("order", "pubdate"), "type": "all",
		}),
	})
	if fallbackErr != nil {
		return result, nil
	}
	if normalized, ok := normalizeSpaceArchive(mid, fallback); ok {
		return normalized, nil
	}
	return result, nil
}

func upstreamCode(result any) int64 {
	root, ok := result.(map[string]any)
	if !ok {
		return -1
	}
	code, ok := root["code"]
	if !ok {
		return -1
	}
	return toInt64(code)
}

func memberInfoFromCard(mid int64, result any) (any, bool) {
	root, ok := result.(map[string]any)
	if !ok || toInt64(root["code"]) != 0 {
		return nil, false
	}
	data, _ := root["data"].(map[string]any)
	card, _ := data["card"].(map[string]any)
	name := stringField(card, "name")
	if name == "" {
		return nil, false
	}
	levelInfo, _ := card["level_info"].(map[string]any)
	info := map[string]any{
		"mid":       mid,
		"name":      name,
		"face":      stringField(card, "face"),
		"sign":      stringField(card, "sign"),
		"level":     numberField(levelInfo, "current_level"),
		"top_photo": "",
		"fans":      numberField(card, "fans"),
		"attention": numberField(card, "attention"),
	}
	return map[string]any{
		"code": 0, "message": "0", "ttl": 1, "data": info,
	}, true
}

func normalizeSpaceArchive(mid int64, result any) (any, bool) {
	root, ok := result.(map[string]any)
	if !ok || toInt64(root["code"]) != 0 {
		return nil, false
	}
	data, _ := root["data"].(map[string]any)
	items, _ := data["item"].([]any)
	vlist := make([]any, 0, len(items))
	for _, raw := range items {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		bvid := stringField(item, "bvid")
		if bvid == "" {
			continue
		}
		vlist = append(vlist, map[string]any{
			"aid":          numberField(item, "param"),
			"bvid":         bvid,
			"title":        stringField(item, "title"),
			"pic":          stringField(item, "cover"),
			"author":       stringField(item, "author"),
			"length":       formatLength(numberField(item, "duration")),
			"description":  stringField(item, "subtitle"),
			"play":         numberField(item, "play"),
			"video_review": numberField(item, "danmaku"),
			"created":      numberField(item, "ctime"),
			"mid":          mid,
		})
	}
	if len(vlist) == 0 {
		return nil, false
	}
	return map[string]any{
		"code": 0, "message": "0", "ttl": 1,
		"data": map[string]any{
			"list": map[string]any{"vlist": vlist, "tlist": []any{}, "slist": []any{}},
			"page": map[string]any{"pn": 1, "ps": len(vlist), "count": numberField(data, "count")},
		},
	}, true
}

func formatLength(seconds int64) string {
	if seconds <= 0 {
		return ""
	}
	if seconds >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d", seconds/3600, seconds%3600/60, seconds%60)
	}
	return fmt.Sprintf("%d:%02d", seconds/60, seconds%60)
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

func (s *Service) userFollowTags(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	p, rpcErr := jsonrpc.ParseParams(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	return s.endpointRequest(ctx, "followUpTag", requestOptions{account: accountRef(p)})
}

func (s *Service) userFollowTagMembers(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	p, rpcErr := jsonrpc.ParseParams(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	tagID, ok := p.ID("tagid")
	if !ok {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "tagid is required")
	}
	return s.endpointQuery(ctx, "followUpGroup", p, values(map[string]any{
		"tagid": tagID, "pn": p.IntDefault("pn", 1), "ps": p.IntDefault("ps", 20),
		"order_type": p.StringDefault("order_type", ""),
	}))
}

func (s *Service) userFollowSearch(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	p, rpcErr := jsonrpc.ParseParams(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	name := strings.TrimSpace(p.String("name"))
	if name == "" {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "name is required")
	}
	mid := p.IntDefault("vmid", 0)
	if mid <= 0 {
		mid = s.opt.Store.ActiveMid()
	}
	if mid <= 0 {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "vmid is required (no active account)")
	}
	return s.endpointQuery(ctx, "followSearch", p, values(map[string]any{
		"vmid": mid, "name": name,
		"pn": p.IntDefault("pn", 1), "ps": p.IntDefault("ps", 20),
	}))
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

func (s *Service) userRelation(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	p, rpcErr := jsonrpc.ParseParams(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	mid, ok := p.ID("mid")
	if !ok || mid <= 0 {
		mid, ok = p.ID("fid")
	}
	if !ok || mid <= 0 {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "mid is required")
	}
	return s.endpointQuery(ctx, "relation", p, url.Values{"fid": {stringify(mid)}})
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
