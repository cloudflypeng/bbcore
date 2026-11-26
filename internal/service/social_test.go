package service

import (
	"encoding/json"
	"testing"

	"github.com/cloudflypeng/bbcore/internal/jsonrpc"
)

func TestSocialMethodsRegistered(t *testing.T) {
	s := New(Options{})
	for _, name := range []string{
		"user.followings", "user.followers", "user.follow_tags",
		"user.follow_tag_members", "user.follow_search", "user.follow",
		"user.relation",
	} {
		if _, ok := s.methods[name]; !ok {
			t.Errorf("method %s is not registered", name)
		}
	}
}

func TestUserRelationRequiresMid(t *testing.T) {
	s := New(Options{})
	if _, err := s.userRelation(t.Context(), json.RawMessage(`{}`)); err == nil {
		t.Fatal("user.relation accepted empty params")
	}
}

func TestFollowTagMembersRequiresTagID(t *testing.T) {
	s := New(Options{})
	if _, err := s.userFollowTagMembers(t.Context(), json.RawMessage(`{}`)); err == nil {
		t.Fatal("follow_tag_members accepted empty params")
	}
}

func TestFollowSearchRequiresName(t *testing.T) {
	s := New(Options{})
	if _, err := s.userFollowSearch(t.Context(), json.RawMessage(`{}`)); err == nil {
		t.Fatal("follow_search accepted an empty name")
	}
}

func TestFollowTagIDAllowsDefaultGroup(t *testing.T) {
	p, err := jsonrpc.ParseParams(json.RawMessage(`{"tagid":0}`))
	if err != nil {
		t.Fatal(err)
	}
	if tagID, ok := p.ID("tagid"); !ok || tagID != 0 {
		t.Fatalf("tagid = %d, ok = %v", tagID, ok)
	}
}
