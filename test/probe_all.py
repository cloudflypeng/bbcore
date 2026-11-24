#!/usr/bin/env python3
"""Probe every bbcore raw endpoint and semantic method.

Read endpoints are called with the active account. Increment-style writes are
called with the anonymous credential (the upstream rejects them, so account
state is untouched). Destructive/content-creating/login/reporting endpoints are
never called; they are listed as skipped.

Usage:
  python3 test/probe_all.py --bin ./bbcore --data-dir ~/.bbcore
  python3 test/probe_all.py --phase raw|semantic --throttle 0.35 --mid 123

No account or fixture data is embedded: the default data directory is
./.bbcore-probe, and the user mid is taken from --mid, the active account, or
the uploader of a hot video.
"""
import argparse
import base64
import json
import os
import queue
import re
import subprocess
import sys
import threading
import time

ROOM = 6
KEYWORD = "初音未来"
APP_STAT = '{"appId":1,"platform":3,"version":"8.43.0","abtest":""}'
APP_COMMON = {
    "build": 8430300, "version": "8.43.0", "c_locale": "zh_CN",
    "channel": "master", "mobi_app": "android", "platform": "android",
    "s_locale": "zh_CN", "statistics": APP_STAT,
}
VC_COMMON = {"build": 0, "mobi_app": "web"}


def app_query(**extra):
    q = dict(APP_COMMON)
    q.update(extra)
    return q

# Never called: login/account security, SMS, uploads, reports, moderation,
# content creation/editing, and destructive mutations (delete/clear/move/...).
SKIP = {
    "_liveTrace", "activateBuvidApi", "gaiaVgateRegister", "gaiaVgateValidate",
    "appSmsCode", "smsCode", "safeCenterSmsCode", "safeCenterSmsVerify",
    "safeCenterGetInfo", "preCapture", "getWebKey",
    "logInByAppSms", "logInByWebPwd", "loginByPwdApi", "oauth2AccessToken",
    "getTVCode", "qrcodePoll", "qrcodeConfirm", "logout",
    "uploadBfs", "uploadImage",
    "danmakuReport", "danmakuRecall", "liveDmReport", "replyReport",
    "dynamicReport", "reportMember", "superChatReport", "imMsgReport",
    "addShieldKeyword", "delShieldKeyword", "liveSetSilent", "liveShieldUser",
    "spaceSettingMod", "setMsgDnd", "setPushSs", "pauseHistory",
    "shootDanmaku", "sendLiveMsg", "sendMsg", "replyAdd",
    "createDynamic", "createTextDynamic", "createVote", "addNote",
    "pgcReviewPost", "pgcReviewMod", "pgcReviewDel", "pgcReviewLike",
    "pgcReviewDislike",
    "addFavArticle", "addFavPugv", "addFavTopic", "addFolder", "cleanFav",
    "copyFav", "copyToview", "delFavArticle", "delFavPugv", "delFavTopic",
    "deleteFolder", "editFolder", "favFavFolder", "favSeason", "favVideo",
    "moveFav", "moveToview", "sortFav", "sortFavFolder", "unfavAll",
    "unfavFavFolder", "unfavFolder", "unfavSeason", "toViewClear",
    "toViewDel", "toViewLater", "clearHistory", "delHistory", "delMsgfeed",
    "delSysMsg", "delNote", "delPublishNote", "addSpecial", "delSpecial",
    "addUsers", "addShieldKeyword", "createFollowTag", "delFollowTag",
    "updateFollowTag", "sortFollowTag", "createReserve", "updateReserve",
    "spaceReserveCancel", "dynPrivatePubSetting", "editDyn", "removeDynamic",
    "removeMsg", "ackSessionMsg", "setTop", "setTopDyn", "rmTopDyn",
    "msgSetNotice", "msgSysUpdateCursor", "msgFeedLike", "liveEditPlugs",
    "setLiveFavTag", "doVote", "updateVote", "danmakuEditState",
    "danmakuFilterAdd", "danmakuFilterDel", "communityAction", "dynReserve",
    # POST-only mutations that used to be probed as GET (HTTP 405).
    "mediaListHistory", "pgcUpdate", "replyDel", "replySubjectModify",
    "replyTop", "spaceReserve",
    # Anonymous calls to these are accepted upstream, so they would create
    # real counters/history entries instead of being rejected.
    "heartBeat", "roomEntryAction",
}

# No usable fixture for these in this account/session, so they are not called.
# (Their plumbing is identical to siblings that are exercised.)
FIXTURE_SKIP = {
    "bubble", "matchInfo", "reserveInfo", "voteInfo", "followeeVotes",
    "bgmDetail", "bgmRecommend", "wishUpdate",
    "spaceSeason", "spaceSeries", "seasonArchives", "seriesArchives",
    "pugvInfo", "pugvUrl",
}

DYN_FEATURES = "itemOpusStyle,listOnlyfans,onlyfansQaCard"

# Endpoints whose registry sign mode is overridden at call time.
SIGN_APP = {"liveFeedIndex", "liveSecondList", "liveSearch", "liveFeedback",
            "getLiveFavTag", "liveAreaList", "liveRoomAreaList", "tvPlayUrl",
            "userRealName"}
SIGN_WBI = {"liveContributionRank", "liveRoomDmToken"}

# Read endpoints that expect a JSON/form body (HTTP method POST).
POST_JSON = {"spaceShop"}

# Write endpoints called with account=anon; upstream must reject them.
WRITE_APP = {"coinVideo", "dislikeVideo", "feedDislike",
             "feedDislikeCancel", "likeVideo"}
WRITE_GET = {"liveFeedback"}

# Called with account=anon as a plumbing check; upstream must reject them.
WRITE_ANON = {
    "coinVideo", "dislikeVideo", "feedDislike", "feedDislikeCancel",
    "hateReply", "likeReply", "likeTopic", "likeVideo",
    "pgcTriple", "ugcTriple", "relationMod", "pgcAdd", "pgcDel",
    "thumbDynamic", "danmakuLike", "historyReport",
    "liveLikeReport", "vipExpAdd", "wishUpdate",
}


