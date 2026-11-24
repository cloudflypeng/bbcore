package service

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/cloudflypeng/bbcore/internal/bili"
	"github.com/cloudflypeng/bbcore/internal/jsonrpc"
	"github.com/cloudflypeng/bbcore/internal/state"
)

// credential resolves an account reference: "" or "active" for the active
// account, "anon" for the anonymous jar, or a numeric mid.
func (s *Service) credential(ref string) (*bili.Credential, *jsonrpc.Error) {
	switch strings.ToLower(strings.TrimSpace(ref)) {
	case "", "active":
		acc := s.opt.Store.ActiveAccount()
		if acc == nil {
			return s.anonymousCredential(), nil
		}
		return credentialFromAccount(acc), nil
	case "anon", "anonymous", "guest":
		return s.anonymousCredential(), nil
	default:
		mid, err := strconv.ParseInt(strings.TrimSpace(ref), 10, 64)
		if err != nil {
			return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "unknown account %q", ref)
		}
		acc := s.opt.Store.Get(mid)
		if acc == nil {
			return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "account %d not found", mid)
		}
		return credentialFromAccount(acc), nil
	}
}

func (s *Service) anonymousCredential() *bili.Credential {
	cred := bili.AnonymousCredential()
	for k, v := range s.opt.Store.AnonymousCookies() {
		cred.Cookies[k] = v
	}
	return cred
}

func credentialFromAccount(acc *state.Account) *bili.Credential {
	cookies := make(map[string]string, len(acc.Cookies)+1)
	for k, v := range acc.Cookies {
		cookies[k] = v
	}
	if acc.Buvid != "" && cookies["buvid3"] == "" {
		cookies["buvid3"] = acc.Buvid
	}
	return &bili.Credential{
		LoggedIn:  true,
		Mid:       acc.Mid,
		Name:      acc.Name,
		AccessKey: acc.AccessKey,
		Cookies:   cookies,
	}
}

func accountRef(p jsonrpc.Params) string {
	return p.String("account")
}

// ---- request plumbing -------------------------------------------------------

type requestOptions struct {
	account       string
	method        string
	query         url.Values
	form          url.Values
	json          any
	body          []byte
	headers       map[string]string
	sign          string
	noCookies     bool
	skipAccessKey bool
	meta          bool
}

// endpointRequest invokes a named endpoint from the generated registry.
func (s *Service) endpointRequest(ctx context.Context, name string, o requestOptions) (any, *jsonrpc.Error) {
	ep, ok := bili.Endpoints[name]
	if !ok {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "unknown endpoint %q", name)
	}
	spec := bili.Spec{
		Base:          ep.Base,
		Path:          ep.Path,
		Method:        o.method,
		Query:         o.query,
		Form:          o.form,
		JSON:          o.json,
		Body:          o.body,
		Headers:       o.headers,
		Sign:          ep.Sign,
		NoCookies:     o.noCookies,
		SkipAccessKey: o.skipAccessKey,
	}
	if o.sign != "" {
		spec.Sign = o.sign
	}
	return s.rawRequest(ctx, spec, o.account, o.meta)
}

// endpointQuery is a convenience for GET calls with a query.
func (s *Service) endpointQuery(ctx context.Context, name string, p jsonrpc.Params, query url.Values) (any, *jsonrpc.Error) {
	return s.endpointRequest(ctx, name, requestOptions{account: accountRef(p), query: query})
}

func (s *Service) rawRequest(ctx context.Context, spec bili.Spec, account string, meta bool) (any, *jsonrpc.Error) {
	cred, rpcErr := s.credential(account)
	if rpcErr != nil {
		return nil, rpcErr
	}
	if spec.Method == "" {
		if spec.Form != nil || spec.JSON != nil || spec.Body != nil {
			spec.Method = http.MethodPost
		} else {
			spec.Method = http.MethodGet
		}
	}
	spec.Credential = cred
	res, err := s.opt.Client.Do(ctx, spec)
	if err != nil {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInternalError, "%v", err)
	}
	if meta {
		return metaPayload(res), nil
	}
	if res.JSON != nil {
		return res.JSON, nil
	}
	return map[string]any{
		"status":       res.Status,
		"content_type": res.ContentType,
		"body_base64":  base64.StdEncoding.EncodeToString(res.Body),
	}, nil
}

func metaPayload(res *bili.Result) map[string]any {
	out := map[string]any{
		"status":  res.Status,
		"headers": flattenHeader(res.Header),
	}
	if res.ContentType != "" {
		out["content_type"] = res.ContentType
	}
	if res.JSON != nil {
		out["json"] = res.JSON
	}
	if res.JSON == nil {
		out["body_base64"] = base64.StdEncoding.EncodeToString(res.Body)
	}
	return out
}

func flattenHeader(h http.Header) map[string]string {
	out := make(map[string]string, len(h))
	for k, v := range h {
		out[strings.ToLower(k)] = strings.Join(v, ", ")
	}
	return out
}

// defaultSign mirrors the sign policy used when generating the registry.
func defaultSign(base string) string {
	base = strings.ToLower(base)
	switch base {
	case bili.BaseApp:
		return bili.SignApp
	case bili.BaseAPI, "":
		return bili.SignWBI
	case bili.BasePassport, bili.BaseLive, bili.BaseLiveWeb, bili.BaseLiveTrace,
		bili.BaseVC, bili.BaseMessage, bili.BaseWWW, bili.BaseSpace,
		bili.BaseAccount, bili.BaseMall, bili.BaseDynamicShare, bili.BaseOpus:
		return bili.SignNone
	default:
		return bili.SignNone
	}
}

// endpointSign returns the registered sign mode for base+path, else the
// policy default.
func endpointSign(base, path string) string {
	for _, ep := range bili.EndpointList {
		if ep.Base == base && ep.Path == path {
			return ep.Sign
		}
	}
	return defaultSign(base)
}

func stringify(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprint(v)
}
