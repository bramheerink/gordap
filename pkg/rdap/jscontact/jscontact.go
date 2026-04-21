// Package jscontact contains the RFC 9553 (JSContact) type definitions used
// inside RDAP responses. The package is self-contained — import it on its
// own if you only need JSContact marshalling.
package jscontact

// Card is a minimal RFC 9553 Card. Only the members the RDAP profile
// actually exercises are typed; the rest stays open via ExtraMembers for
// forward compatibility. Unexported tag: the outer code is expected to
// marshal Card directly.
type Card struct {
	Version        string                    `json:"@version,omitempty"`
	Type           string                    `json:"@type,omitempty"` // always "Card"
	UID            string                    `json:"uid,omitempty"`
	Kind           string                    `json:"kind,omitempty"` // individual|org|location|device|application|group
	Language       string                    `json:"language,omitempty"`
	Created        string                    `json:"created,omitempty"`
	Updated        string                    `json:"updated,omitempty"`
	Name           *Name                     `json:"name,omitempty"`
	Organizations  map[string]Org            `json:"organizations,omitempty"`
	Titles         map[string]Title          `json:"titles,omitempty"`
	Emails         map[string]Email          `json:"emails,omitempty"`
	Phones         map[string]Phone          `json:"phones,omitempty"`
	OnlineServices map[string]OnlineService  `json:"onlineServices,omitempty"`
	Addresses      map[string]Address        `json:"addresses,omitempty"`
	Nicknames      map[string]Nickname       `json:"nicknames,omitempty"`
	Notes          map[string]Note           `json:"notes,omitempty"`

	// ExtraMembers captures unknown JSContact members verbatim. Kept separate
	// from the typed fields so marshalling stays predictable.
	ExtraMembers map[string]any `json:"-"`
}

type Name struct {
	Type       string          `json:"@type,omitempty"`
	Full       string          `json:"full,omitempty"`
	Components []NameComponent `json:"components,omitempty"`
}

type NameComponent struct {
	Type  string `json:"@type,omitempty"`
	Kind  string `json:"kind"` // given, surname, title, ...
	Value string `json:"value"`
}

type Org struct {
	Type  string    `json:"@type,omitempty"`
	Name  string    `json:"name,omitempty"`
	Units []OrgUnit `json:"units,omitempty"`
}

type OrgUnit struct {
	Type string `json:"@type,omitempty"`
	Name string `json:"name"`
}

type Title struct {
	Type string `json:"@type,omitempty"`
	Name string `json:"name"`
	Kind string `json:"kind,omitempty"` // title|role
}

type Email struct {
	Type     string          `json:"@type,omitempty"`
	Address  string          `json:"address"`
	Contexts map[string]bool `json:"contexts,omitempty"`
	Pref     int             `json:"pref,omitempty"`
}

type Phone struct {
	Type     string          `json:"@type,omitempty"`
	Number   string          `json:"number"`
	Features map[string]bool `json:"features,omitempty"`
	Contexts map[string]bool `json:"contexts,omitempty"`
	Pref     int             `json:"pref,omitempty"`
}

type OnlineService struct {
	Type     string          `json:"@type,omitempty"`
	URI      string          `json:"uri,omitempty"`
	Service  string          `json:"service,omitempty"`
	User     string          `json:"user,omitempty"`
	Contexts map[string]bool `json:"contexts,omitempty"`
}

// Address is the structured address from RFC 9553 §2.5.1. Components are an
// ordered list — the order matters for display.
type Address struct {
	Type             string             `json:"@type,omitempty"`
	Components       []AddressComponent `json:"components,omitempty"`
	CountryCode      string             `json:"countryCode,omitempty"`
	Coordinates      string             `json:"coordinates,omitempty"`
	Contexts         map[string]bool    `json:"contexts,omitempty"`
	Full             string             `json:"full,omitempty"`
	DefaultSeparator string             `json:"defaultSeparator,omitempty"`
}

type AddressComponent struct {
	Type  string `json:"@type,omitempty"`
	Kind  string `json:"kind"` // name, number, locality, region, postcode, country, ...
	Value string `json:"value"`
}

type Nickname struct {
	Type string `json:"@type,omitempty"`
	Name string `json:"name"`
}

type Note struct {
	Type string `json:"@type,omitempty"`
	Note string `json:"note"`
}
