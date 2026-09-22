package research

import (
	"errors"
	"golang.org/x/net/idna"
	"net/url"
	"strings"
)

type domainPolicy struct{ allow, block []string }

func newPolicy(allow, block []string) (domainPolicy, error) {
	p := domainPolicy{}
	for i, list := range [][]string{allow, block} {
		for _, h := range list {
			if h != strings.TrimSpace(h) || strings.ContainsAny(h, "/:@*?#\\\r\n\t") {
				return p, errors.New("domain filters require hostnames without schemes, ports or wildcards")
			}
			h, err := idna.Lookup.ToASCII(strings.TrimSuffix(strings.ToLower(h), "."))
			if err != nil || h == "" || strings.Contains(h, "..") {
				return p, errors.New("invalid research domain")
			}
			if i == 0 {
				p.allow = append(p.allow, h)
			} else {
				p.block = append(p.block, h)
			}
		}
	}
	return p, nil
}
func canonical(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Hostname() == "" || u.User != nil || len(raw) > 8192 || strings.ContainsAny(raw, "\r\n\t\\") {
		return "", errors.New("research URL must be absolute HTTP(S) without credentials")
	}
	h, err := idna.Lookup.ToASCII(strings.TrimSuffix(strings.ToLower(u.Hostname()), "."))
	if err != nil {
		return "", err
	}
	if strings.Contains(h, ":") {
		h = "[" + h + "]"
	}
	if p := u.Port(); p != "" {
		h += ":" + p
	}
	u.Host = h
	u.Fragment = ""
	return u.String(), nil
}
func (p domainPolicy) permits(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	h := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	match := func(list []string) bool {
		for _, d := range list {
			if h == d || strings.HasSuffix(h, "."+d) {
				return true
			}
		}
		return false
	}
	return !match(p.block) && (len(p.allow) == 0 || match(p.allow))
}