def build_specs(ctx):
    mid, season, ep = ctx["mid"], ctx["season"], ctx["ep"]
    app = app_query
    return {
        "videoIntro": {"bvid": ctx["bvid"]},
        "ugcUrl": {"bvid": ctx["bvid"], "cid": ctx["cid"], "qn": 80,
                   "fnval": 4048, "fnver": 0, "fourk": 1},
        "playInfo": {"bvid": ctx["bvid"], "cid": ctx["cid"]},
        "relatedList": {"bvid": ctx["bvid"]},
        "videoRelation": {"aid": ctx["aid"]},
        "videoTags": {"bvid": ctx["bvid"]},
        "onlineTotal": {"aid": ctx["aid"], "cid": ctx["cid"]},
        "aiConclusion": {"aid": ctx["aid"], "cid": ctx["cid"]},
        "ab2c": {"bvid": ctx["bvid"]},
        "videoshot": {"bvid": ctx["bvid"], "cid": ctx["cid"]},
        "replyList": {"oid": ctx["aid"], "type": 1, "pn": 1, "ps": 5},
        "replyReplyList": {"oid": ctx["aid"], "root": ctx["rpid"], "type": 1, "pn": 1},
        "replyInteraction": {"oid": ctx["aid"], "type": 1},
        "memberInfo": {"mid": mid},
        "memberCardInfo": {"mid": mid, "photo": False},
        "memberDynamic": {"host_mid": mid},
        "userStat": {"vmid": mid},
        "space": app(vmid=mid),
        "spaceArchive": app(vmid=mid, pn=1, ps=5, qn=80,
                            order="pubdate", sort="desc"),
        "spaceArticle": app(vmid=mid, pn=1, ps=5),
        "spaceAudio": {"pn": 1, "ps": 5, "order": 1, "uid": mid,
                       "web_location": 333.1387},
        "spaceBangumi": app(vmid=mid, pn=1, ps=5, qn=32,
                            order="pubdate", sort="desc"),
        "spaceComic": app(vmid=mid, pn=1, ps=5),
        "spaceCheese": {"mid": mid, "pn": 1, "ps": 5, "web_location": 333.1387},
        "spaceChargingArchive": app(vmid=mid, pn=1, ps=5, qn=32,
                                    order="pubdate", sort="desc"),
        "spaceFav": app(up_mid=mid),
        "spaceOpus": {"host_mid": mid, "page": 1, "offset": "",
                      "type": "all", "web_location": 333.1387},
        "spaceStory": app(vmid=mid, aid=ctx["aid"], cid=ctx["cid"],
                          before_size=3, after_size=3, contain=1, index=0),
        "spaceShop": app(),
        "searchArchive": {"mid": mid, "keyword": KEYWORD, "pn": 1, "ps": 5},
        "searchAll": {"keyword": KEYWORD, "page": 1},
        "searchByType": {"keyword": KEYWORD, "search_type": "video", "page": 1},
        "searchSuggest": {"term": KEYWORD},
        "searchRecommend": app(**{"from": 2}),
        "searchHistory": {"pn": 1, "keyword": "", "business": "all"},
        "hotList": {"pn": 1, "ps": 5},
        "getRankApi": {"rid": 0, "type": "all"},
        "recommendListWeb": {"ps": 5, "fresh_idx": 0, "fresh_type": 4,
                             "feed_version": "V8"},
        "recommendListApp": {"idx": 0, "pull": "true"},
        "historyList": {"ps": 5},
        "mediaListHistory": {"pn": 1, "ps": 5},
        "seeYouLater": {"pn": 1},
        "favFolderInfo": {"media_id": ctx["folder"]},
        "favResourceList": {"media_id": ctx["folder"], "pn": 1, "ps": 5},
        "favPgc": {"vmid": mid, "type": 1, "pn": 1, "ps": 5},
        "favPugv": {"mid": mid, "ps": 5, "pn": 1, "web_location": 333.1387},
        "favArticle": {"page": 1},
        "favTopicList": {"page": 1, "page_size": 5},
        "favSeasonList": {"season_id": season or 43164, "pn": 1, "ps": 5},
        "favFolder": {"up_mid": mid},
        "userFavFolder": {"pn": 1, "ps": 5, "up_mid": mid},
        "userSubFolder": {"up_mid": mid, "ps": 5, "pn": 1, "platform": "web"},
        "followDynamic": {"type": "all"},
        "followUp": {"type": "all"},
        "followUpTag": {},
        "followUpGroup": {"mid": mid, "tagid": ctx["tagid"], "pn": 1, "ps": 5,
                          "order_type": ""},
        "followings": {"vmid": mid, "pn": 1, "ps": 5},
        "fans": {"vmid": mid, "pn": 1, "ps": 5},
        "blackList": {"pn": 1, "ps": 5},
        "followedUp": {"pn": 1, "vmid": mid, "web_location": 333.789,
                       "x-bili-device-req-json":
                       '{"platform":"web","device":"pc","spmid":"333.789"}'},
        "sameFollowing": {"pn": 1, "vmid": mid, "web_location": 333.789,
                          "x-bili-device-req-json":
                          '{"platform":"web","device":"pc","spmid":"333.789"}'},
        "followSearch": {"vmid": mid, "name": "a"},
        "relation": {"fid": ctx["room_uid"]},
        "relations": {"fids": ctx["room_uid"]},
        "memberGuard": {"page": 1, "page_size": 5, "ruid": mid},
        "liveList": {"page": 1, "page_size": 5, "platform": "web"},
        "liveAreaList": app(),
        "liveRoomAreaList": app(need_entrance=1, parent_id=1, source_id=2),
        "liveSecondList": {
            "actionKey": "appkey", "channel": "master", "build": 8430300,
            "version": "8.43.0", "c_locale": "zh_CN", "device": "android",
            "device_name": "android", "device_type": 0, "fnval": 912,
            "disable_rcmd": 0, "https_url_req": 1, "mobi_app": "android",
            "module_select": 0, "network": "wifi", "page": 1, "page_size": 5,
            "platform": "android", "qn": 0, "tag_version": 1,
            "s_locale": "zh_CN", "scale": 2, "statistics": APP_STAT},
        "liveFeedIndex": {
            "channel": "master", "actionKey": "appkey", "build": 8430300,
            "version": "8.43.0", "c_locale": "zh_CN", "device": "android",
            "device_name": "android", "device_type": 0, "fnval": 912,
            "disable_rcmd": 0, "https_url_req": 1, "mobi_app": "android",
            "network": "wifi", "page": 1, "platform": "android",
            "s_locale": "zh_CN", "scale": 2, "statistics": APP_STAT},
        "liveRoomInfo": {"room_id": ctx["room"], "protocol": "0,1",
                         "format": "0,1,2", "codec": "0,1,2", "qn": 10000,
                         "platform": "web", "ptype": 8, "dolby": 5,
                         "panorama": 1, "web_location": 444.8},
        "liveRoomInfoH5": {"room_id": ctx["room"]},
        "liveRoomDmToken": {"id": ctx["room"], "web_location": 444.8},
        "liveRoomDmPrefetch": {"roomid": ctx["room"]},
        "getLiveInfoByUser": {"room_id": ctx["room"], "from": 0,
                              "not_mock_enter_effect": 1, "web_location": 444.8},
        "liveMasterInfo": {"uid": ctx["room_uid"]},
        "liveFollow": {"page": 1, "page_size": 9, "ignoreRecord": 1,
                       "hit_ab": True},
        "liveMedalWall": {"target_id": mid},
        "getLiveEmoticons": {"platform": "pc", "room_id": ctx["room"]},
        "getLiveFavTag": app(),
        "liveSearch": {
            "actionKey": "appkey", "build": 8430300, "channel": "master",
            "version": "8.43.0", "c_locale": "zh_CN", "device": "android",
            "page": 1, "pagesize": 5, "keyword": KEYWORD, "disable_rcmd": 0,
            "mobi_app": "android", "platform": "android", "s_locale": "zh_CN",
            "statistics": APP_STAT, "type": "room"},
        "superChatMsg": {"room_id": ctx["room"]},
        "liveContributionRank": {"ruid": ctx["room_uid"], "room_id": ctx["room"],
                                 "page": 1, "page_size": 100,
                                 "type": "online_rank",
                                 "switch": "contribution_rank",
                                 "platform": "web", "web_location": 444.8},
        "liveFeedback": app(id=ctx["room"], id_type="room",
                            room_id=ctx["room"], type="dislike", page=1),
        "_fansMedal": {"target_id": ctx["room_uid"]},
        "pgcTimeline": {"types": 1},
        "pgcInfo": {"season_id": season},
        "pgcUrl": {"avid": ctx["aid"], "cid": ctx["pgc_cid"] or ctx["cid"],
                   "qn": 80, "fnval": 4048, "fourk": 1},
        "ogvUrl": {"ep_id": ep, "season_id": season,
                   "cid": ctx["pgc_cid"] or ctx["cid"], "qn": 80, "fnval": 4048,
                   "fourk": 1, "fnver": 0, "voice_balance": 0,
                   "gaia_source": "pre-load", "isGaiaAvoided": "true",
                   "biz_type": "ogv", "scene": "normal",
                   "from_client": "BROWSER", "drm_tech_type": 0, "app_id": 100,
                   "web_location": 1315873, "try_look": 1},
        "episodeInfo": {"ep_id": ep},
        "seasonStatus": {"season_id": season},
        "pgcReviewS": {"media_id": ctx["media_id"], "ps": 5, "sort": 0,
                       "web_location": 666.19},
        "pgcReviewL": {"media_id": ctx["media_id"], "ps": 5, "sort": 0,
                       "web_location": 666.19},
        "pgcRank": {"day": 3, "season_type": 1},
        "pgcSeasonRank": {"day": 3, "season_type": 1},
        "pgcIndexCondition": {"season_type": 1, "type": 1, "index_type": 0},
        "pgcIndexResult": {"season_type": 1, "type": 1, "index_type": 0,
                           "page": 1, "pagesize": 5},
        "getMemberSeasonsApi": {"mid": mid, "page_num": 1, "page_size": 5},
        "getMemberViewApi": {"mid": mid},
        "getTopVideoApi": {"vmid": mid},
        "getRecentCoinVideoApi": {"vmid": mid, "pn": 1, "ps": 5},
        "getRecentLikeVideoApi": {"vmid": mid, "pn": 1, "ps": 5},
        "coinArc": {"pn": 1, "ps": 5, "vmid": mid},
        "likeArc": {"pn": 1, "ps": 5, "vmid": mid},
        "topicFeed": {"topic_id": ctx["topic"], "sort_by": 1, "page_size": 5,
                      "source": "Web", "features": DYN_FEATURES},
        "topicFold": {"topic_id": ctx["topic"], "sort_by": 1},
        "topicTop": {"topic_id": ctx["topic"], "source": "Web"},
        "dynTopicRcmd": {"source": "Web", "page_size": 5,
                         "web_location": 333.1365},
        "msgSysNotify": {"cursor": 0, "page_size": 5},
        "msgFeedAt": {"platform": "web", "mobi_app": "web", "build": 0,
                      "web_location": 333.40164},
        "msgFeedReply": {"platform": "web", "mobi_app": "web", "build": 0,
                         "web_location": 333.40164},
        "seasonSeries": {"mid": mid, "page_num": 1, "page_size": 10},
        "sessionMsg": {"talker_id": mid, "session_type": 1, "size": 20,
                       "build": 0, "mobi_app": "web"},
        "spaceSetting": {"mid": mid},
        "topicPubSearch": {"keywords": KEYWORD, "content": "", "page_size": 20,
                           "page_num": 1, "web_location": 333.1365},
        "liveConfigPlugs": {},
        "msgLikeDetail": {"card_id": ctx["card"], "pn": 1, "last_mid": 0,
                          "platform": "web", "build": 0, "mobi_app": "web",
                          "web_location": 333.40164},
        "sessionList": {**VC_COMMON, "session_type": 1, "group_fold": 1,
                        "unfollow_fold": 0, "sort_rule": 2},
        "sessionAccountList": {"uids": mid, "build": 0, "mobi_app": "web"},
        "getSessionSs": {"talker_uid": mid, **VC_COMMON},
        "imUserInfos": {"uids": str(mid), **VC_COMMON},
        "getMsgDnd": {"own_uid": mid, "uids_str": str(mid), **VC_COMMON},
        "coinLog": {"pn": 1, "ps": 5},
        "expLog": {"pn": 1, "ps": 5},
        "moralLog": {"pn": 1, "ps": 5},
        "loginLog": {"pn": 1, "ps": 5},
        "popularPrecious": {"pn": 1, "ps": 5},
        "popularSeriesOne": {"number": 1},
        "dynUplist": {"up_mid": mid},
        "dynMention": {"keyword": KEYWORD},
        "dynSearch": {"mid": mid, "keyword": KEYWORD},
        "dynamicDetail": {"id": ctx["dyn"]},
        "opusDetail": {"id": ctx["dyn"]},
        "dynReaction": {"id": ctx["dyn"], "web_location": 333.1369},
        "dynPic": {"id": ctx.get("draw_dyn") or ctx["dyn"],
                   "web_location": 333.1368},
        "archiveNote": {"oid": ctx["aid"]},
        "archiveNoteList": {"oid": ctx["aid"], "oid_type": 0, "pn": 1, "ps": 5},
        "noteList": {"pn": 1, "ps": 5},
        "userNoteList": {"pn": 1, "ps": 5},
        "articleList": {"id": ctx["artlist"], "web_location": 333.1400},
        "articleInfo": {"id": ctx["article"], "mobi_app": "pc", "from": "web",
                        "gaia_source": "main_web"},
        "articleView": {"id": ctx["article"], "gaia_source": "main_web",
                        "web_location": "333.976"},
        "danmakuFilter": {"type": 1},
        "mediaList": {"mobi_app": "web", "type": 3, "biz_id": ctx["folder"],
                      "ps": 10, "direction": False, "desc": True,
                      "sort_field": 1, "tid": 0, "with_current": False},
        "myEmote": {"business": "reply", "web_location": "333.1245"},
        "tvPlayUrl": {"actionKey": "appkey", "cid": ctx["cid"],
                      "fourk": 1, "is_proj": 1, "object_id": ctx["aid"],
                      "mobi_app": "android", "platform": "android",
                      "playurl_type": 1, "protocol": 0, "qn": 80},
        "upowerRank": {"up_mid": mid, "pn": 1, "ps": 5,
                       "mobi_app": "web", "web_location": 333.1196},
        "userRealName": {"up_mid": mid},
        # Write payloads: only ever called with account=anon.
        "coinVideo": {"aid": ctx["aid"], "multiply": 1, "select_like": 0},
        "likeVideo": {"aid": ctx["aid"], "like": 1},
        "dislikeVideo": {"aid": ctx["aid"], "dislike": 1},
        "feedDislike": {"goto": "av", "id": ctx["aid"], "reason_id": 1,
                        "build": 1, "mobi_app": "android"},
        "feedDislikeCancel": {"goto": "av", "id": ctx["aid"], "reason_id": 1,
                              "build": 1, "mobi_app": "android"},
        "hateReply": {"type": 1, "oid": ctx["aid"], "rpid": ctx["rpid"],
                      "action": 0},
        "likeReply": {"type": 1, "oid": ctx["aid"], "rpid": ctx["rpid"],
                      "action": 1},
        "likeTopic": {"action": "like", "up_mid": mid,
                      "topic_id": ctx["topic"], "business": "topic"},
        "pgcLikeCoinFav": {"ep_id": ep},
        "pgcTriple": {"ep_id": ep, "season_id": season},
        "ugcTriple": {"aid": ctx["aid"], "eab_x": 2, "ramval": 0,
                      "source": "web_normal", "ga": 1},
        "relationMod": {"fid": ctx["room_uid"], "act": 1, "re_src": 11},
        "pgcAdd": {"season_id": season},
        "pgcDel": {"season_id": season},
        "thumbDynamic": {"dyn_id_str": ctx["dyn"], "up": 1,
                         "spmid": "333.1365.0.0"},
        "danmakuLike": {"op": 1, "dmid": ctx.get("dmid") or 0,
                        "oid": ctx["cid"], "platform": "web_player",
                        "polaris_app_id": 100, "polaris_platform": 5,
                        "spmid": "333.788.0.0",
                        "from_spmid": "333.788.0.0",
                        "statistics":
                            '{"appId":100,"platform":5,"abtest":"","version":""}'},
        "historyReport": {"aid": ctx["aid"], "type": 3},
        "heartBeat": {"bvid": ctx["bvid"], "cid": ctx["cid"], "type": 3,
                      "played_time": 0},
        "roomEntryAction": {"room_id": ctx["room"], "platform": "pc"},
        "liveLikeReport": {"click_time": 0, "room_id": ctx["room"],
                           "uid": mid, "web_location": 444.8},
        "vipExpAdd": {"mid": mid},
    }


