package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/cloudflypeng/bbcore/internal/bili"
	"github.com/cloudflypeng/bbcore/internal/jsonrpc"
	"github.com/cloudflypeng/bbcore/internal/state"
)

func (s *Service) registerAuth() {
	s.add("auth.qr.generate", "Start TV QR-code login; returns url and auth_code", s.authQRGenerate)
	s.add("auth.qr.poll", "Poll QR login (params: auth_code, make_active); stores the account on success", s.authQRPoll)
	s.add("auth.import_cookies", "Import web cookies and validate them (params: cookies, access_key, refresh_token, make_active)", s.authImportCookies)
	s.add("auth.accounts", "List stored accounts", s.authAccounts)
	s.add("auth.account", "Show one stored account (default: active)", s.authAccount)
	s.add("auth.account.use", "Set the active account (params: mid)", s.authAccountUse)
	s.add("auth.account.remove", "Remove a stored account (params: mid)", s.authAccountRemove)
	s.add("auth.account.info", "Fetch the live profile (nav) for an account", s.authAccountInfo)
	s.add("auth.logout", "Log out upstream and remove the account", s.authLogout)
}

func (s *Service) authQRGenerate(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	query := url.Values{
		"local_id": {"0"},
		"platform": {"android"},
		"mobi_app": {"android_hd"},
	}
	result, err := s.endpointRequest(ctx, "getTVCode", requestOptions{
		account:   "anonymous",
		method:    http.MethodPost,
		query:     query,
		noCookies: true,
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func (s *Service) authQRPoll(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	p, rpcErr := jsonrpc.ParseParams(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	authCode := p.String("auth_code")
	if authCode == "" {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "auth_code is required")
	}
	result, err := s.endpointRequest(ctx, "qrcodePoll", requestOptions{
		account:   "anonymous",
		method:    http.MethodPost,
		query:     url.Values{"auth_code": {authCode}, "local_id": {"0"}},
		noCookies: true,
	})
	if err != nil {
		return nil, err
	}
	biliJSON, _ := result.(map[string]any)
	if numberField(biliJSON, "code") != 0 {
		return biliJSON, nil
	}
	data, _ := biliJSON["data"].(map[string]any)
	cookies := cookiesFromLoginData(data)
	accessKey := stringField(data, "access_token")
	refreshToken := stringField(data, "refresh_token")
	mid := toInt64Map(cookies, "DedeUserID")
	if mid == 0 {
		mid = numberField(data, "mid")
	}
	if mid == 0 {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInternalError, "login succeeded but no mid was returned")
	}
	acc, saveErr := s.saveLogin(ctx, mid, cookies, accessKey, refreshToken, p.Bool("make_active", true))
	if saveErr != nil {
		return nil, saveErr
	}
	payload := map[string]any{
		"code":    0,
		"message": "0",
		"data":    biliJSON,
		"account": acc,
	}
	return payload, nil
}

func (s *Service) authImportCookies(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	p, rpcErr := jsonrpc.ParseParams(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	cookies := parseCookies(p)
	if len(cookies) == 0 {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "cookies are required (string or object)")
	}
	cred := &bili.Credential{
		LoggedIn:  true,
		AccessKey: p.String("access_key"),
		Cookies:   cookies,
	}
	if mid, err := strconv.ParseInt(p.String("mid"), 10, 64); err == nil && mid > 0 {
		cred.Mid = mid
	}
	check, err := s.opt.Client.Do(ctx, bili.Spec{
		Base:       bili.BaseAPI,
		Method:     http.MethodGet,
		Path:       bili.MemberAccountEndpoint,
		Sign:       bili.SignNone,
		Credential: cred,
	})
	if err != nil {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInternalError, "validate cookies: %v", err)
	}
	if check.BiliCode() != 0 {
		return nil, &jsonrpc.Error{
			Code:    jsonrpc.CodeInvalidParams,
			Message: fmt.Sprintf("cookies rejected: %s (code %d)", check.BiliMessage(), check.BiliCode()),
			Data:    check.JSON,
		}
	}
	profile := s.fetchProfile(ctx, cred)
	mid := cred.Mid
	if profile != nil {
		if id := numberField(profile, "mid"); id != 0 {
			mid = id
		}
	}
	if mid == 0 {
		mid = toInt64Map(cookies, "DedeUserID")
	}
	if mid == 0 {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInternalError, "could not determine account mid")
	}
	cred.Mid = mid
	if cookies["buvid3"] == "" {
		if buvid := s.opt.Store.AnonymousCookies()["buvid3"]; buvid != "" {
			cookies["buvid3"] = buvid
		}
	}
	acc, saveErr := s.saveLogin(ctx, mid, cookies, p.String("access_key"), p.String("refresh_token"), p.Bool("make_active", true))
	if saveErr != nil {
		return nil, saveErr
	}
	return map[string]any{
		"account": acc,
		"profile": profile,
	}, nil
}

