package bili

import "time"

// Base names used by the endpoint registry and Spec.Base.
const (
	BaseWWW          = "www"
	BaseAPI          = "api"
	BaseVC           = "vc"
	BaseApp          = "app"
	BaseLive         = "live"
	BaseLiveTrace    = "live-trace"
	BaseLiveWeb      = "live-web"
	BasePassport     = "passport"
	BaseMessage      = "message"
	BaseDynamicShare = "dynamic-share"
	BaseOpus         = "opus"
	BaseSpace        = "space"
	BaseAccount      = "account"
	BaseMall         = "mall"
	BaseSponsorBlock = "sponsor-block"
)

var baseURLs = map[string]string{
	BaseWWW:          "https://www.bilibili.com",
	BaseAPI:          "https://api.bilibili.com",
	BaseVC:           "https://api.vc.bilibili.com",
	BaseApp:          "https://app.bilibili.com",
	BaseLive:         "https://api.live.bilibili.com",
	BaseLiveTrace:    "https://live-trace.bilibili.com",
	BaseLiveWeb:      "https://live.bilibili.com",
	BasePassport:     "https://passport.bilibili.com",
	BaseMessage:      "https://message.bilibili.com",
	BaseDynamicShare: "https://t.bilibili.com",
	BaseOpus:         "https://www.bilibili.com/opus",
	BaseSpace:        "https://space.bilibili.com",
	BaseAccount:      "https://account.bilibili.com",
	BaseMall:         "https://mall.bilibili.com",
	BaseSponsorBlock: "https://www.bsbsb.top",
}

// Public client identifiers mirrored from PiliPlus common/constants.dart.
// These are part of the open-source client, not user credentials; account
// cookies and access keys are never compiled in.
const (
	AppKey    = "dfca71928277209b"
	AppSec    = "b5475a8825547a4fc26c7d518eaaa02e"
	AuroraKey = "ad1va46a7lza"

	UserAgent    = "Mozilla/5.0 BiliDroid/2.0.1 (bbcallen@gmail.com) os/android model/android_hd mobi_app/android_hd build/2001100 channel/master innerVer/2001100 osVer/15 network/2"
	UserAgentApp = "Mozilla/5.0 BiliDroid/8.43.0 (bbcallen@gmail.com) os/android model/android mobi_app/android build/8430300 channel/master innerVer/8430300 osVer/15 network/2"

	Statistics    = `{"appId":5,"platform":3,"version":"2.0.1","abtest":""}`
	StatisticsApp = `{"appId":1,"platform":3,"version":"8.43.0","abtest":""}`

	appKeyHdr  = "android64"
	auroraZone = "sh001"

	// app-side metadata used by gRPC headers (lib/grpc + utils/accounts/grpc_headers.dart).
	GrpcBuild       = "2001100"
	GrpcVersionName = "2.0.1"
	GrpcChannel     = "master"
	GrpcMobiApp     = "android_hd"
	GrpcDevice      = "android"
	GrpcOSVersion   = "15"
	GrpcAppID       = 5
	TraceIDDefault  = "11111111111111111111111111111111:1111111111111111:0:0"
)

// Dynamic feed features copied from PiliPlus common/constants.dart. The web
// dynamic endpoints return empty payloads without them.
const (
	DynFeatures        = "itemOpusStyle,listOnlyfans,onlyfansQaCard"
	DynDetailFeatures  = "itemOpusStyle,opusBigCover,onlyfansVote,endFooterHidden,decorationCard,onlyfansAssetsV2,ugcDelete,onlyfansQaCard,editable,opusPrivateVisible,avatarAutoTheme,sunflowerStyle,cardsEnhance,eva3CardOpus,eva3CardVideo,eva3CardComment,eva3CardVote,eva3CardUser"
	OpusDetailFeatures = "onlyfansVote,onlyfansAssetsV2,onlyfansOpusCard,decorationCard,htmlNewStyle,ugcDelete,editable,opusPrivateVisible,opusTribeeApprovedThread,opusTribeeRejectThread,opusTribeeFeatured,opusTribeeModifyCategory,opusTribeeRemoveThread,opusTribeeCoin,tribeeEdit,avatarAutoTheme,avatarTypeOpus,sunflowerStyle,articleEnhance,cardsEnhance,eva3CardOpus,eva3CardVideo,eva3CardComment,eva3CardVote,eva3CardUser"
)

// MixinKeyEncTab is the WBI permutation table from PiliPlus utils/wbi_sign.dart.
var MixinKeyEncTab = []int{
	46, 47, 18, 2, 53, 8, 23, 32, 15, 50, 10, 31, 58, 3, 45, 35, 27, 43, 5, 49,
	33, 9, 42, 19, 29, 28, 14, 39, 12, 38, 41, 13,
}

const (
	// AnonymousBuvidEndpoint bootstraps buvid3/buvid4 cookies.
	AnonymousBuvidEndpoint = "/x/frontend/finger/spi"
	// NavEndpoint returns wbi_img and login state.
	NavEndpoint = "/x/web-interface/nav"
	// MemberAccountEndpoint validates a web cookie.
	MemberAccountEndpoint = "/x/member/web/account"

	defaultHTTPTimeout = 30 * time.Second
	maxResponseBytes   = 64 << 20
)