LIVE_APP_QUERY = {
    "actionKey": "appkey", "channel": "master", "build": 8430300,
    "version": "8.43.0", "c_locale": "zh_CN", "device": "android",
    "disable_rcmd": 0, "mobi_app": "android", "platform": "android",
    "s_locale": "zh_CN", "statistics": APP_STAT,
}

EXPECTED_NON_JSON = {"bili.dynamicSpmPrefix"}


def fallback_query(name, path, ctx):
    low = (name + " " + path).lower()
    q = {}
    if "bvid" in low:
        q["bvid"] = ctx["bvid"]
    if "aid" in low or "avid" in low or "oid" in low:
        q["aid"] = ctx["aid"]
    if "cid" in low:
        q["cid"] = ctx["cid"]
    if "mid" in low:
        q["mid"] = ctx["mid"]
    if "room" in low:
        q["room_id"] = ctx["room"]
    if "season" in low:
        q["season_id"] = ctx["season"]
    if "episode_id" in low or "ep_id" in low:
        q["ep_id"] = ctx["ep"]
    if "media" in low:
        q["media_id"] = ctx["season"]
    if "search" in low or "keyword" in low:
        q["keyword"] = KEYWORD
    if "reply" in low:
        q["type"] = 1
    if any(w in low for w in ("list", "feed", "rank", "space", "search", "reply")):
        q.update({"pn": 1, "ps": 5, "page": 1, "page_size": 5})
    return q


