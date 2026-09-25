package authz

import (
	"context"
	"embed"
	"encoding/json"
	"strings"
)

//go:embed messages/*.json
var messageFiles embed.FS

type catalog struct {
	byLang map[string]map[string]string
}

func loadCatalog() catalog {
	byLang := map[string]map[string]string{}
	entries, err := messageFiles.ReadDir("messages")
	if err != nil {
		panic("authz: message catalog missing")
	}
	for _, entry := range entries {
		raw, err := messageFiles.ReadFile("messages/" + entry.Name())
		if err != nil {
			panic("authz: " + err.Error())
		}
		var msgs map[string]string
		if err := json.Unmarshal(raw, &msgs); err != nil {
			panic("authz: " + err.Error())
		}
		lang := strings.TrimSuffix(entry.Name(), ".json")
		byLang[lang] = msgs
	}
	return catalog{byLang: byLang}
}

func (c catalog) text(lang, id string) string {
	if msgs, ok := c.byLang[lang]; ok {
		if s, ok := msgs[id]; ok {
			return s
		}
	}
	if msgs, ok := c.byLang["en"]; ok {
		if s, ok := msgs[id]; ok {
			return s
		}
	}
	return id
}

type langKey struct{}

func withLang(ctx context.Context, lang string) context.Context {
	return context.WithValue(ctx, langKey{}, lang)
}

func langOf(ctx context.Context) string {
	lang, _ := ctx.Value(langKey{}).(string)
	if lang == "" {
		return "en"
	}
	return lang
}

func negotiate(header string) string {
	for _, part := range strings.Split(header, ",") {
		token := strings.TrimSpace(strings.Split(part, ";")[0])
		token = strings.ToLower(token)
		if token == "ar" || strings.HasPrefix(token, "ar-") {
			return "ar"
		}
		if token == "en" || strings.HasPrefix(token, "en-") {
			return "en"
		}
	}
	return "en"
}