// saveLogin persists a freshly authenticated account and optionally activates it.
func (s *Service) saveLogin(ctx context.Context, mid int64, cookies map[string]string, accessKey, refreshToken string, makeActive bool) (map[string]any, *jsonrpc.Error) {
	cred := &bili.Credential{LoggedIn: true, Mid: mid, AccessKey: accessKey, Cookies: cookies}
	profile := s.fetchProfile(ctx, cred)
	name, avatar := "", ""
	if profile != nil {
		name = stringField(profile, "uname")
		avatar = stringField(profile, "face")
	}
	now := time.Now().Unix()
	addedAt := now
	if existing := s.opt.Store.Get(mid); existing != nil && existing.AddedAt != 0 {
		addedAt = existing.AddedAt
	}
	if accessKey == "" {
		if existing := s.opt.Store.Get(mid); existing != nil {
			accessKey = existing.AccessKey
			if refreshToken == "" {
				refreshToken = existing.RefreshToken
			}
		}
	}
	buvid := cookies["buvid3"]
	if buvid == "" {
		buvid = s.opt.Store.AnonymousCookies()["buvid3"]
	}
	acc := &state.Account{
		Mid:          mid,
		Name:         name,
		Avatar:       avatar,
		Cookies:      cookies,
		AccessKey:    accessKey,
		RefreshToken: refreshToken,
		Buvid:        buvid,
		AddedAt:      addedAt,
		CheckedAt:    now,
	}
	if err := s.opt.Store.Upsert(acc); err != nil {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInternalError, "save account: %v", err)
	}
	if makeActive {
		if err := s.opt.Store.SetActive(mid); err != nil {
			return nil, jsonrpc.Errorf(jsonrpc.CodeInternalError, "activate account: %v", err)
		}
	}
	s.Notify("event.account.login", map[string]any{"mid": mid, "name": name})
	return s.accountView(acc), nil
}

func (s *Service) fetchProfile(ctx context.Context, cred *bili.Credential) map[string]any {
	result, err := s.opt.Client.Do(ctx, bili.Spec{
		Base:       bili.BaseAPI,
		Method:     http.MethodGet,
		Path:       bili.NavEndpoint,
		Sign:       bili.SignNone,
		Credential: cred,
	})
	if err != nil || result.BiliCode() != 0 {
		return nil
	}
	data, _ := result.JSONMap()["data"].(map[string]any)
	return data
}

func (s *Service) authAccounts(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	active := s.opt.Store.ActiveMid()
	accounts := s.opt.Store.List()
	out := make([]map[string]any, 0, len(accounts))
	for _, acc := range accounts {
		view := s.accountView(acc)
		view["active"] = acc.Mid == active
		out = append(out, view)
	}
	return map[string]any{"active_mid": active, "accounts": out}, nil
}

func (s *Service) authAccount(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	p, rpcErr := jsonrpc.ParseParams(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	acc, err := s.resolveAccount(accountRef(p))
	if err != nil {
		return nil, err
	}
	view := s.accountView(acc)
	view["active"] = acc.Mid == s.opt.Store.ActiveMid()
	return view, nil
}

func (s *Service) authAccountUse(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	p, rpcErr := jsonrpc.ParseParams(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	mid, ok := p.ID("mid")
	if !ok || mid <= 0 {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "mid is required")
	}
	if s.opt.Store.Get(mid) == nil {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "account %d not found", mid)
	}
	if err := s.opt.Store.SetActive(mid); err != nil {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInternalError, "%v", err)
	}
	return map[string]any{"active_mid": mid}, nil
}