class Driver:
    def __init__(self, binary, data_dir, timeout):
        self.proc = subprocess.Popen(
            [binary, "--data-dir", data_dir],
            stdin=subprocess.PIPE, stdout=subprocess.PIPE,
            stderr=subprocess.DEVNULL, text=True, bufsize=1,
        )
        self.lines = queue.Queue()
        threading.Thread(target=self._pump, daemon=True).start()
        self.seq = 0
        self.timeout = timeout

    def _pump(self):
        for line in self.proc.stdout:
            self.lines.put(line)
        self.lines.put(None)

    def call(self, method, params=None, timeout=None):
        self.seq += 1
        req = {"jsonrpc": "2.0", "id": self.seq, "method": method}
        if params is not None:
            req["params"] = params
        self.proc.stdin.write(json.dumps(req) + "\n")
        self.proc.stdin.flush()
        deadline = time.time() + (timeout or self.timeout)
        while True:
            try:
                line = self.lines.get(timeout=max(0.1, deadline - time.time()))
            except queue.Empty:
                return None, {"code": "timeout", "message": f"no response for {method}"}
            if line is None:
                return None, {"code": "exited", "message": "process exited"}
            msg = json.loads(line)
            if msg.get("id") == self.seq:
                return msg.get("result"), msg.get("error")
            # notifications are ignored during the probe

    def close(self):
        try:
            self.call("core.shutdown", timeout=10)
        except Exception:
            pass
        try:
            self.proc.wait(timeout=10)
        except subprocess.TimeoutExpired:
            self.proc.kill()


