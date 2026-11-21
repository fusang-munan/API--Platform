package beichen

import (
	"regexp"
	"strings"
	"time"
	"unicode"
)

type modelNameMatch struct {
	name string
	kind string
}

// The empty value denotes multiple distinct IDs for the same search term.
type uniqueModelNames map[string]string

func (names uniqueModelNames) add(key, name string) {
	if old, ok := names[key]; ok && old != name {
		names[key] = ""
	} else {
		names[key] = name
	}
}

type catalogNameMatcher struct {
	exact                     map[string]bool
	folded, spelling, version uniqueModelNames
	unpinned, alias           uniqueModelNames
	aliases                   map[string]string
}

func newCatalogNameMatcher(names []string, aliases map[string]string) catalogNameMatcher {
	matcher := catalogNameMatcher{
		exact: map[string]bool{}, folded: uniqueModelNames{}, spelling: uniqueModelNames{},
		version: uniqueModelNames{}, unpinned: uniqueModelNames{}, alias: uniqueModelNames{}, aliases: aliases,
	}
	for _, name := range names {
		matcher.exact[name] = true
		matcher.folded.add(strings.ToLower(name), name)
		stem, suffix := catalogNameParts(name)
		matcher.spelling.add(stem+"@"+suffix, name)
		matcher.version.add(stem, name)
		if suffix == "" || suffix == "latest" {
			matcher.unpinned.add(stem, name)
		}
		matcher.alias.add(matcher.aliasName(name), name)
	}
	return matcher
}

func (matcher catalogNameMatcher) aliasName(name string) string {
	name = strings.ToLower(name)
	if school := matcher.aliases[name]; school != "" {
		return school
	}
	return name
}

func (matcher catalogNameMatcher) match(name string, catalogOnly bool) modelNameMatch {
	name = strings.TrimSpace(name)
	if matcher.exact[name] {
		return modelNameMatch{name, "exact"}
	}
	stem, suffix := catalogNameParts(name)
	type lookup struct {
		names uniqueModelNames
		key   string
		kind  string
	}
	searches := []lookup{
		{matcher.folded, strings.ToLower(name), "normalized"},
		{matcher.spelling, stem + "@" + suffix, "normalized"},
	}
	if catalogOnly {
		version := matcher.unpinned
		if suffix == "" || suffix == "latest" {
			version = matcher.version
		}
		searches = append(searches, lookup{version, stem, "normalized"}, lookup{matcher.alias, matcher.aliasName(name), "alias"})
	}
	ambiguous := false
	for _, search := range searches {
		result, found := search.names[search.key]
		if !found {
			continue
		}
		if result == "" {
			ambiguous = true
			continue
		}
		if !ambiguous || search.kind == "alias" {
			return modelNameMatch{result, search.kind}
		}
	}
	if ambiguous {
		return modelNameMatch{kind: "ambiguous"}
	}
	return modelNameMatch{}
}

func matchWebsiteModel(name string, key, website catalogNameMatcher) modelNameMatch {
	match := key.match(name, true)
	if match.kind != "normalized" {
		return match
	}
	_, siteVersion := catalogNameParts(name)
	_, keyVersion := catalogNameParts(match.name)
	if siteVersion == keyVersion {
		return match
	}
	// A generic Key ID must not stand in for several explicit website releases.
	if !strings.EqualFold(website.match(match.name, true).name, name) {
		return modelNameMatch{kind: "ambiguous"}
	}
	return match
}

var catalogVersionSuffix = regexp.MustCompile(`-(latest|20[0-9]{2}-[0-9]{2}-[0-9]{2}|20[0-9]{6})$`)

func catalogNameParts(name string) (string, string) {
	name = strings.Join(strings.FieldsFunc(strings.ToLower(name), func(r rune) bool {
		return unicode.IsSpace(r) || unicode.Is(unicode.Pd, r) || r == '_'
	}), "-")
	suffix := ""
	if match := catalogVersionSuffix.FindStringSubmatchIndex(name); match != nil {
		version := name[match[2]:match[3]]
		date := strings.ReplaceAll(version, "-", "")
		if _, err := time.Parse("20060102", date); err == nil || version == "latest" {
			name, suffix = name[:match[0]], date
		}
	}
	// Spacing inside a numeric version is significant. Keep the boundary
	// between 6 and 1 so that 6.1 can never match 61.
	runes := []rune(name)
	var stem strings.Builder
	for i, r := range runes {
		if r != '-' {
			stem.WriteRune(r)
		} else if i > 0 && i+1 < len(runes) && unicode.IsDigit(runes[i-1]) && unicode.IsDigit(runes[i+1]) {
			stem.WriteByte('.')
		}
	}
	return stem.String(), suffix
}