func (s *Service) authAccountRemove(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	p, rpcErr := jsonrpc.ParseParams(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	mid, ok := p.ID("mid")
	if !ok || mid <= 0 {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "mid is required")
	}
	if s.opt.Store.Get(mid) == nil {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "account %d not found", mid)
	}
	if err := s.opt.Store.Delete(mid); err != nil {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInternalError, "%v", err)
	}
	s.Notify("event.account.removed", map[string]any{"mid": mid})
	return map[string]any{"removed": mid, "active_mid": s.opt.Store.ActiveMid()}, nil
}

func (s *Service) authAccountInfo(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	p, rpcErr := jsonrpc.ParseParams(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	cred, err := s.credential(accountRef(p))
	if err != nil {
		return nil, err
	}
	result, callErr := s.opt.Client.Do(ctx, bili.Spec{
		Base:       bili.BaseAPI,
		Method:     http.MethodGet,
		Path:       bili.NavEndpoint,
		Sign:       bili.SignNone,
		Credential: cred,
	})
	if callErr != nil {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInternalError, "%v", callErr)
	}
	return result.JSON, nil
}

func (s *Service) authLogout(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	p, rpcErr := jsonrpc.ParseParams(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	acc, err := s.resolveAccount(accountRef(p))
	if err != nil {
		return nil, err
	}
	cred := credentialFromAccount(acc)
	if cred.CSRF() != "" {
		_, _ = s.opt.Client.Do(ctx, bili.Spec{
			Base:       bili.BasePassport,
			Method:     http.MethodPost,
			Path:       bili.Endpoints["logout"].Path,
			Form:       url.Values{"biliCSRF": {cred.CSRF()}},
			Sign:       bili.SignNone,
			Credential: cred,
		})
	}
	if delErr := s.opt.Store.Delete(acc.Mid); delErr != nil {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInternalError, "%v", delErr)
	}
	s.Notify("event.account.logout", map[string]any{"mid": acc.Mid})
	return map[string]any{
		"removed":    acc.Mid,
		"active_mid": s.opt.Store.ActiveMid(),
	}, nil
}

func (s *Service) resolveAccount(ref string) (*state.Account, *jsonrpc.Error) {
	switch strings.ToLower(strings.TrimSpace(ref)) {
	case "", "active":
		acc := s.opt.Store.ActiveAccount()
		if acc == nil {
			return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "no active account is logged in")
		}
		return acc, nil
	default:
		mid, err := strconv.ParseInt(strings.TrimSpace(ref), 10, 64)
		if err != nil {
			return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "unknown account %q", ref)
		}
		acc := s.opt.Store.Get(mid)
		if acc == nil {
			return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "account %d not found", mid)
		}
		return acc, nil
	}
}

func (s *Service) accountView(acc *state.Account) map[string]any {
	return map[string]any{
		"mid":            acc.Mid,
		"name":           acc.Name,
		"avatar":         acc.Avatar,
		"added_at":       acc.AddedAt,
		"checked_at":     acc.CheckedAt,
		"has_access_key": acc.AccessKey != "",
		"cookies":        len(acc.Cookies),
	}
}

// ---- cookie parsing ---------------------------------------------------------

func parseCookies(p jsonrpc.Params) map[string]string {
	out := map[string]string{}
	if m := p.Map("cookies"); len(m) > 0 {
		for k, v := range m {
			if v != nil {
				out[strings.TrimSpace(k)] = stringify(v)
			}
		}
	}
	text := p.String("cookies")
	if m := p.Map("cookie"); len(m) > 0 && len(out) == 0 {
		for k, v := range m {
			if v != nil {
				out[strings.TrimSpace(k)] = stringify(v)
			}
		}
	}
	if text == "" {
		text = p.String("cookie")
	}
	if text != "" && len(out) == 0 {
		for _, part := range strings.FieldsFunc(text, func(r rune) bool { return r == ';' || r == '\n' }) {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			key, value, found := strings.Cut(part, "=")
			if !found {
				continue
			}
			out[strings.TrimSpace(key)] = strings.TrimSpace(value)
		}
	}
	return out
}

func cookiesFromLoginData(data map[string]any) map[string]string {
	out := map[string]string{}
	if data == nil {
		return out
	}
	info, _ := data["cookie_info"].(map[string]any)
	list, _ := info["cookies"].([]any)
	for _, item := range list {
		cookie, _ := item.(map[string]any)
		name := stringField(cookie, "name")
		if name == "" {
			continue
		}
		out[name] = stringField(cookie, "value")
	}
	return out
}
