package service

import (
	"encoding/json"
	"testing"

	"github.com/cloudflypeng/bbcore/internal/jsonrpc"
)

func TestDiscoverRequiresIdentifiers(t *testing.T) {
	s := New(Options{})
	cases := []struct {
		name string
		call func(t *testing.T) (any, *jsonrpc.Error)
	}{
		{"article.view", func(t *testing.T) (any, *jsonrpc.Error) {
			return s.articleView(t.Context(), json.RawMessage(`{}`))
		}},
		{"article.info", func(t *testing.T) (any, *jsonrpc.Error) {
			return s.articleInfo(t.Context(), json.RawMessage(`{}`))
		}},
		{"topic.detail", func(t *testing.T) (any, *jsonrpc.Error) {
			return s.topicDetail(t.Context(), json.RawMessage(`{}`))
		}},
		{"topic.feed", func(t *testing.T) (any, *jsonrpc.Error) {
			return s.topicFeed(t.Context(), json.RawMessage(`{}`))
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := tc.call(t); err == nil {
				t.Fatalf("%s accepted empty params", tc.name)
			}
		})
	}
}

func TestArticleIDAlias(t *testing.T) {
	p, err := jsonrpc.ParseParams(json.RawMessage(`{"article_id":123}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := articleID(p); got != 123 {
		t.Fatalf("article_id = %d", got)
	}
	p, err = jsonrpc.ParseParams(json.RawMessage(`{"id":456}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := articleID(p); got != 456 {
		t.Fatalf("id = %d", got)
	}
}

func TestDiscoverMethodsRegistered(t *testing.T) {
	s := New(Options{})
	for _, name := range []string{"pgc.play", "video.weekly", "article.rank", "article.view", "article.info", "topic.detail", "topic.feed", "topic.square"} {
		if _, ok := s.methods[name]; !ok {
			t.Errorf("method %s is not registered", name)
		}
	}
}