def dig(obj, *keys):
    cur = obj
    for key in keys:
        if isinstance(cur, dict):
            cur = cur.get(key)
        elif isinstance(cur, list) and isinstance(key, int) and key < len(cur):
            cur = cur[key]
        else:
            return None
    return cur


def find_dict(obj, required):
    if isinstance(obj, dict):
        if all(k in obj for k in required):
            return obj
        for v in obj.values():
            found = find_dict(v, required)
            if found:
                return found
    elif isinstance(obj, list):
        for v in obj:
            found = find_dict(v, required)
            if found:
                return found
    return None


def discover(drv, mid=0):
    ctx = {"mid": mid, "room": ROOM, "bvid": None, "aid": 0, "cid": 0,
           "season": 0, "ep": 0, "pgc_cid": 0, "media_id": 0,
           "article": 0, "artlist": 0, "folder": 0, "dyn": "", "draw_dyn": "",
           "rpid": 0, "dmid": 0, "room_uid": 0, "tagid": 0, "topic": 0,
           "card": "", "keyword": KEYWORD}
    accts = drv.call("auth.accounts")[0] or {}
    if not ctx["mid"] and accts.get("active_mid"):
        ctx["mid"] = accts["active_mid"]
    hot, _ = drv.call("video.hot", {"ps": 1, "pn": 1})
    item = dig(hot, "data", "list", 0) or {}
    if not ctx["mid"]:
        ctx["mid"] = dig(item, "owner", "mid") or 0
    ctx["bvid"], ctx["cid"] = item.get("bvid"), item.get("cid")
    if not ctx["bvid"]:
        raise SystemExit("discovery failed: video.hot returned no video")
    info, _ = drv.call("video.info", {"bvid": ctx["bvid"]})
    ctx["aid"] = dig(info, "data", "aid") or item.get("aid") or 0

    bangumi, _ = drv.call("search.type", {"keyword": "孤独摇滚",
                                          "search_type": "media_bangumi",
                                          "page": 1})
    b = find_dict(bangumi, ("season_id", "media_id")) or {}
    ctx["season"] = b.get("season_id") or 0
    ctx["media_id"] = b.get("media_id") or 0
    if not ctx["season"]:
        timeline, _ = drv.call("pgc.timeline", {"types": 1})
        ep = find_dict(timeline, ("season_id", "episode_id")) or {}
        ctx["season"] = ep.get("season_id") or 0
        ctx["ep"] = ep.get("episode_id") or 0
        ctx["media_id"] = ep.get("media_id") or 0
    if ctx["season"]:
        pinfo, _ = drv.call("pgc.info", {"season_id": ctx["season"]})
        eps = dig(pinfo, "result", "episodes") or []
        if eps:
            ctx["ep"] = eps[0].get("id") or eps[0].get("ep_id") or 0
            ctx["pgc_cid"] = eps[0].get("cid") or 0
        ctx["media_id"] = ctx["media_id"] or dig(pinfo, "result", "media_id") or 0
    if not ctx["season"]:
        ctx.update(season=43164, media_id=28339735, ep=693247)
        print("  warning: PGC discovery failed, using known bangumi fixture")

    articles, _ = drv.call("search.type", {"keyword": KEYWORD,
                                           "search_type": "article",
                                           "page": 1})
    a = find_dict(articles, ("id", "title")) or {}
    ctx["article"] = a.get("id") or 23309662
    view, _ = drv.call("bili.articleView", {"query": {
        "id": ctx["article"], "gaia_source": "main_web",
        "web_location": 333.976}})
    ctx["artlist"] = dig(view, "data", "list", "id") or 490638

    feed, _ = drv.call("dynamic.feed", {"type": "all"})
    items = dig(feed, "data", "items") or []
    dyn = items[0] if items else {}
    ctx["dyn"] = dyn.get("id_str") or dyn.get("id") or ""
    for it in items:
        if "draw" in str(it.get("type", "")).lower():
            ctx["draw_dyn"] = it.get("id_str") or it.get("id") or ""
            break

    replies, _ = drv.call("reply.list", {"oid": ctx["aid"], "type": 1, "ps": 1})
    r = dig(replies, "data", "replies", 0) or dig(replies, "data", "top_replies", 0) or {}
    ctx["rpid"] = r.get("rpid") or 0
    seg, _ = drv.call("danmaku.seg_raw", {"cid": ctx["cid"], "segment_index": 1})
    text = seg if isinstance(seg, str) else json.dumps(seg)
    m = re.search(r'<d p="([^"]+)"', text)
    if m:
        ctx["dmid"] = int(m.group(1).split(",")[0]) or 0
    folders, _ = drv.call("fav.all_folders")
    ctx["folder"] = dig(folders, "data", "list", 0, "id") or 0

    livefeed, _ = drv.call("bili.liveFeedIndex",
                           {"query": dict(LIVE_APP_QUERY), "sign": "app"})
    liveitem = find_dict(livefeed, ("roomid", "uid")) or {}
    if liveitem:
        ctx["room"] = liveitem.get("roomid") or ROOM
        ctx["room_uid"] = liveitem.get("uid") or 0
    room, _ = drv.call("live.room_info", {"room_id": ctx["room"]})
    ctx["room_uid"] = dig(room, "data", "uid") or ctx["room_uid"] or 0

    tags, _ = drv.call("bili.followUpTag", {"query": {}})
    t = find_dict(tags, ("tagid",)) or {}
    ctx["tagid"] = t.get("tagid") or 0
    topics, _ = drv.call("bili.topicPubSearch", {"query": {
        "keywords": "孤独摇滚", "content": "", "page_size": 20,
        "page_num": 1, "web_location": 333.1365}})
    t = find_dict(topics, ("id", "name", "view")) or {}
    ctx["topic"] = t.get("id") or 120500
    likes, _ = drv.call("msg.likes")
    items = dig(likes, "data", "total", "items") or []
    if items:
        ctx["card"] = items[0].get("id") or ""

    print(f"  context: bvid={ctx['bvid']} aid={ctx['aid']} cid={ctx['cid']} "
          f"season={ctx['season']} ep={ctx['ep']} media={ctx['media_id']} "
          f"folder={ctx['folder']} dyn={ctx['dyn']} rpid={ctx['rpid']} "
          f"room={ctx['room']} uid={ctx['room_uid']} tag={ctx['tagid']} "
          f"topic={ctx['topic']} article={ctx['article']}/{ctx['artlist']} "
          f"card={ctx['card']}")
    return ctx


