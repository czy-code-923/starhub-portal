package portal

import (
	"context"
	"errors"
	"sync"
	"time"
)

type commercialGroup struct {
	ID       string `json:"id"`
	Services []struct {
		Protocol string `json:"protocol"`
		Managed  bool   `json:"managed_users"`
	} `json:"services"`
}
type groupCache struct {
	mu         sync.Mutex
	key        string
	groups     []commercialGroup
	next       time.Time
	refreshing bool
}

// Catalog polling shares a bounded metadata cache; checkout and writes validate
// fresh metadata. Credentials and proxy configuration never enter this cache.
func (a *App) commercialGroups(ctx context.Context, fresh bool) ([]commercialGroup, error) {
	secret, ok := ctx.Value(controlSecretKey{}).(Secrets)
	if !ok {
		_, s, e := a.settings(ctx)
		if e != nil {
			return nil, e
		}
		secret = s
		ctx = context.WithValue(ctx, controlSecretKey{}, secret)
	}
	key := hash(secret.ControlOrigin + "\x00" + secret.ControlKey + "\x00" + a.TestControlURL)
	c := &a.commercialCache
	c.mu.Lock()
	if !fresh && c.key == key && a.now().Before(c.next) {
		g := c.groups
		c.mu.Unlock()
		if g == nil {
			return nil, errors.New("commercial groups unavailable")
		}
		return g, nil
	}
	if !fresh && c.refreshing {
		g := c.groups
		if c.key != key {
			g = nil
		}
		c.mu.Unlock()
		if g == nil {
			return nil, errors.New("commercial groups loading")
		}
		return g, nil
	}
	c.refreshing = true
	c.mu.Unlock()
	var result struct {
		Groups []commercialGroup `json:"groups"`
	}
	e := a.control(ctx, "GET", "commerce/groups", nil, &result)
	if e == nil && result.Groups == nil {
		e = errors.New("invalid commercial group metadata")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.refreshing = false
	c.key = key
	c.next = a.now().Add(30 * time.Second)
	if e != nil {
		c.groups = nil
		return nil, e
	}
	c.groups = result.Groups
	return c.groups, nil
}
func selectedProtocols(groups []commercialGroup, ids, requested []string) []string {
	available := map[string]bool{}
	for _, g := range groups {
		if !includes(ids, g.ID) {
			continue
		}
		for _, s := range g.Services {
			if s.Managed {
				available[s.Protocol] = true
			}
		}
	}
	out := []string{}
	for _, p := range requested {
		if available[p] && !includes(out, p) {
			out = append(out, p)
		}
	}
	return out
}
func (a *App) checkProtocolSelection(ctx context.Context, ids, protocols []string) error {
	groups, e := a.commercialGroups(ctx, true)
	if e != nil {
		return Problem{503, "暂不能核验星枢用户组和协议，请检查 API 连接后重试"}
	}
	for _, id := range ids {
		found := false
		for _, g := range groups {
			if g.ID == id {
				found = true
				break
			}
		}
		if !found {
			return Problem{409, "所选星枢用户组已不存在，请重新选择"}
		}
	}
	actual := selectedProtocols(groups, ids, protocols)
	if len(actual) != len(protocols) {
		return Problem{409, "所选协议并非全部由所选用户组提供，请取消不可用的协议后保存"}
	}
	return nil
}
