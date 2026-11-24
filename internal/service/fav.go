package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"

	"github.com/cloudflypeng/bbcore/internal/jsonrpc"
)

func (s *Service) registerFav() {
	s.add("fav.all_folders", "All created favourite folders (params: mid default self)", s.favAllFolders)
	s.add("fav.folders", "Paged favourite folders (params: mid default self, pn, ps)", s.favFolders)
	s.add("fav.info", "Folder info (params: media_id)", s.favInfo)
	s.add("fav.list", "Folder contents (params: media_id, pn, ps, keyword, order, type)", s.favList)
	s.add("fav.add", "Add resources to folders (params: media_id or add_media_ids, resources)", s.favAdd)
	s.add("fav.remove", "Remove resources from folders (params: media_id or del_media_ids, resources)", s.favRemove)
	s.add("fav.create_folder", "Create a folder (params: title, intro, privacy, cover)", s.favCreateFolder)
	s.add("fav.edit_folder", "Edit a folder (params: media_id, title, intro, privacy, cover)", s.favEditFolder)
	s.add("fav.delete_folder", "Delete folders (params: media_ids)", s.favDeleteFolder)
	s.add("fav.clean", "Empty a folder (params: media_id)", s.favClean)
}

func (s *Service) defaultMid(p jsonrpc.Params) int64 {
	if mid, ok := p.ID("mid"); ok && mid > 0 {
		return mid
	}
	if upMid, ok := p.ID("up_mid"); ok && upMid > 0 {
		return upMid
	}
	return s.opt.Store.ActiveMid()
}

func (s *Service) favAllFolders(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	p, rpcErr := jsonrpc.ParseParams(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	mid := s.defaultMid(p)
	if mid <= 0 {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "mid is required (no active account)")
	}
	return s.endpointQuery(ctx, "favFolder", p, url.Values{"up_mid": {stringify(mid)}})
}

func (s *Service) favFolders(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	p, rpcErr := jsonrpc.ParseParams(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	mid := s.defaultMid(p)
	if mid <= 0 {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "mid is required (no active account)")
	}
	return s.endpointQuery(ctx, "userFavFolder", p, values(map[string]any{
		"pn": p.IntDefault("pn", 1), "ps": p.IntDefault("ps", 20), "up_mid": mid,
	}))
}

func (s *Service) favInfo(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	p, rpcErr := jsonrpc.ParseParams(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	mediaID, ok := p.ID("media_id")
	if !ok || mediaID <= 0 {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "media_id is required")
	}
	return s.endpointQuery(ctx, "favFolderInfo", p, url.Values{"media_id": {stringify(mediaID)}})
}

func (s *Service) favList(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	p, rpcErr := jsonrpc.ParseParams(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	mediaID, ok := p.ID("media_id")
	if !ok || mediaID <= 0 {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "media_id is required")
	}
	return s.endpointQuery(ctx, "favResourceList", p, values(map[string]any{
		"media_id": mediaID, "pn": p.IntDefault("pn", 1), "ps": p.IntDefault("ps", 20),
		"keyword": p.StringDefault("keyword", ""), "order": p.StringDefault("order", "mtime"),
		"type": p.IntDefault("type", 0), "tid": 0, "platform": "web",
	}))
}

func (s *Service) favAdd(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	return s.favBatch(ctx, raw, "add_media_ids", "media_id")
}

func (s *Service) favRemove(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	return s.favBatch(ctx, raw, "del_media_ids", "media_id")
}

func (s *Service) favBatch(ctx context.Context, raw json.RawMessage, idKey, mediaKey string) (any, *jsonrpc.Error) {
	p, rpcErr := jsonrpc.ParseParams(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	resources := joinList(p.Value("resources"))
	if resources == "" {
		resources = joinList(p.Value("aids"))
	}
	if resources == "" {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "resources is required (e.g. \"BV1xx411c7mD:2\")")
	}
	ids := joinList(p.Value(idKey))
	if ids == "" {
		ids = joinList(p.Value(mediaKey))
	}
	if ids == "" {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "%s is required", idKey)
	}
	form := url.Values{"resources": {resources}}
	if idKey == "add_media_ids" {
		form.Set("add_media_ids", ids)
		form.Set("del_media_ids", "")
	} else {
		form.Set("del_media_ids", ids)
		form.Set("add_media_ids", "")
	}
	return s.endpointRequest(ctx, "favVideo", requestOptions{
		account: accountRef(p),
		method:  http.MethodPost,
		form:    form,
	})
}

func (s *Service) favCreateFolder(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	p, rpcErr := jsonrpc.ParseParams(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	title := p.String("title")
	if title == "" {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "title is required")
	}
	return s.endpointRequest(ctx, "addFolder", requestOptions{
		account: accountRef(p),
		method:  http.MethodPost,
		form: url.Values{
			"title":   {title},
			"intro":   {p.StringDefault("intro", "")},
			"privacy": {stringify(p.IntDefault("privacy", 0))},
			"cover":   {p.StringDefault("cover", "")},
		},
	})
}

func (s *Service) favEditFolder(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	p, rpcErr := jsonrpc.ParseParams(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	mediaID, ok := p.ID("media_id")
	if !ok || mediaID <= 0 {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "media_id is required")
	}
	title := p.String("title")
	if title == "" {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "title is required")
	}
	return s.endpointRequest(ctx, "editFolder", requestOptions{
		account: accountRef(p),
		method:  http.MethodPost,
		form: url.Values{
			"media_id": {stringify(mediaID)},
			"title":    {title},
			"intro":    {p.StringDefault("intro", "")},
			"privacy":  {stringify(p.IntDefault("privacy", 0))},
			"cover":    {p.StringDefault("cover", "")},
		},
	})
}

func (s *Service) favDeleteFolder(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	p, rpcErr := jsonrpc.ParseParams(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	ids := joinList(p.Value("media_ids"))
	if ids == "" {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "media_ids is required")
	}
	return s.endpointRequest(ctx, "deleteFolder", requestOptions{
		account: accountRef(p),
		method:  http.MethodPost,
		form:    url.Values{"media_ids": {ids}, "platform": {"web"}},
	})
}

func (s *Service) favClean(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	p, rpcErr := jsonrpc.ParseParams(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	mediaID, ok := p.ID("media_id")
	if !ok || mediaID <= 0 {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "media_id is required")
	}
	return s.endpointRequest(ctx, "cleanFav", requestOptions{
		account: accountRef(p),
		method:  http.MethodPost,
		form:    url.Values{"media_id": {stringify(mediaID)}, "platform": {"web"}},
	})
}