def describe(res, err):
    if err:
        return "rpc_error", err.get("code"), err.get("message") or ""
    if isinstance(res, dict):
        if "code" in res:
            return ("ok" if res.get("code") == 0 else "upstream",
                    res.get("code"), res.get("message") or res.get("msg") or "")
        if "grpc_status" in res:
            return ("ok" if res.get("grpc_status") == 0 else "upstream",
                    res.get("grpc_status"), res.get("grpc_message") or "")
        if "status" in res and "body_base64" in res:
            return "non-json", res.get("status"), res.get("content_type") or ""
        return "ok", 0, ""
    return "ok", 0, ""


def probe(drv, label, method, params, results, category, throttle):
    res, err = drv.call(method, params)
    kind, code, msg = describe(res, err)
    if kind == "upstream" and code in (-352, -509):
        time.sleep(20)
        res, err = drv.call(method, params)
        kind, code, msg = describe(res, err)
        if kind == "upstream" and code in (-352, -509):
            time.sleep(20)
    elif kind == "upstream" and code in (-1, -500):
        time.sleep(5)
        res, err = drv.call(method, params)
        kind, code, msg = describe(res, err)
    results.append({"method": method, "category": category, "kind": kind,
                    "code": code, "message": str(msg)[:200]})
    if throttle:
        time.sleep(throttle)


def fixture_missing(name, ctx):
    if name in FIXTURE_SKIP:
        return "no fixture"
    if name in ("topicFeed", "topicFold", "topicTop") and not ctx.get("topic"):
        return "no topic fixture"
    if name == "msgLikeDetail" and not ctx.get("card"):
        return "no msg like card"
    if name == "followUpGroup" and not ctx.get("tagid"):
        return "no follow tag"
    if name in ("dynPic", "dynamicDetail", "opusDetail", "dynReaction") \
            and not ctx.get("dyn"):
        return "no dynamic"
    return None


