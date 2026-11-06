package plugins

import (
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"

	"github.com/xlsft/harness/packages/client/skills"
)

// Browsing: what registered marketplaces offer, read from the copies this device already holds. It needs no network, runs nothing,
// and says only what the marketplace's files say.

// LooseText is a string field of a file other tools write: anything but a string reads as empty instead of failing the whole file.
type LooseText string

// UnmarshalJSON keeps a string and drops the rest.
func (t *LooseText) UnmarshalJSON(b []byte) error {
	var s string
	if json.Unmarshal(b, &s) == nil {
		*t = LooseText(s)
	}
	return nil
}

// LooseList is a list of strings, or one string, in a file other tools write. Anything else reads as empty.
type LooseList []string

// UnmarshalJSON keeps the strings and drops the rest.
func (l *LooseList) UnmarshalJSON(b []byte) error {
	var one string
	if json.Unmarshal(b, &one) == nil {
		if one != "" {
			*l = LooseList{one}
		}
		return nil
	}
	var many []any
	if json.Unmarshal(b, &many) == nil {
		for _, v := range many {
			if s, ok := v.(string); ok && s != "" {
				*l = append(*l, s)
			}
		}
	}
	return nil
}

// Meta is what a manifest or an entry says about a plugin, for showing it.
type Meta struct {
	Category, Publisher, Homepage, License string
	Tags                                   []string
}

func (m Meta) over(o Meta) Meta {
	if o.Category != "" {
		m.Category = o.Category
	}
	if o.Publisher != "" {
		m.Publisher = o.Publisher
	}
	if o.Homepage != "" {
		m.Homepage = o.Homepage
	}
	if o.License != "" {
		m.License = o.License
	}
	if len(o.Tags) > 0 {
		m.Tags = o.Tags
	}
	return m
}

// personName reads an author or owner field: a string, or an object with a name.
func personName(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var o struct {
		Name LooseText `json:"name"`
	}
	if json.Unmarshal(raw, &o) == nil {
		return string(o.Name)
	}
	return ""
}

func metaOf(m Manifest) Meta {
	tags := append([]string{}, m.Tags...)
	for _, k := range m.Keywords {
		if !slices.Contains(tags, k) {
			tags = append(tags, k)
		}
	}
	return Meta{Category: strings.TrimSpace(string(m.Category)), Publisher: personName(m.Author), Homepage: m.Homepage, License: m.License, Tags: tags}
}

// ListingSkill is a skill of a marketplace's plugin, before it is installed.
type ListingSkill struct {
	// Name is what it goes by once installed, plugin:skill.
	Name, Description string
}

// Listing is a plugin a marketplace offers.
type Listing struct {
	ID, Name, Marketplace, DisplayName, Description, Version string
	Meta                                                     Meta
	Installed                                                bool
	// Contents is what its folder holds, when the folder is in the marketplace's copy. A plugin with a repository of its own isn't
	// on this device, and nobody knows until it is installed.
	Contents *Contents
	Skills   []ListingSkill
	Problem  string
}

// MarketListings is a registered marketplace with its listings.
type MarketListings struct {
	Marketplace
	// Owner is who the file says keeps the marketplace.
	Owner    string
	Listings []Listing
}

type browseMemo struct {
	stamp    string
	owner    string
	listings []Listing
}

// Browse returns every registered marketplace with what it offers. Installed marks are told now; the rest is kept while the copy
// of the marketplace stays the same.
func (m *Manager) Browse() ([]MarketListings, error) {
	st, err := m.snapshot()
	if err != nil {
		return nil, err
	}
	markets, err := m.Marketplaces()
	if err != nil {
		return nil, err
	}
	out := make([]MarketListings, 0, len(markets))
	for _, mk := range markets {
		ml := MarketListings{Marketplace: mk}
		if rec := st.Marketplaces[mk.Name]; rec != nil && mk.Problem == "" {
			ml.Owner, ml.Listings = m.listings(rec)
		}
		ml.Listings = slices.Clone(ml.Listings)
		for i := range ml.Listings {
			_, ml.Listings[i].Installed = st.Plugins[ml.Listings[i].ID]
		}
		out = append(out, ml)
	}
	return out, nil
}

func (m *Manager) listings(rec *MarketplaceRecord) (string, []Listing) {
	man, root, err := m.readMarketplace(rec)
	if err != nil {
		return "", nil
	}
	stamp := rec.Commit + "|" + rec.UpdatedAt
	if rec.Commit == "" {
		if s, err := treeStamp(root); err == nil {
			stamp = s
		}
	}
	m.mu.Lock()
	e, ok := m.browsed[rec.Name]
	m.mu.Unlock()
	if ok && e.stamp == stamp {
		return e.owner, e.listings
	}
	owner := personName(man.Owner)
	list := make([]Listing, 0, len(man.Plugins))
	for i := range man.Plugins {
		list = append(list, readListing(&man.Plugins[i], man, root, rec.Name, owner))
	}
	slices.SortFunc(list, func(a, b Listing) int { return strings.Compare(a.Name, b.Name) })
	m.mu.Lock()
	if m.browsed == nil {
		m.browsed = map[string]browseMemo{}
	}
	m.browsed[rec.Name] = browseMemo{stamp: stamp, owner: owner, listings: list}
	m.mu.Unlock()
	return owner, list
}

// readListing reads one entry. Its folder is read, as files, when it is in the marketplace's copy; nothing of it runs.
func readListing(e *Entry, man *MarketplaceManifest, root, market, owner string) Listing {
	l := Listing{ID: e.Name + "@" + market, Name: e.Name, Marketplace: market, DisplayName: e.DisplayName, Description: e.Description, Version: e.Version, Meta: metaOf(e.Manifest)}
	if !ValidName(e.Name) {
		l.Problem = "The name of this entry can't be used."
		return l
	}
	rel, _, err := entrySource(*e, man)
	if err != nil {
		l.Problem = err.Error()
	} else if rel != "" && root != "" {
		// The data folder is never made: nothing is run or written for a listing.
		p, err := Load(filepath.Join(root, rel), e.Name, market, "", overlayOf(e))
		if err != nil {
			l.Problem = err.Error()
		} else {
			contents := p.Contents
			l.Contents = &contents
			l.Meta = metaOf(p.Manifest).over(l.Meta)
			if l.Description == "" {
				l.Description = p.Manifest.Description
			}
			if l.DisplayName == "" {
				l.DisplayName = p.Manifest.DisplayName
			}
			if l.Version == "" {
				l.Version = p.Manifest.Version
			}
			for _, s := range skills.ScanPlugin(p.SkillSource()) {
				if s.Kind == skills.KindSkill {
					l.Skills = append(l.Skills, ListingSkill{Name: s.Name, Description: s.Description})
				}
			}
		}
	}
	if l.Meta.Publisher == "" {
		l.Meta.Publisher = owner
	}
	return l
}
