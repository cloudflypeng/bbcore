package service

import (
	"context"
	"crypto/md5"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/cloudflypeng/bbcore/internal/bili"
	"github.com/cloudflypeng/bbcore/internal/jsonrpc"
)

func (s *Service) registerAuthSMS() {
	s.add("auth.sms.send", "Send an SMS login code (params: tel, cid, gee_challenge, gee_validate, gee_seccode, recaptcha_token)", s.authSMSSend)
	s.add("auth.sms.login", "Log in with phone + SMS code (params: tel, cid, code, captcha_key); stores the account on success", s.authSMSLogin)
}

func (s *Service) authSMSSend(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	p, rpcErr := jsonrpc.ParseParams(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	tel := digitsOnly(p.String("tel"))
	if len(tel) < 5 {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "tel is required")
	}
	cid := digitsOnly(p.String("cid"))
	if cid == "" {
		cid = "86"
	}
	buvid := s.opt.Store.AnonymousCookies()["buvid3"]
	if buvid == "" {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInternalError, "anonymous buvid is not ready")
	}
	now := time.Now()
	form := url.Values{
		"build":             {"2001100"},
		"buvid":             {buvid},
		"c_locale":          {"zh_CN"},
		"channel":           {"master"},
		"cid":               {cid},
		"disable_rcmd":      {"0"},
		"local_id":          {buvid},
		"login_session_id":  {md5Hex(buvid + strconv.FormatInt(now.UnixMilli(), 10))},
		"mobi_app":          {"android_hd"},
		"platform":          {"android"},
		"s_locale":          {"zh_CN"},
		"statistics":        {bili.StatisticsApp},
		"tel":               {tel},
		"ts":                {strconv.FormatInt(now.Unix(), 10)},
	}
	if v := p.String("gee_challenge"); v != "" {
		form.Set("gee_challenge", v)
	}
	if v := p.String("gee_validate"); v != "" {
		form.Set("gee_validate", v)
	}
	if v := p.String("gee_seccode"); v != "" {
		form.Set("gee_seccode", v)
	}
	if v := p.String("recaptcha_token"); v != "" {
		form.Set("recaptcha_token", v)
	}
	spec := bili.Endpoints["appSmsCode"]
	result, err := s.opt.Client.Do(ctx, bili.Spec{
		Base:       bili.BasePassport,
		Method:     http.MethodPost,
		Path:       spec.Path,
		Form:       form,
		Sign:       bili.SignApp,
		Credential: &bili.Credential{},
	})
	if err != nil {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInternalError, "send sms: %v", err)
	}
	return result.JSON, nil
}

func (s *Service) authSMSLogin(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	p, rpcErr := jsonrpc.ParseParams(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	tel := digitsOnly(p.String("tel"))
	code := digitsOnly(p.String("code"))
	captchaKey := p.String("captcha_key")
	if len(tel) < 5 || code == "" || captchaKey == "" {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "tel, code and captcha_key are required")
	}
	cid := digitsOnly(p.String("cid"))
	if cid == "" {
		cid = "86"
	}
	buvid := s.opt.Store.AnonymousCookies()["buvid3"]
	if buvid == "" {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInternalError, "anonymous buvid is not ready")
	}

	pub, keyErr := s.passportPublicKey(ctx)
	if keyErr != nil {
		return nil, keyErr
	}
	plain := randomASCII(16)
	encrypted, err := rsa.EncryptPKCS1v15(rand.Reader, pub, []byte(plain))
	if err != nil {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInternalError, "encrypt device token: %v", err)
	}
	dt := url.QueryEscape(base64.StdEncoding.EncodeToString(encrypted))
	deviceID := md5Hex(randomASCII(24))

	form := url.Values{
		"bili_local_id":   {deviceID},
		"build":           {"2001100"},
		"buvid":           {buvid},
		"c_locale":        {"zh_CN"},
		"captcha_key":     {captchaKey},
		"channel":         {"master"},
		"cid":             {cid},
		"code":            {code},
		"device":          {"phone"},
		"device_id":       {deviceID},
		"device_name":     {"vivo"},
		"device_platform": {"Android14vivo"},
		"disable_rcmd":    {"0"},
		"dt":              {dt},
		"from_pv":         {"main.my-information.my-login.0.click"},
		"from_url":        {url.QueryEscape("bilibili://user_center/mine")},
		"local_id":        {buvid},
		"mobi_app":        {"android_hd"},
		"platform":        {"android"},
		"s_locale":        {"zh_CN"},
		"statistics":      {bili.StatisticsApp},
		"tel":             {tel},
	}
	spec := bili.Endpoints["logInByAppSms"]
	result, err := s.opt.Client.Do(ctx, bili.Spec{
		Base:       bili.BasePassport,
		Method:     http.MethodPost,
		Path:       spec.Path,
		Form:       form,
		Sign:       bili.SignApp,
		Credential: &bili.Credential{},
	})
	if err != nil {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInternalError, "sms login: %v", err)
	}
	biliJSON, _ := result.JSON.(map[string]any)
	if numberField(biliJSON, "code") != 0 {
		return biliJSON, nil
	}
	data, _ := biliJSON["data"].(map[string]any)
	tokenInfo, _ := data["token_info"].(map[string]any)
	cookies := cookiesFromLoginData(data)
	mid := toInt64Map(cookies, "DedeUserID")
	if mid == 0 {
		mid = numberField(tokenInfo, "mid")
	}
	if mid == 0 {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInternalError, "login succeeded but no mid was returned")
	}
	acc, saveErr := s.saveLogin(ctx, mid, cookies,
		stringField(tokenInfo, "access_token"),
		stringField(tokenInfo, "refresh_token"),
		p.Bool("make_active", true))
	if saveErr != nil {
		return nil, saveErr
	}
	return map[string]any{
		"code":    0,
		"message": "0",
		"data":    biliJSON,
		"account": acc,
	}, nil
}

func (s *Service) passportPublicKey(ctx context.Context) (*rsa.PublicKey, *jsonrpc.Error) {
	spec := bili.Endpoints["getWebKey"]
	result, err := s.opt.Client.Do(ctx, bili.Spec{
		Base:   bili.BasePassport,
		Method: http.MethodGet,
		Path:   spec.Path,
		Sign:   bili.SignNone,
	})
	if err != nil {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInternalError, "fetch passport key: %v", err)
	}
	m, _ := result.JSON.(map[string]any)
	if numberField(m, "code") != 0 {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInternalError, "fetch passport key: %s", stringField(m, "message"))
	}
	data, _ := m["data"].(map[string]any)
	key := stringField(data, "key")
	block, _ := pem.Decode([]byte(key))
	if block == nil {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInternalError, "passport key is not PEM")
	}
	if parsed, err := x509.ParsePKIXPublicKey(block.Bytes); err == nil {
		if pub, ok := parsed.(*rsa.PublicKey); ok {
			return pub, nil
		}
	}
	pub, err := x509.ParsePKCS1PublicKey(block.Bytes)
	if err != nil {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInternalError, "parse passport key: %v", err)
	}
	return pub, nil
}

func digitsOnly(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func md5Hex(s string) string {
	sum := md5.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}

func randomASCII(n int) string {
	const charset = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		for i := range buf {
			buf[i] = charset[i%len(charset)]
		}
		return string(buf)
	}
	for i := range buf {
		buf[i] = charset[int(buf[i])%len(charset)]
	}
	return string(buf)
}