def shop_body(ctx):
    mid = str(ctx["mid"])
    return {"from": f"cps_productTab_{mid}", "searchAfter": 0,
            "msource": f"cps_productTab_{mid}", "pageSize": 8, "upMid": mid}


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--bin", default="./bbcore")
    ap.add_argument("--data-dir", default=os.environ.get("BBCORE_PROBE_DATA", "./.bbcore-probe"))
    ap.add_argument("--mid", type=int, default=0,
                    help="public user mid for fixtures (default: active account, then a hot video uploader)")
    ap.add_argument("--phase", choices=["all", "raw", "semantic"], default="all")
    ap.add_argument("--throttle", type=float, default=0.35)
    ap.add_argument("--timeout", type=float, default=20)
    ap.add_argument("--out", default="./probe-report.json")
    ap.add_argument("--verbose", action="store_true")
    args = ap.parse_args()

    drv = Driver(args.bin, args.data_dir, args.timeout)
    try:
        print("== discovery ==")
        ctx = discover(drv, args.mid)
        specs = build_specs(ctx)
        results = []
        skipped = []

        if args.phase in ("all", "raw"):
            print("== raw endpoints ==")
            endpoints, rpc_err = drv.call("core.endpoints")
            if rpc_err:
                raise SystemExit(f"core.endpoints failed: {rpc_err}")
            for ep in sorted(endpoints, key=lambda e: e["name"]):
                name, path = ep["name"], ep["path"]
                if name in SKIP:
                    skipped.append({"method": "bili." + name, "category": "raw",
                                    "reason": "destructive/login/report"})
                    continue
                missing = fixture_missing(name, ctx)
                if missing:
                    skipped.append({"method": "bili." + name, "category": "raw",
                                    "reason": missing})
                    continue
                spec = specs.get(name)
                if spec is None:
                    spec = fallback_query(name, path, ctx)
                category = "write-anon" if name in WRITE_ANON else "read"
                if name == "spaceShop":
                    params = {"method": "POST", "sign": "app",
                              "query": app_query(build=8430300, mVersion=309,
                                                 mallVersion=8430300,
                                                 actionKey="appkey"),
                              "json": shop_body(ctx)}
                elif category == "write-anon":
                    params = {"account": "anon"}
                    if name in WRITE_APP:
                        params.update(method="POST", query=app_query(),
                                      form=spec or {}, sign="app")
                    elif name in WRITE_GET:
                        params.update(query=spec, sign="app")
                    else:
                        params.update(method="POST", form=spec or {})
                else:
                    params = {"query": spec}
                    if name in SIGN_APP:
                        params["sign"] = "app"
                    if name in SIGN_WBI:
                        params["sign"] = "wbi"
                probe(drv, name, "bili." + name, params, results, category,
                      args.throttle)
                if args.verbose:
                    r = results[-1]
                    print(f"  {r['kind']:<9} {r['code']!s:<7} bili.{name} {r['message'][:60]}")

        if args.phase in ("all", "semantic"):
            print("== semantic methods ==")
            sem = build_semantics(ctx)
            for method, params, category in sem:
                if method in ("core.shutdown",):
                    continue
                probe(drv, method, method, params, results, category, args.throttle)
                if args.verbose:
                    r = results[-1]
                    print(f"  {r['kind']:<9} {r['code']!s:<7} {method} {r['message'][:60]}")
            # live connect/disconnect over the same process
            res, err = drv.call("live.connect", {"room_id": ROOM})
            kind, code, msg = describe(res, err)
            results.append({"method": "live.connect", "category": "read",
                            "kind": kind, "code": code, "message": str(msg)[:200]})
            time.sleep(1)
            res, err = drv.call("live.disconnect", {"room_id": ROOM})
            kind, code, msg = describe(res, err)
            results.append({"method": "live.disconnect", "category": "read",
                            "kind": kind, "code": code, "message": str(msg)[:200]})

        report = {"time": int(time.time()), "context": ctx,
                  "results": results, "skipped": skipped}
        with open(args.out, "w") as f:
            json.dump(report, f, ensure_ascii=False, indent=1)
        summarize(results, skipped, args.out)
    finally:
        drv.close()


def build_semantics(ctx):
    bvid, aid, cid = ctx["bvid"], ctx["aid"], ctx["cid"]
    mid, room = ctx["mid"], ctx["room"]
    season, ep, folder = ctx["season"], ctx["ep"], ctx["folder"]
    dyn = ctx["dyn"]
    read = [
        ("core.ping", {}), ("core.version", {}), ("core.info", {}),
        ("core.methods", {}), ("core.endpoints", {"q": "playurl"}),
        ("core.endpoint", {"name": "playInfo"}),
        ("auth.accounts", {}), ("auth.account", {}), ("auth.account.info", {}),
        ("auth.qr.generate", {}),
        ("api.call", {"base": "api", "path": "/x/web-interface/view",
                      "sign": "none", "query": {"bvid": bvid}}),
        ("api.endpoint", {"name": "playInfo", "query": {"bvid": bvid, "cid": cid}}),
        ("video.recommend", {"ps": 5}), ("video.recommend_app", {}),
        ("video.hot", {"ps": 5}), ("video.rank", {}),
        ("video.info", {"bvid": bvid}),
        ("video.playurl", {"bvid": bvid, "cid": cid}),
        ("video.play_info", {"bvid": bvid, "cid": cid}),
        ("video.related", {"bvid": bvid}),
        ("video.relation", {"bvid": bvid}),
        ("video.tags", {"bvid": bvid}),
        ("video.online", {"bvid": bvid, "cid": cid}),
        ("video.ai_conclusion", {"bvid": bvid, "cid": cid}),
        ("user.me", {}), ("user.info", {"mid": mid}),
        ("user.stat", {"mid": mid}), ("user.card", {"mid": mid}),
        ("user.videos", {"mid": mid, "ps": 5}),
        ("user.history", {"ps": 5}), ("user.later", {"ps": 5}),
        ("user.followings", {"vmid": mid, "ps": 5}),
        ("user.followers", {"vmid": mid, "ps": 5}),
        ("user.blacklist", {"ps": 5}),
        ("user.folders", {"mid": mid}), ("user.sub_folders", {"mid": mid}),
        ("search.default", {}), ("search.all", {"keyword": KEYWORD}),
        ("search.type", {"keyword": KEYWORD, "search_type": "video"}),
        ("search.suggest", {"term": KEYWORD}), ("search.trending", {}),
        ("search.hot", {}), ("search.recommend", {"keyword": KEYWORD}),
        ("dynamic.feed", {"type": "all"}), ("dynamic.space", {"mid": mid}),
        ("dynamic.detail", {"id": dyn}), ("dynamic.opus", {"id": dyn}),
        ("live.list", {"page": 1, "page_size": 5}),
        ("live.following", {"page": 1}),
        ("live.room_info", {"room_id": room}),
        ("live.room_info_h5", {"room_id": room}),
        ("live.info_user", {"room_id": room}),
        ("live.danmaku_info", {"room_id": room}),
        ("live.history", {"room_id": room}),
        ("live.super_chat", {"room_id": room}),
        ("live.rooms", {}),
        ("reply.list", {"oid": aid, "type": 1, "pn": 1, "ps": 5}),
        ("reply.replies", {"oid": aid, "root": ctx["rpid"], "type": 1, "pn": 1}),
        ("danmaku.seg", {"cid": cid, "segment_index": 1}),
        ("danmaku.seg_raw", {"cid": cid, "segment_index": 1}),
        ("fav.all_folders", {}), ("fav.folders", {}),
        ("fav.info", {"media_id": folder}),
        ("fav.list", {"media_id": folder, "ps": 5}),
        ("msg.replies", {}), ("msg.ats", {}), ("msg.likes", {}),
        ("msg.like_detail", {"card_id": ctx["card"], "pn": 1}),
        ("msg.unread", {}), ("msg.system", {"cursor": 0, "page_size": 5}),
        ("pgc.timeline", {"types": 1}), ("pgc.info", {"season_id": season}),
        ("pgc.playurl", {"season_id": season, "ep_id": ep,
                         "cid": ctx["pgc_cid"] or cid}),
        ("pgc.rank", {}), ("pgc.season_rank", {}),
        ("pgc.status", {"season_id": season}),
        ("pgc.review", {"media_id": ctx["media_id"], "type": "short"}),
        ("misc.sponsor_segments", {"bvid": bvid, "cid": cid}),
        ("misc.match_info", {"cid": cid}),
        ("misc.video_note", {"oid": aid}),
        ("grpc.reply_main", {"oid": aid, "type": 1, "mode": 2, "page_size": 5}),
        ("grpc.call", {"path": "/bilibili.community.service.dm.v1.DM/DmSegMobile",
                       "payload_base64": dm_payload(cid)}),
    ]
    out = [(m, p, "read") for m, p in read]
    out += [
        ("video.like", {"bvid": bvid, "like": True, "account": "anon"}, "write-anon"),
        ("video.coin", {"bvid": bvid, "multiply": 1, "select_like": 1, "account": "anon"}, "write-anon"),
        ("video.triple", {"bvid": bvid, "account": "anon"}, "write-anon"),
        ("video.dislike", {"bvid": bvid, "dislike": True, "account": "anon"}, "write-anon"),
        ("user.follow", {"fid": ctx["room_uid"], "follow": True, "account": "anon"}, "write-anon"),
        ("dynamic.like", {"id": dyn, "up": True, "account": "anon"}, "write-anon"),
        ("reply.add", {"oid": aid, "type": 1, "message": "probe", "account": "anon"}, "write-anon"),
        ("danmaku.send", {"bvid": bvid, "cid": cid, "msg": "probe", "account": "anon"}, "write-anon"),
        ("fav.add", {"media_id": folder, "resources": bvid, "account": "anon"}, "write-anon"),
        ("pgc.follow", {"season_id": season, "follow": True, "account": "anon"}, "write-anon"),
        ("user.later.add", {"bvid": bvid, "account": "anon"}, "write-anon"),
    ]
    return out


def dm_payload(cid):
    def varint(n):
        out = bytearray()
        while True:
            b = n & 0x7F
            n >>= 7
            if n:
                out.append(b | 0x80)
            else:
                out.append(b)
                return bytes(out)
    body = b"\x10" + varint(cid) + b"\x18\x01" + b"\x20" + varint(1)
    return base64.b64encode(body).decode()


def summarize(results, skipped, out_path):
    print("\n== summary ==")
    raw = [r for r in results if r["method"].startswith("bili.")]
    sem = [r for r in results if not r["method"].startswith("bili.")]
    for category in ("read", "write-anon"):
        rows = [r for r in raw if r["category"] == category]
        if not rows:
            continue
        counts = {}
        for r in rows:
            counts[(r["kind"], r["code"])] = counts.get((r["kind"], r["code"]), 0) + 1
        print(f"  raw {category}: {len(rows)} endpoints")
        for (kind, code), n in sorted(counts.items(), key=lambda kv: -kv[1]):
            print(f"    {kind:<9} {code!s:<6} x{n}")
    if sem:
        counts = {}
        for r in sem:
            counts[(r["kind"], r["code"])] = counts.get((r["kind"], r["code"]), 0) + 1
        print(f"  semantic: {len(sem)} methods")
        for (kind, code), n in sorted(counts.items(), key=lambda kv: -kv[1]):
            print(f"    {kind:<9} {code!s:<6} x{n}")

    problems = [r for r in results
                if r["method"] not in EXPECTED_NON_JSON
                and (r["kind"] == "rpc_error"
                     or (r["kind"] == "non-json" and r["category"] != "write-anon")
                     or (r["kind"] == "upstream"
                         and r["code"] not in (-101, -400, -404, -412, -352,
                                               -509, 86101, 86090, 86038)))]
    if problems:
        print("\n  notable (rpc errors / unexpected upstream codes):")
        for r in problems[:40]:
            print(f"    {r['kind']:<9} {r['code']!s:<6} {r['method']}  {r['message'][:100]}")
        if len(problems) > 40:
            print(f"    ... {len(problems) - 40} more (see JSON)")
    destructive = [s for s in skipped if s["reason"] == "destructive/login/report"]
    fixtures = [s for s in skipped if s["reason"] != "destructive/login/report"]
    print(f"\n  skipped {len(destructive)} destructive endpoints, "
          f"{len(fixtures)} fixture-less endpoints")
    print(f"  full report: {out_path}")


if __name__ == "__main__":
    main()
